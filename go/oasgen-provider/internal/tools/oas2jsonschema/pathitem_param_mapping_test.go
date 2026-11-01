package oas2jsonschema

import (
	"encoding/json"
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
	require.Empty(t, warnings)

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
