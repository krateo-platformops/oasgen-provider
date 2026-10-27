package fieldmapping

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

var secretGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}

func newFakeClientWithSecret(t *testing.T, ns, name, key, value string) *fake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{secretGVR: "SecretList"})

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
	return dyn
}

func mgWithCredsRef(ns, secretName, secretKey string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{"namespace": ns, "name": "my-cr"},
			"spec": map[string]interface{}{
				"credentialsRef": map[string]interface{}{
					"name": secretName,
					"key":  secretKey,
				},
			},
		},
	}
}

func TestResolveRequestResolvers_SecretRef(t *testing.T) {
	dyn := newFakeClientWithSecret(t, "ns1", "db-creds", "password", "hunter2")
	mg := mgWithCredsRef("ns1", "db-creds", "password")

	mapping := []getter.FieldMappingItem{
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

	resolved, err := ResolveRequestResolvers(context.Background(), dyn, mapping, mg, nil)
	if err != nil {
		t.Fatalf("ResolveRequestResolvers: %v", err)
	}
	key := ResolverKey(mapping[0])
	if resolved[key] != "hunter2" {
		t.Fatalf("expected resolved value %q, got %q", "hunter2", resolved[key])
	}
}

// TestResolveRequestResolvers_SecretRefArrayPath is a regression test for issue #33: nameFromCustomResource
// / keyFromCustomResource must be able to address a secretRef nested inside an array — e.g. Keycloak's
// spec.credentials[0].valueSecretRef — the exact shape the issue's reproduction used.
func TestResolveRequestResolvers_SecretRefArrayPath(t *testing.T) {
	dyn := newFakeClientWithSecret(t, "ns1", "alice-secret", "password", "hunter2")
	mg := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{"namespace": "ns1", "name": "alice"},
			"spec": map[string]interface{}{
				"credentials": []interface{}{
					map[string]interface{}{
						"valueSecretRef": map[string]interface{}{
							"name": "alice-secret",
							"key":  "password",
						},
					},
				},
			},
		},
	}

	mapping := []getter.FieldMappingItem{
		{
			InBody:           "credentials[0].value",
			InCustomResource: "spec.credentials[0].valueSecretRef",
			Resolver: &getter.FieldResolver{
				Type: "secretRef",
				SecretRef: &getter.SecretRefResolver{
					NameFromCustomResource: "spec.credentials[0].valueSecretRef.name",
					KeyFromCustomResource:  "spec.credentials[0].valueSecretRef.key",
				},
			},
		},
	}

	resolved, err := ResolveRequestResolvers(context.Background(), dyn, mapping, mg, nil)
	if err != nil {
		t.Fatalf("ResolveRequestResolvers: %v", err)
	}
	key := ResolverKey(mapping[0])
	if resolved[key] != "hunter2" {
		t.Fatalf("expected resolved value %q, got %q", "hunter2", resolved[key])
	}
}

func TestResolveRequestResolvers_MissingSecretIsError(t *testing.T) {
	scheme := runtime.NewScheme()
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{secretGVR: "SecretList"})
	mg := mgWithCredsRef("ns1", "does-not-exist", "password")

	mapping := []getter.FieldMappingItem{
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

	_, err := ResolveRequestResolvers(context.Background(), dyn, mapping, mg, nil)
	if err == nil {
		t.Fatal("expected an error when the referenced secret does not exist")
	}
}

func TestResolveRequestResolvers_NoResolversIsNilNoError(t *testing.T) {
	mg := mgWithCredsRef("ns1", "db-creds", "password")
	mapping := []getter.FieldMappingItem{
		{InBody: "name", InCustomResource: "spec.name"},
	}
	resolved, err := ResolveRequestResolvers(context.Background(), nil, mapping, mg, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("expected no resolved entries, got %v", resolved)
	}
}

