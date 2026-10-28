//go:build unit

package restResources

import (
	"testing"

	customcondition "github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
	unstructuredtools "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func observedCR(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "sample.krateo.io/v1-0",
		"kind":       "Lookupsample",
		"metadata":   map[string]interface{}{"name": "x", "namespace": "default"},
	}}
}

// TestSetObservedIfChangedOnlyReportsRealTransitions is the test that protects the two properties the
// caller depends on.
//
// tools.UpdateStatus issues an unconditional API write, and the condition constructors stamp
// LastTransitionTime: metav1.Now() on every call -- so without this comparison every set would look like
// a change, the bodyless observe path would write status on every single reconcile forever, and
// lastTransitionTime would mark "the most recent reconcile" rather than "when this last changed".
func TestSetObservedIfChangedOnlyReportsRealTransitions(t *testing.T) {
	mg := observedCR(t)

	t.Run("first set is a change", func(t *testing.T) {
		changed, err := setObservedIfChanged(mg, customcondition.Observed())
		require.NoError(t, err)
		require.True(t, changed, "the condition was absent, so setting it is a transition")
	})

	t.Run("setting the same state again is NOT a change", func(t *testing.T) {
		changed, err := setObservedIfChanged(mg, customcondition.Observed())
		require.NoError(t, err)
		require.False(t, changed, "an unchanged condition must not report a transition, or the caller writes every reconcile")
	})

	t.Run("flipping to False IS a change", func(t *testing.T) {
		changed, err := setObservedIfChanged(mg, customcondition.NotObserved("no body"))
		require.NoError(t, err)
		require.True(t, changed)
	})

	t.Run("and back again", func(t *testing.T) {
		changed, err := setObservedIfChanged(mg, customcondition.Observed())
		require.NoError(t, err)
		require.True(t, changed)
	})
}

// TestSetObservedPreservesOtherConditionTypes pins that Observed coexists with Ready.
//
// Conditions are upserted BY TYPE, so this should hold -- but if it ever stopped holding, every resource
// would trade its readiness report for this one, which is a far worse outcome than the problem #233
// describes.
func TestSetObservedPreservesOtherConditionTypes(t *testing.T) {
	mg := observedCR(t)

	// Seed a Ready condition the way the controller does.
	require.NoError(t, unstructuredtools.SetConditions(mg, customcondition.Pending()))

	_, err := setObservedIfChanged(mg, customcondition.Observed())
	require.NoError(t, err)

	types := map[string]string{}
	for _, c := range unstructuredtools.GetConditions(mg) {
		types[c.Type] = string(c.Status)
	}
	require.Contains(t, types, customcondition.TypeObserved, "Observed must be present")
	require.Contains(t, types, "Ready", "Ready must survive alongside Observed")
	require.Equal(t, "True", types[customcondition.TypeObserved])
	require.Equal(t, "False", types["Ready"], "the seeded Pending (Ready=False) must be untouched")
}

// TestSetObservedComparesReasonNotOnlyStatus guards the comparison being loosened to status alone, which
// would swallow a future second reason on the same status.
func TestSetObservedComparesReasonNotOnlyStatus(t *testing.T) {
	mg := observedCR(t)

	_, err := setObservedIfChanged(mg, customcondition.NotObserved("first"))
	require.NoError(t, err)

	same := customcondition.NotObserved("first")
	changed, err := setObservedIfChanged(mg, same)
	require.NoError(t, err)
	require.False(t, changed, "same status and reason is not a transition")

	// Same status, different reason -> must be treated as a change.
	other := customcondition.NotObserved("second")
	other.Reason = "SomeOtherReason"
	changed, err = setObservedIfChanged(mg, other)
	require.NoError(t, err)
	require.True(t, changed, "a different reason on the same status must count as a change")
}
