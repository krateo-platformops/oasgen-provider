package oas2jsonschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shape here is #199's motivating case, reduced: GitHub workflow_dispatch. The trigger POST declares
// a request body and NO successful response content -- 204, empty -- which is the whole reason the async
// machinery cannot reach this endpoint and the reason a dispatch kind has nowhere else to get a status
// schema from.
func dispatchShapedSet() *DocumentSet {
	doc := &mockOASDocument{Paths: map[string]*mockPathItem{
		"/repos/{owner}/{repo}/actions/workflows/{id}/dispatches": {Ops: map[string]Operation{
			"post": &mockOperation{
				Parameters: []ParameterInfo{
					{Name: "owner", In: "path", Required: true, Schema: &Schema{Type: []string{"string"}}},
					{Name: "repo", In: "path", Required: true, Schema: &Schema{Type: []string{"string"}}},
				},
				RequestBody: RequestBodyInfo{Content: map[string]*Schema{
					"application/json": {
						Type: []string{"object"},
						Properties: []Property{
							{Name: "ref", Schema: &Schema{Type: []string{"string"}}},
							{Name: "inputs", Schema: &Schema{Type: []string{"object"}}},
						},
						Required: []string{"ref"},
					},
				}},
				// No Responses at all: the 204-empty trigger.
			},
		}},
		"/repos/{owner}/{repo}/actions/runs": {Ops: map[string]Operation{
			"get": &mockOperation{
				Responses: map[int]ResponseInfo{200: {Content: map[string]*Schema{
					"application/json": {
						Type: []string{"object"},
						Properties: []Property{
							{Name: "total_count", Schema: &Schema{Type: []string{"integer"}}},
							{Name: "workflow_runs", Schema: &Schema{
								Type: []string{"array"},
								Items: &Schema{
									Type: []string{"object"},
									Properties: []Property{
										{Name: "id", Schema: &Schema{Type: []string{"integer"}}},
										{Name: "status", Schema: &Schema{Type: []string{"string"}}},
										{Name: "conclusion", Schema: &Schema{Type: []string{"string"}}},
									},
								},
							}},
						},
					},
				}}},
			},
		}},
	}}
	return NewDocumentSet(doc)
}

func dispatchShapedVerbs() []Verb {
	return []Verb{{
		Action: ActionDispatch, Method: "POST",
		Path: "/repos/{owner}/{repo}/actions/workflows/{id}/dispatches",
		Correlation: &Correlation{
			FromSpec:         "inputs.correlationId",
			ObserveMethod:    "GET",
			ObservePath:      "/repos/{owner}/{repo}/actions/runs",
			ObserveItemsPath: "workflow_runs",
			EchoedAt:         ".inputs.correlationId",
		},
	}}
}

func dispatchGenerator() *OASSchemaGenerator {
	return &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		docs:            dispatchShapedSet(),
		resourceConfig:  &ResourceConfig{Verbs: dispatchShapedVerbs()},
	}
}

// A dispatch kind declares no create verb -- CEL refuses the combination -- so without dispatch being
// accepted here its entire spec would be empty and the correlation token would have nowhere to live.
func TestSpecBaseComesFromTheDispatchVerbWhenThereIsNoCreate(t *testing.T) {
	schema, err := dispatchGenerator().getBaseSchemaForSpec()

	require.NoError(t, err)
	require.NotNil(t, schema)
	names := make([]string, 0, len(schema.Properties))
	for _, p := range schema.Properties {
		names = append(names, p.Name)
	}
	assert.ElementsMatch(t, []string{"ref", "inputs"}, names,
		"the spec must come from the dispatch request body; a dispatch kind has no other source")
	assert.Equal(t, []string{"ref"}, schema.Required)
}

// The status of a dispatch kind describes the INVOCATION, and the only place the invocation is described
// is an item of the correlation collection. The trigger's own response is empty by construction.
func TestStatusBaseComesFromAnItemOfTheCorrelationCollection(t *testing.T) {
	schema, err := dispatchGenerator().getBaseSchemaForStatus()

	require.NoError(t, err)
	require.NotNil(t, schema, "a dispatch kind with no get/findby still has an observable shape")
	names := make([]string, 0, len(schema.Properties))
	for _, p := range schema.Properties {
		names = append(names, p.Name)
	}
	assert.ElementsMatch(t, []string{"id", "status", "conclusion"}, names,
		"the ITEM, not the envelope: total_count/workflow_runs would mean every status field is a level too shallow")
}

