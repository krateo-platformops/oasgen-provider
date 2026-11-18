//go:build unit

package oas2jsonschema

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestASpecSchemaFieldThatIsNotADeclaredParameterIsNoLongerSilent is the case #828 is actually about,
// which is NARROWER than the issue as filed.
//
// The issue said a configurationFields entry that does not resolve is skipped silently. That is not true
// in general: removeConfiguredFields ALREADY warns ("field 'x' set in configurationFields not found in
// schema") whenever the name is absent from the generated spec schema, which covers the ordinary typo. A
// test named "should generate a warning when a configured field is not found" has asserted that warning
// for as long as the behaviour has existed.
//
// The silence needs BOTH halves to disagree: a name that IS a property of the spec schema, so the removal
// succeeds and says nothing, and that is NOT declared as a parameter, so the Configuration builder skips
// it. `limit` as a create-body property is exactly that shape -- and it is the shape a consumer hits,
// because a name worth putting in configurationFields usually is somewhere in the document.
//
// MEASURED, both arms, on this fixture:
//
//	before  1 warning   "no identifiers or additional status fields defined"  <- nothing names 'limit'
//	after   2 warnings  the second names 'limit', the consequence, and the CAUSE
//
// The before-arm warning is the control: the generator was reached and did emit a diagnostic, so the
// absence of one naming 'limit' is a real silence rather than a dead instrument.
func TestASpecSchemaFieldThatIsNotADeclaredParameterIsNoLongerSilent(t *testing.T) {
	resourceConfig := &ResourceConfig{
		Verbs: []Verb{{Action: "create", Path: "/widgets", Method: "post"}},
		ConfigurationFields: []ConfigurationField{{
			FromOpenAPI:        FromOpenAPI{Name: "limit", In: "query"},
			FromRestDefinition: FromRestDefinition{Actions: []string{"create"}},
		}},
	}
	mockDoc := &mockOASDocument{Paths: map[string]*mockPathItem{"/widgets": {Ops: map[string]Operation{
		"post": &mockOperation{RequestBody: RequestBodyInfo{Content: map[string]*Schema{
			"application/json": {Type: []string{"object"}, Properties: []Property{
				{Name: "limit", Schema: &Schema{Type: []string{"integer"}}},
			}},
		}}},
	}}}}

	res, err := NewOASSchemaGenerator(mockDoc, DefaultGeneratorConfig(), resourceConfig).Generate()
	require.NoError(t, err)

	// PARSE CONTROL: the generator ran and produced diagnostics at all. Without this, a fixture that
	// failed to build would give an empty list and the assertion below would read as a pass.
	require.NotEmpty(t, res.GenerationWarnings, "the generator emitted nothing at all -- the fixture is broken, not the subject")

	var named, removalWarned bool
	for _, w := range res.GenerationWarnings {
		m := w.Error()
		if strings.Contains(m, "'limit'") || strings.Contains(m, `"limit"`) {
			if strings.Contains(m, "not found in schema") {
				removalWarned = true
			} else {
				named = true
			}
		}
	}

	require.True(t, named,
		"the omission of 'limit' from the generated Configuration CRD is not reported, so an author learns "+
			"of it only when the apiserver rejects their Configuration CR for an unknown field")

	// THE OTHER HALF, asserted so the narrowing in this test's name stays true. If removeConfiguredFields
	// starts warning here too, the premise above has changed and whoever changed it should read this.
	require.False(t, removalWarned,
		"removeConfiguredFields now warns for a name that IS in the spec schema -- the silence this test "+
			"describes no longer needs the Configuration builder's diagnostic, and #828's shape has moved")
}

