package oas2jsonschema

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TWO FEATURES THAT COMPOSE, WITH NOTHING ASSERTING THE COMPOSITION until now.
//
// #197 taught the generator to merge a path item's `parameters` into each operation's — Keycloak declares
// them on 273 of its 274 paths, and reading only `op.Parameters` made a required `realm` invisible.
//
// #325 then taught it to OMIT a parameter whose value a request-direction fieldMapping supplies, so the
// author is not asked for a field the controller never reads.
//
// #299 is their intersection: a path-ITEM parameter that a fieldMapping redirects. Both halves were
// tested alone and their combination was not, which is the failure mode this repo keeps meeting — two
// good halves, never compared. They do compose, because the suppression at spec_builder.go:240 iterates
// `op.GetParameters()` at :211, which is the MERGING accessor. This pins that, so a future change to
// either feature cannot silently break the pair.
//
// The document's shape is deliberately the one that cannot pass for the wrong reason: `uuid` is declared
// ONLY at path-item level. A reader of `op.Parameters` finds nothing at all, so a test that passed by
// looking there would be asserting against an empty list.
const pathItemParamRedirectedSpec = `
openapi: 3.0.3
info: {title: ironic, version: "1.0"}
paths:
  /v1/nodes/{uuid}:
    parameters:
      - name: uuid
        in: path
        required: true
        schema: {type: string}
    get:
      responses:
        '200': {description: ok}
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
`

func TestPathItemParameterSuppressedByFieldMapping(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(pathItemParamRedirectedSpec))
	require.NoError(t, err)

	// The premise, asserted before the behaviour: the parameter really is inherited rather than declared
	// on the operation. If this ever fails, the test below proves nothing about path-item parameters.
	pi, ok := doc.FindPath("/v1/nodes/{uuid}")
	require.True(t, ok, "path not found")
	op, ok := pi.GetOperations()["get"]
	require.True(t, ok, "get not found")
	_, found := paramNamed(op.GetParameters(), "uuid", "path")
	require.True(t, found, "precondition: `uuid` must reach the operation via the path item (#197), "+
		"otherwise this test is asserting against an empty parameter list")

	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Verbs: []Verb{
			{Action: "create", Method: "post", Path: "/v1/nodes"},
			{
				Action: "get", Method: "get", Path: "/v1/nodes/{uuid}",
				// The redirect: Ironic resolves /v1/nodes/{name} as well as by UUID, so the CR's identity
				// is the operator-chosen name and `uuid` is not the author's to supply.
				FieldMapping: []FieldMappingEntry{
					{InPath: "uuid", InCustomResource: "spec.name"},
				},
			},
		},
		Identifiers: []string{"name"},
	})

	specBytes, warnings, err := g.BuildSpecSchema()
	require.NoError(t, err)
	require.NotNil(t, specBytes)

	// THIS ASSERTION USED TO BE require.Empty(t, warnings), and that silence was the defect (#409).
	//
	// The removal below is correct and unchanged. What was wrong is that it happened with no signal: the
	// generated spec sets neither additionalProperties:false nor x-kubernetes-preserve-unknown-fields, so
	// an existing CR setting `uuid` has the value PRUNED by the API server rather than rejected --
	// `kubectl apply` succeeds, `get -o yaml` stops showing it, and nothing says why. Meanwhile
	// excludedSpecFields WARNS when it matches nothing, so a stanza doing no work was loud while a
	// property leaving the schema was quiet. The asymmetry was backwards.
	//
	// Asserted on the SHIPPED message via mappingRemovalMessage rather than on a copy of its text: a test
	// that rebuilds the string passes whatever the real one says.
	require.Len(t, warnings, 1, "the removal must be reported; its silence is #409")
	require.Contains(t, warnings[0].Error(),
		mappingRemovalMessage("uuid", mappingSource{action: "get", in: "path", from: "spec.name"}),
		"the warning must name the property, the verb whose mapping removed it, and the CR path the value "+
			"now comes from -- a message saying a field was removed and not by WHAT sends the author to "+
			"read every verb's fieldMapping")

	var schema map[string]any
	require.NoError(t, json.Unmarshal(specBytes, &schema))

	props, _ := schema["properties"].(map[string]any)
	if _, present := props["uuid"]; present {
		t.Error("`uuid` is a spec property. It is supplied by a fieldMapping from spec.name, so the " +
			"author would be asked for a field the controller never reads (#299) — and because it is " +
			"declared at PATH-ITEM level, this is the combination of #197 and #325 rather than either alone")
	}

	if req, _ := schema["required"].([]any); req != nil {
		for _, r := range req {
			if r == "uuid" {
				t.Error("`uuid` is in `required`. A CR could not be admitted without setting a field " +
					"whose value comes from elsewhere, which makes the kind unusable rather than merely untidy")
			}
		}
	}

	// And the control: the identifier the author DOES supply must still be there. Without this the test
	// could pass by suppressing everything.
	if _, present := props["name"]; !present {
		t.Error("`name` is missing from the spec: the suppression has removed more than the mapped " +
			"parameter, and the kind now has no identity field at all")
	}
}

// --- fixtures for the #409 suppression cases ---

// bodyAndPathNameCollisionSpec is the consumer's measured shape: `name` is BOTH a create-body property
// and a get path parameter. Nothing synthetic about it -- it is how a vendor that lets you address a
// resource by the name you gave it is described.
const bodyAndPathNameCollisionSpec = `
openapi: 3.0.3
info: {title: github, version: "1.0"}
paths:
  /repos/{org}/{name}:
    get:
      parameters:
        - {name: org, in: path, required: true, schema: {type: string}}
        - {name: name, in: path, required: true, schema: {type: string}}
      responses:
        '200': {description: ok}
  /orgs/{org}/repos:
    post:
      parameters:
        - {name: org, in: path, required: true, schema: {type: string}}
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name: {type: string}
                visibility: {type: string}
      responses:
        '201': {description: created}
`

