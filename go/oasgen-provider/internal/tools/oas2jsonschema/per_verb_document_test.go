package oas2jsonschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shape every test in this file is built on is #108's motivating case, reduced.
//
// Aruba's CloudServer has findby/get/delete in compute-provider.json (1.0.0) and create ALONE in
// compute-provider_v1.1.json (1.1.0), where the 1.1 document holds exactly that one path. So the
// decisive property is not "the override is preferred" but something stronger and much easier to
// assert: the create path EXISTS ONLY IN THE OVERRIDE. A lookup that reaches for the default document
// does not quietly return a worse answer -- it finds nothing at all.
//
// That is deliberate. A test where both documents contain the path can pass for the wrong reason if the
// two happen to agree; this one cannot.
func arubaShapedSet() *DocumentSet {
	defaultDoc := &mockOASDocument{Paths: map[string]*mockPathItem{
		"/projects/{projectId}/providers/Aruba.Compute/cloudServers/{cloudServerId}": {Ops: map[string]Operation{
			"get": &mockOperation{
				Parameters: []ParameterInfo{
					{Name: "projectId", In: "path", Required: true, Schema: &Schema{Type: []string{"string"}}},
					{Name: "cloudServerId", In: "path", Required: true, Schema: &Schema{Type: []string{"string"}}},
				},
			},
		}},
	}}

	overrideDoc := &mockOASDocument{Paths: map[string]*mockPathItem{
		"/projects/{projectId}/providers/Aruba.Compute/cloudServers": {Ops: map[string]Operation{
			"post": &mockOperation{
				Parameters: []ParameterInfo{
					{Name: "apiVersion", In: "query", Required: true, Schema: &Schema{Type: []string{"string"}}},
				},
				RequestBody: RequestBodyInfo{Content: map[string]*Schema{
					"application/json": {
						Type: []string{"object"},
						Properties: []Property{
							{Name: "name", Schema: &Schema{Type: []string{"string"}}},
							{Name: "flavorId", Schema: &Schema{Type: []string{"string"}}},
						},
						Required: []string{"name"},
					},
				}},
			},
		}},
	}}

	return NewDocumentSetWithOverrides(defaultDoc, map[string]OASDocument{"create": overrideDoc})
}

func arubaShapedVerbs() []Verb {
	return []Verb{
		{Action: "create", Method: "POST", Path: "/projects/{projectId}/providers/Aruba.Compute/cloudServers"},
		{Action: "get", Method: "GET", Path: "/projects/{projectId}/providers/Aruba.Compute/cloudServers/{cloudServerId}"},
	}
}

// TestCreateBodyComesFromTheCreateVerbsOwnDocument covers getBaseSchemaForSpec, which produces the CRD's
// ENTIRE spec from the create request body.
//
// This was the most consequential of the three lookups still reading the default document: the resource
// #108 exists for could not be generated at all, failing with "path not found in OpenAPI spec" for a
// path that was present exactly where the verb said it was.
func TestCreateBodyComesFromTheCreateVerbsOwnDocument(t *testing.T) {
	g := &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		docs:            arubaShapedSet(),
		resourceConfig:  &ResourceConfig{Verbs: arubaShapedVerbs()},
	}

	schema, err := g.getBaseSchemaForSpec()
	require.NoError(t, err, "the create path exists only in the override document, so resolving it against the default fails outright")
	require.NotNil(t, schema)

	names := make([]string, 0, len(schema.Properties))
	for _, p := range schema.Properties {
		names = append(names, p.Name)
	}
	assert.ElementsMatch(t, []string{"name", "flavorId"}, names,
		"the spec must be built from the 1.1 document's create body")
	assert.Equal(t, []string{"name"}, schema.Required)
}

