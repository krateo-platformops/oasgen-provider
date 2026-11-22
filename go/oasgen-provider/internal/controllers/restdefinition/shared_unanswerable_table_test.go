//go:build unit || integration

package restdefinition

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
)

// sharedUnanswerableTablePath is the canonical case table, read by BOTH modules' tests.
//
// oasgen-provider decides this at generation time and WARNS on the RestDefinition;
// rest-dynamic-controller decides the same thing at runtime and returns unstructured-runtime's
// ErrExistenceUnanswerable so the runtime stops and reports instead of stranding the CR. They cannot
// share code -- separate Go modules, no dependency either way, both predicates under internal/ -- so
// they share this file. Same arrangement as testdata/action-only-shape.json.
//
// A "keep these in sync" comment would not do this: it fails silently, and the whole point is that a
// WARNING and a RUNTIME REFUSAL must not disagree about which kinds they describe.
const sharedUnanswerableTablePath = "../../../../../testdata/unanswerable-existence-cases.json"

type unanswerableCase struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Verbs []struct {
		Action   string `json:"action"`
		Path     string `json:"path"`
		Mappings []struct {
			InPath           string `json:"inPath"`
			InCustomResource string `json:"inCustomResource"`
		} `json:"mappings"`
		DeprecatedMappings []struct {
			InPath           string `json:"inPath"`
			InCustomResource string `json:"inCustomResource"`
		} `json:"deprecatedMappings"`
	} `json:"verbs"`
	SpecFields   []string `json:"specFields"`
	Unanswerable bool     `json:"unanswerable"`
	Param        string   `json:"param"`
	Source       string   `json:"source"`
}

// TestTheGenerationTimePredicateMatchesTheSharedTable asserts this module's predicate against every
// row, so a change here that rest-dynamic-controller does not follow turns the OTHER module's suite
// red and names the row.
func TestTheGenerationTimePredicateMatchesTheSharedTable(t *testing.T) {
	b, err := os.ReadFile(sharedUnanswerableTablePath)
	require.NoError(t, err,
		"the shared unanswerable-existence table is missing. It is the only thing keeping this "+
			"warning and rest-dynamic-controller's runtime refusal from disagreeing, so a missing "+
			"file is a FAILURE, not a skip")

	var doc struct {
		Cases []unanswerableCase `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(b, &doc), "the shared table does not parse")

	// CONTROLS ON THE TABLE ITSELF, because a table that silently emptied would make every
	// assertion below pass over nothing.
	require.NotEmpty(t, doc.Cases, "the shared table parsed to ZERO cases")
	var wantTrue, wantFalse int
	for _, c := range doc.Cases {
		if c.Unanswerable {
			wantTrue++
		} else {
			wantFalse++
		}
	}
	require.Greater(t, wantTrue, 0, "no row expects unanswerable=true, so a predicate that always "+
		"returned false would pass every row")
	require.Greater(t, wantFalse, 0, "no row expects unanswerable=false, so a predicate that always "+
		"returned true would pass every row")

	for _, c := range doc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			res := definitionv1alpha1.Resource{Kind: c.Kind}
			for _, v := range c.Verbs {
				vd := definitionv1alpha1.VerbsDescription{Action: v.Action, Method: "GET", Path: v.Path}
				for _, m := range v.Mappings {
					vd.FieldMapping = append(vd.FieldMapping, definitionv1alpha1.FieldMappingItem{
						InPath: m.InPath, InCustomResource: m.InCustomResource,
					})
				}
				for _, m := range v.DeprecatedMappings {
					vd.RequestFieldMapping = append(vd.RequestFieldMapping, definitionv1alpha1.RequestFieldMappingItem{
						InPath: m.InPath, InCustomResource: m.InCustomResource,
					})
				}
				res.VerbsDescription = append(res.VerbsDescription, vd)
			}

			param, source, got := unanswerableExistence(res, specFieldSet(c.SpecFields))
			require.Equal(t, c.Unanswerable, got,
				"this module's generation-time predicate disagrees with the shared table "+
					"(param=%q source=%q). If rest-dynamic-controller's runtime predicate agrees with "+
					"the table, the two have DRIFTED and a kind is warned about in one place and not "+
					"refused in the other", param, source)

			if !c.Unanswerable {
				return
			}
			require.Equal(t, c.Param, param, "the offending parameter differs from the table")
			require.Equal(t, c.Source, source, "the parameter's source differs from the table")
		})
	}
}

// specFieldSet turns the fixture's list into the predicate's set.
//
// AN EMPTY LIST BECOMES AN EMPTY SET, NOT NIL, and the distinction is the whole point of the column:
// nil means "unknown" and keeps the conservative verdict, while empty means "this kind really has no
// spec properties". Every fixture case states its list explicitly so none of them depends on which
// one a reader happens to produce.
func specFieldSet(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}