func TestCollectSecretRefNames_AcrossAllVerbs(t *testing.T) {
	mg := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"spec": map[string]interface{}{
				"createSecretRef": map[string]interface{}{"name": "secret-a", "key": "k"},
				"updateSecretRef": map[string]interface{}{"name": "secret-b", "key": "k"},
			},
		},
	}
	verbs := []getter.VerbsDescription{
		{
			Action: "create",
			FieldMapping: []getter.FieldMappingItem{
				{
					InBody: "token", InCustomResource: "spec.createSecretRef",
					Resolver: &getter.FieldResolver{
						Type: "secretRef",
						SecretRef: &getter.SecretRefResolver{
							NameFromCustomResource: "spec.createSecretRef.name",
							KeyFromCustomResource:  "spec.createSecretRef.key",
						},
					},
				},
			},
		},
		{
			Action: "update",
			FieldMapping: []getter.FieldMappingItem{
				{
					InBody: "token", InCustomResource: "spec.updateSecretRef",
					Resolver: &getter.FieldResolver{
						Type: "secretRef",
						SecretRef: &getter.SecretRefResolver{
							NameFromCustomResource: "spec.updateSecretRef.name",
							KeyFromCustomResource:  "spec.updateSecretRef.key",
						},
					},
				},
			},
		},
	}

	names := CollectSecretRefNames(verbs, mg)
	if len(names) != 2 || names[0] != "secret-a" || names[1] != "secret-b" {
		t.Fatalf("expected [secret-a secret-b] (from both verbs, sorted), got %v", names)
	}
}

func TestCollectSecretRefNames_UnresolvableEntrySkipped(t *testing.T) {
	mg := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{}}}
	verbs := []getter.VerbsDescription{
		{
			Action: "create",
			FieldMapping: []getter.FieldMappingItem{
				{
					InBody: "token", InCustomResource: "spec.credentialsRef",
					Resolver: &getter.FieldResolver{
						Type: "secretRef",
						SecretRef: &getter.SecretRefResolver{
							NameFromCustomResource: "spec.credentialsRef.name",
							KeyFromCustomResource:  "spec.credentialsRef.key",
						},
					},
				},
			},
		},
	}

	names := CollectSecretRefNames(verbs, mg)
	if len(names) != 0 {
		t.Fatalf("expected no names when the reference isn't resolvable yet, got %v", names)
	}
}

func TestResolverKey_StableAndDistinct(t *testing.T) {
	a := getter.FieldMappingItem{InPath: "id", InCustomResource: "spec.alias"}
	b := getter.FieldMappingItem{InQuery: "id", InCustomResource: "spec.alias"}
	if ResolverKey(a) == ResolverKey(b) {
		t.Fatal("expected different anchors to produce different keys")
	}
	if ResolverKey(a) != ResolverKey(a) {
		t.Fatal("expected ResolverKey to be deterministic")
	}
}

// TestResolveRequestResolvers_SecretRefDedupedAcrossEntries proves two FieldMapping entries referencing
// the SAME secret trigger exactly one Secret read, not one per entry.
func TestResolveRequestResolvers_SecretRefDedupedAcrossEntries(t *testing.T) {
	dyn := newFakeClientWithSecret(t, "ns1", "db-creds", "password", "hunter2")
	mg := mgWithCredsRef("ns1", "db-creds", "password")

	sameResolver := &getter.FieldResolver{
		Type: "secretRef",
		SecretRef: &getter.SecretRefResolver{
			NameFromCustomResource: "spec.credentialsRef.name",
			KeyFromCustomResource:  "spec.credentialsRef.key",
		},
	}
	mapping := []getter.FieldMappingItem{
		{InBody: "token", InCustomResource: "spec.credentialsRef", Resolver: sameResolver},
		{InQuery: "auth", InCustomResource: "spec.credentialsRef", Resolver: sameResolver},
	}

	dyn.Fake.ClearActions()
	resolved, err := ResolveRequestResolvers(context.Background(), dyn, mapping, mg, nil)
	if err != nil {
		t.Fatalf("ResolveRequestResolvers: %v", err)
	}

	if resolved[ResolverKey(mapping[0])] != "hunter2" || resolved[ResolverKey(mapping[1])] != "hunter2" {
		t.Fatalf("expected both entries to resolve to the same value, got %v", resolved)
	}

	gets := 0
	for _, a := range dyn.Fake.Actions() {
		if a.GetVerb() == "get" && a.GetResource().Resource == "secrets" {
			gets++
		}
	}
	if gets != 1 {
		t.Fatalf("expected exactly one secret Get despite two referencing entries, got %d", gets)
	}
}

