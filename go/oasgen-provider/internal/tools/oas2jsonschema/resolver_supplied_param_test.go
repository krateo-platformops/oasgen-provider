package oas2jsonschema

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A document whose `get` needs a path parameter AND a query parameter that the author does not own: the
// shape a consumer hit building a Keycloak kind, where `userId` is resolved by a pre-call lookup.
const resolverSuppliedParamsSpec = `
openapi: 3.0.0
info: {title: users, version: "1.0"}
components:
  schemas:
    User:
      type: object
      properties:
        name: {type: string}
      required: [name]
paths:
  /users/{userId}/role:
    get:
      operationId: getRole
      parameters:
        - {name: userId, in: path, required: true, schema: {type: string}}
        - {name: realm, in: query, required: true, schema: {type: string}}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/User'}}}}
  /users:
    post:
      operationId: createUser
      requestBody:
        content: {application/json: {schema: {$ref: '#/components/schemas/User'}}}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/User'}}}}
`

func resolverParamGenerator(t *testing.T, mapping []FieldMappingEntry) *OASSchemaGenerator {
	t.Helper()
	doc, err := NewLibOASParser().Parse([]byte(resolverSuppliedParamsSpec))
	require.NoError(t, err)
	return NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		// `userId` is deliberately NOT an identifier: addIdentifiersToSpec re-adds any identifier it does
		// not find, which would put the property back for an unrelated reason and make the assertion below
		// unreadable.
		Identifiers: []string{"name"},
		Verbs: []Verb{
			{Action: ActionCreate, Method: "POST", Path: "/users"},
			{Action: ActionGet, Method: "GET", Path: "/users/{userId}/role", FieldMapping: mapping},
		},
	})
}

// THE REGRESSION TEST FOR #555, with its own control.
//
// A parameter supplied by an unconditional resolver stayed a REQUIRED top-level spec field. The author
// cannot meaningfully set it -- the controller overwrites it from the lookup -- and the only escape was
// excludedSpecFields, which carries self == oldSelf in the shipped CRD and is therefore IMMUTABLE once the
// CRD exists, besides warning only when it matches nothing.
//
// The CONTROL matters more than the subject here: "userId is absent" is the expected outcome of the fix
// and also the outcome of a fixture that never declared the parameter, a parse failure, or a schema this
// test is not reading. The same document with the resolver removed must still carry the property.
func TestAnUnconditionalResolverSuppliedParameterLeavesTheSpec(t *testing.T) {
	withResolver, _, err := resolverParamGenerator(t, []FieldMappingEntry{
		{InPath: "userId", ResolverType: "dependentLookup", ResolverLookupName: "userByUsername"},
	}).BuildSpecSchema()
	require.NoError(t, err)

	noResolver, _, err := resolverParamGenerator(t, nil).BuildSpecSchema()
	require.NoError(t, err)

	require.Contains(t, string(noResolver), `"userId"`,
		"CONTROL FAILED: with no fieldMapping at all the parameter must be in spec, so an absence below "+
			"would not be evidence of anything")
	require.NotContains(t, string(withResolver), `"userId"`,
		"a path parameter supplied by an unconditional dependentLookup is still in the generated spec: the "+
			"author is being asked for a value the controller resolves and overwrites (#555)")
}

// THE GUARD AGAINST THIS FIX BECOMING A NEW BUG.
//
// A `when` guard makes the resolver CONDITIONAL, and the API contract is explicit about what happens when
// it does not hold: the whole mapping is skipped, "no Secret is read and nothing is written to the
// request". So in that case the AUTHOR supplies the value -- and if the fix above omitted the property, a
// guard-false resource would have no field to supply it through and the kind could not build its URL.
//
// That is the #419 shape (a rule that removes its own source) arriving from a different direction, and it
// is why ResolverHasWhen exists as a carried bit rather than the fix keying on ResolverType alone.
func TestAConditionalResolverKeepsTheParameterSettable(t *testing.T) {
	schema, _, err := resolverParamGenerator(t, []FieldMappingEntry{
		{InPath: "userId", ResolverType: "dependentLookup", ResolverLookupName: "userByUsername", ResolverHasWhen: true},
	}).BuildSpecSchema()
	require.NoError(t, err)

	require.Contains(t, string(schema), `"userId"`,
		"a CONDITIONAL resolver's parameter was omitted from the spec. When the `when` guard does not hold "+
			"the mapping is skipped and nothing is written to the request, so the author is the supplier -- "+
			"omitting the property leaves that case with no way to provide the value at all")
}

// The warning is the author's only notice that a property left the schema, so it must say what supplies
// the value. A resolver-supplied entry has no inCustomResource, and the pre-fix message would have read
// `from ""` -- which describes a mapping with an empty source: a different and worse thing to be told.
//
// Asserts the SHIPPED string via mappingRemovalMessage rather than rebuilding it, because a test that
// reimplements the message passes whatever the real one says.
func TestTheRemovalWarningNamesTheResolverRatherThanAnEmptySource(t *testing.T) {
	msg := mappingRemovalMessage("userId", mappingSource{action: "get", in: "path", resolver: "dependentLookup"})

	require.Contains(t, msg, "supplied by its dependentLookup resolver")
	require.NotContains(t, msg, `from ""`,
		"the message describes a resolver-supplied parameter as having an empty CR source")

	// And the CR-sourced form must be unchanged, or this fix silently rewrote every existing warning.
	fromCR := mappingRemovalMessage("id", mappingSource{action: "update", in: "path", from: "status.thingId"})
	require.Contains(t, fromCR, `from "status.thingId"`)
	require.NotContains(t, fromCR, "resolver")
}