// TestTheUnresolvedCauseIsNotAlwaysAnUndeclaredParameter asserts the distinction the refusal decided for
// #828 must turn on: four different things produce "did not resolve", and only one of them is the
// author's parameter being undeclared.
//
// Without this, a refusal would tell an author to check a spelling that is correct.
func TestTheUnresolvedCauseIsNotAlwaysAnUndeclaredParameter(t *testing.T) {
	doc := &mockOASDocument{Paths: map[string]*mockPathItem{"/widgets": {Ops: map[string]Operation{
		"post": &mockOperation{},
	}}}}

	cases := []struct {
		name  string
		verbs []Verb
		field ConfigurationField
		want  UnresolvedCause
	}{{
		name:  "the parameter is undeclared, and the path and method are both served",
		verbs: []Verb{{Action: "create", Path: "/widgets", Method: "post"}},
		field: ConfigurationField{
			FromOpenAPI:        FromOpenAPI{Name: "nope", In: "query"},
			FromRestDefinition: FromRestDefinition{Actions: []string{"create"}},
		},
		want: CauseParameterUndeclared,
	}, {
		name:  "the resource declares no such action",
		verbs: []Verb{{Action: "get", Path: "/widgets", Method: "post"}},
		field: ConfigurationField{
			FromOpenAPI:        FromOpenAPI{Name: "nope", In: "query"},
			FromRestDefinition: FromRestDefinition{Actions: []string{"create"}},
		},
		want: CauseNoSuchAction,
	}, {
		name:  "the document does not contain the verb's path",
		verbs: []Verb{{Action: "create", Path: "/gadgets", Method: "post"}},
		field: ConfigurationField{
			FromOpenAPI:        FromOpenAPI{Name: "nope", In: "query"},
			FromRestDefinition: FromRestDefinition{Actions: []string{"create"}},
		},
		want: CausePathAbsent,
	}, {
		name:  "the method is not declared on that path",
		verbs: []Verb{{Action: "create", Path: "/widgets", Method: "put"}},
		field: ConfigurationField{
			FromOpenAPI:        FromOpenAPI{Name: "nope", In: "query"},
			FromRestDefinition: FromRestDefinition{Actions: []string{"create"}},
		},
		want: CauseMethodAbsent,
	}}

	seen := map[UnresolvedCause]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewOASSchemaGenerator(doc, DefaultGeneratorConfig(), &ResourceConfig{
				Verbs: tc.verbs, ConfigurationFields: []ConfigurationField{tc.field},
			})
			_, err := g.findParameterInOAS(tc.field)
			require.Error(t, err, "this fixture must NOT resolve, or the row tests nothing")

			set, ok := err.(*UnresolvedFieldSet)
			require.True(t, ok, "the error must carry the cause, not just a message")
			require.Len(t, set.PerAction, 1)
			require.Equal(t, tc.want, set.PerAction[0].Cause,
				"got %q, which would send the author to fix the wrong thing", set.PerAction[0].Cause)

			require.Equal(t, tc.want == CauseParameterUndeclared, set.AllUndeclared(),
				"AllUndeclared is what the refusal turns on, and it disagrees with the cause")
			seen[tc.want] = true
		})
	}

	// EXHAUSTIVENESS: every cause the type declares is exercised. A new cause added without a row here
	// would otherwise be refused on, or not, by accident.
	for _, c := range []UnresolvedCause{CauseParameterUndeclared, CauseNoSuchAction, CausePathAbsent, CauseMethodAbsent} {
		require.True(t, seen[c], "no row produces cause %q, so nothing asserts what a refusal would do with it", c)
	}
}

// TestAnEmptyCauseSetIsNotAllUndeclared pins the one answer that would make the refusal fire on nothing
// at all -- the vacuous true this whole precondition exists to avoid.
func TestAnEmptyCauseSetIsNotAllUndeclared(t *testing.T) {
	require.False(t, (&UnresolvedFieldSet{Name: "x", In: "query"}).AllUndeclared(),
		"a field with NO recorded causes reports that every cause was an undeclared parameter, so a "+
			"refusal built on it would fire where nothing was measured")
}
