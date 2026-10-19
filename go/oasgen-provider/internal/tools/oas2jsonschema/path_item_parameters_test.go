package oas2jsonschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseOperation parses spec and returns the operation at path/method, through the real adapter.
func parseOperation(t *testing.T, spec []byte, path, method string) Operation {
	t.Helper()
	doc, err := NewLibOASParser().Parse(spec)
	require.NoError(t, err)
	pi, ok := doc.FindPath(path)
	require.True(t, ok, "path %s not found", path)
	op, ok := pi.GetOperations()[method]
	require.True(t, ok, "method %s not found on %s", method, path)
	return op
}

// paramNamed finds a parameter by name AND location, which is how OAS identifies one.
func paramNamed(params []ParameterInfo, name, in string) (ParameterInfo, bool) {
	for _, p := range params {
		if p.Name == name && p.In == in {
			return p, true
		}
	}
	return ParameterInfo{}, false
}

// The shape that cannot pass for the wrong reason: `realm` is declared ONLY at path-item level, so a
// reader of op.Parameters finds nothing at all rather than quietly finding something worse.
//
// This is Keycloak's shape, which declares path-item parameters on 273 of its 274 paths (#197).
const keycloakShapedSpec = `
openapi: 3.0.3
info: {title: keycloak, version: "26.0.0"}
paths:
  /admin/realms/{realm}/users:
    parameters:
      - name: realm
        in: path
        required: true
        description: realm name
        schema: {type: string}
    get:
      parameters:
        - name: max
          in: query
          schema: {type: integer}
      responses:
        '200': {description: ok}
    post:
      responses:
        '201': {description: created}
`

func TestPathItemParameters_ReachTheOperation(t *testing.T) {
	op := parseOperation(t, []byte(keycloakShapedSpec), "/admin/realms/{realm}/users", "get")
	params := op.GetParameters()

	realm, ok := paramNamed(params, "realm", "path")
	require.True(t, ok, "path-item parameter must be visible to the operation; got %v", params)
	assert.True(t, realm.Required, "a required path parameter must stay required")
	assert.Equal(t, "realm name", realm.Description)
	require.NotNil(t, realm.Schema)
	assert.Equal(t, []string{"string"}, realm.Schema.Type)

	_, ok = paramNamed(params, "max", "query")
	assert.True(t, ok, "the operation's own parameters must survive the merge")
}

// Every operation on the path shares the path item's parameters -- including one that declares no
// parameters of its own, which is the case that would otherwise return nil.
func TestPathItemParameters_SharedByEveryOperation(t *testing.T) {
	for _, method := range []string{"get", "post"} {
		t.Run(method, func(t *testing.T) {
			op := parseOperation(t, []byte(keycloakShapedSpec), "/admin/realms/{realm}/users", method)
			_, ok := paramNamed(op.GetParameters(), "realm", "path")
			assert.True(t, ok, "%s must see the path item's shared parameter", method)
		})
	}
}

// The OAS override rule: same name AND location, operation wins. Asserted on a field that differs, so
// a merge that kept the path-item copy would fail rather than coincide.
func TestPathItemParameters_OperationOverridesSameNameAndLocation(t *testing.T) {
	spec := []byte(`
openapi: 3.0.3
info: {title: t, version: "1.0"}
paths:
  /things:
    parameters:
      - name: scope
        in: query
        required: false
        description: path-item copy
        schema: {type: string}
    get:
      parameters:
        - name: scope
          in: query
          required: true
          description: operation copy
          schema: {type: string}
      responses:
        '200': {description: ok}
`)
	params := parseOperation(t, spec, "/things", "get").GetParameters()

	var scopes []ParameterInfo
	for _, p := range params {
		if p.Name == "scope" {
			scopes = append(scopes, p)
		}
	}
	require.Len(t, scopes, 1, "the overridden parameter must not appear twice; got %v", scopes)
	assert.Equal(t, "operation copy", scopes[0].Description, "the operation's declaration must win")
	assert.True(t, scopes[0].Required)
}

