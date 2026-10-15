package oas2jsonschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// argoShapedV2 is a Swagger 2.0 document in the shape the four 2.0 targets publish: securityDefinitions
// (basic + apiKey), a path-item parameter, inline-typed non-body parameters, an `in: body` request and a
// bare response schema.
const argoShapedV2 = `
swagger: "2.0"
info: {title: argocd, version: "2.11.0"}
produces: ["application/json"]
consumes: ["application/json"]
securityDefinitions:
  basicAuth:
    type: basic
  apiKey:
    type: apiKey
    name: Authorization
    in: header
paths:
  /api/v1/applications/{name}:
    parameters:
      - name: name
        in: path
        required: true
        type: string
        description: application name
    get:
      parameters:
        - name: refresh
          in: query
          type: string
          enum: ["normal", "hard"]
          default: "normal"
      responses:
        '200':
          description: ok
          schema:
            type: object
            properties:
              metadata: {type: object, properties: {name: {type: string}}}
              status: {type: string}
    post:
      parameters:
        - name: body
          in: body
          required: true
          schema:
            type: object
            required: ["project"]
            properties:
              project: {type: string}
              destination: {type: object, properties: {server: {type: string}}}
      responses:
        '200':
          description: created
          schema:
            type: object
            properties: {status: {type: string}}
`

func parseV2Doc(t *testing.T, spec string) OASDocument {
	t.Helper()
	doc, err := NewLibOASParser().Parse([]byte(spec))
	require.NoError(t, err, "a Swagger 2.0 document must be accepted, not rejected at the front door")
	require.NotNil(t, doc)
	return doc
}

// Before #196 this failed with "supplied spec is a different version (oas2)".
func TestV2_DocumentIsAccepted(t *testing.T) {
	doc := parseV2Doc(t, argoShapedV2)
	assert.Equal(t, "2.11.0", doc.Version(), "the CRD version comes from info.version in 2.0 just as in 3.x")
}

// `type: basic` must become the v3 vocabulary the configuration builder matches on, or a 2.0 document
// declaring basic auth generates NO authentication field and every request goes out unauthenticated.
func TestV2_SecurityDefinitionsTranslateToTheV3Vocabulary(t *testing.T) {
	doc := parseV2Doc(t, argoShapedV2)
	schemes := doc.SecuritySchemes()
	require.Len(t, schemes, 2)

	byName := map[string]SecuritySchemeInfo{}
	for _, s := range schemes {
		byName[s.Name] = s
	}

	basic := byName["basicAuth"]
	assert.Equal(t, SchemeTypeHTTP, basic.Type, "2.0's `type: basic` is OAS 3's `type: http`")
	assert.Equal(t, "basic", basic.Scheme)

	apiKey := byName["apiKey"]
	assert.Equal(t, SchemeTypeAPIKey, apiKey.Type)
	assert.Equal(t, "header", apiKey.In)
	assert.Equal(t, "Authorization", apiKey.ParamName)
}

func v2Operation(t *testing.T, doc OASDocument, path, method string) Operation {
	t.Helper()
	pi, ok := doc.FindPath(path)
	require.True(t, ok, "path %s must resolve", path)
	op, ok := pi.GetOperations()[method]
	require.True(t, ok, "method %s must resolve on %s", method, path)
	return op
}

// v2.PathItem carries Parameters too, so #197's merge has to hold on this side as well.
func TestV2_PathItemParametersReachTheOperation(t *testing.T) {
	doc := parseV2Doc(t, argoShapedV2)
	params := v2Operation(t, doc, "/api/v1/applications/{name}", "get").GetParameters()

	name, ok := paramNamed(params, "name", "path")
	require.True(t, ok, "a path-item parameter must be visible here too; got %v", params)
	assert.True(t, name.Required)
	require.NotNil(t, name.Schema)
	assert.Equal(t, []string{"string"}, name.Schema.Type, "a 2.0 parameter types itself inline, with no schema")
}

// The one real piece of conversion: a non-body 2.0 parameter carries type/enum/default inline.
func TestV2_InlineParameterFieldsBecomeASchema(t *testing.T) {
	doc := parseV2Doc(t, argoShapedV2)
	params := v2Operation(t, doc, "/api/v1/applications/{name}", "get").GetParameters()

	refresh, ok := paramNamed(params, "refresh", "query")
	require.True(t, ok)
	require.NotNil(t, refresh.Schema)
	assert.Equal(t, []string{"string"}, refresh.Schema.Type)
	assert.Equal(t, "normal", refresh.Schema.Default)
	assert.ElementsMatch(t, []interface{}{"normal", "hard"}, refresh.Schema.Enum)
}

