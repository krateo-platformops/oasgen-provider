//go:build unit || integration

package restResources

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	customcondition "github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/krateo-platformops/unstructured-runtime/pkg/meta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// THE OVERWRITE HAZARD, pinned on the source because no behavioural test can see it from outside.
//
// The up-to-date path ends by setting a Ready condition. If a CreateUnreachable is written anywhere
// EARLIER in Observe -- including right beside the findby match that detects it -- that final write
// overwrites it with Ready=True and the whole diagnostic becomes inert, while still passing any test
// that asserts the condition was set at the moment it was set.
//
// So the decision must happen AT that call site, and this asserts it does. A future author adding a
// second unconditional condition.Available() on this path reintroduces the bug silently; they have to
// come here and say why.
func TestReadyOnTheUpToDatePathIsDecidedAtTheSetConditionsCallSite(t *testing.T) {
	b, err := os.ReadFile("restResources.go")
	require.NoError(t, err)
	src := string(b)
	require.NotEmpty(t, src, "restResources.go is empty, so every Count below would be a vacuous 0")

	// The up-to-date path must route its Ready through the variable, not through a literal.
	require.Contains(t, src, "readyCondition := condition.Available()",
		"the up-to-date path no longer computes its Ready condition into a variable, so the "+
			"CreateUnreachable verdict cannot replace it")
	require.Contains(t, src, "unstructuredtools.SetConditions(mg, readyCondition)",
		"the final SetConditions no longer uses the computed condition, so a CreateUnreachable verdict "+
			"is discarded")
	assert.Contains(t, src, "h.createUnreachableCondition(mg, &clientInfo.Resource, log)",
		"the verdict is no longer consulted where Ready is decided")

	// CONTROL: the literal form this replaced must NOT come back on this path. Counting all
	// occurrences would be brittle -- Available() is legitimately set on other paths -- so this pins
	// the exact line that used to overwrite the verdict.
	assert.NotContains(t, src, "unstructuredtools.SetConditions(mg, condition.Available())\n\tif err != nil {\n\t\tlog.Error(err, \"Setting condition\")",
		"the unconditional Available() on the up-to-date path is back. That is the line that makes "+
			"issue 629's condition inert: it runs after the findby match and overwrites Ready=False "+
			"with Ready=True on the very resource the diagnostic is about")
}

// THE BEHAVIOUR AT THE HANDLER LEVEL: what a consumer actually receives.
//
// The predicate tests above establish the shape logic. This asserts the two things an operator sees
// -- the condition and the event -- and, critically, that a HEALTHY action-only resource receives
// NEITHER. The silent case is the one worth guarding: a diagnostic that fires on every provisioned
// node would be worse than none, and nothing in the predicate tests would catch it.
func TestTheUnreachableCreateVerdictProducesAConditionAndAnEvent(t *testing.T) {
	actionOnly := resourceFor(shapeCase{Verbs: []string{"findby", "create"}})
	log := logging.NewNopLogger()

	t.Run("a stuck resource gets the condition and the event", func(t *testing.T) {
		evs := &recordingEvents{}
		h := &handler{eventRecorder: evs}
		mg := &unstructured.Unstructured{Object: map[string]any{}}

		got := h.createUnreachableCondition(mg, actionOnly, log)

		require.NotNil(t, got, "a kind whose create has never succeeded and whose findby matches must be reported")
		assert.Equal(t, metav1.ConditionFalse, got.Status,
			"reporting Ready=True is the inverted health signal issue 629 measured")
		assert.Equal(t, customcondition.ReasonCreateUnreachable, got.Reason)
		assert.Contains(t, got.Message, "dispatch verb", "the message must offer the remedy, not just the problem")

		require.Len(t, evs.all(), 1, "exactly one event, so a repeating reconcile does not spam")
		assert.Equal(t, reasonCreateUnreachable, evs.all()[0].Reason)
	})

	t.Run("a resource whose create already succeeded gets NEITHER", func(t *testing.T) {
		evs := &recordingEvents{}
		h := &handler{eventRecorder: evs}
		mg := &unstructured.Unstructured{Object: map[string]any{}}
		meta.SetExternalCreateSucceeded(mg, time.Now().Add(-time.Hour))

		got := h.createUnreachableCondition(mg, actionOnly, log)

		assert.Nil(t, got,
			"a provisioned resource's findby matches forever, correctly. Reporting it would warn about "+
				"every successful resource of exactly the kind this exists to help")
		assert.Empty(t, evs.all(), "and it must not emit an event either")
	})

	t.Run("a kind with a get verb gets NEITHER", func(t *testing.T) {
		evs := &recordingEvents{}
		h := &handler{eventRecorder: evs}
		mg := &unstructured.Unstructured{Object: map[string]any{}}

		got := h.createUnreachableCondition(mg,
			resourceFor(shapeCase{Verbs: []string{"findby", "create", "get"}}), log)

		assert.Nil(t, got, "a kind with a get verb does not decide existence from findby alone")
		assert.Empty(t, evs.all())
	})
}
