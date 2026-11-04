package oas2jsonschema

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #419: A MAPPING WHOSE SOURCE IS THE PROPERTY SUPPRESSING IT WOULD DELETE.
//
// The suppression omits a parameter a request-direction fieldMapping supplies, so the author is not asked
// for a field the controller never reads from them. Its doc comment reasons that a `spec.*` source is the
// mild case because "the value IS author-supplied, just at another path".
//
// That reasoning fails when the other path is the SAME path. `inCustomResource: spec.node_id` for path
// parameter {node_id} names the very property the suppression deletes, so the rule removes its own source:
// the controller then reads a property that is not in the CRD, finds nothing, skips silently, and the
// literal {node_id} is refused downstream with "unresolved path parameter(s)". The kind cannot execute
// that verb at all.
//
// Measured on a consumer's NodeProvision and NodePower, whose generated spec was
// [clean_steps configdrive configurationRef target] with `node_id` absent while their chart set it.
const selfReferentialSpec = `
openapi: 3.0.3
info: {title: ironic, version: "1.0"}
paths:
  /v1/nodes:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                name: {type: string}
      responses:
        '201': {description: created}
  /v1/nodes/{node_id}/states/provision:
    put:
      parameters:
        - {name: node_id, in: path, required: true, schema: {type: string}}
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [target]
              properties:
                target: {type: string}
                configdrive: {type: string}
      responses:
        '202': {description: accepted}
`

// bodyAndPathSameNameSpec is the consumer's GA shape: `username` is BOTH a create-body property (with its
// own minLength) and a get path parameter (with its own maxLength), mapped self-referentially from
// spec.username. The two declare DIFFERENT keywords, which is what makes the merge observable without a
// conflict confusing the result.
const bodyAndPathSameNameSpec = `
openapi: 3.0.3
info: {title: aruba, version: "1.0"}
paths:
  /users:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [username]
              properties:
                username: {type: string, minLength: 3}
                password: {type: string}
      responses:
        '201': {description: created}
  /users/{username}:
    get:
      parameters:
        - {name: username, in: path, required: true, schema: {type: string, maxLength: 64}}
      responses:
        '200': {description: ok}
`

func selfRefSpecFor(t *testing.T, mapping []FieldMappingEntry) (map[string]any, []any, []error) {
	t.Helper()
	doc, err := NewLibOASParser().Parse([]byte(selfReferentialSpec))
	require.NoError(t, err)
	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Verbs: []Verb{
			{Action: "create", Method: "post", Path: "/v1/nodes"},
			{
				Action: "update", Method: "put", Path: "/v1/nodes/{node_id}/states/provision",
				FieldMapping: mapping,
			},
		},
		Identifiers: []string{"name"},
	})
	raw, warnings, err := g.BuildSpecSchema()
	require.NoError(t, err)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))
	props, _ := schema["properties"].(map[string]any)
	req, _ := schema["required"].([]any)
	return props, req, warnings
}

// CASE 1 — the reported bug. A mapping reading the parameter's own top-level spec property must leave that
// property in place, because deleting it is deleting the mapping's source.
func TestSelfReferentialMappingLeavesItsOwnSourceInTheSpec(t *testing.T) {
	props, req, warnings := selfRefSpecFor(t, []FieldMappingEntry{
		{InPath: "node_id", InCustomResource: "spec.node_id"},
	})

	require.Contains(t, props, "node_id",
		"the mapping reads spec.node_id, so removing the property deletes the value the mapping needs: the "+
			"controller finds nothing, skips silently, and the request is refused for an unresolved {node_id}")
	require.Contains(t, req, "node_id",
		"the document declares the parameter required, and nothing else can supply it, so it must stay required")

	// And NO removal warning, because nothing was removed. This is not a second suppression of the #409
	// warning -- it is the warning being correct: there is no removal to report.
	for _, w := range warnings {
		require.NotContains(t, w.Error(), "is omitted from the generated spec because verb",
			"a removal warning for a property that is still present is the #409 false positive again")
	}
}

// CASE 1b — THE CONSEQUENCE A CONSUMER FOUND BEFORE THE TAG, pinned because it is deliberate.
//
// When the parameter's name is ALSO a create-body property, the property has an independent origin and
// never disappeared, so #419 was never a breakage for that shape. A consumer with three GA kinds in
// exactly this configuration asked why their verbs executed correctly if the property was being deleted --
// and the answer is that it was not. Their three are unaffected by the DEFECT.
//
// But they are NOT unaffected by the FIX, and that is worth a test rather than a release note alone.
// Lifting the suppression lets execution reach reconcileParameterValidation at the end of
// addParametersToSpec, which merges the parameter's validation keywords into the body property. So the
// generated property gains the parameter's constraints where before it kept only the body's.
//
// THAT IS #257's BEHAVIOUR FINALLY REACHING A CASE THE SUPPRESSION WAS HIDING IT FROM, not a new defect.
// #257's own words: a colliding parameter's keywords "used to be discarded entirely... Worse than not
// supporting the keyword, because the document reads as though the constraint applies." The suppression
// was bypassing that fix for every mapping-supplied parameter. Narrowing #419 to avoid the merge would
// have reintroduced #257 for this class, which is why it is pinned here instead of avoided.
//
// It IS a tightening -- a CR that previously passed admission can now be rejected -- which is the same
// consumer-visible consequence 0.49.0 carried for #257 itself, and it belongs in the release note.
func TestASelfReferentialMappingNowCarriesTheParametersKeywordsOntoABodyProperty(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(bodyAndPathSameNameSpec))
	require.NoError(t, err)
	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Verbs: []Verb{
			{Action: "create", Method: "post", Path: "/users"},
			{Action: "get", Method: "get", Path: "/users/{username}",
				FieldMapping: []FieldMappingEntry{{InPath: "username", InCustomResource: "spec.username"}}},
		},
		Identifiers: []string{"username"},
	})
	raw, warnings, err := g.BuildSpecSchema()
	require.NoError(t, err)
	var spec map[string]any
	require.NoError(t, json.Unmarshal(raw, &spec))
	props, _ := spec["properties"].(map[string]any)
	u, ok := props["username"].(map[string]any)
	require.True(t, ok, "premise: the property must exist from the create BODY, independently of the parameter")

	assert.EqualValues(t, 3, u["minLength"], "the body's own constraint must survive")
	assert.EqualValues(t, 64, u["maxLength"],
		"the PARAMETER's constraint now reaches the property too (#257's merge, which the suppression was "+
			"bypassing). A value longer than 64 cannot work in the URL, so carrying it is the faithful "+
			"result -- and it is a TIGHTENING, which belongs in the release note")

	// No conflict here because the two declare different keywords rather than the same one differently.
	// A test that merely counted warnings would pass for the wrong reason, so assert the absence of the
	// specific conflict rather than emptiness.
	for _, w := range warnings {
		assert.NotContains(t, w.Error(), "maxLength",
			"body and parameter declare DIFFERENT keywords, so there is nothing to conflict over")
	}
}