// TestParametersComeFromEachVerbsOwnDocument covers addParametersToSpec, which merges the parameters of
// EVERY verb into one spec schema. Each verb must contribute from the document it named: the default
// document has no create path, so the override's query parameter is reachable only through For().
func TestParametersComeFromEachVerbsOwnDocument(t *testing.T) {
	g := &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		docs:            arubaShapedSet(),
		resourceConfig:  &ResourceConfig{Verbs: arubaShapedVerbs()},
	}

	schema := &Schema{Type: []string{"object"}}
	warnings, err := g.addParametersToSpec(schema)
	require.NoError(t, err)
	assert.Empty(t, warnings, "every verb's path resolves in its own document, so nothing should be reported missing")

	names := make([]string, 0, len(schema.Properties))
	for _, p := range schema.Properties {
		names = append(names, p.Name)
	}
	assert.Contains(t, names, "apiVersion", "the create verb's query parameter lives in the 1.1 document")
	assert.Contains(t, names, "cloudServerId", "the get verb's path parameter lives in the 1.0 document")
	assert.Contains(t, names, "projectId")
}

// TestConfigurationFieldParameterIsFoundInTheVerbsOwnDocument covers findParameterInOAS, which resolves a
// configuration field declared against a named action. An override's parameter is otherwise invisible to
// it, and the field fails with "not found for any of the specified actions".
func TestConfigurationFieldParameterIsFoundInTheVerbsOwnDocument(t *testing.T) {
	g := &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		docs:            arubaShapedSet(),
		resourceConfig:  &ResourceConfig{Verbs: arubaShapedVerbs()},
	}

	param, err := g.findParameterInOAS(ConfigurationField{
		FromOpenAPI:        FromOpenAPI{Name: "apiVersion", In: "query"},
		FromRestDefinition: FromRestDefinition{Actions: []string{"create"}},
	})
	require.NoError(t, err)
	require.NotNil(t, param)
	assert.Equal(t, "apiVersion", param.Name)
	assert.Equal(t, "query", param.In)
}

// TestResourceLevelReadsStayOnTheDefaultDocument is the other half of the contract, and the reason
// doc() was not simply deleted: security schemes are a property of the RESOURCE, not of a verb, and
// #108 settled that they come from spec.oasPath's document. Routing them per-verb would be just as
// wrong as the bug this change fixes, in the opposite direction.
func TestResourceLevelReadsStayOnTheDefaultDocument(t *testing.T) {
	defaultDoc := &mockOASDocument{
		Paths:           map[string]*mockPathItem{},
		securitySchemes: []SecuritySchemeInfo{{Name: "bearerAuth", Type: SchemeTypeHTTP}},
		version:         "1.0.0",
	}
	overrideDoc := &mockOASDocument{
		Paths:           map[string]*mockPathItem{},
		securitySchemes: []SecuritySchemeInfo{{Name: "wrong", Type: SchemeTypeAPIKey}},
		version:         "1.1.0",
	}

	g := &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		docs:            NewDocumentSetWithOverrides(defaultDoc, map[string]OASDocument{"create": overrideDoc}),
		resourceConfig:  &ResourceConfig{},
	}

	require.Len(t, g.doc().SecuritySchemes(), 1)
	assert.Equal(t, "bearerAuth", g.doc().SecuritySchemes()[0].Name)
	assert.Equal(t, "1.0.0", g.doc().Version(), "the CRD version comes from spec.oasPath's document")
}

// conflictingSet builds two documents that both declare a `projectId` path parameter, with the schemas
// the caller gives, so a test can make them agree or disagree.
func conflictingSet(defSchema, overrideSchema *Schema) *DocumentSet {
	defaultDoc := &mockOASDocument{Paths: map[string]*mockPathItem{
		"/cloudServers/{cloudServerId}": {Ops: map[string]Operation{
			"get": &mockOperation{Parameters: []ParameterInfo{
				{Name: "projectId", In: "path", Required: true, Schema: defSchema},
			}},
		}},
	}}
	overrideDoc := &mockOASDocument{Paths: map[string]*mockPathItem{
		"/cloudServers": {Ops: map[string]Operation{
			"post": &mockOperation{Parameters: []ParameterInfo{
				{Name: "projectId", In: "path", Required: true, Schema: overrideSchema},
			}},
		}},
	}}
	return NewDocumentSetWithOverrides(defaultDoc, map[string]OASDocument{"create": overrideDoc})
}

