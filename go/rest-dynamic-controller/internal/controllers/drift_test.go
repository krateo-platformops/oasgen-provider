package restResources

import (
	"context"
	"testing"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/comparison"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func driftMG(generation int64, ann map[string]string) *unstructured.Unstructured {
	mg := &unstructured.Unstructured{}
	mg.SetGroupVersionKind(widgetGVK)
	mg.SetNamespace("default")
	mg.SetName("w1")
	mg.SetGeneration(generation)
	if ann != nil {
		mg.SetAnnotations(ann)
	}
	return mg
}

func notEqualAt(path []string, reason string) comparison.ComparisonResult {
	return comparison.ComparisonResult{
		IsEqual: false,
		Reason:  &comparison.Reason{Reason: reason, Path: path},
	}
}

func driftHandler(t *testing.T, seed *unstructured.Unstructured) (*handler, *fake.FakeDynamicClient) {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{widgetGVR: "WidgetList"}, seed)
	return &handler{
		pluralizer:    fakePluralizer{},
		logger:        logging.NewNopLogger(),
		dynamicClient: dyn,
	}, dyn
}

func TestDriftSignatureNamesTheFieldAndTheGeneration(t *testing.T) {
	res := notEqualAt([]string{"nodePools", "0", "instance"}, "values differ")

	assert.Equal(t, "gen=4;field=nodePools[0].instance", driftSignature(driftMG(4, nil), res))
}

func TestDriftSignatureChangesWhenTheSpecIsEdited(t *testing.T) {
	res := notEqualAt([]string{"nodePools", "0", "instance"}, "values differ")

	before := driftSignature(driftMG(4, nil), res)
	after := driftSignature(driftMG(5, nil), res)

	assert.NotEqual(t, before, after,
		"editing the spec must start a fresh attempt: a human acting on the condition has to get one")
}

func TestDriftSignatureChangesWhenADifferentFieldDiffers(t *testing.T) {
	mg := driftMG(1, nil)

	a := driftSignature(mg, notEqualAt([]string{"size"}, "values differ"))
	b := driftSignature(mg, notEqualAt([]string{"region"}, "values differ"))

	assert.NotEqual(t, a, b)
}

func TestDriftSignatureFallsBackToTheReasonWhenNoFieldIsAttributable(t *testing.T) {
	sig := driftSignature(driftMG(2, nil), notEqualAt(nil, "slice lengths differ"))

	assert.Equal(t, "gen=2;field=slice lengths differ", sig)
}

func TestDriftRepeatsCountsOnlyTheSameSignature(t *testing.T) {
	mg := driftMG(1, map[string]string{
		annotationDriftSignature: "gen=1;field=size",
		annotationDriftRepeats:   "3",
	})

	assert.Equal(t, 3, driftRepeats(mg, "gen=1;field=size"))
	assert.Equal(t, 0, driftRepeats(mg, "gen=1;field=region"),
		"a different difference must start its own count, not inherit another's")
}

func TestDriftRepeatsFailsOpenOnUnusableValues(t *testing.T) {
	for name, val := range map[string]string{
		"not a number": "banana",
		"negative":     "-2",
		"empty":        "",
	} {
		t.Run(name, func(t *testing.T) {
			mg := driftMG(1, map[string]string{
				annotationDriftSignature: "gen=1;field=size",
				annotationDriftRepeats:   val,
			})
			assert.Equal(t, 0, driftRepeats(mg, "gen=1;field=size"),
				"a corrupt brake must fail open rather than wedge the controller")
		})
	}
}

func TestDriftRepeatsIsNilSafe(t *testing.T) {
	assert.Equal(t, 0, driftRepeats(nil, "gen=1;field=size"))
	assert.Equal(t, 0, driftRepeats(driftMG(1, nil), ""))
}

