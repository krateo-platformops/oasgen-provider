//go:build unit || integration

package oas2jsonschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// typeOf returns the declared type of one property, or "" when absent.
func typeOf(t *testing.T, s *Schema, name string) string {
	t.Helper()
	require.NotNil(t, s, "no base schema was produced at all")
	for _, p := range s.Properties {
		if p.Name == name {
			require.NotNil(t, p.Schema, "property %q has no schema", name)
			require.Len(t, p.Schema.Type, 1, "property %q should carry exactly one type", name)
			return p.Schema.Type[0]
		}
	}
	return ""
}

// TestADeclaredObserveStatusSchemaTypesTheFields is issue 889: an observeApiRef kind has no get, no
// findby and no correlation item, so every additionalStatusFields entry was emitted as `type: string`.
//
// THE CONSUMER CASE, verbatim from keycloak-provider-kog: {found: boolean, clientUuid: string,
// clientScopeId: string}. A boolean arriving as "true" is the defect.
func TestADeclaredObserveStatusSchemaTypesTheFields(t *testing.T) {
	g := &OASSchemaGenerator{resourceConfig: &ResourceConfig{
		ObserveDelegated: true,
		ObserveStatusSchema: map[string]string{
			"found":         "boolean",
			"clientUuid":    "string",
			"clientScopeId": "string",
		},
	}}

	schema, err := g.getBaseSchemaForStatus()
	require.NoError(t, err)

	assert.Equal(t, "boolean", typeOf(t, schema, "found"),
		"a declared boolean must not be emitted as a string; that is the whole defect")
	assert.Equal(t, "string", typeOf(t, schema, "clientUuid"))
	assert.Equal(t, "string", typeOf(t, schema, "clientScopeId"))
	assert.Equal(t, []string{"object"}, schema.Type)
}

// TestTheDeclaredSchemaWINSOverAGetResponse pins the ORDERING decision, which is the part of this change
// that is a judgement rather than a mechanism.
//
// A kind MAY declare observeApiRef and a get verb: #887's rule made observeApiRef+notFoundExpr an
// ALTERNATIVE to get/findby, not a replacement. When both exist the observe is DELEGATED, so the get
// response describes a read that never happens -- typing status from it would type the status of a
// request nobody sends.
func TestTheDeclaredSchemaWINSOverAGetResponse(t *testing.T) {
	g := &OASSchemaGenerator{resourceConfig: &ResourceConfig{
		// A get verb IS declared. Without the declared schema this would be consulted, and with no
		// document loaded it would fail -- which is itself the proof that the declared branch returned
		// before reaching it.
		Verbs:               []Verb{{Action: ActionGet, Path: "/things/{id}", Method: "GET"}},
		ObserveDelegated:    true,
		ObserveStatusSchema: map[string]string{"found": "boolean"},
	}}

	schema, err := g.getBaseSchemaForStatus()
	require.NoError(t, err,
		"the declared schema must short-circuit before the get verb is resolved; an error here means "+
			"the get path ran first")
	assert.Equal(t, "boolean", typeOf(t, schema, "found"))
}

// TestAnAbsentDeclarationFallsThrough is what keeps this field purely additive.
//
// Nil must not become an empty object schema: an empty one would REPLACE the get/findby sources and
// silently empty the status of every existing resource. The distinction between "declared nothing" and
// "declared an empty set" is the whole safety property.
func TestAnAbsentDeclarationFallsThrough(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared map[string]string
	}{
		{"nil map", nil},
		{"empty map", map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &OASSchemaGenerator{resourceConfig: &ResourceConfig{
				ObserveDelegated:    true,
				ObserveStatusSchema: tc.declared,
			}}
			assert.Nil(t, g.declaredObserveStatusSchema(),
				"an absent declaration must return nil so the caller falls through to the three "+
					"existing sources; returning an empty schema would empty every status")
		})
	}
}

// TestThePropertyOrderIsDeterministic matters because Schema.Properties is a slice and a generated CRD
// that reorders between identical inputs is an unattributable diff for a consumer who fingerprints
// theirs.
//
// Go map iteration is deliberately randomised, so an unsorted implementation fails this intermittently
// rather than never -- which is why the assertion runs the build repeatedly rather than once.
func TestThePropertyOrderIsDeterministic(t *testing.T) {
	declared := map[string]string{"zeta": "string", "alpha": "boolean", "mid": "integer", "beta": "number"}
	want := []string{"alpha", "beta", "mid", "zeta"}

	for i := 0; i < 50; i++ {
		g := &OASSchemaGenerator{resourceConfig: &ResourceConfig{ObserveStatusSchema: declared}}
		schema := g.declaredObserveStatusSchema()
		require.NotNil(t, schema)
		got := make([]string, 0, len(schema.Properties))
		for _, p := range schema.Properties {
			got = append(got, p.Name)
		}
		require.Equal(t, want, got, "property order must be sorted, not map order (iteration %d)", i)
	}
}