// Name alone does not identify a parameter. A `scope` in path and a `scope` in query are two
// parameters, and neither overrides the other.
func TestPathItemParameters_SameNameDifferentLocationBothSurvive(t *testing.T) {
	spec := []byte(`
openapi: 3.0.3
info: {title: t, version: "1.0"}
paths:
  /things/{scope}:
    parameters:
      - name: scope
        in: path
        required: true
        schema: {type: string}
    get:
      parameters:
        - name: scope
          in: query
          schema: {type: string}
      responses:
        '200': {description: ok}
`)
	params := parseOperation(t, spec, "/things/{scope}", "get").GetParameters()

	pathScope, ok := paramNamed(params, "scope", "path")
	require.True(t, ok, "the path parameter must survive; got %v", params)
	assert.True(t, pathScope.Required)
	_, ok = paramNamed(params, "scope", "query")
	assert.True(t, ok, "the query parameter must survive too; got %v", params)
}

// --- The two consumers (#197 is two sites, not one) ---

// pathItemParamGenerator builds a generator over a real document whose only declaration of `realm` is
// at path-item level.
func pathItemParamGenerator(t *testing.T, fields []ConfigurationField) *OASSchemaGenerator {
	t.Helper()
	spec := []byte(`
openapi: 3.0.3
info: {title: keycloak, version: "26.0.0"}
paths:
  /admin/realms/{realm}/users:
    parameters:
      - name: realm
        in: path
        required: true
        description: realm name
        schema: {type: string}
    get:
      responses:
        '200': {description: ok}
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                username: {type: string}
      responses:
        '201': {description: created}
`)
	doc, err := NewLibOASParser().Parse(spec)
	require.NoError(t, err)

	return NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Verbs: []Verb{
			{Action: "create", Method: "POST", Path: "/admin/realms/{realm}/users"},
			{Action: "findby", Method: "GET", Path: "/admin/realms/{realm}/users"},
		},
		ConfigurationFields: fields,
	})
}

// Consumer 1: addParametersToSpec. Without the merge the generated kind has no `realm` field and
// cannot build its own URL.
func TestPathItemParameters_BecomeSpecFields(t *testing.T) {
	raw, warnings, err := pathItemParamGenerator(t, nil).BuildSpecSchema()
	require.NoError(t, err)
	require.Empty(t, warnings)

	var spec map[string]any
	require.NoError(t, json.Unmarshal(raw, &spec))

	props, _ := spec["properties"].(map[string]any)
	require.Contains(t, props, "realm", "a path-item parameter must become a spec field")

	required, _ := spec["required"].([]any)
	assert.Contains(t, required, "realm", "a REQUIRED path parameter must become a required spec field")
}

// Consumer 2: findParameterForAction / findParameterInOAS. A configurationFields entry naming a
// path-item parameter must resolve, or it cannot be promoted into the <Kind>Configuration.
func TestPathItemParameters_ResolveAsConfigurationFields(t *testing.T) {
	field := ConfigurationField{
		FromOpenAPI:        FromOpenAPI{Name: "realm", In: "path"},
		FromRestDefinition: FromRestDefinition{Actions: []string{"create", "findby"}},
	}
	g := pathItemParamGenerator(t, []ConfigurationField{field})

	t.Run("per action", func(t *testing.T) {
		for _, action := range []string{"create", "findby"} {
			param, err := g.findParameterForAction(field, action)
			require.NoError(t, err, "action %s must resolve a path-item parameter", action)
			require.NotNil(t, param)
			assert.Equal(t, "path", param.In)
			assert.True(t, param.Required)
		}
	})

	t.Run("field level", func(t *testing.T) {
		param, err := g.findParameterInOAS(field)
		require.NoError(t, err)
		require.NotNil(t, param)
		assert.Equal(t, "realm", param.Name)
	})
}