// CASE 2 — THE REGRESSION GUARD, with a named consumer instance behind it.
//
// A consumer's GitRef declares dependentLookups whose `parameters` read `spec.owner` for {owner} and
// `spec.repo` for {repo} -- the SAME-name shape -- and both properties are present AND required today,
// which is correct: a dependent lookup's parameter is supplied FROM the spec by design.
//
// This passes structurally rather than by care: DependentLookups is a separate field on ResourceConfig and
// the suppression only iterates Verbs[].FieldMapping, so it cannot reach them. The test exists because a
// DIFFERENT fix shape -- a containment check written over all mappings rather than narrowing the
// suppression -- would have deleted them and reproduced #419 in a kind that works. If someone later
// broadens the matching, this is what fails.
func TestADependentLookupParameterReadingTheSameNameIsUntouched(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(selfReferentialSpec))
	require.NoError(t, err)
	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Verbs: []Verb{
			{Action: "create", Method: "post", Path: "/v1/nodes"},
			{Action: "update", Method: "put", Path: "/v1/nodes/{node_id}/states/provision"},
		},
		Identifiers: []string{"name"},
		DependentLookups: []DependentLookup{{
			Name:       "owningNode",
			Method:     "get",
			Path:       "/v1/nodes/{node_id}/states/provision",
			Parameters: []LookupParameter{{InPath: "node_id", InCustomResource: "spec.node_id"}},
		}},
	})
	raw, _, err := g.BuildSpecSchema()
	require.NoError(t, err)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))
	props, _ := schema["properties"].(map[string]any)

	require.Contains(t, props, "node_id",
		"a dependentLookup parameter is supplied FROM the spec by design, so its property must survive; "+
			"a fix that broadened the matching instead of narrowing the suppression would delete it")
}

// CASE 3 — the designed behaviour must survive, or the fix is a no-op dressed as a narrowing.
//
// A source whose NAME DIFFERS from the parameter is the case the suppression exists for: the author really
// does supply a different property, so asking them for the parameter as well is a redundant field nothing
// reads. It must still be removed, and the removal must still be reported.
func TestAMappingFromADifferentlyNamedSpecPropertyStillSuppresses(t *testing.T) {
	props, _, warnings := selfRefSpecFor(t, []FieldMappingEntry{
		{InPath: "node_id", InCustomResource: "spec.name"},
	})

	require.NotContains(t, props, "node_id",
		"spec.name is a different property the author does supply, so the flat duplicate is redundant and "+
			"the suppression is correct here -- if this passes, the fix disabled the rule rather than narrowing it")
	require.Contains(t, props, "name", "the control: the property the mapping actually reads must remain")

	var reported bool
	for _, w := range warnings {
		if strings.Contains(w.Error(), "is omitted from the generated spec because verb") {
			reported = true
		}
	}
	require.True(t, reported, "a real removal must still be reported (#409), or the narrowing silenced the signal")
}

// CASE 4 — a status source is the case the suppression was written for and must be unaffected.
//
// The vendor assigns the value at create and the controller reads it back, so the author cannot supply it:
// keeping the property would make the kind uncreatable, which is what #325 measured five times out of five.
func TestAStatusSourcedMappingStillSuppresses(t *testing.T) {
	props, _, _ := selfRefSpecFor(t, []FieldMappingEntry{
		{InPath: "node_id", InCustomResource: "status.node_id"},
	})
	require.NotContains(t, props, "node_id",
		"a status-sourced parameter is unsatisfiable by the author; suppressing it is why this rule exists, "+
			"and the self-referential exemption must not reach it -- the prefix is the whole difference")
}

// Nested spec sources are NOT self-referential: spec.keypair.name for {name} is a different property.
func TestANestedSpecSourceIsNotTreatedAsSelfReferential(t *testing.T) {
	require.False(t, selfReferentialSpecSource("spec.keypair.name", "name"),
		"spec.keypair.name is a property the author supplies; exempting it would reintroduce the redundant "+
			"flat duplicate the suppression removes")
	require.False(t, selfReferentialSpecSource("status.node_id", "node_id"), "a status source is never self-referential")
	require.False(t, selfReferentialSpecSource("spec.", ""), `"" names no property, so "spec." cannot be self-referential`)
	require.True(t, selfReferentialSpecSource("spec.node_id", "node_id"), "the exact top-level case")
}