func TestRecordDriftRepeatIncrementsAcrossReconciles(t *testing.T) {
	mg := driftMG(1, nil)
	h, _ := driftHandler(t, mg)
	sig := "gen=1;field=nodePools[0].instance"

	for want := 1; want <= maxDriftRepeats; want++ {
		got, err := h.recordDriftRepeat(context.Background(), mg, sig)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	assert.Equal(t, sig, mg.GetAnnotations()[annotationDriftSignature])
}

func TestRecordDriftRepeatRestartsOnANewSignature(t *testing.T) {
	mg := driftMG(1, nil)
	h, _ := driftHandler(t, mg)

	_, err := h.recordDriftRepeat(context.Background(), mg, "gen=1;field=size")
	require.NoError(t, err)
	_, err = h.recordDriftRepeat(context.Background(), mg, "gen=1;field=size")
	require.NoError(t, err)

	got, err := h.recordDriftRepeat(context.Background(), mg, "gen=2;field=size")
	require.NoError(t, err)
	assert.Equal(t, 1, got, "a new signature starts at one, so an edited spec gets the full budget again")
}

func TestClearDriftRepeatsRemovesTheBookkeeping(t *testing.T) {
	mg := driftMG(1, map[string]string{
		annotationDriftSignature: "gen=1;field=size",
		annotationDriftRepeats:   "4",
	})
	h, _ := driftHandler(t, mg)

	require.NoError(t, h.clearDriftRepeats(context.Background(), mg))

	assert.Empty(t, mg.GetAnnotations()[annotationDriftSignature])
	assert.Empty(t, mg.GetAnnotations()[annotationDriftRepeats])
}

func TestClearDriftRepeatsDoesNotWriteWhenThereIsNothingRecorded(t *testing.T) {
	mg := driftMG(1, nil)
	h, dyn := driftHandler(t, mg)
	dyn.ClearActions()

	require.NoError(t, h.clearDriftRepeats(context.Background(), mg))

	assert.Empty(t, dyn.Actions(),
		"the agreeing case is the common one and must not cost an apiserver write per reconcile")
}

// TestDriftSignatureIsStableWhenSiblingsDrift is the property the non-convergence budget rests on: the
// same unconvergeable resource must produce the SAME signature on every observe, or driftRepeats resets
// and the budget never reaches maxDriftRepeats by any bounded number of polls.
//
// It drives the real Aruba Kaas shape, where two siblings of nodePools[0] both drift (string in the CR,
// object in the response) and either is a legitimate "first difference". Before CompareExisting walked
// keys in sorted order this flipped per call, so a budget meant to halt after 5 futile updates halted
// only when the reported field happened to repeat 5 times running.
func TestDriftSignatureIsStableWhenSiblingsDrift(t *testing.T) {
	mg := driftMG(7, nil)

	sigs := map[string]int{}
	for i := 0; i < 100; i++ {
		spec := map[string]interface{}{
			"nodePools": []interface{}{map[string]interface{}{
				"instance":   "A4",
				"dataCenter": "ITBG-Bergamo",
				"name":       "pool-1",
			}},
		}
		resp := map[string]interface{}{
			"nodePools": []interface{}{map[string]interface{}{
				"instance":   map[string]interface{}{"id": "A4"},
				"dataCenter": map[string]interface{}{"id": "ITBG-Bergamo"},
				"name":       "pool-1",
			}},
		}
		res, err := comparison.CompareExisting(spec, resp)
		require.NoError(t, err)
		require.False(t, res.IsEqual, "the Kaas shape must register as drift")
		sigs[driftSignature(mg, res)]++
	}

	require.Len(t, sigs, 1, "the signature must be identical on every observe, got %v", sigs)
	for sig := range sigs {
		assert.Contains(t, sig, "gen=7", "the generation keeps a spec edit from inheriting the old budget")
		assert.Contains(t, sig, "nodePools[0].dataCenter", "and the field path is the deterministic one")
	}
}

// TestDriftRepeatsKeepsCountingAfterTheLatch pins the property that made the DriftNotConverging
// message wrong: the counter does NOT stop at maxDriftRepeats, because recordDriftRepeat runs on
// every observe that reports the same signature -- including the latching one and every one after,
// when no update is being issued at all.
//
// SO repeats IS NOT A COUNT OF UPDATES, and a message printing it as one is wrong twice over: by one
// at the moment of latching, and then by a growing amount forever. Measured by github-provider-kog
// on a live GitHub branch protection: 4 update PUTs against a message that said 5, and 7 shortly
// after.
//
// The arithmetic, which is the whole bug:
//
//	observes 1..4   each reports the difference AND issues an update   -> repeats reaches 4
//	observe  5      reports it, repeats hits maxDriftRepeats, LATCHES  -> no update issued
//	observes 6..n   still report it, repeats keeps climbing            -> still no update
//
// so at the latch: repeats == 5, updates issued == 4.
func TestDriftRepeatsKeepsCountingAfterTheLatch(t *testing.T) {
	mg := driftMG(1, nil)
	h, _ := driftHandler(t, mg)
	sig := "gen=1;field=reviews.bypass_pull_request_allowances"

	for want := 1; want <= maxDriftRepeats; want++ {
		got, err := h.recordDriftRepeat(context.Background(), mg, sig)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}

	// PAST THE LATCH. Nothing tested this before, and it is the half that makes "the update has been
	// issued N times" diverge without bound from the truth.
	for want := maxDriftRepeats + 1; want <= maxDriftRepeats+3; want++ {
		got, err := h.recordDriftRepeat(context.Background(), mg, sig)
		require.NoError(t, err)
		assert.Equal(t, want, got,
			"the counter must keep counting observes after the latch; if it saturated at %d instead, "+
				"then printing it as updates-issued would have been merely off by one rather than "+
				"unboundedly wrong, and the fix to the message would be a different fix",
			maxDriftRepeats)
	}
}

// NOT ASSERTED ABOVE, deliberately: that updates-issued at the latch is maxDriftRepeats-1. Nothing in
// this package counts updates, so any assertion here would have compared two constants and passed by
// construction -- a check that cannot fail still reports success, which is worse than its absence. The
// relationship is what the message must respect, and it is stated in the comment rather than dressed
// up as a test. github-provider-kog measured the real counts from the outside: 4 PUTs, message said 5.