// In 2.0 the request body IS a parameter. It must surface through GetRequestBody and NOT also as a spec
// field of its own called "body".
func TestV2_BodyParameterBecomesTheRequestBodyAndNotAParameter(t *testing.T) {
	doc := parseV2Doc(t, argoShapedV2)
	op := v2Operation(t, doc, "/api/v1/applications/{name}", "post")

	_, present := paramNamed(op.GetParameters(), "body", "body")
	assert.False(t, present, "the body parameter must not also appear as an ordinary parameter")

	body := op.GetRequestBody()
	require.NotNil(t, body.Content)
	schema, ok := body.Content["application/json"]
	require.True(t, ok, "consumes must key the body content; got %v", body.Content)
	require.NotNil(t, schema)

	var names []string
	for _, p := range schema.Properties {
		names = append(names, p.Name)
	}
	assert.ElementsMatch(t, []string{"project", "destination"}, names,
		"the existing v3 converter must take a v2 body schema unchanged -- both are *base.SchemaProxy")
	assert.Equal(t, []string{"project"}, schema.Required)
}

// 2.0 responses have no MIME map, so one is synthesised from produces.
func TestV2_ResponseSchemaIsKeyedByProduces(t *testing.T) {
	doc := parseV2Doc(t, argoShapedV2)
	responses := v2Operation(t, doc, "/api/v1/applications/{name}", "get").GetResponses()

	ok200, present := responses[200]
	require.True(t, present)
	schema, present := ok200.Content["application/json"]
	require.True(t, present, "produces must key the response content; got %v", ok200.Content)
	require.NotNil(t, schema)
}

// produces must NARROW the map rather than always claiming JSON: AcceptedMIMETypes filters on these, so
// a document producing only XML must not have its responses presented as something the runtime cannot parse.
func TestV2_OperationProducesNarrowsTheResponseContent(t *testing.T) {
	doc := parseV2Doc(t, `
swagger: "2.0"
info: {title: t, version: "1.0"}
produces: ["application/json"]
paths:
  /things:
    get:
      produces: ["application/xml"]
      responses:
        '200':
          description: ok
          schema: {type: object, properties: {id: {type: string}}}
`)
	responses := v2Operation(t, doc, "/things", "get").GetResponses()
	_, isXML := responses[200].Content["application/xml"]
	_, isJSON := responses[200].Content["application/json"]
	assert.True(t, isXML, "the operation's own produces must win")
	assert.False(t, isJSON, "the document-level produces must not leak in alongside it")
}

// A 2.0 document with neither produces nor consumes still has to produce something usable.
func TestV2_DefaultsToJSONWhenNothingIsDeclared(t *testing.T) {
	doc := parseV2Doc(t, `
swagger: "2.0"
info: {title: t, version: "1.0"}
paths:
  /things:
    post:
      parameters:
        - name: body
          in: body
          schema: {type: object, properties: {id: {type: string}}}
      responses:
        '201':
          description: created
          schema: {type: object, properties: {id: {type: string}}}
`)
	op := v2Operation(t, doc, "/things", "post")
	_, ok := op.GetRequestBody().Content["application/json"]
	assert.True(t, ok, "a body under no media type at all would read downstream as no body")
	_, ok = op.GetResponses()[201].Content["application/json"]
	assert.True(t, ok)
}

// End to end: a 2.0 document must generate a spec schema, which is the whole point of accepting one.
func TestV2_GeneratesASpecSchema(t *testing.T) {
	doc := parseV2Doc(t, argoShapedV2)

	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Verbs: []Verb{
			{Action: "create", Method: "POST", Path: "/api/v1/applications/{name}"},
			{Action: "get", Method: "GET", Path: "/api/v1/applications/{name}"},
		},
	})

	raw, _, err := g.BuildSpecSchema()
	require.NoError(t, err)

	var spec map[string]any
	require.NoError(t, json.Unmarshal(raw, &spec))
	props := spec["properties"].(map[string]any)

	assert.Contains(t, props, "project", "the create body's fields must become spec fields")
	assert.Contains(t, props, "name", "the path-item path parameter must become a spec field")
	assert.Contains(t, spec["required"].([]any), "name")
}

// 3.x must be untouched by the sniff: the overwhelming majority of documents take that path.
func TestV2_SniffStillRoutesV3Documents(t *testing.T) {
	doc := parseV2Doc(t, keycloakShapedSpec)
	assert.Equal(t, "26.0.0", doc.Version())
	_, ok := doc.FindPath("/admin/realms/{realm}/users")
	assert.True(t, ok)
}
