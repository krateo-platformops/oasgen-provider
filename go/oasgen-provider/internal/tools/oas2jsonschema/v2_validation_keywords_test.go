package oas2jsonschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two documents declaring the SAME constraints on the same parameters, one in Swagger 2.0 and one in
// OAS 3. The 2.0 form writes the facets directly on the parameter object; the 3.x form wraps them in a
// `schema`. Everything else about the two is deliberately identical, so a difference in the generated
// spec can only come from the adapter.
const v2KeywordsDoc = `
swagger: "2.0"
info: {title: things, version: "1.0.0"}
paths:
  /things/{id}:
    get:
      parameters:
        - name: id
          in: path
          required: true
          type: string
          pattern: "^[a-z]+$"
          minLength: 3
          maxLength: 10
        - name: count
          in: query
          type: integer
          minimum: 1
          maximum: 100
          exclusiveMaximum: true
          multipleOf: 5
        - name: tags
          in: query
          type: array
          minItems: 1
          maxItems: 5
          uniqueItems: true
          items:
            type: string
            pattern: "^t-"
            maxLength: 4
      responses:
        "200": {description: ok, schema: {type: object, properties: {id: {type: string}}}}
    post:
      parameters:
        - name: id
          in: path
          required: true
          type: string
          pattern: "^[a-z]+$"
          minLength: 3
          maxLength: 10
      responses:
        "200": {description: ok, schema: {type: object, properties: {id: {type: string}}}}
`

const v3KeywordsDoc = `
openapi: 3.0.3
info: {title: things, version: "1.0.0"}
paths:
  /things/{id}:
    get:
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: string
            pattern: "^[a-z]+$"
            minLength: 3
            maxLength: 10
        - name: count
          in: query
          schema:
            type: integer
            minimum: 1
            maximum: 100
            exclusiveMaximum: true
            multipleOf: 5
        - name: tags
          in: query
          schema:
            type: array
            minItems: 1
            maxItems: 5
            uniqueItems: true
            items:
              type: string
              pattern: "^t-"
              maxLength: 4
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {type: object, properties: {id: {type: string}}}
    post:
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: string
            pattern: "^[a-z]+$"
            minLength: 3
            maxLength: 10
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {type: object, properties: {id: {type: string}}}
`

// specPropsFor builds the spec schema the way the controller does and returns its properties.
func specPropsFor(t *testing.T, spec string) map[string]any {
	t.Helper()

	g := NewOASSchemaGenerator(parseV2Doc(t, spec), DefaultGeneratorConfig(), &ResourceConfig{
		Verbs: []Verb{
			{Action: "create", Method: "POST", Path: "/things/{id}"},
			{Action: "get", Method: "GET", Path: "/things/{id}"},
		},
	})

	raw, _, err := g.BuildSpecSchema()
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	props, ok := out["properties"].(map[string]any)
	require.True(t, ok, "the spec schema must have properties; got %s", raw)
	return props
}

// TestV2_ParameterValidationKeywordsReachTheGeneratedSpec is the regression test for #255.
//
// It asserts on the rendered spec schema -- the JSON that becomes the CRD -- rather than on the
// ParameterInfo the adapter returns. The adapter's output is an intermediate value, and #249 was lost
// precisely between an intermediate value that was correct and a CRD that was not, so a test that stops
// at the adapter cannot tell the two apart.
func TestV2_ParameterValidationKeywordsReachTheGeneratedSpec(t *testing.T) {
	props := specPropsFor(t, v2KeywordsDoc)

	id, ok := props["id"].(map[string]any)
	require.True(t, ok, "the path parameter must be a spec field; got %v", props)
	assert.Equal(t, "^[a-z]+$", id["pattern"], "a 2.0 path parameter's pattern must reach the CRD")
	assert.EqualValues(t, 3, id["minLength"])
	assert.EqualValues(t, 10, id["maxLength"])

	count, ok := props["count"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 1, count["minimum"])
	assert.EqualValues(t, 100, count["maximum"])
	assert.Equal(t, true, count["exclusiveMaximum"], "2.0's exclusiveMaximum is the boolean modifier a CRD expects")
	assert.EqualValues(t, 5, count["multipleOf"])
	assert.NotContains(t, count, "exclusiveMinimum", "an undeclared facet must stay absent")

	tags, ok := props["tags"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 1, tags["minItems"])
	assert.EqualValues(t, 5, tags["maxItems"])
	assert.Equal(t, true, tags["uniqueItems"])

	// 2.0's `items` object is a different type from a schema, with its own facet fields, so the element
	// constraints travel a separate code path from the array's own.
	items, ok := tags["items"].(map[string]any)
	require.True(t, ok, "the array's item schema must survive; got %v", tags)
	assert.Equal(t, "^t-", items["pattern"], "a constraint on 2.0 array ITEMS must reach the CRD too")
	assert.EqualValues(t, 4, items["maxLength"])
}

