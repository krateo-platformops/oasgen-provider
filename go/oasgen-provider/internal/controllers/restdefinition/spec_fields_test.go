//go:build unit || integration

package restdefinition

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// crdWithSpec builds a CRD whose named version declares the given spec properties.
//
// THE PROPERTY TYPES ARE THE SUBJECT HERE, so they are passed explicitly rather than defaulted: a
// helper that silently used "string" everywhere would make every assertion below pass for the wrong
// reason, which is the exact shape of a vacuous gate.
func crdWithSpec(version string, props map[string]string) *apiextensionsv1.CustomResourceDefinition {
	p := map[string]apiextensionsv1.JSONSchemaProps{}
	for name, typ := range props {
		p[name] = apiextensionsv1.JSONSchemaProps{Type: typ}
	}
	return &apiextensionsv1.CustomResourceDefinition{
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: version,
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"spec": {Type: "object", Properties: p},
						},
					},
				},
			}},
		},
	}
}

// TestOnlyAScalarSpecPropertyCountsAsResolvable is the arm for the gap a consumer's question exposed.
//
// A spec property can only fill a path parameter if the runtime can render it, and the runtime's
// scalarParamValue accepts string, bool, int64, float64 and nil and SKIPS everything else. Counting
// an object- or array-typed property here would suppress a CORRECT flag -- a kind that genuinely
// cannot answer existence would look answerable.
func TestOnlyAScalarSpecPropertyCountsAsResolvable(t *testing.T) {
	got := specPropertiesOf(crdWithSpec("v1alpha1", map[string]string{
		"name":        "string",
		"replicas":    "integer",
		"ratio":       "number",
		"enabled":     "boolean",
		"metadataMap": "object",
		"tags":        "array",
		"untyped":     "",
	}), "v1alpha1")
	require.NotNil(t, got, "a well-formed CRD returned nil, so every assertion below would be vacuous")

	for _, scalar := range []string{"name", "replicas", "ratio", "enabled"} {
		assert.True(t, got[scalar], "%q is a scalar and must count as resolvable from the spec", scalar)
	}
	for _, composite := range []string{"metadataMap", "tags"} {
		assert.False(t, got[composite],
			"%q is composite: the runtime's scalarParamValue SKIPS it, so counting it here would "+
				"suppress a correct flag and hide a kind that cannot answer existence", composite)
	}
	assert.False(t, got["untyped"],
		"a property with NO declared type could be an object, so it must not count -- unknown stays "+
			"conservative here exactly as a nil set does at the function level")

	// SETUP CONTROL: the two verdicts must be distinguishable by the type alone. Both groups above
	// are present in the same schema under the same version, so a result that got the wrong version
	// or the wrong schema branch could not produce this split by coincidence.
	assert.Len(t, got, 4, "expected exactly the four scalars, got %v", got)
}

// TestSpecPropertiesOfStaysConservative pins every path that must answer "unknown" rather than
// "none". Each of these returning an EMPTY set instead of nil would assert that the kind has no spec
// properties, which flags every status-mapped parameter including the ones that do resolve.
func TestSpecPropertiesOfStaysConservative(t *testing.T) {
	t.Run("a version that is not served", func(t *testing.T) {
		assert.Nil(t, specPropertiesOf(crdWithSpec("v1alpha1", map[string]string{"name": "string"}), "v1beta1"),
			"the asked-for version is absent; mixing another version's schema could suppress a correct flag")
	})

	t.Run("no schema at all", func(t *testing.T) {
		c := crdWithSpec("v1alpha1", map[string]string{"name": "string"})
		c.Spec.Versions[0].Schema = nil
		assert.Nil(t, specPropertiesOf(c, "v1alpha1"))
	})

	t.Run("no spec property in the schema", func(t *testing.T) {
		c := crdWithSpec("v1alpha1", map[string]string{"name": "string"})
		delete(c.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties, "spec")
		assert.Nil(t, specPropertiesOf(c, "v1alpha1"))
	})

	t.Run("a spec with zero properties", func(t *testing.T) {
		assert.Nil(t, specPropertiesOf(crdWithSpec("v1alpha1", map[string]string{}), "v1alpha1"),
			"indistinguishable from a pruned or unexpected shape, so it must read as unknown")
	})

	t.Run("a spec whose only properties are composite", func(t *testing.T) {
		// NOT NIL, AND THE DISTINCTION MATTERS: the schema was read successfully and genuinely offers
		// nothing that can fill a parameter. That is an answer, not an absence of one -- so every
		// status-mapped parameter on such a kind SHOULD be flagged.
		got := specPropertiesOf(crdWithSpec("v1alpha1", map[string]string{"tags": "array"}), "v1alpha1")
		require.NotNil(t, got, "a readable schema with only composite properties is a real answer, not unknown")
		assert.Empty(t, got)
	})

	// POSITIVE CONTROL: the happy path still works, or every nil above could be the helper being
	// broken rather than the branch under test.
	assert.True(t, specPropertiesOf(crdWithSpec("v1alpha1", map[string]string{"name": "string"}), "v1alpha1")["name"])
}
