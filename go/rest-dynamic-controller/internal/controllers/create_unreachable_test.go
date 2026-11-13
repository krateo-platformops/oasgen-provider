//go:build unit || integration

package restResources

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/unstructured-runtime/pkg/meta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// sharedShapeTable is the path, from this package, to the table BOTH modules' tests read.
const sharedShapeTable = "../../../../testdata/action-only-shape.json"

type shapeCase struct {
	Name          string   `json:"name"`
	Verbs         []string `json:"verbs"`
	CreateApiRef  bool     `json:"createApiRef"`
	ObserveApiRef bool     `json:"observeApiRef"`
	ActionOnly    bool     `json:"actionOnly"`
	Why           string   `json:"why"`
}

func loadSharedShapeTable(t *testing.T) []shapeCase {
	t.Helper()
	b, err := os.ReadFile(sharedShapeTable)
	require.NoError(t, err,
		"the shared action-only table is missing. It is the ONLY thing keeping this predicate and "+
			"oasgen-provider's generation-time one from disagreeing, so a missing file is a failure, "+
			"not a skip. Looked in %s", filepath.Clean(sharedShapeTable))

	var doc struct {
		Cases []shapeCase `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(b, &doc))
	require.NotEmpty(t, doc.Cases, "the shared table is empty, so every assertion over it is vacuous")
	return doc.Cases
}

func resourceFor(tc shapeCase) *getter.Resource {
	r := &getter.Resource{}
	for _, v := range tc.Verbs {
		r.VerbsDescription = append(r.VerbsDescription, getter.VerbsDescription{Action: v})
	}
	if tc.CreateApiRef {
		r.CreateApiRef = &getter.ApiRef{Name: "create-action", Namespace: "krateo-system"}
	}
	if tc.ObserveApiRef {
		r.ObserveApiRef = &getter.ApiRef{Name: "observe-action", Namespace: "krateo-system"}
	}
	return r
}

// THE CROSS-MODULE CONTRACT. Every row here is also asserted by oasgen-provider's suite against its
// own generation-time predicate. If the two ever disagree about a shape, one of the two suites goes
// red and names the row.
func TestTheActionOnlyPredicateMatchesTheSharedTable(t *testing.T) {
	cases := loadSharedShapeTable(t)

	// The table must contain BOTH outcomes, asserted rather than assumed: an all-true table passes
	// against a predicate that returns true unconditionally, and an all-false one against the
	// opposite. Either would make this file a decoration.
	var wantTrue, wantFalse int
	for _, tc := range cases {
		if tc.ActionOnly {
			wantTrue++
		} else {
			wantFalse++
		}
	}
	require.NotZero(t, wantTrue, "no row expects action-only: this table passes against a constant false")
	require.NotZero(t, wantFalse, "no row forbids it: this table passes against a constant true")

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			assert.Equal(t, tc.ActionOnly, isActionOnlyResource(resourceFor(tc)), tc.Why)
		})
	}
}

func TestANilResourceIsNotActionOnly(t *testing.T) {
	assert.False(t, isActionOnlyResource(nil),
		"a nil resource must not be reported as action-only: the diagnostic makes a positive claim "+
			"about a shape, and no shape was observed")
}

// THE THIRD CLAUSE, which is the difference between a useful diagnostic and a false positive on every
// healthy resource of exactly the kind this exists to help.
//
// For an action-only kind the create fires on not-found, and afterwards findby MATCHES -- that is the
// normal terminal state of a provisioned node. Only a kind whose create has NEVER succeeded is stuck.
func TestOnlyAResourceWhoseCreateNeverSucceededIsReported(t *testing.T) {
	actionOnly := resourceFor(shapeCase{Verbs: []string{"findby", "create"}})
	require.True(t, isActionOnlyResource(actionOnly), "the fixture must be action-only or this proves nothing")

	never := &unstructured.Unstructured{Object: map[string]any{}}
	assert.True(t, createIsUnreachable(actionOnly, never),
		"a kind whose create has never succeeded and whose findby matches is the issue 629 defect")

	// THE CASE THAT MUST NOT BE REPORTED: the create fired, so findby matching afterwards is success.
	succeeded := &unstructured.Unstructured{Object: map[string]any{}}
	meta.SetExternalCreateSucceeded(succeeded, time.Now().Add(-time.Hour))
	assert.False(t, createIsUnreachable(actionOnly, succeeded),
		"a resource whose create ALREADY SUCCEEDED was reported as unreachable. A provisioned node is "+
			"still in the collection, so its findby matches forever -- correctly. Reporting on a matched "+
			"findby alone warns about every successful resource, which is the false positive issue 625 "+
			"is about, in a new place")

	// And a non-action-only kind is never reported, whatever its create history.
	withGet := resourceFor(shapeCase{Verbs: []string{"findby", "create", "get"}})
	assert.False(t, createIsUnreachable(withGet, never),
		"a kind with a get verb does not decide existence from findby alone")
}
