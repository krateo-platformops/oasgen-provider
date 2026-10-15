package oas2jsonschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wideRealmSchema is Keycloak's shape in miniature: a vendor body far wider than anyone wants in a CRD.
// RealmRepresentation has 152 fields, which is what makes a blocklist unusable -- "I want 3 of these"
// should not mean naming the other 7.
func wideRealmSchema() *Schema {
	props := []Property{
		{Name: "realm", Schema: &Schema{Type: []string{"string"}}},
		{Name: "displayName", Schema: &Schema{Type: []string{"string"}}},
		{Name: "enabled", Schema: &Schema{Type: []string{"boolean"}}},
		{Name: "sslRequired", Schema: &Schema{Type: []string{"string"}}},
		{Name: "bruteForceProtected", Schema: &Schema{Type: []string{"boolean"}}},
		{Name: "otpPolicyType", Schema: &Schema{Type: []string{"string"}}},
		{Name: "smtpServer", Schema: &Schema{Type: []string{"object"}}},
		{Name: "browserFlow", Schema: &Schema{Type: []string{"string"}}},
		{Name: "attributes", Schema: &Schema{Type: []string{"object"}}},
		{Name: "metadata", Schema: &Schema{
			Type: []string{"object"},
			Properties: []Property{
				{Name: "owner", Schema: &Schema{Type: []string{"string"}}},
				{Name: "team", Schema: &Schema{Type: []string{"string"}}},
				{Name: "costCentre", Schema: &Schema{Type: []string{"string"}}},
			},
		}},
	}
	return &Schema{Type: []string{"object"}, Properties: props, Required: []string{"realm", "enabled"}}
}

type includeCase struct {
	included   []string
	excluded   []string
	params     []ParameterInfo
	identifers []string
	readOnly   bool
	cfgFields  []ConfigurationField
}

func includeGenerator(c includeCase) *OASSchemaGenerator {
	post := &mockOperation{
		Parameters:  c.params,
		RequestBody: RequestBodyInfo{Content: map[string]*Schema{"application/json": wideRealmSchema()}},
	}
	get := &mockOperation{Parameters: c.params}

	ops := map[string]Operation{"get": get}
	verbs := []Verb{{Action: "findby", Method: "GET", Path: "/realms"}}
	if !c.readOnly {
		ops["post"] = post
		verbs = append([]Verb{{Action: "create", Method: "POST", Path: "/realms"}}, verbs...)
	}

	doc := &mockOASDocument{Paths: map[string]*mockPathItem{"/realms": {Ops: ops}}}

	return NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Verbs:               verbs,
		Identifiers:         c.identifers,
		ConfigurationFields: c.cfgFields,
		ExcludedSpecFields:  c.excluded,
		IncludedSpecFields:  c.included,
	})
}

func specProps(t *testing.T, raw []byte) (map[string]any, []any) {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	props, _ := doc["properties"].(map[string]any)
	req, _ := doc["required"].([]any)
	return props, req
}

func propNames(props map[string]any) []string {
	out := make([]string, 0, len(props))
	for k := range props {
		out = append(out, k)
	}
	return out
}

// The motivating case: name what you want, not the 149 things you do not.
func TestIncludedSpecFields_NarrowsToTheAllowlist(t *testing.T) {
	g := includeGenerator(includeCase{included: []string{"realm", "enabled", "displayName"}})
	raw, _, err := g.BuildSpecSchema()
	require.NoError(t, err)

	props, _ := specProps(t, raw)
	assert.ElementsMatch(t, []string{"realm", "enabled", "displayName"}, propNames(props))
}

// Unset must be byte-for-byte the old behaviour, or this is not an additive feature.
func TestIncludedSpecFields_UnsetChangesNothing(t *testing.T) {
	withRaw, _, err := includeGenerator(includeCase{}).BuildSpecSchema()
	require.NoError(t, err)
	emptyRaw, _, err := includeGenerator(includeCase{included: []string{}}).BuildSpecSchema()
	require.NoError(t, err)
	assert.JSONEq(t, string(withRaw), string(emptyRaw))

	props, _ := specProps(t, withRaw)
	assert.Len(t, props, len(wideRealmSchema().Properties), "every field must survive when no allowlist is declared")
}

// Dot-notation keeps the parent and only the listed children.
func TestIncludedSpecFields_NestedPathKeepsOnlyListedChildren(t *testing.T) {
	g := includeGenerator(includeCase{included: []string{"realm", "metadata.owner"}})
	raw, _, err := g.BuildSpecSchema()
	require.NoError(t, err)

	props, _ := specProps(t, raw)
	require.Contains(t, props, "metadata")
	meta := props["metadata"].(map[string]any)["properties"].(map[string]any)
	assert.ElementsMatch(t, []string{"owner"}, propNames(meta))
}

