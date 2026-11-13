package oas2jsonschema

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE OTHER HALF OF THE CROSS-MODULE CONTRACT.
//
// rest-dynamic-controller decides the same "action-only" shape at RUNTIME
// (restResources.isActionOnlyResource) to report a create that can never fire; this package decides
// it at GENERATION time, to suppress issue 625's warning and to emit issue 629's.
//
// The two cannot share code -- separate Go modules, both predicates under internal/ -- so they share
// `testdata/action-only-shape.json` at the repo root, and each module's suite asserts its OWN
// predicate against every row. If the two ever disagree about a shape, one of the two suites goes red
// and names the row.
//
// A "keep these in sync" comment would not do this. It fails silently, and the entire argument for
// one predicate is that a suppression and its inverse must not disagree about what they describe.
const sharedShapeTablePath = "../../../../../testdata/action-only-shape.json"

func TestTheGenerationTimePredicateMatchesTheSharedTable(t *testing.T) {
	b, err := os.ReadFile(sharedShapeTablePath)
	require.NoError(t, err,
		"the shared action-only table is missing. It is the only thing keeping this predicate and "+
			"rest-dynamic-controller's runtime one from disagreeing, so a missing file is a failure, "+
			"not a skip")

	var doc struct {
		Cases []struct {
			Name          string   `json:"name"`
			Verbs         []string `json:"verbs"`
			CreateApiRef  bool     `json:"createApiRef"`
			ObserveApiRef bool     `json:"observeApiRef"`
			ActionOnly    bool     `json:"actionOnly"`
			Why           string   `json:"why"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(b, &doc))
	require.NotEmpty(t, doc.Cases, "the shared table is empty, so every assertion over it is vacuous")

	// Both outcomes must be present, or the table passes against a constant predicate.
	var wantTrue, wantFalse int
	for _, tc := range doc.Cases {
		if tc.ActionOnly {
			wantTrue++
		} else {
			wantFalse++
		}
	}
	require.NotZero(t, wantTrue, "no row expects action-only: this table passes against a constant false")
	require.NotZero(t, wantFalse, "no row forbids it: this table passes against a constant true")

	// The row count is pinned so that a row added on the OTHER module's behalf, without this suite
	// being re-run, cannot go unnoticed here.
	assert.Equal(t, 9, len(doc.Cases),
		"the shared table has %d rows, not the 9 this suite was written against. A row was added or "+
			"removed -- confirm this predicate agrees with it, then update this count deliberately",
		len(doc.Cases))

	for _, tc := range doc.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			g := &OASSchemaGenerator{resourceConfig: &ResourceConfig{
				CreateDelegated:  tc.CreateApiRef,
				ObserveDelegated: tc.ObserveApiRef,
			}}
			for _, v := range tc.Verbs {
				g.resourceConfig.Verbs = append(g.resourceConfig.Verbs, Verb{Action: v})
			}
			assert.Equal(t, tc.ActionOnly, g.isActionOnly(), tc.Why)
		})
	}
}
