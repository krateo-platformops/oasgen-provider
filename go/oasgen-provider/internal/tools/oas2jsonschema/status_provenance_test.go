package oas2jsonschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStatusWarningNamesTheStanzaItCameFrom covers a diagnostics defect that cost real debugging time.
// The status schema is built from identifiers AND additionalStatusFields combined, but every warning was
// labelled "additionalStatusFields.<name>", so an IDENTIFIER that failed to resolve pointed at a stanza
// that does not contain it.
//
// Found while generating a dispatch resource: `workflowId` is an identifier and a path parameter, so it
// is absent from the correlated-run response and warns. Removing it from additionalStatusFields -- where
// it had never been -- changed nothing, and the warning persisted.
func TestStatusWarningNamesTheStanzaItCameFrom(t *testing.T) {
	g := &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		resourceConfig: &ResourceConfig{
			Identifiers:            []string{"workflowId"},
			AdditionalStatusFields: []string{"absentExtra"},
		},
	}

	// A response carrying neither name, so both fall into the not-found branch.
	responseSchema := &Schema{Type: []string{"object"}, Properties: []Property{
		{Name: "unrelated", Schema: &Schema{Type: []string{"string"}}},
	}}

	_, warnings := g.composeStatusSchema(statusFieldsFor(g.resourceConfig), responseSchema)
	require.Len(t, warnings, 2, "both names are absent from the response, so both warn")

	paths := map[string]bool{}
	for _, w := range warnings {
		sge, ok := w.(SchemaGenerationError)
		require.True(t, ok, "warning should be a SchemaGenerationError, got %T", w)
		paths[sge.Path] = true
	}

	assert.True(t, paths["identifiers.workflowId"],
		"an identifier must be reported against the identifiers stanza; got %v", paths)
	assert.True(t, paths["additionalStatusFields.absentExtra"],
		"an additionalStatusFields entry must still be reported against its own stanza; got %v", paths)
	assert.False(t, paths["additionalStatusFields.workflowId"],
		"the identifier must NOT be reported against a stanza that does not contain it")
}

// TestStatusFieldsForDoesNotWriteThroughIdentifiers pins the aliasing fix. The combined list was built
// with append(Identifiers, AdditionalStatusFields...), which writes into Identifiers' OWN backing array
// whenever that array has spare capacity, replacing whatever the owner held after the append point.
//
// Nothing depends on it today because Identifiers arrives from unmarshalling with cap == len, so the
// append always allocates -- but that is a property of the caller, not of this function. The test gives
// Identifiers a backing array with room to spare and a sibling view over the rest of it, which is the
// memory a write-through corrupts.
func TestStatusFieldsForDoesNotWriteThroughIdentifiers(t *testing.T) {
	backing := []string{"id", "SENTINEL-1", "SENTINEL-2", "SENTINEL-3"}
	identifiers := backing[:1] // len 1, cap 4 -- an append lands on backing[1:]
	sibling := backing[1:4]    // what a write-through would overwrite

	rc := &ResourceConfig{
		Identifiers:            identifiers,
		AdditionalStatusFields: []string{"first", "second", "third"},
	}

	combined := statusFieldsFor(rc)
	assert.Equal(t, []string{"id", "first", "second", "third"}, combined,
		"identifiers first, then additionalStatusFields")

	assert.Equal(t, []string{"SENTINEL-1", "SENTINEL-2", "SENTINEL-3"}, sibling,
		"combining the stanzas must not write into the Identifiers slice's backing array")
	assert.Equal(t, []string{"id"}, identifiers, "and must not change Identifiers itself")
}

// TestStatusFieldsForNilConfig guards the nil path, since the helper is reachable from a generator built
// without a resource config in tests.
func TestStatusFieldsForNilConfig(t *testing.T) {
	assert.Nil(t, statusFieldsFor(nil))
}
