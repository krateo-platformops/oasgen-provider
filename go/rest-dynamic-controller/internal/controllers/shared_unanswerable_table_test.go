//go:build unit || integration

package restResources

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

// sharedUnanswerableTable is the canonical case table, read by BOTH modules' tests.
//
// oasgen-provider decides this at generation time and WARNS on the RestDefinition, so somebody can
// add a findby while the document is still being written. This module decides the same thing at
// runtime and returns unstructured-runtime's ErrExistenceUnanswerable, so the runtime stops and
// reports instead of stranding the CR. They cannot share code -- separate Go modules, no dependency
// either way, both predicates under internal/ -- so they share this file. Same arrangement as
// testdata/action-only-shape.json and testdata/resolver-digest-contract.json.
//
// THIS IS THE SECOND READER, and it is what makes the table load-bearing. With one reader a shared
// table is just a file: it pins nothing that a local literal would not. The commit that adds the
// second reader is the commit that turns it into a cross-module contract.
const sharedUnanswerableTable = "../../../../testdata/unanswerable-existence-cases.json"

// TestTheRuntimePredicateMatchesTheSharedTable asserts this module's predicate against every row,
// so a change here that oasgen-provider does not follow turns the OTHER module's suite red.
func TestTheRuntimePredicateMatchesTheSharedTable(t *testing.T) {
	b, err := os.ReadFile(sharedUnanswerableTable)
	require.NoError(t, err,
		"the shared unanswerable-existence table is missing. It is the only thing keeping this "+
			"runtime refusal and oasgen-provider's generation-time warning from disagreeing, so a "+
			"missing file is a FAILURE, not a skip")

	var doc struct {
		Cases []struct {
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
			Unanswerable bool   `json:"unanswerable"`
			Param        string `json:"param"`
			Source       string `json:"source"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(b, &doc), "the shared table does not parse")

	// CONTROLS ON THE TABLE ITSELF. A table that silently emptied, or that expected only one
	// verdict, would make a predicate that always answered the same way pass every row.
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
		"returned true would pass every row -- and a false positive here STOPS A RESOURCE FOREVER")

	for _, c := range doc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			res := getter.Resource{Kind: c.Kind}
			for _, v := range c.Verbs {
				vd := getter.VerbsDescription{Action: v.Action, Method: "GET", Path: v.Path}
				for _, m := range v.Mappings {
					vd.FieldMapping = append(vd.FieldMapping, getter.FieldMappingItem{
						InPath: m.InPath, InCustomResource: m.InCustomResource,
					})
				}
				for _, m := range v.DeprecatedMappings {
					vd.RequestFieldMapping = append(vd.RequestFieldMapping, getter.RequestFieldMappingItem{
						InPath: m.InPath, InCustomResource: m.InCustomResource,
					})
				}
				res.VerbsDescription = append(res.VerbsDescription, vd)
			}

			param, source, got := existenceIsPermanentlyUnanswerable(res)
			require.Equal(t, c.Unanswerable, got,
				"this module's RUNTIME predicate disagrees with the shared table (param=%q "+
					"source=%q). If oasgen-provider's generation-time predicate agrees with the table, "+
					"the two have DRIFTED: a kind would be warned about at apply time and not refused "+
					"at runtime, or refused at runtime with no warning that it would be", param, source)

			if !c.Unanswerable {
				return
			}
			require.Equal(t, c.Param, param, "the offending parameter differs from the table")
			require.Equal(t, c.Source, source, "the parameter's source differs from the table")
		})
	}
}
