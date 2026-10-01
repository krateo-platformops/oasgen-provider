package policy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The policy is per-GROUP, not a cluster singleton, because oasgen generates CRDs into arbitrary groups
// declared per RestDefinition. Matching apiGroups ["*"] instead would stamp this label onto every object
// in the cluster, which is not ours to do.
func TestPolicyNameIsDerivedFromTheGroup(t *testing.T) {
	cases := map[string]string{
		"github.krateo.io":          "krateo-oas-version-github-krateo-io",
		"arubacloud.ogen.krateo.io": "krateo-oas-version-arubacloud-ogen-krateo-io",
		"petstore.example.io":       "krateo-oas-version-petstore-example-io",
		"Mixed.Case.IO":             "krateo-oas-version-mixed-case-io",
	}
	for group, want := range cases {
		assert.Equal(t, want, PolicyName(group), "group %q", group)
	}

	// Two RestDefinitions in the same group must converge on ONE policy rather than fight over it.
	assert.Equal(t, PolicyName("github.krateo.io"), PolicyName("github.krateo.io"))
	assert.NotEqual(t, PolicyName("a.krateo.io"), PolicyName("b.krateo.io"))
}

// TestMutationIsAJSONPatch is the one that matters, and it is not a style assertion.
//
// ApplyConfiguration performs a structured merge, converting the WHOLE incoming object to its typed form
// first. That conversion fails whenever any unrelated field holds a value its schema does not accept —
// including values Kubernetes itself considers valid — and with failurePolicy: Fail the write is then
// DENIED. core-provider shipped exactly that and a composition whose schema typed cpu as numeric but
// whose value was the Quantity string "200m" wedged an entire install. A label stamp took out a deploy.
//
// A JSON Patch touches only the path it names, so an unrelated field's representation cannot deny the
// request.
func TestMutationIsAJSONPatchAndTouchesOnlyTheLabel(t *testing.T) {
	p, _ := objects("github.krateo.io")

	spec := p.Object["spec"].(map[string]any)
	muts := spec["mutations"].([]any)
	require.Len(t, muts, 1)
	m := muts[0].(map[string]any)

	assert.Equal(t, "JSONPatch", m["patchType"],
		"ApplyConfiguration converts the whole object and can DENY a write over an unrelated field")
	assert.NotContains(t, m, "applyConfiguration")

	expr := m["jsonPatch"].(map[string]any)["expression"].(string)
	assert.Contains(t, expr, VersionLabel)
	assert.Contains(t, expr, "request.requestKind.version",
		"the label must record the version of the write ENDPOINT, which is the whole point")
	assert.Contains(t, expr, "jsonpatch.escapeKey",
		"the label key contains a / and needs RFC 6901 escaping; hand-writing ~1 breaks on a rename")

	// Both branches: a JSON Patch `add` to /metadata/labels/<key> fails when /metadata/labels is absent.
	assert.Contains(t, expr, "has(object.metadata.labels)")
	assert.True(t, strings.Count(expr, "JSONPatch{") == 2,
		"an object with no labels at all needs the map created in one step")
}

// Scope: only the declared group, and only writes.
func TestPolicyMatchesOnlyItsOwnGroup(t *testing.T) {
	p, _ := objects("github.krateo.io")
	rules := p.Object["spec"].(map[string]any)["matchConstraints"].(map[string]any)["resourceRules"].([]any)
	require.Len(t, rules, 1)
	r := rules[0].(map[string]any)

	assert.Equal(t, []any{"github.krateo.io"}, r["apiGroups"],
		`["*"] would stamp this label onto every object in the cluster`)
	assert.Equal(t, []any{"CREATE", "UPDATE"}, r["operations"])
	assert.Equal(t, []any{"*"}, r["apiVersions"], "every served version writes the label naming itself")
}

// failurePolicy: an instance admitted WITHOUT the label is invisible to its version's controller — a
// silent non-reconcile. Refusing the write is the better failure.
func TestFailurePolicyIsFail(t *testing.T) {
	p, _ := objects("x.krateo.io")
	assert.Equal(t, "Fail", p.Object["spec"].(map[string]any)["failurePolicy"])
}

func TestBindingReferencesThePolicy(t *testing.T) {
	p, b := objects("github.krateo.io")
	assert.Equal(t, p.GetName(), b.Object["spec"].(map[string]any)["policyName"])
	assert.Equal(t, "MutatingAdmissionPolicyBinding", b.GetKind())
}

// A cluster below 1.36 has no MutatingAdmissionPolicy API. That must not fail an install the chart
// permits (floor 1.33) — RDC stamps the label itself there.
func TestIsUnsupportedRecognisesAMissingAPI(t *testing.T) {
	for _, msg := range []string{
		`no matches for kind "MutatingAdmissionPolicy" in version "admissionregistration.k8s.io/v1"`,
		"the server could not find the requested resource",
		"no kind is registered for the type v1.MutatingAdmissionPolicy",
	} {
		assert.True(t, IsUnsupported(errString(msg)), msg)
	}
	assert.False(t, IsUnsupported(nil))
	assert.False(t, IsUnsupported(errString("connection refused")),
		"a transport failure is not a missing API and must not be swallowed")
}

type errString string

func (e errString) Error() string { return string(e) }