// Unwrapping is the same operation findby performs, so it inherits the same failure mode and must not
// guess: an envelope with two array properties has no obvious collection.
func TestCorrelationItemSchemaRefusesAnAmbiguousEnvelope(t *testing.T) {
	g := dispatchGenerator()
	runs := g.docs.Default().(*mockOASDocument).Paths["/repos/{owner}/{repo}/actions/runs"]
	resp := runs.Ops["get"].(*mockOperation).Responses[200]
	resp.Content["application/json"].Properties = append(resp.Content["application/json"].Properties,
		Property{Name: "included", Schema: &Schema{Type: []string{"array"}, Items: &Schema{Type: []string{"object"}}}})
	g.resourceConfig.Verbs[0].Correlation.ObserveItemsPath = "" // force inference

	_, err := g.correlationItemSchema()

	require.Error(t, err, "two candidate arrays and no itemsPath must refuse rather than pick")
}

func TestCorrelationItemSchemaRefusesAnUndeclaredCollection(t *testing.T) {
	g := dispatchGenerator()
	g.resourceConfig.Verbs[0].Correlation.ObservePath = "/repos/{owner}/{repo}/actions/run"

	_, err := g.correlationItemSchema()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a path declared in the OAS document")
}

func TestCorrelationItemSchemaRefusesAWrongMethod(t *testing.T) {
	g := dispatchGenerator()
	g.resourceConfig.Verbs[0].Correlation.ObserveMethod = "POST"

	_, err := g.correlationItemSchema()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not declared for path")
}

// nil in, nil out -- every ordinary resource takes this path, and it must not disturb get/findby.
func TestCorrelationItemSchemaIsInertWithoutADispatchVerb(t *testing.T) {
	g := dispatchGenerator()
	g.resourceConfig.Verbs = []Verb{{Action: ActionGet, Method: "GET", Path: "/x"}}

	schema, err := g.correlationItemSchema()

	require.NoError(t, err)
	assert.Nil(t, schema)
}

// A dispatch resource has no get and no findby, which used to be reported as CodeMissingBaseAction --
// a validation warning on every correctly-declared dispatch kind, for a base action it is defined not
// to have.
func TestDispatchIsAValidBaseActionOnItsOwn(t *testing.T) {
	base, err := determineBaseAction(dispatchShapedVerbs())

	require.NoError(t, err)
	assert.Equal(t, ActionDispatch, base)
}

func TestValidateSchemasIsQuietForADispatchOnlyResource(t *testing.T) {
	errs := ValidateSchemas(dispatchShapedSet(), dispatchShapedVerbs(), DefaultGeneratorConfig())

	assert.Empty(t, errs, "there is nothing to compare against a 204-empty trigger response")
}

// The last way to reach the failure the whole verb exists to prevent: a mistyped echo path never
// matches, so the invocation reads as absent forever and Create fires on every reconcile.
func TestValidateCorrelationEchoRefusesAnUndeclaredRoot(t *testing.T) {
	g := dispatchGenerator()
	g.resourceConfig.Verbs[0].Correlation.EchoedAt = ".conclsion" // typo for .conclusion

	err := g.ValidateCorrelationEcho()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "conclsion")
	assert.Contains(t, err.Error(), "fire on every reconcile")
	assert.Contains(t, err.Error(), "conclusion, id, status",
		"naming the item's real fields is what makes this fixable; sorted, so the message is stable")
}

func TestValidateCorrelationEchoAcceptsADeclaredRoot(t *testing.T) {
	g := dispatchGenerator()
	g.resourceConfig.Verbs[0].Correlation.EchoedAt = ".status"

	require.NoError(t, g.ValidateCorrelationEcho())
}

// Only the ROOT can be required, exactly as for fromSpec: an open object promises nothing about its
// leaves, and demanding one would refuse #199's motivating case.
func TestValidateCorrelationEchoSkipsAnExplicitlyOpenItem(t *testing.T) {
	g := dispatchGenerator()
	runs := g.docs.Default().(*mockOASDocument).Paths["/repos/{owner}/{repo}/actions/runs"]
	item := runs.Ops["get"].(*mockOperation).Responses[200].Content["application/json"].Properties[1].Schema.Items
	item.AdditionalProperties = &AdditionalProperties{}
	g.resourceConfig.Verbs[0].Correlation.EchoedAt = ".inputs.correlationId"

	require.NoError(t, g.ValidateCorrelationEcho(),
		"an item that declares itself open may still carry an undeclared root")
}

func TestValidateCorrelationEchoIsInertWithoutADispatch(t *testing.T) {
	g := dispatchGenerator()
	g.resourceConfig.Verbs = []Verb{{Action: ActionGet, Method: "GET", Path: "/x"}}

	require.NoError(t, g.ValidateCorrelationEcho())
}