// #419 exempts a SELF-REFERENTIAL source -- `inCustomResource: spec.userId` for {userId} -- because
// suppressing the parameter would delete the very property the mapping reads. A resolver does not read
// that property: measured in the controller, a secretRef reads NameFromCustomResource/KeyFromCustomResource
// and a dependentLookup reads lookupValues[name], with inCustomResource appearing only in error labels.
//
// So the exemption's premise is absent for a resolver, and both halves are asserted here: the non-resolver
// entry must still be exempt, or this change has broken #419.
func TestTheSelfReferentialExemptionDoesNotApplyToAResolver(t *testing.T) {
	withResolver, _, err := resolverParamGenerator(t, []FieldMappingEntry{
		{InPath: "userId", InCustomResource: "spec.userId", ResolverType: "dependentLookup", ResolverLookupName: "l"},
	}).BuildSpecSchema()
	require.NoError(t, err)
	require.NotContains(t, string(withResolver), `"userId"`,
		"a resolver entry was exempted as self-referential, but a resolver does not read that property, so "+
			"there is no source for the suppression to destroy")

	plainMapping, _, err := resolverParamGenerator(t, []FieldMappingEntry{
		{InPath: "userId", InCustomResource: "spec.userId"},
	}).BuildSpecSchema()
	require.NoError(t, err)
	require.Contains(t, string(plainMapping), `"userId"`,
		"#419 REGRESSION: a self-referential NON-resolver mapping must keep its property, or the rule "+
			"removes its own source and the verb cannot build a URL")
}

// -------- the secretRef-in-a-URL refusal --------

// Decided deliberately rather than assumed: a secretRef on a path or query parameter is REFUSED at
// generation, because the Secret's value would be interpolated into the request URL and redaction covers
// request bodies, not the request line.
func TestASecretRefIsRefusedOnlyWhereTheValueWouldReachTheURL(t *testing.T) {
	cases := []struct {
		name    string
		mapping FieldMappingEntry
		refused bool
	}{
		{"secretRef on a path parameter", FieldMappingEntry{InPath: "userId", ResolverType: "secretRef"}, true},
		{"secretRef on a query parameter", FieldMappingEntry{InQuery: "realm", ResolverType: "secretRef"}, true},
		{"secretRef in the body is allowed", FieldMappingEntry{InBody: "credentials.token", ResolverType: "secretRef"}, false},
		{"dependentLookup on a path parameter is allowed", FieldMappingEntry{InPath: "userId", ResolverType: "dependentLookup"}, false},
		{"dependentLookup on a query parameter is allowed", FieldMappingEntry{InQuery: "realm", ResolverType: "dependentLookup"}, false},
		{"a response-direction secretRef entry addresses no request destination", FieldMappingEntry{InResponse: "id", InCustomResource: "status.id", ResolverType: "secretRef"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := resolverParamGenerator(t, []FieldMappingEntry{tc.mapping}).refuseSecretRefInURL()
			if tc.refused {
				require.Error(t, err, "this mapping puts a Secret's value in the request URL and must be refused")
				require.Contains(t, err.Error(), "secretRef resolver cannot supply a path or query parameter")
				return
			}
			require.NoError(t, err, "this mapping does not put the value in the URL and must not be refused")
		})
	}
}

// The refusal must be REACHED, not merely implemented. Asserted through Generate, which is what the
// controller and oasgen-render both call -- a check wired into a function nobody invokes is the dead-sink
// defect this repo has already recorded twice.
func TestGenerateRefusesASecretRefBoundToTheURL(t *testing.T) {
	_, err := resolverParamGenerator(t, []FieldMappingEntry{
		{InPath: "userId", ResolverType: "secretRef"},
	}).Generate()
	require.Error(t, err, "Generate returned a result for a definition that would put a Secret in the URL")
	require.Contains(t, err.Error(), "request URL")

	// CONTROL: the same document without the offending mapping must generate, or the assertion above
	// could be passing because this fixture cannot generate at all.
	_, err = resolverParamGenerator(t, nil).Generate()
	require.NoError(t, err, "CONTROL FAILED: the fixture itself does not generate, so the refusal above proves nothing")
}

// Every offence is reported at once and in a stable order, so an author with several learns from one
// render and the message does not depend on verb iteration order.
func TestAllURLBoundSecretRefsAreReportedTogetherAndSorted(t *testing.T) {
	err := resolverParamGenerator(t, []FieldMappingEntry{
		{InQuery: "realm", ResolverType: "secretRef"},
		{InPath: "userId", ResolverType: "secretRef"},
	}).refuseSecretRefInURL()
	require.Error(t, err)

	msg := err.Error()
	require.Contains(t, msg, `path parameter "userId"`)
	require.Contains(t, msg, `query parameter "realm"`)
	require.Less(t, strings.Index(msg, `path parameter "userId"`), strings.Index(msg, `query parameter "realm"`),
		"offences are not in sorted order, so the message depends on fieldMapping order")
}
