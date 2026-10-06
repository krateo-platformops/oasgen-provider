package oas2jsonschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// apiVersionSplitSet reproduces the live Aruba shape that found this bug.
//
// `api-version` is declared by BOTH compute documents and they disagree about it: the 1.1 document that
// serves create says required, default "1.1"; the 1.0 document that serves everything else says optional,
// default "1". A configuration field scoped to every action therefore has no single right answer, and
// before the fix whichever document resolved first was stamped onto all of them.
func apiVersionSplitSet() *DocumentSet {
	v10 := &mockOASDocument{Paths: map[string]*mockPathItem{
		"/cloudServers": {Ops: map[string]Operation{
			"get": &mockOperation{Parameters: []ParameterInfo{
				{Name: "api-version", In: "query", Required: false,
					Schema: &Schema{Type: []string{"string"}, Default: "1"}},
			}},
		}},
	}}
	v11 := &mockOASDocument{Paths: map[string]*mockPathItem{
		"/cloudServers": {Ops: map[string]Operation{
			"post": &mockOperation{Parameters: []ParameterInfo{
				{Name: "api-version", In: "query", Required: true,
					Schema: &Schema{Type: []string{"string"}, Default: "1.1"}},
			}},
		}},
	}}
	return NewDocumentSetWithOverrides(v10, map[string]OASDocument{"create": v11})
}

func apiVersionGenerator() *OASSchemaGenerator {
	return &OASSchemaGenerator{
		generatorConfig: DefaultGeneratorConfig(),
		docs:            apiVersionSplitSet(),
		resourceConfig: &ResourceConfig{
			Verbs: []Verb{
				{Action: "create", Method: "POST", Path: "/cloudServers"},
				{Action: "findby", Method: "GET", Path: "/cloudServers"},
			},
			ConfigurationFields: []ConfigurationField{{
				FromOpenAPI: FromOpenAPI{Name: "api-version", In: "query"},
				// What `actions: ["*"]` expands to, which is what the real CloudServer declares.
				FromRestDefinition: FromRestDefinition{Actions: []string{"create", "findby"}},
			}},
		},
	}
}

// queryParam digs out configuration.query.<action>.<name> from the generated schema.
func queryParam(t *testing.T, raw []byte, action, name string) (map[string]any, []any) {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))

	props := doc["properties"].(map[string]any)
	cfg := props["configuration"].(map[string]any)["properties"].(map[string]any)
	q := cfg["query"].(map[string]any)["properties"].(map[string]any)
	act, ok := q[action].(map[string]any)
	require.True(t, ok, "no configuration.query.%s bucket; buckets present: %v", action, keysOf(q))
	param, ok := act["properties"].(map[string]any)[name].(map[string]any)
	require.True(t, ok, "no %s under configuration.query.%s", name, action)
	req, _ := act["required"].([]any)
	return param, req
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestConfigurationFieldTakesEachActionsOwnDocument is the defect, found by installing #108 rather than
// by reading it.
//
// The Configuration CRD offered create `api-version` as the 1.0 document describes it — optional,
// defaulting to "1" — against an endpoint that requires "1.1". Nothing refused the resulting Configuration:
// the field was not marked required, so omitting it was admitted, and the wrong version went on the wire.
func TestConfigurationFieldTakesEachActionsOwnDocument(t *testing.T) {
	raw, err := apiVersionGenerator().BuildConfigurationSchema()
	require.NoError(t, err)
	require.NotNil(t, raw)

	createParam, createRequired := queryParam(t, raw, "create", "api-version")
	assert.Equal(t, "1.1", createParam["default"],
		"create is served by the 1.1 document, which defaults api-version to 1.1")
	assert.Contains(t, createRequired, "api-version",
		"the 1.1 document declares api-version REQUIRED; losing that is what let the wrong value be omitted")

	findbyParam, findbyRequired := queryParam(t, raw, "findby", "api-version")
	assert.Equal(t, "1", findbyParam["default"],
		"findby is served by the 1.0 document and must keep ITS default")
	assert.NotContains(t, findbyRequired, "api-version",
		"the 1.0 document declares it optional; the fix must not make it required everywhere either")
}

// TestConfigurationFieldKeepsActionsWhoseDocumentIsSilent pins that this is a correction, not a removal.
//
// `delete` is declared as an action here but its operation does not declare api-version at all. The
// previous behaviour gave it an entry anyway, from the field-level resolution, and a Configuration in the
// wild may already set it. Dropping the entry would be a silent breaking change to generated output for
// every single-document resource using a wildcard scope, so the field-level fallback is retained.
func TestConfigurationFieldKeepsActionsWhoseDocumentIsSilent(t *testing.T) {
	g := apiVersionGenerator()
	g.resourceConfig.Verbs = append(g.resourceConfig.Verbs,
		Verb{Action: "delete", Method: "DELETE", Path: "/cloudServers"})
	g.resourceConfig.ConfigurationFields[0].FromRestDefinition.Actions = []string{"create", "findby", "delete"}

	raw, err := g.BuildConfigurationSchema()
	require.NoError(t, err)

	param, _ := queryParam(t, raw, "delete", "api-version")
	assert.NotNil(t, param, "delete keeps an entry from the field-level fallback, as before the fix")
}
