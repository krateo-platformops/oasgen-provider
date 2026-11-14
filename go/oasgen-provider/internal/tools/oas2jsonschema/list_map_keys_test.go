package oas2jsonschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two documents declaring the SAME array-of-objects with the SAME list-semantics extensions, one in
// Swagger 2.0 and one in OAS 3. Everything else is identical, so a difference in the generated spec
// can only come from the adapter.
//
// The pairing is not thoroughness. #249 shipped a feature that reached OAS 3 only, and #255 had to
// hand-copy validation keywords into the 2.0 path for the same reason: the v2/v3 split is where this
// package loses features silently, so a single-version test here would be the known failure mode.
const v2ListKeysDoc = `
swagger: "2.0"
info: {title: clients, version: "1.0.0"}
paths:
  /clients:
    post:
      parameters:
        - name: body
          in: body
          required: true
          schema:
            type: object
            properties:
              name: {type: string}
              protocolMappers:
                type: array
                x-kubernetes-list-type: map
                x-kubernetes-list-map-keys: [name]
                x-not-allowlisted: should-not-appear
                items:
                  type: object
                  properties:
                    name: {type: string}
                    protocol: {type: string}
      responses:
        "200": {description: ok}
`

const v3ListKeysDoc = `
openapi: 3.0.3
info: {title: clients, version: "1.0.0"}
paths:
  /clients:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              properties:
                name: {type: string}
                protocolMappers:
                  type: array
                  x-kubernetes-list-type: map
                  x-kubernetes-list-map-keys: [name]
                  x-not-allowlisted: should-not-appear
                  items:
                    type: object
                    properties:
                      name: {type: string}
                      protocol: {type: string}
      responses:
        "200": {description: ok}
`

func listKeysSpecProps(t *testing.T, spec string) map[string]any {
	t.Helper()
	doc, err := NewLibOASParser().Parse([]byte(spec))
	require.NoError(t, err)
	require.NotNil(t, doc)

	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Verbs: []Verb{{Action: "create", Method: "POST", Path: "/clients"}},
	})
	raw, _, err := g.BuildSpecSchema()
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	props, ok := out["properties"].(map[string]any)
	require.True(t, ok, "the spec schema must have properties; got %s", raw)
	return props
}

// THE POINT OF #650: the two extensions must reach the generated CRD, from BOTH document versions.
//
// Without them rest-dynamic-controller has no way to know which field identifies an element, so it
// compares an array of objects by position -- and a vendor that stores one as a set returns it in an
// arbitrary order, so the comparison reports drift no update can retire.
func TestTheListSemanticsExtensionsReachTheGeneratedSpecFromBothVersions(t *testing.T) {
	for _, tc := range []struct{ name, doc string }{
		{"swagger 2.0", v2ListKeysDoc},
		{"openapi 3", v3ListKeysDoc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			props := listKeysSpecProps(t, tc.doc)

			pm, ok := props["protocolMappers"].(map[string]any)
			require.True(t, ok, "protocolMappers is missing from the generated spec: %v", props)

			assert.Equal(t, "map", pm[ExtListType],
				"%s must carry %s=map, or the controller cannot know the array is keyed", tc.name, ExtListType)
			assert.Equal(t, []any{"name"}, pm[ExtListMapKeys],
				"%s must carry %s=[name], or the controller has no key to match on", tc.name, ExtListMapKeys)

			// THE ALLOWLIST CONTROL, and it is the reason this is an allowlist rather than a
			// pass-through. schemaToMap copies every extension verbatim, so populating Extensions
			// wholesale would put any x-* key from any consumer's document into their generated CRD --
			// and at least one consumer fingerprints theirs and treats an unattributable diff as a
			// spoiled instrument.
			assert.NotContains(t, pm, "x-not-allowlisted",
				"a non-allowlisted extension reached the CRD from %s: the allowlist is not being applied, "+
					"so every x-* key in every consumer document now changes their generated CRD", tc.name)

			// CONTROL: the sibling property must be present and UNDECORATED, or an assertion that the
			// extensions landed on protocolMappers would be satisfied by a schema that decorated
			// everything.
			n, ok := props["name"].(map[string]any)
			require.True(t, ok, "the sibling property is missing, so the assertions above prove little")
			assert.NotContains(t, n, ExtListType, "a scalar property must not acquire list semantics")
			assert.NotContains(t, n, ExtListMapKeys)
		})
	}
}

// THE TWO VERSIONS MUST AGREE EXACTLY, asserted by comparing the rendered property rather than by
// checking each against the same expectation separately. Two assertions against one expectation can
// both be wrong in the same direction; a diff of the two outputs cannot.
func TestV2AndV3AgreeOnTheListSemanticsExtensions(t *testing.T) {
	v2 := listKeysSpecProps(t, v2ListKeysDoc)["protocolMappers"]
	v3 := listKeysSpecProps(t, v3ListKeysDoc)["protocolMappers"]
	require.NotNil(t, v2, "the 2.0 document produced no protocolMappers")
	require.NotNil(t, v3, "the OAS 3 document produced no protocolMappers")

	v2m, _ := v2.(map[string]any)
	v3m, _ := v3.(map[string]any)
	assert.Equal(t, v2m[ExtListType], v3m[ExtListType], "the two versions disagree on %s", ExtListType)
	assert.Equal(t, v2m[ExtListMapKeys], v3m[ExtListMapKeys], "the two versions disagree on %s", ExtListMapKeys)
}

