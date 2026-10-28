package oas2jsonschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolverRequiredDoc has a create body requiring both properties, and a query parameter deliberately
// sharing a name with one of them.
const resolverRequiredDoc = `
openapi: 3.0.0
info: {title: t, version: "1.0"}
paths:
  /things:
    post:
      parameters:
        - name: token
          in: query
          required: true
          schema: {type: string}
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [name, token]
              properties:
                name: {type: string}
                token: {type: string}
      responses:
        '201': {description: ok, content: {application/json: {schema: {type: object, properties: {name: {type: string}}}}}}
  /things/{name}:
    get:
      parameters: [{name: name, in: path, required: true, schema: {type: string}}]
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: object, properties: {name: {type: string}}}}}}
`

// noParamDoc is the same body with no same-named parameter, for the plain cases.
const noParamDoc = `
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
              required: [name, token]
              properties:
                name: {type: string}
                token: {type: string}
      responses:
        '201': {description: ok, content: {application/json: {schema: {type: object, properties: {name: {type: string}}}}}}
  /things/{name}:
    get:
      parameters: [{name: name, in: path, required: true, schema: {type: string}}]
      responses:
        '200': {description: ok, content: {application/json: {schema: {type: object, properties: {name: {type: string}}}}}}
`

func specRequiredAndProps(t *testing.T, doc OASDocument, verbs []Verb) ([]string, map[string]bool) {
	t.Helper()
	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{Verbs: verbs, Identifiers: []string{"name"}})
	base, _, err := g.BuildSpecSchema()
	require.NoError(t, err)

	var parsed struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(base, &parsed))
	props := map[string]bool{}
	for k := range parsed.Properties {
		props[k] = true
	}
	return parsed.Required, props
}

func createVerbWith(entries ...FieldMappingEntry) Verb {
	return Verb{Action: ActionCreate, Method: "POST", Path: "/things", FieldMapping: entries}
}

var getVerb = Verb{Action: ActionGet, Method: "GET", Path: "/things/{name}"}

// TestResolverSuppliedFieldIsNotRequired covers #246: the controller substitutes the value, so requiring
// it of the author forces them to write something that is then overwritten.
//
// For secretRef that is actively harmful, not merely redundant: the obvious value to put in a required
// field called `token` is the credential, in cleartext, in the CR spec -- which is the outcome secretRef
// exists to prevent. For dependentLookup it makes the feature inert, since a `when` guard keyed on the
// field's absence can never be satisfied.
func TestResolverSuppliedFieldIsNotRequired(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(noParamDoc))
	require.NoError(t, err)

	t.Run("secretRef", func(t *testing.T) {
		req, props := specRequiredAndProps(t, doc, []Verb{
			createVerbWith(FieldMappingEntry{InBody: "token", ResolverType: "secretRef"}),
			getVerb,
		})
		assert.NotContains(t, req, "token", "a secretRef-supplied field must not be required of the author")
		assert.Contains(t, req, "name", "an unrelated required field must stay required")
		assert.True(t, props["token"], "the property must remain: the API still accepts the field, and it is the mapping's anchor")
	})

	t.Run("dependentLookup", func(t *testing.T) {
		req, props := specRequiredAndProps(t, doc, []Verb{
			createVerbWith(FieldMappingEntry{InBody: "token", ResolverType: "dependentLookup", ResolverLookupName: "x"}),
			getVerb,
		})
		assert.NotContains(t, req, "token")
		assert.True(t, props["token"])
	})

	t.Run("no resolver: required is untouched", func(t *testing.T) {
		req, _ := specRequiredAndProps(t, doc, []Verb{
			createVerbWith(FieldMappingEntry{InBody: "token", InCustomResource: "spec.token"}),
			getVerb,
		})
		assert.Contains(t, req, "token", "an ordinary fieldMapping entry supplies nothing, so the field stays required")
	})

	t.Run("a resolver on inQuery does not touch the body's required list", func(t *testing.T) {
		req, _ := specRequiredAndProps(t, doc, []Verb{
			createVerbWith(FieldMappingEntry{InQuery: "token", ResolverType: "secretRef"}),
			getVerb,
		})
		assert.Contains(t, req, "token",
			"an inQuery resolver addresses a URL, not a spec property; matching it by name would unrequire an unrelated field")
	})
}

// TestRequiredParameterSurvivesASameNamedResolver is the ordering test.
//
// The prune runs against the request body's own `required`, BEFORE parameters are added. A required query
// parameter that merely shares a name with a resolver-supplied body field is a different thing and must
// stay required -- pruning after addParametersToSpec would silently unrequire it.
func TestRequiredParameterSurvivesASameNamedResolver(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(resolverRequiredDoc))
	require.NoError(t, err)

	req, _ := specRequiredAndProps(t, doc, []Verb{
		createVerbWith(FieldMappingEntry{InBody: "token", ResolverType: "secretRef"}),
		getVerb,
	})
	assert.Contains(t, req, "token",
		"the required QUERY PARAMETER named token must survive; only the body's requirement is dropped")
}
