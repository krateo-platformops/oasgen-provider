package restResources

import (
	"context"
	"encoding/base64"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/secretrbac"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
)

var (
	widgetGVK  = schema.GroupVersionKind{Group: "widgets.example.io", Version: "v1alpha1", Kind: "Widget"}
	widgetGVR  = schema.GroupVersionResource{Group: "widgets.example.io", Version: "v1alpha1", Resource: "widgets"}
	roleGVR    = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}
	bindingGVR = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}
	// secretGVR is declared in resolver_digest.go: production code needs it, and two copies of
	// the same GVR is a pair of names nothing asserts are equal.
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
		pluralizer:    fakePluralizer{},
		logger:        logging.NewNopLogger(),
		dynamicClient: dyn,
	}
}

func newSecretRBACFakeClient(t *testing.T) *fake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	return fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		roleGVR:    "RoleList",
		bindingGVR: "RoleBindingList",
		secretGVR:  "SecretList",
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

// seedLegacyPerCRRole writes the Role and RoleBinding pair a release before #540 would have
// self-provisioned for this instance, under the name that release used. Hand-seeded on purpose: nothing
// in the controller creates them any more, so the only honest way to test the teardown is to put a
// cluster into the state an upgrade finds it in.
func seedLegacyPerCRRole(t *testing.T, dyn *fake.FakeDynamicClient, ns, name string) string {
	t.Helper()
	roleName := secretrbac.RoleName(widgetGVR, types.NamespacedName{Namespace: ns, Name: name})
	for gvr, kind := range map[schema.GroupVersionResource]string{roleGVR: "Role", bindingGVR: "RoleBinding"} {
		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion("rbac.authorization.k8s.io/v1")
		obj.SetKind(kind)
		obj.SetNamespace(ns)
		obj.SetName(roleName)
		if _, err := dyn.Resource(gvr).Namespace(ns).Create(context.Background(), obj, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding legacy %s: %v", kind, err)
		}
	}
	return roleName
}

func countIn(t *testing.T, dyn *fake.FakeDynamicClient, gvr schema.GroupVersionResource, ns string) int {
	t.Helper()
	list, err := dyn.Resource(gvr).Namespace(ns).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("List %s: %v", gvr.Resource, err)
	}
	return len(list.Items)
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

func resolveOneSecretRef(t *testing.T, h *handler, mg *unstructured.Unstructured, action string) (map[string]interface{}, error) {
	t.Helper()
	mapping := secretRefMapping()
	clientInfo := &getter.Info{
		Resource: getter.Resource{
			VerbsDescription: []getter.VerbsDescription{{Action: action, FieldMapping: mapping}},
		},
	}
	return h.resolveRequestFields(context.Background(), nil, clientInfo, mapping, mg)
}

// TestResolveRequestFields_GrantsNothing is the assertion the removal exists for, and it is written
// against what the cluster HOLDS rather than against the absence of a call.
//
// A test that checked "EnsureSecretRole is not called" would pass the moment the symbol was deleted and
// prove nothing about whether some other path writes a Role. Counting Roles and RoleBindings in the
// namespace after a full resolve is a claim about the outcome, and it can fail for any mechanism.
//
// The resolved value is asserted in the same test, as the control: a zero Role count means nothing if
// the resolve never ran. Before #540 this test could not have been written at all -- resolving REQUIRED
// the grant, so the two assertions were contradictory.
func TestResolveRequestFields_GrantsNothing(t *testing.T) {
	dyn := newSecretRBACFakeClient(t)
	seedSecret(t, dyn, "ns1", "db-creds", "password", "hunter2")
	h := newSecretRBACTestHandler(dyn)
	mg := widgetMG("ns1", "my-widget", "db-creds", "password")

	resolved, err := resolveOneSecretRef(t, h, mg, "create")
	if err != nil {
		t.Fatalf("resolveRequestFields: %v", err)
	}

	// CONTROL: the resolve really happened, so the counts below are about a code path that ran.
	if len(resolved) != 1 {
		t.Fatalf("expected exactly one resolved entry, got %v", resolved)
	}
	for _, v := range resolved {
		if v != "hunter2" {
			t.Fatalf("expected the secretRef to resolve to %q, got %v", "hunter2", v)
		}
	}

	if n := countIn(t, dyn, roleGVR, "ns1"); n != 0 {
		t.Errorf("resolving a secretRef created %d Role(s); the provider owns the grant now (#540)", n)
	}
	if n := countIn(t, dyn, bindingGVR, "ns1"); n != 0 {
		t.Errorf("resolving a secretRef created %d RoleBinding(s); the provider owns the grant now (#540)", n)
	}
}

