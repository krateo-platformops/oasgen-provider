package oas2jsonschema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A document where TWO verbs declare the same path parameter. That is the shape the resource-wide map
// could not express, and the shape no existing arm had: the nearest one
// (excluded_required_mapping_exempt_test.go) has `uuid` on the get path only, so its single declaring
// verb is also its supplier and the resource-wide answer happened to be right.
const twoVerbsShareAPathParamSpec = `
openapi: 3.0.0
info: {title: things, version: "1.0"}
components:
  schemas:
    Thing:
      type: object
      properties:
        name: {type: string}
      required: [name]
paths:
  /things/{id}:
    get:
      operationId: getThing
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
    put:
      operationId: updateThing
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      requestBody:
        content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
  /things:
    post:
      operationId: createThing
      requestBody:
        content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}}
`

// THE REGRESSION TEST FOR #457.
//
// mappingSuppliedParams built ONE map keyed by parameter name across every verb, so `update`'s
// fieldMapping exempted `id` for `get` as well. `get` has no mapping, so nothing supplied the value, and
// the parameter was not in spec for the author to supply either: the kind could not build its own GET URL.
//
// The fix is per-verb, and the rule it produces is "omit only if EVERY declaring verb supplies it". That
// emerges from the accumulation rather than from a pre-pass: `update` skips the parameter, `get` adds it,
// so the field exists exactly when some declaring verb needs it.
func TestAMappingOnOneVerbDoesNotStripTheParameterFromAnother(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(twoVerbsShareAPathParamSpec))
	require.NoError(t, err)

	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Identifiers: []string{"name"},
		Verbs: []Verb{
			{Action: ActionCreate, Method: "POST", Path: "/things"},
			// get declares `id` and has NO mapping: it needs the value from spec.
			{Action: ActionGet, Method: "GET", Path: "/things/{id}"},
			// update declares `id` and DOES supply it.
			{
				Action: ActionUpdate, Method: "PUT", Path: "/things/{id}",
				FieldMapping: []FieldMappingEntry{
					{InPath: "id", InCustomResource: "status.thingId"},
				},
			},
		},
	})

	schema, _, err := g.BuildSpecSchema()
	require.NoError(t, err)

	require.Contains(t, string(schema), `"id"`,
		"`id` is missing from the generated spec. update supplies it through a fieldMapping, but GET does "+
			"not -- so the author has no field to set and nothing supplies the value for get: the kind "+
			"cannot build its own request URL")
}

// VERB ORDER MUST NOT CHANGE THE ANSWER. The fix relies on accumulation, and accumulation is only
// order-independent if adding and skipping commute. Asserted rather than assumed, because the previous
// shape's first-wins rule made order matter for the warning and it would be easy to reintroduce.
func TestTheAnswerDoesNotDependOnVerbOrder(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(twoVerbsShareAPathParamSpec))
	require.NoError(t, err)

	supplierFirst := []Verb{
		{Action: ActionUpdate, Method: "PUT", Path: "/things/{id}", FieldMapping: []FieldMappingEntry{{InPath: "id", InCustomResource: "status.thingId"}}},
		{Action: ActionGet, Method: "GET", Path: "/things/{id}"},
		{Action: ActionCreate, Method: "POST", Path: "/things"},
	}
	neederFirst := []Verb{
		{Action: ActionGet, Method: "GET", Path: "/things/{id}"},
		{Action: ActionUpdate, Method: "PUT", Path: "/things/{id}", FieldMapping: []FieldMappingEntry{{InPath: "id", InCustomResource: "status.thingId"}}},
		{Action: ActionCreate, Method: "POST", Path: "/things"},
	}

	for name, verbs := range map[string][]Verb{"supplier first": supplierFirst, "needer first": neederFirst} {
		t.Run(name, func(t *testing.T) {
			g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{Identifiers: []string{"name"}, Verbs: verbs})
			schema, warnings, err := g.BuildSpecSchema()
			require.NoError(t, err)
			require.Contains(t, string(schema), `"id"`, "the field must exist whichever verb is seen first")

			// And the mapping-removal warning must not fire: nothing was removed, `id` is in the spec.
			for _, w := range warnings {
				var sge SchemaGenerationError
				if asSchemaGenerationError(w, &sge) {
					require.NotContains(t, sge.Message, "id",
						"a mapping-removal warning naming `id` fired while `id` is in the spec -- that is "+
							"the order-dependent warning the deferral exists to prevent. Warning: %s", sge.Message)
				}
			}
		})
	}
}

// THE CONTROL. When EVERY declaring verb supplies the parameter, it must still be omitted -- otherwise the
// fix has simply disabled the exemption, and the author is asked for a value the controller overrides.
func TestAParameterSuppliedByEveryDeclaringVerbIsStillOmitted(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(twoVerbsShareAPathParamSpec))
	require.NoError(t, err)

	mapping := []FieldMappingEntry{{InPath: "id", InCustomResource: "status.thingId"}}
	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Identifiers: []string{"name"},
		Verbs: []Verb{
			{Action: ActionCreate, Method: "POST", Path: "/things"},
			{Action: ActionGet, Method: "GET", Path: "/things/{id}", FieldMapping: mapping},
			{Action: ActionUpdate, Method: "PUT", Path: "/things/{id}", FieldMapping: mapping},
		},
	})

	schema, _, err := g.BuildSpecSchema()
	require.NoError(t, err)

	require.NotContains(t, string(schema), `"id"`,
		"`id` is in the spec although BOTH declaring verbs supply it -- the exemption has been disabled "+
			"rather than scoped, and an author who sets this field is silently ignored")
}
