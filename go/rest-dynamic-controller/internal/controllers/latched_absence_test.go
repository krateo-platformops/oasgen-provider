package restResources

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// latchedHandler builds a handler whose fake client holds mg, so patchAnnotations can round-trip. It
// reuses driftHandler's fake-client wiring deliberately: the latched counter and the drift counter are the
// same persistence mechanism, and a second hand-built harness would be a second thing to keep in step.
func latchedHandler(t *testing.T, mg *unstructured.Unstructured) (*handler, *recordingEvents) {
	t.Helper()
	h, _ := driftHandler(t, mg)
	evs := &recordingEvents{}
	h.eventRecorder = evs
	return h, evs
}

func synthesised404() error {
	return &restclient.StatusError{StatusCode: http.StatusNotFound, Synthesised: true}
}

// TestTheLatchedReportFiresExactlyOnceAtTheBudget is the test the first implementation of #485 could not
// have passed: the report lives on the path that returns before reaching the else-if, so this exercises
// the destination reportInferredAbsence cannot see.
//
// It asserts the SHAPE of the signal, not merely its existence: silent through a normal async create,
// exactly one warning when the latch has held for the whole budget, and never a second one. An
// implementation that reported on every latched observe passes "it fires" and fails here.
func TestTheLatchedReportFiresExactlyOnceAtTheBudget(t *testing.T) {
	mg := driftMG(1, nil)
	h, evs := latchedHandler(t, mg)
	ctx := context.Background()

	// Below the budget: a create in flight must produce nothing at all.
	for i := 1; i < maxLatchedAbsenceRepeats; i++ {
		fired := h.reportLatchedInferredAbsence(ctx, mg, synthesised404(), logging.NewNopLogger())
		assert.False(t, fired, "must stay silent on latched observe %d of %d", i, maxLatchedAbsenceRepeats)
		assert.Empty(t, evs.all(), "no event may be emitted before the budget is reached")
	}

	// At the budget: exactly one warning.
	require.True(t, h.reportLatchedInferredAbsence(ctx, mg, synthesised404(), logging.NewNopLogger()),
		"the report must fire once the latch has held for the whole budget")
	require.Len(t, evs.all(), 1, "exactly one event at the budget")
	assert.Equal(t, reasonAbsenceLatched, evs.all()[0].Reason)
	assert.Contains(t, evs.all()[0].Message, "consecutive checks WITHOUT being observed")

	// Past the budget: never again, however long the latch holds.
	for i := 0; i < 3; i++ {
		assert.False(t, h.reportLatchedInferredAbsence(ctx, mg, synthesised404(), logging.NewNopLogger()),
			"the report must not re-fire while the latch continues to hold")
	}
	assert.Len(t, evs.all(), 1, "one event per latch, not one per poll")
}

// TestTheCountStopsBeingWrittenOnceReported is the guarantee that separates this counter from the drift
// counter it is modelled on. recordDriftRepeat patches unconditionally, including past its threshold --
// acceptable there, because a non-converging update reaches a human. Here the latch may hold for days (the
// measured case held for 318 observes), so an instrument that kept patching would be writing to the stuck
// resource it is complaining about, forever.
func TestTheCountStopsBeingWrittenOnceReported(t *testing.T) {
	mg := driftMG(1, nil)
	h, _ := latchedHandler(t, mg)
	ctx := context.Background()

	for i := 0; i < maxLatchedAbsenceRepeats; i++ {
		h.reportLatchedInferredAbsence(ctx, mg, synthesised404(), logging.NewNopLogger())
	}
	atBudget := mg.GetAnnotations()[annotationLatchedAbsenceRepeats]
	require.Equal(t, strconv.Itoa(maxLatchedAbsenceRepeats), atBudget,
		"the count must reach the budget exactly")

	for i := 0; i < 4; i++ {
		h.reportLatchedInferredAbsence(ctx, mg, synthesised404(), logging.NewNopLogger())
	}
	assert.Equal(t, atBudget, mg.GetAnnotations()[annotationLatchedAbsenceRepeats],
		"the count must stop advancing once reported -- a latched resource must not be written to every poll")
}

// TestOnlyASynthesisedNotFoundIsCounted keeps the latched path keyed on the same distinction as the
// non-latched one. A vendor 404 while Pending is a different situation: the API said the resource is not
// there, which is a report rather than an inference, and nothing about it is unobserved.
func TestOnlyASynthesisedNotFoundIsCounted(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"a vendor 404", &restclient.StatusError{StatusCode: http.StatusNotFound}},
		{"a non-404", &restclient.StatusError{StatusCode: http.StatusInternalServerError, Synthesised: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mg := driftMG(1, nil)
			h, evs := latchedHandler(t, mg)
			for i := 0; i < maxLatchedAbsenceRepeats+2; i++ {
				assert.False(t, h.reportLatchedInferredAbsence(context.Background(), mg, tc.err, logging.NewNopLogger()))
			}
			assert.Empty(t, evs.all(), "%s must never produce a latched-absence event", tc.name)
			assert.Empty(t, mg.GetAnnotations()[annotationLatchedAbsenceRepeats],
				"%s must not be counted at all", tc.name)
		})
	}
}