// TestResolveRequestFields_TheUpdatePathNoLongerDemandsARole is #540's headline symptom, as a test.
//
// An adopted resource never runs Create, so it never had a per-CR Role; the Update path used to pass
// expectExisting=true and hard-fail with "secretRef RBAC Role ... is missing but was expected to already
// exist", forever, for every secretRef kind at every version from at least 0.52.0. Reported twice from a
// consumer's cluster. Nothing here is special about "update" any more, which is the point: the same call
// with no RBAC in the namespace must simply resolve.
func TestResolveRequestFields_TheUpdatePathNoLongerDemandsARole(t *testing.T) {
	dyn := newSecretRBACFakeClient(t)
	seedSecret(t, dyn, "ns1", "db-creds", "password", "hunter2")
	h := newSecretRBACTestHandler(dyn)
	mg := widgetMG("ns1", "adopted-widget", "db-creds", "password")

	// CONTROL: no Role exists, which is exactly the state that used to be fatal here.
	if n := countIn(t, dyn, roleGVR, "ns1"); n != 0 {
		t.Fatalf("the fixture must start with no Role for this test to mean anything, found %d", n)
	}

	resolved, err := resolveOneSecretRef(t, h, mg, "update")
	if err != nil {
		t.Fatalf("the update path must not require a per-CR Role: %v", err)
	}
	for _, v := range resolved {
		if v != "hunter2" {
			t.Fatalf("expected the secretRef to resolve to %q, got %v", "hunter2", v)
		}
	}
}

// TestDeleteSecretRefRBAC_RemovesARoleLeftByAnOlderRelease is the migration half. The per-CR names do
// not overlap the provider's, so nothing else will ever revoke these.
func TestDeleteSecretRefRBAC_RemovesARoleLeftByAnOlderRelease(t *testing.T) {
	dyn := newSecretRBACFakeClient(t)
	h := newSecretRBACTestHandler(dyn)
	mg := widgetMG("ns1", "my-widget", "db-creds", "password")
	roleName := seedLegacyPerCRRole(t, dyn, "ns1", "my-widget")

	// CONTROL: both objects are there before the teardown, under the legacy name.
	if n := countIn(t, dyn, roleGVR, "ns1"); n != 1 {
		t.Fatalf("fixture: expected 1 seeded Role %q, found %d", roleName, n)
	}
	if n := countIn(t, dyn, bindingGVR, "ns1"); n != 1 {
		t.Fatalf("fixture: expected 1 seeded RoleBinding %q, found %d", roleName, n)
	}

	if err := h.deleteSecretRefRBAC(context.Background(), mg); err != nil {
		t.Fatalf("deleteSecretRefRBAC: %v", err)
	}

	if n := countIn(t, dyn, roleGVR, "ns1"); n != 0 {
		t.Errorf("expected the legacy Role to be removed, %d remain", n)
	}
	if n := countIn(t, dyn, bindingGVR, "ns1"); n != 0 {
		t.Errorf("expected the legacy RoleBinding to be removed, %d remain", n)
	}
}

// TestDeleteSecretRefRBAC_IdempotentWhenNothingWasEverProvisioned is now the COMMON case, not the edge
// one: from #540 onwards no release creates these, so every delete on a fresh install hits this path.
func TestDeleteSecretRefRBAC_IdempotentWhenNothingWasEverProvisioned(t *testing.T) {
	dyn := newSecretRBACFakeClient(t)
	h := newSecretRBACTestHandler(dyn)
	mg := widgetMG("ns1", "never-provisioned", "db-creds", "password")

	if err := h.deleteSecretRefRBAC(context.Background(), mg); err != nil {
		t.Fatalf("expected teardown with nothing to tear down to succeed, got %v", err)
	}
}
