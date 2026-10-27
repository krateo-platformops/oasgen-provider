package oas2jsonschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// specBaseDoc declares a PUT and a POST whose bodies differ, plus a GET, so a test can tell which verb
// the spec base came from by looking at which field appeared.
const specBaseDoc = `
openapi: 3.0.0
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
                fromCreate: {type: string}
      responses:
        '201': {description: ok, content: {application/json: {schema: {type: object, properties: {id: {type: string}}}}}}
  /things/{id}:
    put:
      parameters: [{name: id, in: path, required: true, schema: {type: string}}]
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                fromUpdate: {type: string}
                nested:
                  type: object
                  properties:
                    deep: {type: string}
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: object, properties: {id: {type: string}}}}}}
    get:
      parameters: [{name: id, in: path, required: true, schema: {type: string}}]
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: object, properties: {id: {type: string}}}}}}
`

// bodylessCreateDoc declares a POST with NO request body alongside a body-carrying PUT.
const bodylessCreateDoc = `
openapi: 3.0.0
info: {title: t, version: "1.0"}
paths:
  /things:
    post:
      responses:
        '201': {description: ok}
  /things/{id}:
    put:
      parameters: [{name: id, in: path, required: true, schema: {type: string}}]
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                fromUpdate: {type: string}
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: object, properties: {id: {type: string}}}}}}
`

func specFieldNames(t *testing.T, doc OASDocument, verbs []Verb) []string {
	t.Helper()
	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{Verbs: verbs, Identifiers: []string{"id"}})
	base, err := g.getBaseSchemaForSpec()
	require.NoError(t, err)
	names := make([]string, 0, len(base.Properties))
	for _, p := range base.Properties {
		names = append(names, p.Name)
	}
	return names
}

// TestSpecBaseFallsBackToUpdateBody covers #236: a document with no create endpoint is not necessarily a
// read-only resource, and treating it as one gave the declared update a spec that could not carry its own
// request body.
//
// The precedence cases are the ones that matter most. Before this change the resolution was a single loop
// over verbsDescription that took the first create-or-dispatch it met, so widening that condition to
// include update would have made YAML ORDER decide which body defined the spec -- silently, and only for
// resources that declare both.
func TestSpecBaseFallsBackToUpdateBody(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(specBaseDoc))
	require.NoError(t, err)

	create := Verb{Action: ActionCreate, Method: "POST", Path: "/things"}
	update := Verb{Action: ActionUpdate, Method: "PUT", Path: "/things/{id}"}
	get := Verb{Action: ActionGet, Method: "GET", Path: "/things/{id}"}

	t.Run("no create verb: the spec comes from the update body", func(t *testing.T) {
		got := specFieldNames(t, doc, []Verb{get, update})
		assert.Contains(t, got, "fromUpdate")
		assert.Contains(t, got, "nested", "a nested object in the update body must survive too")
		assert.NotContains(t, got, "fromCreate")
	})

	t.Run("create present: it wins, update is ignored", func(t *testing.T) {
		got := specFieldNames(t, doc, []Verb{create, update, get})
		assert.Contains(t, got, "fromCreate")
		assert.NotContains(t, got, "fromUpdate")
	})

	t.Run("create declared AFTER update still wins", func(t *testing.T) {
		// Resolution must be by precedence, not by position in verbsDescription.
		got := specFieldNames(t, doc, []Verb{update, get, create})
		assert.Contains(t, got, "fromCreate")
		assert.NotContains(t, got, "fromUpdate", "declaration order must not decide which body defines the spec")
	})

	t.Run("neither create nor update: the base stays empty", func(t *testing.T) {
		assert.Empty(t, specFieldNames(t, doc, []Verb{get}))
	})
}

// TestBodylessCreateDoesNotBorrowTheUpdateBody pins the deliberate limit of the fallback.
//
// A create verb that IS declared but carries no body in an accepted MIME type keeps its previous
// behaviour, an empty base. Borrowing the update body there would inject fields into the spec of a
// resource that defines itself differently, which is a behaviour change for existing definitions rather
// than a fix.
func TestBodylessCreateDoesNotBorrowTheUpdateBody(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(bodylessCreateDoc))
	require.NoError(t, err)

	got := specFieldNames(t, doc, []Verb{
		{Action: ActionCreate, Method: "POST", Path: "/things"},
		{Action: ActionUpdate, Method: "PUT", Path: "/things/{id}"},
	})
	assert.Empty(t, got, "a declared-but-bodyless create must not fall back to the update body")
}
