package oas2jsonschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func arr(items *Schema) *Schema {
	return &Schema{Type: []string{"array"}, Items: items}
}

func obj(props ...Property) *Schema {
	return &Schema{Type: []string{"object"}, Properties: props}
}

func prop(name string, s *Schema) Property { return Property{Name: name, Schema: s} }

func scalar(t string) *Schema { return &Schema{Type: []string{t}} }

// item is the shape the #110 report reproduced against: an identifier nested inside the item, typed
// something other than string. Both facts matter -- nesting is what #106/#109 fixed, the TYPE is what
// stayed broken, because the envelope was never unwrapped so nothing downstream ever saw this schema.
func item() *Schema {
	return obj(
		prop("metadata", obj(
			prop("name", scalar("string")),
			prop("serial", scalar("integer")),
		)),
		prop("state", scalar("string")),
	)
}

// TestUnwrapFindByItems_Envelope is the fix: an envelope response must yield the ITEM, not the envelope.
//
// Unwrapping only a bare array meant that for {total, values:[...]} -- what most real APIs return -- the
// base schema for status and selectors was the envelope. Identifiers were then looked for one level
// above where they live, so their types degraded to string and CodeIdentifierNotResolvable fired
// identically for a valid identifier and a genuinely absent one (#110).
func TestUnwrapFindByItems_Envelope(t *testing.T) {
	envelope := obj(
		prop("total", scalar("integer")),
		prop("values", arr(item())),
	)

	got, err := unwrapFindByItems(envelope, "")
	require.NoError(t, err)

	md := findProp(got, "metadata")
	require.NotNil(t, md, "the item's own properties must be visible, not the envelope's")
	serial := findProp(md, "serial")
	require.NotNil(t, serial)
	assert.Equal(t, []string{"integer"}, serial.Type,
		"a nested non-string identifier must keep its real type; emitting string gets the CR rejected by the API server")

	assert.Nil(t, findProp(got, "total"), "the envelope's own fields must not leak into the item schema")
}

// A bare array response is the case that always worked. It must keep working unchanged.
func TestUnwrapFindByItems_BareArray(t *testing.T) {
	got, err := unwrapFindByItems(arr(item()), "")
	require.NoError(t, err)
	require.NotNil(t, findProp(got, "metadata"))
}

// TestUnwrapFindByItems_AmbiguousIsRefused is the forbidding test.
//
// Two array properties have no single obvious collection. The runtime side used to take "the first value
// that is a list" while ranging a Go map -- and map order is randomised, so it searched a different array
// on different reconciles. Refusing at GENERATION time is the whole point: it happens once, while the
// author can still fix it, instead of forever and invisibly. A guess that loses reports not-found, and
// the reconciler creates on not-found.
func TestUnwrapFindByItems_AmbiguousIsRefused(t *testing.T) {
	jsonapi := obj(
		prop("data", arr(item())),
		prop("included", arr(obj(prop("id", scalar("string"))))),
	)

	got, err := unwrapFindByItems(jsonapi, "")
	require.Error(t, err, "two candidate collections must not be resolved by picking one")
	assert.Nil(t, got)

	var ambiguous *ErrAmbiguousCollection
	require.ErrorAs(t, err, &ambiguous)
	assert.Equal(t, []string{"data", "included"}, ambiguous.Candidates,
		"the candidates must be named, and in a stable order -- the author has to know what to choose between")
	assert.Contains(t, err.Error(), "itemsPath", "and be told the field that resolves it")
}

// Declaring itemsPath resolves the ambiguity, in either direction.
func TestUnwrapFindByItems_DeclaredPath(t *testing.T) {
	jsonapi := obj(
		prop("data", arr(item())),
		prop("included", arr(obj(prop("id", scalar("string"))))),
	)

	got, err := unwrapFindByItems(jsonapi, "data")
	require.NoError(t, err)
	require.NotNil(t, findProp(got, "metadata"), "itemsPath=data selects the real collection")

	got, err = unwrapFindByItems(jsonapi, "included")
	require.NoError(t, err)
	require.NotNil(t, findProp(got, "id"), "itemsPath=included selects the other one just as readily")
	assert.Nil(t, findProp(got, "metadata"))
}

func TestUnwrapFindByItems_NestedDeclaredPath(t *testing.T) {
	nested := obj(prop("result", obj(prop("values", arr(item())))))
	got, err := unwrapFindByItems(nested, "result.values")
	require.NoError(t, err)
	require.NotNil(t, findProp(got, "metadata"))
}

// A declared path that is wrong is an error, NOT a fallback to inference. The author stated where the
// collection is; quietly searching somewhere else would hide the wrong statement behind a result that
// looks fine, at the one moment when it is cheap to correct.
func TestUnwrapFindByItems_WrongDeclaredPathIsAnError(t *testing.T) {
	envelope := obj(
		prop("total", scalar("integer")),
		prop("values", arr(item())),
	)

	_, err := unwrapFindByItems(envelope, "items")
	require.Error(t, err, "a path naming a property that is not there must fail")
	assert.Contains(t, err.Error(), "values", "and should say what IS available")

	_, err = unwrapFindByItems(envelope, "total")
	require.Error(t, err, "a path naming a non-array must fail")
}

// A findby whose 200 is a single object is legitimate -- the controller treats such a body as a list of
// one -- so it is not an error and the object itself is the item.
func TestUnwrapFindByItems_NoArrayIsTheItemItself(t *testing.T) {
	single := obj(prop("id", scalar("string")), prop("state", scalar("string")))
	got, err := unwrapFindByItems(single, "")
	require.NoError(t, err)
	require.NotNil(t, findProp(got, "id"))
}

// findProp returns a named property's schema, or nil.
func findProp(s *Schema, name string) *Schema {
	if s == nil {
		return nil
	}
	for _, p := range s.Properties {
		if p.Name == name {
			return p.Schema
		}
	}
	return nil
}