// TestResolveRequestResolvers_DistinctSecretsNotDeduped proves the cache is content-keyed, not blanket
// dedup: two entries referencing DIFFERENT secrets each still get their own value.
func TestResolveRequestResolvers_DistinctSecretsNotDeduped(t *testing.T) {
	dyn := newFakeClientWithSecret(t, "ns1", "db-creds", "password", "hunter2")
	sec2 := &unstructured.Unstructured{}
	sec2.SetAPIVersion("v1")
	sec2.SetKind("Secret")
	sec2.SetNamespace("ns1")
	sec2.SetName("api-creds")
	data := map[string]interface{}{"token": base64.StdEncoding.EncodeToString([]byte("other-value"))}
	if err := unstructured.SetNestedMap(sec2.Object, data, "data"); err != nil {
		t.Fatalf("SetNestedMap: %v", err)
	}
	if _, err := dyn.Resource(secretGVR).Namespace("ns1").Create(context.Background(), sec2, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding second secret: %v", err)
	}

	mg := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{"namespace": "ns1", "name": "my-cr"},
			"spec": map[string]interface{}{
				"credentialsRef": map[string]interface{}{"name": "db-creds", "key": "password"},
				"apiRef":         map[string]interface{}{"name": "api-creds", "key": "token"},
			},
		},
	}
	mapping := []getter.FieldMappingItem{
		{
			InBody: "token", InCustomResource: "spec.credentialsRef",
			Resolver: &getter.FieldResolver{Type: "secretRef", SecretRef: &getter.SecretRefResolver{
				NameFromCustomResource: "spec.credentialsRef.name", KeyFromCustomResource: "spec.credentialsRef.key",
			}},
		},
		{
			InQuery: "auth", InCustomResource: "spec.apiRef",
			Resolver: &getter.FieldResolver{Type: "secretRef", SecretRef: &getter.SecretRefResolver{
				NameFromCustomResource: "spec.apiRef.name", KeyFromCustomResource: "spec.apiRef.key",
			}},
		},
	}

	resolved, err := ResolveRequestResolvers(context.Background(), dyn, mapping, mg, nil)
	if err != nil {
		t.Fatalf("ResolveRequestResolvers: %v", err)
	}
	if resolved[ResolverKey(mapping[0])] != "hunter2" {
		t.Fatalf("expected first entry to resolve to hunter2, got %v", resolved[ResolverKey(mapping[0])])
	}
	if resolved[ResolverKey(mapping[1])] != "other-value" {
		t.Fatalf("expected second entry to resolve to other-value, got %v", resolved[ResolverKey(mapping[1])])
	}
}

func TestResolveSecretRefValueNotLeakedInErrors(t *testing.T) {
	scheme := runtime.NewScheme()
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{secretGVR: "SecretList"})
	mg := mgWithCredsRef("ns1", "does-not-exist", "password")

	mapping := []getter.FieldMappingItem{
		{
			InBody: "token", InCustomResource: "spec.credentialsRef",
			Resolver: &getter.FieldResolver{
				Type: "secretRef",
				SecretRef: &getter.SecretRefResolver{
					NameFromCustomResource: "spec.credentialsRef.name",
					KeyFromCustomResource:  "spec.credentialsRef.key",
				},
			},
		},
	}
	_, err := ResolveRequestResolvers(context.Background(), dyn, mapping, mg, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("error must never contain a secret value: %v", err)
	}
}
