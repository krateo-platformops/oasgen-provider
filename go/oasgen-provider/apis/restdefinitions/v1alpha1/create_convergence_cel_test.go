//go:build unit || integration

package v1alpha1

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// resourceSchema returns the `resource` subschema of the SHIPPED CRD, so these arms evaluate the rules
// the apiserver will actually enforce rather than a copy of them in the test.
func resourceSchema(t *testing.T) map[string]any {
	t.Helper()

	raw, err := os.ReadFile(generatedCRDPath)
	require.NoError(t, err, "the generated CRD must be readable; run `make generate`")

	var crd map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &crd))

	versions, _ := crd["spec"].(map[string]any)["versions"].([]any)
	require.NotEmpty(t, versions)

	for _, v := range versions {
		vm := v.(map[string]any)
		if vm["name"] != Version {
			continue
		}
		node := vm["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)
		for _, step := range []string{"properties", "spec", "properties", "resource"} {
			next, ok := node[step].(map[string]any)
			require.True(t, ok, "walking the CRD stopped at %q", step)
			node = next
		}
		return node
	}
	t.Fatalf("version %q not found in the generated CRD", Version)
	return nil
}

// TestCreateConvergenceAcceptsADelegatedObserve pins the rule that a consumer's kind was blocked by.
//
// TWO ADJACENT RULES CONTRADICTED EACH OTHER AND NOTHING CAUGHT IT. One requires
// observeApiRef.notFoundExpr when createApiRef meets observeApiRef -- because that predicate is what
// lets a delegated observe trigger a create at all. The other then demanded a get or findby verb and
// ignored notFoundExpr entirely, so a definition satisfying the first was rejected by the second and
// the composition the first exists to enable could not be expressed.
//
// FOUND BY THE CONSUMER IT BLOCKED, not by a test: keycloak-config-kog's client-scope assignment has
// no per-item read -- the vendor answers 404 for the assignment URL whether or not the scope is
// assigned -- so get and findby are both unavailable while notFoundExpr over the client's whole
// optional-scope list answers the question exactly.
//
// AND DO NOT "FIX" SUCH A KIND BY GIVING IT A get ANYWAY. A get pointed at a URL that always answers
// 200 is worse than none: externalResourceStillExists would read the resource as verifiably PRESENT
// after a successful delete and hold the finalizer forever (issue 885).
func TestCreateConvergenceAcceptsADelegatedObserve(t *testing.T) {
	rules := rulesOf(t, resourceSchema(t))
	require.NotEmpty(t, rules, "no CEL rules on the resource schema: it would admit everything")

	t.Run("createApiRef with observeApiRef.notFoundExpr and NO get or findby is admitted", func(t *testing.T) {
		msg := evalRules(t, rules, map[string]any{
			"kind":             "KeycloakClientOptionalClientScope",
			"identifiers":      []any{"realm", "clientId", "clientScope"},
			"createApiRef":     map[string]any{"name": "assign", "namespace": "krateo-system"},
			"deleteApiRef":     map[string]any{"name": "unassign", "namespace": "krateo-system"},
			"observeApiRef":    map[string]any{"name": "list", "namespace": "krateo-system", "notFoundExpr": `[.status.scopes[].id] | index(.spec.clientScope) == null`},
			"verbsDescription": []any{},
		})
		require.Empty(t, msg, "the consumer's shape must be admitted; it was rejected by: %s", msg)
	})

	// POSITIVE CONTROL: the rule's purpose must survive. A createApiRef with NO convergence check of
	// any kind is still refused, or this change would have replaced a contradiction with a hole.
	t.Run("createApiRef with no get, no findby and no observeApiRef is still REFUSED", func(t *testing.T) {
		msg := evalRules(t, rules, map[string]any{
			"kind":             "NoWayToTell",
			"identifiers":      []any{"id"},
			"createApiRef":     map[string]any{"name": "create", "namespace": "krateo-system"},
			"verbsDescription": []any{},
		})
		require.NotEmpty(t, msg, "a createApiRef with no way to verify convergence must be refused")
		require.Contains(t, msg, "level-based convergence",
			"refused, but by an unrelated rule -- this arm would pass for the wrong reason: %s", msg)
	})

	// POSITIVE CONTROL: the NEIGHBOURING rule must still bite. An observeApiRef without notFoundExpr
	// cannot trigger a create, and widening the convergence rule must not have made that admissible.
	t.Run("createApiRef with observeApiRef but NO notFoundExpr is still REFUSED", func(t *testing.T) {
		msg := evalRules(t, rules, map[string]any{
			"kind":             "ObserveWithoutNotFound",
			"identifiers":      []any{"id"},
			"createApiRef":     map[string]any{"name": "create", "namespace": "krateo-system"},
			"observeApiRef":    map[string]any{"name": "list", "namespace": "krateo-system"},
			"verbsDescription": []any{},
		})
		require.NotEmpty(t, msg, "observeApiRef without notFoundExpr must still be refused")
		require.Contains(t, msg, "notFoundExpr",
			"refused, but not by the notFoundExpr rule: %s", msg)
	})

	// SETUP CONTROL: a get verb alone still satisfies the rule, so the arms above are about the
	// delegated-observe branch and not about the rule having stopped working altogether.
	t.Run("createApiRef with a get verb is admitted, as before", func(t *testing.T) {
		msg := evalRules(t, rules, map[string]any{
			"kind":         "Ordinary",
			"identifiers":  []any{"id"},
			"createApiRef": map[string]any{"name": "create", "namespace": "krateo-system"},
			"verbsDescription": []any{
				map[string]any{"action": "get", "method": "GET", "path": "/things/{id}"},
			},
		})
		require.Empty(t, msg, "an ordinary get-verb kind must still be admitted; rejected by: %s", msg)
	})
}