// TestASpecEditRestartsTheCount is why the signature carries the generation. An author who reads the
// warning and changes the CR in response must get a fresh budget rather than an instrument that has
// already fired and gone quiet.
func TestASpecEditRestartsTheCount(t *testing.T) {
	mg := driftMG(1, nil)
	h, evs := latchedHandler(t, mg)
	ctx := context.Background()

	for i := 0; i < maxLatchedAbsenceRepeats; i++ {
		h.reportLatchedInferredAbsence(ctx, mg, synthesised404(), logging.NewNopLogger())
	}
	require.Len(t, evs.all(), 1)

	mg.SetGeneration(2)
	assert.False(t, h.reportLatchedInferredAbsence(ctx, mg, synthesised404(), logging.NewNopLogger()),
		"a spec edit must restart the count, not re-fire immediately")
	assert.Equal(t, "1", mg.GetAnnotations()[annotationLatchedAbsenceRepeats],
		"the count must restart at 1 for the new generation")
	assert.Len(t, evs.all(), 1, "no second event until the new budget is reached")
}

// TestTheCountIsClearedWhenTheLatchEnds covers the exit: a resource whose create completes normally must
// not carry a stale count into any future latch.
func TestTheCountIsClearedWhenTheLatchEnds(t *testing.T) {
	mg := driftMG(1, nil)
	h, _ := latchedHandler(t, mg)
	ctx := context.Background()

	h.reportLatchedInferredAbsence(ctx, mg, synthesised404(), logging.NewNopLogger())
	require.Equal(t, "1", mg.GetAnnotations()[annotationLatchedAbsenceRepeats])

	require.NoError(t, h.clearLatchedAbsence(ctx, mg))
	assert.Empty(t, mg.GetAnnotations()[annotationLatchedAbsenceSignature])
	assert.Empty(t, mg.GetAnnotations()[annotationLatchedAbsenceRepeats])

	// And clearing again must cost nothing -- the guard is what keeps the quiet path free of a write per
	// reconcile, which is the whole reason this is cheap enough to call on every converged observe.
	require.NoError(t, h.clearLatchedAbsence(ctx, mg))
}

// TestAConvergingCreatePatchesBoundedlyThenLeavesNothingBehind is the realistic sequence, and it exists
// because a consumer corrected a claim they had made to me -- and that I had been about to rely on.
//
// The claim was "the quiet path stays a no-op". It is not quite: a normal async create IS
// Pending-plus-synthesised-404 for its first few observes, by definition, so it DOES patch while the count
// accumulates. A count has to accumulate to be a count. The guarantee is therefore narrower than "no
// writes on the healthy path", and worth pinning as what it actually is:
//
//	converging async create   patches k times, k = observes until the create lands, then CLEARED
//	latched resource          patches N times, then stops
//	never latched             never patches
//
// Every other test here drives one function in isolation. This one drives the order the call sites use,
// which is the only way the "then cleared" half is exercised at all: a regression that left the count
// behind would show up here as an annotation that outlives the latch, and nowhere else.
func TestAConvergingCreatePatchesBoundedlyThenLeavesNothingBehind(t *testing.T) {
	mg := driftMG(1, nil)
	h, evs := latchedHandler(t, mg)
	ctx := context.Background()

	// Three latched observes -- a create in flight, below the budget.
	const k = 3
	require.Less(t, k, maxLatchedAbsenceRepeats, "k must be below the budget or this tests the latched case")
	for i := 0; i < k; i++ {
		h.reportLatchedInferredAbsence(ctx, mg, synthesised404(), logging.NewNopLogger())
	}
	require.Equal(t, strconv.Itoa(k), mg.GetAnnotations()[annotationLatchedAbsenceRepeats],
		"the count must accumulate while the create is in flight -- it is a count")
	require.Empty(t, evs.all(), "a converging create must stay silent")

	// The create lands: the next observe does not take the latch, so the call site clears.
	require.NoError(t, h.clearLatchedAbsence(ctx, mg))

	assert.Empty(t, mg.GetAnnotations()[annotationLatchedAbsenceRepeats],
		"nothing may outlive the latch -- a count left behind would shorten the budget of a LATER latch")
	assert.Empty(t, mg.GetAnnotations()[annotationLatchedAbsenceSignature])
	assert.Empty(t, evs.all(), "and still no event: this resource converged")
}

// TestTheCallSitesAreCoveredByInspectionNotByThisFile documents what this file does NOT establish, for the
// same reason its sibling in inferred_absence_report_test.go does.
//
// Every test here calls the helpers directly. None goes through Observe, so none would notice if a call
// site were deleted -- the report call inside the Pending latch, or either clear. Those are verified by
// reading restResources.go: reportLatchedInferredAbsence immediately before the latch's return, and
// clearLatchedAbsence on the two paths that do not take the latch.
//
// Stating the gap is the point. A suite that kills five mutations reads as thorough, and a deleted call
// site would leave every one of them green.
func TestTheCallSitesAreCoveredByInspectionNotByThisFile(t *testing.T) {
	t.Skip("documents the coverage boundary; the call-site wiring is verified by inspection and by a live run")
}