// TestV2_And_V3_AgreeOnValidationKeywords encodes the claim the old doc comment made and got wrong --
// that dropping these facets for 2.0 was "parity, not a v2 gap". It was parity until #249 gave the v3
// parameter path the keywords for free, at which point the same document in two notations started
// producing two different CRDs.
//
// Comparing whole rendered field schemas rather than facet by facet, so a keyword added to one path and
// not the other fails here without anyone remembering to extend the assertions.
func TestV2_And_V3_AgreeOnValidationKeywords(t *testing.T) {
	fromV2 := specPropsFor(t, v2KeywordsDoc)
	fromV3 := specPropsFor(t, v3KeywordsDoc)

	for _, field := range []string{"id", "count", "tags"} {
		v2Field, ok := fromV2[field].(map[string]any)
		require.True(t, ok, "%s missing from the 2.0 spec", field)
		v3Field, ok := fromV3[field].(map[string]any)
		require.True(t, ok, "%s missing from the 3.x spec", field)

		// The description is the one field allowed to differ: it is assembled from the parameter's
		// location and prose, which the two notations word differently, and it carries no validation.
		delete(v2Field, "description")
		delete(v3Field, "description")

		assert.Equal(t, v3Field, v2Field,
			"spec.%s must have the same schema whichever notation declared it", field)
	}
}

// TestV2_ItemsFacetAtItsZeroValueIsNotInvented covers the one place the obvious implementation is wrong.
//
// v2.Items types its facets as PLAIN values where v2.Parameter uses pointers, so reading them off the
// high model makes an undeclared `maximum` read as 0 -- "no greater than zero" -- and stamps that
// constraint onto every array of numbers in the document. Rejecting valid values is worse than the
// missing-constraint bug #255 set out to fix, so presence is taken from the low model instead.
func TestV2_ItemsFacetAtItsZeroValueIsNotInvented(t *testing.T) {
	const doc = `
swagger: "2.0"
info: {title: t, version: "1.0.0"}
paths:
  /things/{id}:
    get:
      parameters:
        - name: id
          in: path
          required: true
          type: string
        - name: declared
          in: query
          type: array
          items: {type: integer, maximum: 0}
        - name: undeclared
          in: query
          type: array
          items: {type: integer}
      responses:
        "200": {description: ok, schema: {type: object, properties: {id: {type: string}}}}
    post:
      parameters:
        - name: id
          in: path
          required: true
          type: string
      responses:
        "200": {description: ok, schema: {type: object, properties: {id: {type: string}}}}
`
	props := specPropsFor(t, doc)

	declared := props["declared"].(map[string]any)["items"].(map[string]any)
	require.Contains(t, declared, "maximum", "`maximum: 0` is a real constraint and must be carried")
	assert.EqualValues(t, 0, declared["maximum"])

	undeclared := props["undeclared"].(map[string]any)["items"].(map[string]any)
	assert.NotContains(t, undeclared, "maximum",
		"no `maximum` was declared, so none may appear -- reading the zero value would reject every positive value")
	assert.NotContains(t, undeclared, "minimum")
	assert.NotContains(t, undeclared, "maxLength")
	assert.NotContains(t, undeclared, "minItems")
}

// TestV2_ParameterFacetAtItsZeroValueIsCarried is the mirror of the above for the parameter object, where
// the pointer typing makes presence unambiguous. `minimum: 0` is the common real-world case -- a count or
// an offset that may be zero -- and dropping it would leave the field unbounded below.
func TestV2_ParameterFacetAtItsZeroValueIsCarried(t *testing.T) {
	const doc = `
swagger: "2.0"
info: {title: t, version: "1.0.0"}
paths:
  /things/{id}:
    get:
      parameters:
        - name: id
          in: path
          required: true
          type: string
        - name: offset
          in: query
          type: integer
          minimum: 0
      responses:
        "200": {description: ok, schema: {type: object, properties: {id: {type: string}}}}
    post:
      parameters:
        - name: id
          in: path
          required: true
          type: string
      responses:
        "200": {description: ok, schema: {type: object, properties: {id: {type: string}}}}
`
	props := specPropsFor(t, doc)

	offset, ok := props["offset"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, offset, "minimum", "`minimum: 0` must survive; it is a bound, not an absent facet")
	assert.EqualValues(t, 0, offset["minimum"])
}