func conflictGenerator(docs *DocumentSet) *OASSchemaGenerator {
	return &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		docs:            docs,
		resourceConfig: &ResourceConfig{Verbs: []Verb{
			{Action: "create", Method: "POST", Path: "/cloudServers"},
			{Action: "get", Method: "GET", Path: "/cloudServers/{cloudServerId}"},
		}},
	}
}

// TestCrossDocumentParameterConflictIsRefused is #108's "version skew" case, at the one site where two
// documents demonstrably feed the same CRD field.
//
// Parameters from every verb are merged into one spec, deduplicated by name, FIRST VERB WINS. With both
// verbs reading one document that is harmless: a path parameter repeated across verbs is the same
// parameter. Across two documents it is a silent choice between definitions that disagree, decided by
// the order of verbsDescription, after which the losing verb sends a value shaped for the other
// document's contract. Nothing downstream could report it.
func TestCrossDocumentParameterConflictIsRefused(t *testing.T) {
	g := conflictGenerator(conflictingSet(
		&Schema{Type: []string{"string"}},
		&Schema{Type: []string{"integer"}},
	))

	_, err := g.addParametersToSpec(&Schema{Type: []string{"object"}})
	require.Error(t, err, "two documents disagreeing about a merged parameter must not be resolved by verb order")

	var gerr SchemaGenerationError
	require.ErrorAs(t, err, &gerr)
	assert.Equal(t, CodeCrossDocumentParameterConflict, gerr.Code)
	assert.Contains(t, err.Error(), "projectId", "the message must name the parameter")
	assert.Contains(t, err.Error(), "create", "and both verbs, which is how the author finds the two documents")
	assert.Contains(t, err.Error(), "get")
}

// TestAgreeingDocumentsAreNotAConflict is the half that keeps the rule usable. A vendor's 1.0 and 1.1
// documents share most of their surface; a parameter they both declare IDENTICALLY is not skew, and
// refusing it would make the split-document case unusable for the resource #108 exists for.
func TestAgreeingDocumentsAreNotAConflict(t *testing.T) {
	g := conflictGenerator(conflictingSet(
		&Schema{Type: []string{"string"}},
		&Schema{Type: []string{"string"}},
	))

	schema := &Schema{Type: []string{"object"}}
	warnings, err := g.addParametersToSpec(schema)
	require.NoError(t, err)
	assert.Empty(t, warnings)

	names := make([]string, 0, len(schema.Properties))
	for _, p := range schema.Properties {
		names = append(names, p.Name)
	}
	assert.Equal(t, []string{"projectId"}, names, "the agreeing parameter is merged exactly once")
}

// TestSameDocumentDisagreementIsNotRefused pins the scoping, and it is the test that says why the rule is
// cross-document rather than global.
//
// Two verbs in ONE document declaring the same parameter differently is pre-existing and resolved by
// first-wins. It is reachable by every RestDefinition that exists today, so refusing it here would be a
// regression dressed as a fix -- and it is unreachable by the skew #108 introduces. The rule can only
// fire once a verb overrides spec.oasPath.
func TestSameDocumentDisagreementIsNotRefused(t *testing.T) {
	doc := &mockOASDocument{Paths: map[string]*mockPathItem{
		"/a": {Ops: map[string]Operation{
			"post": &mockOperation{Parameters: []ParameterInfo{
				{Name: "projectId", In: "path", Required: true, Schema: &Schema{Type: []string{"string"}}},
			}},
		}},
		"/b": {Ops: map[string]Operation{
			"get": &mockOperation{Parameters: []ParameterInfo{
				{Name: "projectId", In: "path", Required: true, Schema: &Schema{Type: []string{"integer"}}},
			}},
		}},
	}}

	g := &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		docs:            NewDocumentSet(doc),
		resourceConfig: &ResourceConfig{Verbs: []Verb{
			{Action: "create", Method: "POST", Path: "/a"},
			{Action: "get", Method: "GET", Path: "/b"},
		}},
	}

	_, err := g.addParametersToSpec(&Schema{Type: []string{"object"}})
	assert.NoError(t, err,
		"first-wins within one document is long-standing behaviour; this rule must not reach it")
}
