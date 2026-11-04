package oas2jsonschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #420 (nullable) and #423 (minProperties): TWO KEYWORDS, ONE FIX SHAPE.
//
// Both were read off the document and never emitted, so the declaration read as working while the
// generated CRD could not express it. For `nullable` that is not merely untidy -- a vendor field whose
// documented "disabled" value is null gets a CRD that REJECTS the only value that means disabled.
//
// They are tested together because they are fixed together, and because the interesting risk is shared:
// this repo's recurring defect is a feature reaching one document path through the shared converter and
// skipping every hand-built one (#249 shipped OAS-3-only; #255 had to follow it). So every arm below
// names its document path, and each carries a CONTROL keyword that already worked on that path -- without
// one, an empty result cannot be told from a path that never ran.

func specOf(t *testing.T, doc string) map[string]any {
	t.Helper()
	d, err := NewLibOASParser().Parse([]byte(doc))
	require.NoError(t, err)
	g := NewOASSchemaGenerator(d, DefaultGeneratorConfig(), &ResourceConfig{
		Verbs:       []Verb{{Action: "create", Method: "post", Path: "/things"}},
		Identifiers: []string{"id"},
	})
	raw, _, err := g.BuildSpecSchema()
	require.NoError(t, err)
	var s map[string]any
	require.NoError(t, json.Unmarshal(raw, &s))
	props, _ := s["properties"].(map[string]any)
	return props
}

const nmV3 = `
openapi: 3.0.3
info: {title: t, version: "1.0"}
paths:
  /things:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                count:    {type: integer, nullable: true}
                plain:    {type: integer}
                shutoff:  {type: object, nullable: true, properties: {code: {type: integer, nullable: true}}}
                labels:   {type: object, maxProperties: 16, minProperties: 1, additionalProperties: {type: string}}
                exact:    {type: object, minProperties: 0, additionalProperties: {type: string}}
                tags:     {type: array, maxItems: 8, items: {type: string}}
      responses:
        '201': {description: ok}
`

// OAS 3.1 spells nullability as type: [T, "null"]. It worked BEFORE this change, by accident -- Type is a
// []string the converter copies whole. This is the regression guard: the fix must not disturb it.
const nmV31 = `
openapi: 3.1.0
info: {title: t, version: "1.0"}
paths:
  /things:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                count: {type: [integer, "null"]}
                tags:  {type: array, maxItems: 8, items: {type: string}}
      responses:
        '201': {description: ok}
`

// Swagger 2.0 has no `nullable` keyword; the ecosystem uses the x-nullable EXTENSION. Two distinct paths
// here: the body schema (which reaches the shared converter) and a non-body parameter (hand-built).
const nmV2 = `
swagger: "2.0"
info: {title: t, version: "1.0.0"}
paths:
  /things:
    post:
      parameters:
        - name: body
          in: body
          schema:
            type: object
            properties:
              count:  {type: integer, x-nullable: true}
              plain:  {type: integer}
              labels: {type: object, maxProperties: 16, minProperties: 1, additionalProperties: {type: string}}
              tags:   {type: array, maxItems: 8, items: {type: string}}
        - name: trace
          in: query
          type: integer
          x-nullable: true
      responses:
        '201': {description: ok}
`

func TestNullableSurvivesOnEveryDocumentPath(t *testing.T) {
	t.Run("OAS 3.0 nullable keyword", func(t *testing.T) {
		props := specOf(t, nmV3)
		require.Contains(t, props, "tags", "CONTROL: a keyword that already worked, so an empty result is not a dead path")
		assert.Equal(t, true, props["count"].(map[string]any)["nullable"],
			"a vendor field documented as nullable must get a CRD that can hold null")
		assert.Nil(t, props["plain"].(map[string]any)["nullable"],
			"nullable:false and absent are identical to apiextensions, so emitting it would add a key to "+
				"every property in every generated CRD for nothing")
	})

	t.Run("OAS 3.0 nested, where a status write is rejected per property", func(t *testing.T) {
		props := specOf(t, nmV3)
		sh := props["shutoff"].(map[string]any)
		assert.Equal(t, true, sh["nullable"], "the CONTAINER's nullability")
		inner := sh["properties"].(map[string]any)["code"].(map[string]any)
		assert.Equal(t, true, inner["nullable"],
			"and each PROPERTY's, separately: for a vendor object whose members are all nullable, both "+
				"forms the document permits were rejected, at two independent levels")
	})

	t.Run("OAS 3.1 type:[T,null] is untouched", func(t *testing.T) {
		props := specOf(t, nmV31)
		require.Contains(t, props, "tags", "CONTROL")
		ty := props["count"].(map[string]any)["type"]
		assert.Equal(t, []any{"integer", "null"}, ty,
			"3.1 expresses nullability INSIDE Type, which the converter already copied -- that is why 3.1 "+
				"worked while 3.0 did not, and the fix must not convert it into something else")
	})

	t.Run("Swagger 2.0 body schema x-nullable", func(t *testing.T) {
		props := specOf(t, nmV2)
		require.Contains(t, props, "tags", "CONTROL")
		assert.Equal(t, true, props["count"].(map[string]any)["nullable"],
			"a 2.0 body schema reaches the SHARED converter, and x-nullable arrives only as an extension -- "+
				"a fix reading s.Nullable alone would miss the spelling the Swagger ecosystem actually uses")
		assert.Nil(t, props["plain"].(map[string]any)["nullable"])
	})

	t.Run("Swagger 2.0 non-body parameter x-nullable", func(t *testing.T) {
		props := specOf(t, nmV2)
		tr, ok := props["trace"].(map[string]any)
		require.True(t, ok, "precondition: the query parameter must reach the spec at all, or this arm is vacuous")
		assert.Equal(t, true, tr["nullable"],
			"a 2.0 NON-BODY parameter is hand-built -- it has no `schema` and never reaches the shared "+
				"converter, which is exactly the path #249 shipped without and #255 had to follow up")
	})
}

func TestMinPropertiesSurvivesOnBothPaths(t *testing.T) {
	for _, tc := range []struct{ name, doc string }{{"OAS 3.0", nmV3}, {"Swagger 2.0 body", nmV2}} {
		t.Run(tc.name, func(t *testing.T) {
			labels := specOf(t, tc.doc)["labels"].(map[string]any)
			assert.EqualValues(t, 16, labels["maxProperties"],
				"CONTROL: maxProperties already survived on this path, so a missing minProperties is a "+
					"missing keyword rather than a path that never ran")
			assert.EqualValues(t, 1, labels["minProperties"],
				"a CR with too few entries was admitted by the API server and then refused by the vendor")
		})
	}
}

// THE POINTER DISTINCTION, which is why MinProperties is *int64 while its own neighbour MaxProperties is a
// plain int. MaxProperties is emitted under a `> 0` guard, so a declared 0 is indistinguishable from
// absent. For a maximum that is pathological; `minProperties: 0` is meaningful and is JSON Schema's own
// default, so it has to survive as a declaration rather than be collapsed into silence.
func TestMinPropertiesZeroIsADeclarationNotAnAbsence(t *testing.T) {
	exact := specOf(t, nmV3)["exact"].(map[string]any)
	v, present := exact["minProperties"]
	require.True(t, present, "minProperties: 0 must be emitted; a plain int with a >0 guard would drop it")
	assert.EqualValues(t, 0, v)
}
