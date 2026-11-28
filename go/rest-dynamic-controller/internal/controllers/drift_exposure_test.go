package restResources

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// observeStep replays the part of Observe that bookkeeps a reported difference, in the order Observe does
// it: read the previous signature, record, then derive the exposure and the latch decision.
func observeStep(t *testing.T, h *handler, mg *unstructured.Unstructured, sig string) (repeats int, msg string, latched bool) {
	t.Helper()
	prev := mg.GetAnnotations()[annotationDriftSignature]
	n, err := h.recordDriftRepeat(context.Background(), mg, sig)
	require.NoError(t, err)
	return n, driftNotUpToDateMessage("size", "values differ", driftProgress(prev, sig, n)), driftLatched(n)
}

func TestDriftExposureClimbingShowsCountAndCeiling(t *testing.T) {
	mg := driftMG(1, nil)
	h, _ := driftHandler(t, mg)
	sig := "gen=1;field=size"

	var msg string
	for i := 0; i < 3; i++ {
		_, msg, _ = observeStep(t, h, mg, sig)
	}
	t.Logf("climbing: %s", msg)
	assert.Contains(t, msg, "drift check 3 of 5", "count AND ceiling must both be visible")
	assert.Contains(t, msg, "the same difference as the previous check")
}

func TestDriftExposureResetIsNamedAndDistinctFromFirstReport(t *testing.T) {
	mg := driftMG(1, nil)
	h, _ := driftHandler(t, mg)

	_, first, _ := observeStep(t, h, mg, "gen=1;field=scopes")
	observeStep(t, h, mg, "gen=1;field=scopes")
	n, _, _ := observeStep(t, h, mg, "gen=1;field=scopes")
	require.Equal(t, 3, n)

	// The path progresses (issue 970): the count restarts.
	n, reset, _ := observeStep(t, h, mg, "gen=1;field=scopes[0]")
	t.Logf("first report: %s", first)
	t.Logf("reset:        %s", reset)
	require.Equal(t, 1, n, "persistence is unchanged: a new signature restarts the count")
	assert.Contains(t, reset, "drift check 1 of 5")
	assert.Contains(t, reset, "changed since the previous check, so the count restarted")
	assert.NotEqual(t, first, reset, "a restart must not read as a fresh first report")
	assert.Contains(t, first, "first report of this difference")
	assert.NotContains(t, reset, "first report")
}

// Nothing else moved: the latch fires at exactly maxDriftRepeats, and the counter is exactly what it was.
// Asserts on counts and the decision only, never on the exposure strings.
func TestDriftExposureDoesNotMoveTheLatch(t *testing.T) {
	require.Equal(t, 5, maxDriftRepeats)
	mg := driftMG(1, nil)
	h, _ := driftHandler(t, mg)
	for want := 1; want <= maxDriftRepeats+2; want++ {
		n, _, latched := observeStep(t, h, mg, "gen=1;field=size")
		assert.Equal(t, want, n)
		assert.Equal(t, want >= 5, latched, "latch at check %d", want)
	}
	// A reset restarts the count and un-latches, as before.
	n, _, latched := observeStep(t, h, mg, "gen=1;field=other")
	assert.Equal(t, 1, n)
	assert.False(t, latched)
}

// TestUpdateCallsTheDriftMessageCarryForward is a STATIC call-site check, not a behavioural one, and is
// labelled so deliberately. Update cannot be driven without a cluster here: its only existing driver is
// the kind-based integration test, which the clusterguard refuses to run locally. Faking its HTTP and
// swagger machinery would make scaffolding larger than the one line it covers.
//
// What it guards: observedDriftMessage can be entirely correct and never called, and then every other
// check passes (build, vet, the helper's own test, mutation of the strings). This fails if Update's body
// stops calling it, or if the call is made from anywhere other than Update.
func TestUpdateCallsTheDriftMessageCarryForward(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "restResources.go", nil, 0)
	require.NoError(t, err)

	var inUpdate, elsewhere int
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		isUpdate := fn.Name.Name == "Update" && fn.Recv != nil
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "observedDriftMessage" {
					if isUpdate {
						inUpdate++
					} else {
						elsewhere++
					}
				}
			}
			return true
		})
	}
	assert.Equal(t, 1, inUpdate,
		"Update must call observedDriftMessage exactly once: without it Update's Updating condition "+
			"erases the drift progress Observe wrote, and a consumer reading between polls sees none (issue 964)")
	assert.Equal(t, 0, elsewhere, "the carry-forward belongs in Update only")
}

func TestObservedDriftMessageSurvivesIntoUpdating(t *testing.T) {
	mg := driftMG(1, nil)
	msg := "field size: values differ (drift check 2 of 5; the same difference as the previous check)"
	mg.Object["status"] = map[string]interface{}{"conditions": []interface{}{
		map[string]interface{}{"type": "Ready", "reason": reasonResourceNotUpToDate, "message": msg},
	}}
	assert.Equal(t, msg, observedDriftMessage(mg))

	mg.Object["status"] = map[string]interface{}{"conditions": []interface{}{
		map[string]interface{}{"type": "Ready", "reason": "Available", "message": "x"},
	}}
	assert.Equal(t, "", observedDriftMessage(mg), "only a reported difference is carried")
}