// A DOCUMENT THAT DECLARES NEITHER EXTENSION MUST PRODUCE NO EXTENSION KEYS AT ALL.
//
// This is what keeps the change invisible to the consumers it does not serve: most documents declare
// none of this, and their generated CRDs must not move by a byte.
func TestADocumentWithoutTheExtensionsIsUnchanged(t *testing.T) {
	plain := `
openapi: 3.0.3
info: {title: clients, version: "1.0.0"}
paths:
  /clients:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              properties:
                name: {type: string}
                tags: {type: array, items: {type: string}}
      responses:
        "200": {description: ok}
`
	props := listKeysSpecProps(t, plain)
	tags, ok := props["tags"].(map[string]any)
	require.True(t, ok, "tags is missing: %v", props)
	assert.NotContains(t, tags, ExtListType)
	assert.NotContains(t, tags, ExtListMapKeys)

	// CONTROL: the array must still be rendered as an array, or "no extensions" would be satisfied by
	// a schema that rendered nothing.
	assert.Equal(t, "array", tags["type"], "the array lost its type, so this test is not reading a schema")
}

// NORMALISATION, unit-level because the interesting inputs are malformed documents and parsing one
// per row would obscure which input produced which outcome.
//
// The lift of a bare scalar is deliberate rather than lenient: `x-kubernetes-list-map-keys: name` is a
// plausible authoring mistake whose intent is unambiguous, and emitting it unchanged would make the
// apiserver refuse the WHOLE generated CRD -- taking down every kind in it, not just this field.
func TestTheMapKeysValueIsNormalised(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   interface{}
		want []interface{}
		why  string
	}{
		{"a proper list", []interface{}{"name"}, []interface{}{"name"}, "the declared form"},
		{"two keys", []interface{}{"name", "ns"}, []interface{}{"name", "ns"}, "a composite key is legal"},
		{"a bare scalar is lifted", "name", []interface{}{"name"}, "unambiguous intent; emitting it raw would have the apiserver refuse the whole CRD"},
		{"an empty string", "", nil, "names no key"},
		{"an empty list", []interface{}{}, nil, "names no key"},
		{"a number in the list", []interface{}{"name", 7}, nil, "one bad element invalidates the declaration; a partial key would match the wrong elements"},
		{"an empty string in the list", []interface{}{"name", ""}, nil, "a blank key field cannot identify anything"},
		{"a map", map[string]interface{}{"a": "b"}, nil, "not a key list in any reading"},
		{"nil", nil, nil, "not declared"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, normaliseStringList(tc.in), tc.why)
		})
	}

	// Both outcomes must be present, or the table passes against a constant.
	var nonNil, isNil int
	for _, w := range []([]interface{}){
		{"name"}, {"name", "ns"}, {"name"}, nil, nil, nil, nil, nil, nil,
	} {
		if w == nil {
			isNil++
		} else {
			nonNil++
		}
	}
	require.NotZero(t, nonNil, "no row expects a value: this table passes against a function returning nil")
	require.NotZero(t, isNil, "no row expects nil: it passes against one that accepts anything")
}

// normaliseExtension IS the allowlist, so this is the test of the allowlist itself.
//
// The end-to-end test above asserts a non-allowlisted key does not reach the CRD, and it could not
// distinguish the two gates an earlier version of this file had — a key set AND this switch, where
// only the switch was load-bearing. Testing the gate directly is what makes the mutation
// "add a default that passes the value through" fail.
func TestOnlyTheAllowlistedExtensionsSurviveTheGate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		key     string
		decoded interface{}
		want    interface{}
		why     string
	}{
		{"list-type map", ExtListType, "map", "map", "the value #650 exists for"},
		{"list-type set", ExtListType, "set", "set", "legal for a scalar array"},
		{"list-type atomic", ExtListType, "atomic", "atomic", "legal, and the apiserver's default"},
		{
			name: "list-type with an invented value", key: ExtListType, decoded: "keyed", want: nil,
			why: "apiextensions accepts only atomic/set/map; anything else makes it refuse the WHOLE " +
				"CRD, taking down every kind in it rather than this one field",
		},
		{"list-type non-string", ExtListType, 7, nil, "not a list type in any reading"},
		{"map-keys list", ExtListMapKeys, []interface{}{"name"}, []interface{}{"name"}, "the declared form"},
		{
			name: "an extension that is not allowlisted", key: "x-anything-else", decoded: "value", want: nil,
			why: "schemaToMap copies every extension verbatim, so a key that gets past this gate lands " +
				"in every affected consumer's generated CRD",
		},
		{
			name: "a k8s-looking extension that is still not allowlisted",
			key:  "x-kubernetes-preserve-unknown-fields", decoded: true, want: nil,
			why: "being a kubernetes extension is not the criterion; being allowlisted is. This one has " +
				"real consequences and would need its own decision",
		},
		{"x-nullable is deliberately excluded", "x-nullable", true, nil,
			"the adapter already consumes it into Schema.Nullable; it has no business in the CRD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, normaliseExtension(tc.key, tc.decoded), tc.why)
		})
	}

	// BOTH OUTCOMES, counted: an all-nil table passes against a gate that rejects everything (which
	// would make #650 inert), and an all-value table against one that accepts everything.
	var allowed, rejected int
	for _, w := range []interface{}{"map", "set", "atomic", nil, nil, []interface{}{"name"}, nil, nil, nil} {
		if w == nil {
			rejected++
		} else {
			allowed++
		}
	}
	require.NotZero(t, allowed, "no row expects a value: this table passes against a gate that rejects all, "+
		"which would make the whole feature inert while looking tested")
	require.NotZero(t, rejected, "no row expects nil: it passes against a gate that accepts all")
}
