package oas2jsonschema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// THE THREE-WAY COMPOSITION, and it is the one neither reader checked.
//
// #455's guard condition is a CONJUNCTION: excludedSpecFields ∩ required ∩ NOT mapping-supplied. Two of
// the three terms already had arms — the path-item merge (#197/#299) and the exclusion guard (#410) — and
// the pair of them had one (pathitem_param_mapping_test.go, whose own comment names "two good halves,
// never compared"). The THREE together had none.
//
// That gap produced a concrete error, recorded here because the test exists to stop it recurring. The
// openstack-ironic session reported four live RestDefinitions as broken by 0.51.0, with twelve live CRs at
// stake, on the reasoning `excludedSpecFields ∩ required`. I verified the conjunct they were unsure about
// — that a path-ITEM parameter does reach requiredParameters, which is true — and relayed the conclusion
// onward. All four supply `uuid` through a request-direction fieldMapping, so the pre-existing exemption
// already covered them and a fresh install generates them correctly. A correct answer to one half of a
// conjunction read as confirmation of the whole.
//
// This arm is therefore an OVER-EAGERNESS control, not a fix arm: the behaviour it pins is pre-existing
// and #455's narrowing must not change it. It is the arm most likely to be skipped, because the behaviour
// already looks right.
//
// The document reuses pathItemParamRedirectedSpec deliberately: `uuid` is declared ONLY at path-item
// level, so a reader of op.Parameters alone finds nothing and a test passing by looking there would be
// asserting against an empty list.
func TestExcludingAMappingSuppliedRequiredPathItemParameterIsHonoured(t *testing.T) {
	doc, err := NewLibOASParser().Parse([]byte(pathItemParamRedirectedSpec))
	require.NoError(t, err)

	g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
		Identifiers: []string{"name"},
		Verbs: []Verb{
			{Action: ActionCreate, Method: "POST", Path: "/v1/nodes"},
			{
				Action: ActionGet, Method: "GET", Path: "/v1/nodes/{uuid}",
				// The real shape: the controller fills the path parameter from the CR rather than
				// asking the author for it.
				FieldMapping: []FieldMappingEntry{
					{InPath: "uuid", InCustomResource: "status.uuid"},
				},
			},
		},
		// And the author ALSO excludes it. Before #455 this was already fine, because the mapping keeps
		// `uuid` out of requiredParameters entirely -- the guard never sees it.
		ExcludedSpecFields: []string{"uuid"},
	})

	schema, warnings, err := g.BuildSpecSchema()
	require.NoError(t, err)

	for _, w := range warnings {
		var sge SchemaGenerationError
		if ok := asSchemaGenerationError(w, &sge); ok {
			require.NotContains(t, sge.Message, "REQUIRED parameter",
				"the guard refused a mapping-supplied parameter: #455's narrowing has over-reached into "+
					"a case the pre-existing mapping exemption already handled. Warning: %s", sge.Message)
		}
	}

	require.NotContains(t, string(schema), `"uuid"`,
		"`uuid` is in the generated spec. Either the exclusion was refused, or the mapping suppression "+
			"stopped working -- both leave the author asked for a value the controller supplies itself")
}

// asSchemaGenerationError is a local errors.As without importing errors into this file's surface, kept
// here so the assertion above reads as one statement.
func asSchemaGenerationError(err error, out *SchemaGenerationError) bool {
	if sge, ok := err.(SchemaGenerationError); ok {
		*out = sge
		return true
	}
	return false
}