// A listed PARENT keeps its whole subtree. Recursing into it with no listed children would turn
// "keep metadata" into "keep an empty metadata", which is the easy way to get this wrong.
func TestIncludedSpecFields_ListedParentKeepsWholeSubtree(t *testing.T) {
	g := includeGenerator(includeCase{included: []string{"metadata"}})
	raw, _, err := g.BuildSpecSchema()
	require.NoError(t, err)

	props, _ := specProps(t, raw)
	meta := props["metadata"].(map[string]any)["properties"].(map[string]any)
	assert.ElementsMatch(t, []string{"owner", "team", "costCentre"}, propNames(meta))
}

// A parent listed alongside one of its children means the broader of the two.
func TestIncludedSpecFields_ParentOutranksChild(t *testing.T) {
	g := includeGenerator(includeCase{included: []string{"metadata", "metadata.owner"}})
	raw, _, err := g.BuildSpecSchema()
	require.NoError(t, err)

	props, _ := specProps(t, raw)
	meta := props["metadata"].(map[string]any)["properties"].(map[string]any)
	assert.Len(t, meta, 3, "the explicit parent must win over the narrower child entry")
}

// A typo in an allowlist silently leaves a field OUT, which is invisible in the result -- so it is
// refused, where the blocklist equivalent only warns.
func TestIncludedSpecFields_UnknownFieldIsFatal(t *testing.T) {
	g := includeGenerator(includeCase{included: []string{"realm", "dispalyName"}})
	_, _, err := g.BuildSpecSchema()
	require.Error(t, err)

	var gerr SchemaGenerationError
	require.ErrorAs(t, err, &gerr)
	assert.Equal(t, CodeIncludedFieldNotFound, gerr.Code)
	assert.Contains(t, err.Error(), "dispalyName")
}

func TestIncludedSpecFields_UnknownNestedFieldIsFatal(t *testing.T) {
	g := includeGenerator(includeCase{included: []string{"metadata.ownr"}})
	_, _, err := g.BuildSpecSchema()
	require.Error(t, err)
}

// Dropping a required entry's property must drop the entry too: a required name with no property is a
// schema no object can ever satisfy.
func TestIncludedSpecFields_PrunesRequiredAlongsideProperties(t *testing.T) {
	g := includeGenerator(includeCase{included: []string{"realm"}})
	raw, _, err := g.BuildSpecSchema()
	require.NoError(t, err)

	props, req := specProps(t, raw)
	assert.NotContains(t, propNames(props), "enabled")
	assert.NotContains(t, req, "enabled", "`enabled` was required and is now gone; the required entry must go with it")
	assert.Contains(t, req, "realm")
}

// --- Machinery the allowlist may never drop ---

// Without configurationRef the resource has no way to reference its credentials.
func TestIncludedSpecFields_AlwaysKeepsConfigurationRef(t *testing.T) {
	g := includeGenerator(includeCase{
		included:  []string{"realm"},
		cfgFields: []ConfigurationField{{FromOpenAPI: FromOpenAPI{Name: "api-version", In: "query"}, FromRestDefinition: FromRestDefinition{Actions: []string{"create"}}}},
	})
	raw, _, err := g.BuildSpecSchema()
	require.NoError(t, err)

	props, req := specProps(t, raw)
	assert.Contains(t, propNames(props), "configurationRef", "an allowlist must not be able to strip the credential reference")
	assert.Contains(t, req, "configurationRef")
}

// Without its required path parameter the kind cannot build its own URL -- the breakage #197 fixed,
// which an allowlist could otherwise reintroduce by configuration.
func TestIncludedSpecFields_AlwaysKeepsRequiredParameters(t *testing.T) {
	g := includeGenerator(includeCase{
		included: []string{"enabled"},
		params: []ParameterInfo{
			{Name: "tenantId", In: "path", Required: true, Schema: &Schema{Type: []string{"string"}}},
			{Name: "max", In: "query", Required: false, Schema: &Schema{Type: []string{"integer"}}},
		},
	})
	raw, _, err := g.BuildSpecSchema()
	require.NoError(t, err)

	props, _ := specProps(t, raw)
	names := propNames(props)
	assert.Contains(t, names, "tenantId", "a REQUIRED parameter is how the request gets addressed at all")
	assert.NotContains(t, names, "max", "an OPTIONAL parameter is an ordinary field and may be dropped")
}

// For a resource with no create verb the identifiers ARE the spec; without them findby never matches.
func TestIncludedSpecFields_AlwaysKeepsIdentifierSelectors(t *testing.T) {
	g := includeGenerator(includeCase{
		readOnly:   true,
		identifers: []string{"realmId"},
		included:   []string{"realmId"},
	})
	raw, _, err := g.BuildSpecSchema()
	require.NoError(t, err)
	props, _ := specProps(t, raw)
	assert.Contains(t, propNames(props), "realmId")

	// And still kept when the author did not list it.
	g2 := includeGenerator(includeCase{readOnly: true, identifers: []string{"realmId"}, included: []string{"realmId"}})
	raw2, _, err := g2.BuildSpecSchema()
	require.NoError(t, err)
	props2, _ := specProps(t, raw2)
	assert.Contains(t, propNames(props2), "realmId")
}
