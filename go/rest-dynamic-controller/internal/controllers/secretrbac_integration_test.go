package restResources

import (
	"context"
	"encoding/base64"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
)

var (
	widgetGVK = schema.GroupVersionKind{Group: "widgets.example.io", Version: "v1alpha1", Kind: "Widget"}
	widgetGVR = schema.GroupVersionResource{Group: "widgets.example.io", Version: "v1alpha1", Resource: "widgets"}
	roleGVR   = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}
	secretGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
)

type fakePluralizer struct{}

func (fakePluralizer) GVKtoGVR(gvk schema.GroupVersionKind) (schema.GroupVersionResource, error) {
	if gvk == widgetGVK {
		return widgetGVR, nil
	}
	return schema.GroupVersionResource{}, nil
}

func newSecretRBACTestHandler(dyn *fake.FakeDynamicClient) *handler {
	return &handler{
		pluralizer:         fakePluralizer{},
		logger:             logging.NewNopLogger(),
		dynamicClient:      dyn,
		selfServiceAccount: types.NamespacedName{Namespace: "rdc-system", Name: "rest-dynamic-controller"},
	}
}

func newSecretRBACFakeClient(t *testing.T) *fake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	return fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		roleGVR:   "RoleList",
		secretGVR: "SecretList",
	})
}

func widgetMG(ns, name, secretName, secretKey string) *unstructured.Unstructured {
	mg := &unstructured.Unstructured{}
	mg.SetGroupVersionKind(widgetGVK)
	mg.SetNamespace(ns)
	mg.SetName(name)
	_ = unstructured.SetNestedMap(mg.Object, map[string]interface{}{
		"credentialsRef": map[string]interface{}{"name": secretName, "key": secretKey},
	}, "spec")
	return mg
}

func seedSecret(t *testing.T, dyn *fake.FakeDynamicClient, ns, name, key, value string) {
	t.Helper()
	sec := &unstructured.Unstructured{}
	sec.SetAPIVersion("v1")
	sec.SetKind("Secret")
	sec.SetNamespace(ns)
	sec.SetName(name)
	data := map[string]interface{}{key: base64.StdEncoding.EncodeToString([]byte(value))}
	if err := unstructured.SetNestedMap(sec.Object, data, "data"); err != nil {
		t.Fatalf("SetNestedMap: %v", err)
	}
	if _, err := dyn.Resource(secretGVR).Namespace(ns).Create(context.Background(), sec, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding secret: %v", err)
	}
}

func secretRefMapping() []getter.FieldMappingItem {
	return []getter.FieldMappingItem{
		{
			InBody:           "token",
			InCustomResource: "spec.credentialsRef",
			Resolver: &getter.FieldResolver{
				Type: "secretRef",
				SecretRef: &getter.SecretRefResolver{
					NameFromCustomResource: "spec.credentialsRef.name",
					KeyFromCustomResource:  "spec.credentialsRef.key",
				},
			},
		},
	}
}

func TestEnsureSecretRefRBACAndResolve_ProvisionsAndResolves(t *testing.T) {
	dyn := newSecretRBACFakeClient(t)
	seedSecret(t, dyn, "ns1", "db-creds", "password", "hunter2")
	h := newSecretRBACTestHandler(dyn)
	mg := widgetMG("ns1", "my-widget", "db-creds", "password")
	mapping := secretRefMapping()
	clientInfo := &getter.Info{
		Resource: getter.Resource{
			VerbsDescription: []getter.VerbsDescription{{Action: "create", FieldMapping: mapping}},
		},
	}

	resolved, err := h.ensureSecretRefRBACAndResolve(context.Background(), nil, clientInfo, mapping, mg, false)
	if err != nil {
		t.Fatalf("ensureSecretRefRBACAndResolve: %v", err)
	}

	// The Role must exist, scoped to exactly db-creds.
	roleList, err := dyn.Resource(roleGVR).Namespace("ns1").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(roleList.Items) != 1 {
		t.Fatalf("expected exactly one Role to be provisioned, got %v (err %v)", roleList, err)
	}

	key := ""
	for k := range resolved {
		key = k
	}
	if resolved[key] != "hunter2" {
		t.Fatalf("expected the secretRef value to resolve to %q, got %v", "hunter2", resolved)
	}
}

func TestEnsureSecretRefRBACAndResolve_UpdateHardFailsWhenRoleMissing(t *testing.T) {
	dyn := newSecretRBACFakeClient(t)
	seedSecret(t, dyn, "ns1", "db-creds", "password", "hunter2")
	h := newSecretRBACTestHandler(dyn)
	mg := widgetMG("ns1", "my-widget", "db-creds", "password")
	mapping := secretRefMapping()
	clientInfo := &getter.Info{
		Resource: getter.Resource{
			VerbsDescription: []getter.VerbsDescription{{Action: "update", FieldMapping: mapping}},
		},
	}

	// expectExisting=true (the Update path) with no prior Create must hard-fail (decision D), not silently
	// provision the Role for the first time.
	_, err := h.ensureSecretRefRBACAndResolve(context.Background(), nil, clientInfo, mapping, mg, true)
	if err == nil {
		t.Fatal("expected a hard error when the Role is unexpectedly missing on the Update path")
	}
}

func TestDeleteSecretRefRBAC_RemovesProvisionedRole(t *testing.T) {
	dyn := newSecretRBACFakeClient(t)
	seedSecret(t, dyn, "ns1", "db-creds", "password", "hunter2")
	h := newSecretRBACTestHandler(dyn)
	mg := widgetMG("ns1", "my-widget", "db-creds", "password")
	mapping := secretRefMapping()
	clientInfo := &getter.Info{
		Resource: getter.Resource{
			VerbsDescription: []getter.VerbsDescription{{Action: "create", FieldMapping: mapping}},
		},
	}

	if _, err := h.ensureSecretRefRBACAndResolve(context.Background(), nil, clientInfo, mapping, mg, false); err != nil {
		t.Fatalf("provisioning: %v", err)
	}

	if err := h.deleteSecretRefRBAC(context.Background(), mg); err != nil {
		t.Fatalf("deleteSecretRefRBAC: %v", err)
	}

	roleList, err := dyn.Resource(roleGVR).Namespace("ns1").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(roleList.Items) != 0 {
		t.Fatalf("expected the Role to be removed, got %v", roleList.Items)
	}
}

func TestDeleteSecretRefRBAC_IdempotentWhenNeverProvisioned(t *testing.T) {
	dyn := newSecretRBACFakeClient(t)
	h := newSecretRBACTestHandler(dyn)
	mg := widgetMG("ns1", "never-provisioned", "db-creds", "password")

	if err := h.deleteSecretRefRBAC(context.Background(), mg); err != nil {
		t.Fatalf("expected teardown of a never-provisioned CR instance to succeed, got %v", err)
	}
}
