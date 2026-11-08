//go:build unit

package deploy

import (
	"testing"

	"github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func secretRefVerbs(paths ...string) []v1alpha1.VerbsDescription {
	fm := make([]v1alpha1.FieldMappingItem, 0, len(paths))
	for _, p := range paths {
		fm = append(fm, v1alpha1.FieldMappingItem{
			InBody: "x",
			Resolver: &v1alpha1.FieldResolver{
				Type:      "secretRef",
				SecretRef: &v1alpha1.SecretRefResolver{NameFromCustomResource: p, KeyFromCustomResource: p},
			},
		})
	}
	return []v1alpha1.VerbsDescription{{Action: "create", FieldMapping: fm}}
}

func keycloakUser(ns string, creds []any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"credentials": creds},
	}}
	u.SetNamespace(ns)
	u.SetName("u1")
	return u
}

// TestTheKeycloakUserAcceptanceCase is #510's acceptance case, supplied by the consumer whose kind the
// public demo's gate 1 depends on: a KeycloakUser with both credentials in two different Secrets gets a
// Role naming exactly those two.
//
// It exercises PREDICATE resolution, which my own design note wrongly said was not needed on day one --
// that note was written from one consumer's plain-dotted paths, and this consumer's are predicate paths.
// A collector built to the note would have granted this kind no Role at all.
func TestTheKeycloakUserAcceptanceCase(t *testing.T) {
	verbs := secretRefVerbs(
		"spec.credentials[?type=password].valueSecretRef.name",
		"spec.credentials[?type=otp].secretDataSecretRef.name",
	)
	cr := keycloakUser("kc", []any{
		map[string]any{"type": "password", "valueSecretRef": map[string]any{"name": "pw-secret", "key": "password"}},
		map[string]any{"type": "otp", "secretDataSecretRef": map[string]any{"name": "otp-secret", "key": "secretData"}},
	})

	got, err := CollectSecretRefNames(verbs, cr)
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"kc": {"otp-secret", "pw-secret"}}, got,
		"exactly those two, sorted; grouped by the CR's own namespace because that is where the controller "+
			"reads them")
}

// TestAPasswordOnlyUserIsNotBlockedByTheUnusedOtpPath is the leniency, and the reason for it.
func TestAPasswordOnlyUserIsNotBlockedByTheUnusedOtpPath(t *testing.T) {
	verbs := secretRefVerbs(
		"spec.credentials[?type=password].valueSecretRef.name",
		"spec.credentials[?type=otp].secretDataSecretRef.name",
	)
	for _, tc := range []struct {
		name  string
		creds []any
		why   string
	}{
		{name: "the otp entry is absent", creds: []any{
			map[string]any{"type": "password", "valueSecretRef": map[string]any{"name": "pw-secret"}},
		}, why: "found=false, err=nil -- a password-only user"},
		{name: "the otp ref is null", creds: []any{
			map[string]any{"type": "password", "valueSecretRef": map[string]any{"name": "pw-secret"}},
			map[string]any{"type": "otp", "secretDataSecretRef": nil},
		}, why: "fieldpath.Get ERRORS on a null intermediate; handled explicitly rather than generically"},
		{name: "the otp ref is a scalar", creds: []any{
			map[string]any{"type": "password", "valueSecretRef": map[string]any{"name": "pw-secret"}},
			map[string]any{"type": "otp", "secretDataSecretRef": "not-a-map"},
		}, why: "a shape error, also lenient here"},
		{name: "the name is an empty string", creds: []any{
			map[string]any{"type": "password", "valueSecretRef": map[string]any{"name": "pw-secret"}},
			map[string]any{"type": "otp", "secretDataSecretRef": map[string]any{"name": ""}},
		}, why: "an empty name would scope a Role to a Secret that cannot exist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CollectSecretRefNames(verbs, keycloakUser("kc", tc.creds))
			require.NoError(t, err, "the collector must never fail on CR DATA: %s", tc.why)
			assert.Equal(t, map[string][]string{"kc": {"pw-secret"}}, got,
				"the password Role must still be granted: %s", tc.why)
		})
	}
}