func newGeneratorForBodyAndPathCollision(t *testing.T) *OASSchemaGenerator {
	t.Helper()
	doc, err := NewLibOASParser().Parse([]byte(bodyAndPathNameCollisionSpec))
	require.NoError(t, err)
	return NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Verbs: []Verb{
			{Action: "create", Method: "post", Path: "/orgs/{org}/repos"},
			{
				Action: "get", Method: "get", Path: "/repos/{org}/{name}",
				// Keyed on the vendor's NORMALISED name, read back from status -- which is why the path
				// parameter is mapping-supplied while the body property is still the author's.
				FieldMapping: []FieldMappingEntry{
					{InPath: "name", InCustomResource: "status.name"},
				},
			},
		},
		Identifiers: []string{"name"},
	})
}

// newGeneratorForMappedPathParam builds the simple case: one mapped path parameter with no same-named
// body property, so the suppression genuinely removes it from the spec. excluded controls whether the
// author declared the exclusion.
func newGeneratorForMappedPathParam(t *testing.T, excluded []string) *OASSchemaGenerator {
	t.Helper()
	doc, err := NewLibOASParser().Parse([]byte(pathItemParamRedirectedSpec))
	require.NoError(t, err)
	return NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Verbs: []Verb{
			{Action: "create", Method: "post", Path: "/v1/nodes"},
			{
				Action: "get", Method: "get", Path: "/v1/nodes/{uuid}",
				FieldMapping: []FieldMappingEntry{
					{InPath: "uuid", InCustomResource: "status.uuid"},
				},
			},
		},
		Identifiers:        []string{"name"},
		ExcludedSpecFields: excluded,
	})
}

// #409's FALSE POSITIVE, reported by a consumer against f93f6fe before the tag.
//
// A name can be BOTH a path parameter and a create-body property. Suppressing the parameter does not
// remove the body's property, so the field stays in the spec -- often required. Their measured case: the
// Repository CRD has `spec.required = [configurationRef, name, org]` while `name` is also a get/update
// path parameter mapped from `status.name`, and the warning said the property was "omitted from the
// generated spec" and told them to "remove it from the CR". Following that advice produces a CR rejected
// for a missing required field.
//
// So the warning must assert the OUTCOME (is the property actually gone) rather than the MECHANISM (does
// a mapping supply the parameter). Asserted both ways round here, because either alone passes on the bug:
// the no-warning check alone would pass if the property were also wrongly removed, and the
// property-present check alone was already true when the warning was wrong.
func TestNoRemovalWarningWhenTheNameIsAlsoACreateBodyProperty(t *testing.T) {
	g := newGeneratorForBodyAndPathCollision(t)

	specBytes, warnings, err := g.BuildSpecSchema()
	require.NoError(t, err)
	require.NotNil(t, specBytes)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(specBytes, &schema))
	props, _ := schema["properties"].(map[string]any)

	// THE PREMISE. If `name` were genuinely absent the warning would be correct and this test would be
	// asserting the wrong thing, so establish that the property survives before judging the warning.
	require.Contains(t, props, "name",
		"`name` must remain a spec property: it is a create-body field, and suppressing the same-named "+
			"PATH parameter does not remove it. If this fails the fix is wrong in the other direction")

	for _, w := range warnings {
		require.NotContains(t, w.Error(), mappingRemovalMessage("name", mappingSource{action: "get", in: "path", from: "status.name"}),
			"the removal warning fired for a property that is still in the spec, and its advice "+
				"(\"remove it from the CR\") would produce a CR rejected for a missing required field")
	}
}

// An excludedSpecFields entry is the author's acknowledgement that the field is not theirs to set, so
// pruning is the DECLARED INTENT and there is nothing to tell them. And it is not merely noise:
// ExcludedSpecFields carries `self == oldSelf` (types.go:958), so an author CANNOT withdraw the entry --
// warning about it is a permanent message they have no action for.
//
// The consumer's argument inverted what I had proposed: the case worth warning about is a mapped
// parameter the author NEVER declared, where a CR plausibly set it before.
func TestNoRemovalWarningWhenTheAuthorDeclaredTheExclusion(t *testing.T) {
	g := newGeneratorForMappedPathParam(t, []string{"uuid"})

	_, warnings, err := g.BuildSpecSchema()
	require.NoError(t, err)
	for _, w := range warnings {
		require.NotContains(t, w.Error(), "is omitted from the generated spec because verb",
			"the author declared `uuid` in excludedSpecFields, which they cannot withdraw; a warning they "+
				"have no action for teaches people to ignore warnings")
	}
}

// AND THE CASE THE WARNING EXISTS FOR must still fire, or the two suppressions above have removed the
// signal rather than narrowed it: a mapped parameter the author never declared, absent from the spec.
func TestTheRemovalWarningStillFiresForAnUndeclaredMappedParameter(t *testing.T) {
	g := newGeneratorForMappedPathParam(t, nil)

	specBytes, warnings, err := g.BuildSpecSchema()
	require.NoError(t, err)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(specBytes, &schema))
	props, _ := schema["properties"].(map[string]any)
	require.NotContains(t, props, "uuid", "premise: `uuid` must genuinely be absent for the warning to be right")

	var found bool
	for _, w := range warnings {
		if strings.Contains(w.Error(), "is omitted from the generated spec because verb") {
			found = true
		}
	}
	require.True(t, found,
		"the warning must still fire for an undeclared mapped parameter that really is gone -- that is "+
			"the silent removal #409 exists to report, and suppressing it too would make the fix a no-op")
}
