//go:build unit

package oas2jsonschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestABracketEscapedBindingIsRecognisedAsSupplyingItsParameter is #514.
//
// A parameter whose NAME contains a dot must be bracket-escaped in a fieldMapping, because an unescaped dot
// reads as a path separator. The supplied map was keyed on the author's raw spelling while suppliesFor looks
// it up by the OAS parameter's name, so the exemption never fired and the parameter appeared as a spec field
// alongside the one the author meant to expose -- forcing "exclude AND bind" for something that is bound.
//
// Reported from a live Azure DevOps fleet: `inQuery: '["searchCriteria.sourceRefName"]'` bound to
// spec.sourceRefName, with searchCriteria.sourceRefName still generated as a spec field.
func TestABracketEscapedBindingIsRecognisedAsSupplyingItsParameter(t *testing.T) {
	verbs := []Verb{{
		Action: "findby",
		FieldMapping: []FieldMappingEntry{
			{InQuery: `["searchCriteria.sourceRefName"]`, InCustomResource: "spec.sourceRefName"},
			{InPath: `["project.id"]`, InCustomResource: "spec.projectId"},
		},
	}}
	sp := mappingSuppliedParams(verbs)

	src, ok := sp.suppliesFor("findby", "searchCriteria.sourceRefName")
	require.True(t, ok,
		"the escaped inQuery binds this parameter; keying on the raw spelling made the lookup impossible")
	assert.Equal(t, "query", src.in)
	assert.Equal(t, "spec.sourceRefName", src.from)

	// THE OTHER MAP. suppliesFor consults byAction as well as firstBy, so normalising only one of them
	// would leave the lookup failing for a different reason -- which is what makes this an assertion and
	// not a duplicate of the one above.
	pathSrc, ok := sp.suppliesFor("findby", "project.id")
	require.True(t, ok, "inPath has the identical defect, two lines above the inQuery case")
	assert.Equal(t, "path", pathSrc.in)

	// anySupplier reads firstBy directly, so it must agree.
	_, any := sp.anySupplier("searchCriteria.sourceRefName")
	assert.True(t, any)
}

// TestTheNormalisationIsAsymmetricAndCannotBreakWhatWorks pins the three cases, because the fix must only
// ever turn a non-match into a match.
func TestTheNormalisationIsAsymmetricAndCannotBreakWhatWorks(t *testing.T) {
	for _, tc := range []struct {
		in, want, why string
	}{
		{in: `["searchCriteria.sourceRefName"]`, want: "searchCriteria.sourceRefName",
			why: "one segment, so the mapping value names a parameter"},
		{in: "pullRequestId", want: "pullRequestId",
			why: "one segment already; unchanged"},
		{in: "searchCriteria.sourceRefName", want: "searchCriteria.sourceRefName",
			why: "TWO segments -- a dotted name written WITHOUT brackets is ambiguous, so the raw string " +
				"is kept. Guessing here would change behaviour for definitions that work today"},
		{in: "a.b.c", want: "a.b.c",
			why: "a genuine nested path stays raw"},
		{in: `["a"]["b"]`, want: `["a"]["b"]`,
			why: "two segments, raw"},
		{in: "credentials[0]", want: "credentials[0]",
			why: "unparseable since #513 step 2 refuses indices -- falls back to raw, so the refusal " +
				"cannot turn a working binding into a broken one here"},
	} {
		assert.Equal(t, tc.want, boundParameterName(tc.in), "%q: %s", tc.in, tc.why)
	}
}