// TestADuplicatePredicateContributesNothing pins the one case where leniency is doing real work: the
// walker refuses a non-unique match, deliberately, and the collector must not turn that into a Role
// naming an arbitrary one of them.
func TestADuplicatePredicateContributesNothing(t *testing.T) {
	verbs := secretRefVerbs("spec.credentials[?type=otp].secretDataSecretRef.name")
	cr := keycloakUser("kc", []any{
		map[string]any{"type": "otp", "secretDataSecretRef": map[string]any{"name": "first"}},
		map[string]any{"type": "otp", "secretDataSecretRef": map[string]any{"name": "second"}},
	})
	got, err := CollectSecretRefNames(verbs, cr)
	require.NoError(t, err)
	assert.Nil(t, got,
		"the walker refuses an ambiguous selector; granting EITHER name would be the wrong-element bug "+
			"selectors exist to eliminate, and granting BOTH would widen the Role beyond what any single "+
			"resolution needs")
}

// TestAnUnparseablePathIsADefinitionDefect is the one error the collector DOES return, and the line
// between the two.
func TestAnUnparseablePathIsADefinitionDefect(t *testing.T) {
	got, err := CollectSecretRefNames(secretRefVerbs("spec..credentials"), keycloakUser("kc", nil))
	require.Error(t, err, "the path comes from the RestDefinition, so every CR of the kind is affected")
	assert.Contains(t, err.Error(), "definition defect")
	assert.Nil(t, got)
}

// TestOnlySecretRefResolversAreCollected -- a dependentLookup resolver names no Secret, and sweeping one
// in would scope a Role to a value that is not a Secret name at all.
func TestOnlySecretRefResolversAreCollected(t *testing.T) {
	verbs := []v1alpha1.VerbsDescription{{Action: "create", FieldMapping: []v1alpha1.FieldMappingItem{
		{InBody: "a", Resolver: &v1alpha1.FieldResolver{Type: "dependentLookup"}},
		// A dependentLookup resolver that ALSO carries a SecretRef. Nonsensical as a definition, and it is
		// the only input that tests the TYPE guard: a mutation dropping the type check survived, because
		// every other non-secretRef row in this test has a nil SecretRef and so was excluded by the nil
		// check instead. A surviving mutant meant the type guard was asserted nowhere.
		{InBody: "d", Resolver: &v1alpha1.FieldResolver{
			Type: "dependentLookup",
			// A DIFFERENT path, naming a DIFFERENT Secret. The first version of this row reused the
			// secretRef row's path, so dropping the type check produced the same DEDUPED result and the
			// mutant survived: the input could not separate "collected" from "excluded".
			SecretRef: &v1alpha1.SecretRefResolver{NameFromCustomResource: "spec.credentials[?type=otp].secretDataSecretRef.name"},
		}},
		{InBody: "b"},
		{InBody: "c", Resolver: &v1alpha1.FieldResolver{
			Type:      "secretRef",
			SecretRef: &v1alpha1.SecretRefResolver{NameFromCustomResource: "spec.credentials[?type=password].valueSecretRef.name"},
		}},
	}}}
	cr := keycloakUser("kc", []any{
		map[string]any{"type": "password", "valueSecretRef": map[string]any{"name": "pw-secret"}},
		// Resolvable and named differently, so collecting the dependentLookup row would ADD a name.
		map[string]any{"type": "otp", "secretDataSecretRef": map[string]any{"name": "otp-not-to-be-collected"}},
	})
	got, err := CollectSecretRefNames(verbs, cr)
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"kc": {"pw-secret"}}, got)
}

// TestACrWithoutANamespaceIsRefused -- the namespace is where the Role goes, so there is nothing sensible
// to do without one, and silently returning nothing would grant no access with no explanation.
func TestACrWithoutANamespaceIsRefused(t *testing.T) {
	cr := keycloakUser("", []any{map[string]any{"type": "password", "valueSecretRef": map[string]any{"name": "pw"}}})
	_, err := CollectSecretRefNames(secretRefVerbs("spec.credentials[?type=password].valueSecretRef.name"), cr)
	assert.Error(t, err)
}
