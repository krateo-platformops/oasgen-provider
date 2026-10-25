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
