package restResources

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// withConditions builds a CR carrying exactly the conditions given, in the runtime's own shape.
func withConditions(conds ...metav1.Condition) *unstructured.Unstructured {
	raw := make([]any, 0, len(conds))
	for _, c := range conds {
		raw = append(raw, map[string]any{
			"type":               c.Type,
			"status":             string(c.Status),
			"reason":             c.Reason,
			"message":            c.Message,
			"lastTransitionTime": metav1.Now().Format("2006-01-02T15:04:05Z"),
		})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "zendesk.krateo.io/v2",
		"kind":       "Group",
		"metadata":   map[string]any{"name": "g", "namespace": "default"},
		"status":     map[string]any{"conditions": raw},
	}}
}

// withMarkers builds a CR carrying the create-outcome annotations, optionally alongside conditions.
//
// The markers are ANNOTATIONS on the main resource; conditions live on the status subresource. That
// split is why this guard now reads the markers -- see createFailedMostRecently.
func withMarkers(failed, succeeded string, conds ...metav1.Condition) *unstructured.Unstructured {
	mg := withConditions(conds...)
	ann := map[string]any{}
	if failed != "" {
		ann["krateo.io/external-create-failed"] = failed
	}
	if succeeded != "" {
		ann["krateo.io/external-create-succeeded"] = succeeded
	}
	if len(ann) > 0 {
		mg.Object["metadata"].(map[string]any)["annotations"] = ann
	}
	return mg
}

// The defect this guards, measured against a real Zendesk (#347).
//
// An observe path with no get and no findby cannot tell a resource that was created from one the vendor
// REFUSED to create. The measured consequence was a CR permanently Ready=True with no identifier for a
// group that never existed.
//
// #347 separated the two by reading Synced == ReconcileError. THAT CHANNEL WAS WRONG, and it took a
// second defect to show why: Synced=ReconcileError is set by every error path, including this file's own
// no-identifier refusal -- so the refusal produced the error that this branch read as "the create
// failed, retry it", and the pair looped. Measured on 0.70.0 against real GitHub: three POSTs in five
// minutes (issue 696).
//
// The marker records the CREATE's own outcome, which is the question actually being asked.
func TestAFailedCreateIsNotMistakenForASuccessfulOne(t *testing.T) {
	// #347's case: the vendor refused the create, so the runtime stamped failed.
	failed := withMarkers("2026-11-15T12:00:00Z", "")
	require.True(t, createFailedMostRecently(failed),
		"a create the vendor refused must report not-exists so it is retried; treating it as created is "+
			"the #347 latch -- Ready=True for a resource that never existed")

	// A create that succeeded, with no failure ever recorded.
	succeeded := withMarkers("", "2026-11-15T12:00:00Z")
	require.False(t, createFailedMostRecently(succeeded),
		"a successful create must not be retried")

	// Failed, then fixed and succeeded. The old marker stays on the object forever, which is why this
	// compares the two rather than checking presence.
	recovered := withMarkers("2026-11-15T12:00:00Z", "2026-11-15T12:05:00Z")
	require.False(t, createFailedMostRecently(recovered),
		"a create that failed and LATER succeeded must not be retried: the failed marker is never "+
			"removed, so a presence check would retry this resource for the rest of its life")
}

// THE REGRESSION THIS FIX EXISTS FOR (issue 696). The no-identifier refusal returns an error, so the
// runtime writes Synced=ReconcileError -- and the create SUCCEEDED, with no failure marker. Reading the
// condition made this look like a failed create and dispatched another one, every resync.
func TestTheRefusalsOwnErrorIsNotAFailedCreate(t *testing.T) {
	mg := withMarkers("", "2026-11-15T12:00:00Z",
		condition.ReconcileError(assertedError{}),
		condition.Creating(),
	)
	require.False(t, createFailedMostRecently(mg),
		"Synced=ReconcileError with a SUCCEEDED create marker is this Observe's own refusal, not a "+
			"failed create. Reading it as one dispatches a create for a resource that already exists -- "+
			"measured as three POSTs in five minutes on 0.70.0, and as a duplicate per resync on a "+
			"vendor that mints the identifier")

	// CONTROL: the same condition WITH a newer failure marker must still be a failed create, or the
	// assertion above would pass for any input carrying ReconcileError.
	alsoFailed := withMarkers("2026-11-15T12:05:00Z", "2026-11-15T12:00:00Z",
		condition.ReconcileError(assertedError{}),
	)
	require.True(t, createFailedMostRecently(alsoFailed),
		"control: a newer failure marker must still report a failed create, whatever the conditions say")
}

// assertedError is a stand-in for the error ReconcileError wraps; only its presence matters here.
type assertedError struct{}

func (assertedError) Error() string { return "asserted" }

func TestAMarkerlessCRIsNotTreatedAsAFailure(t *testing.T) {
	require.False(t, createFailedMostRecently(&unstructured.Unstructured{Object: map[string]any{}}),
		"a CR with no annotations at all has had no create outcome recorded; reporting a failure here "+
			"would retry a create for a resource nobody has acted on")
	require.False(t, createFailedMostRecently(withConditions()),
		"nor does an empty condition list imply a failed create")
}

func TestOnlyTheCreateMarkersDecide(t *testing.T) {
	// A Ready condition saying Creating, and nothing else: no create outcome is recorded.
	require.False(t, createFailedMostRecently(withConditions(condition.Creating())),
		"Creating is set on BOTH create outcomes, which is exactly why it cannot decide this")

	// And an unrelated annotation must not be mistaken for a marker.
	other := withMarkers("", "")
	other.Object["metadata"].(map[string]any)["annotations"] = map[string]any{
		"krateo.io/connector-verbose": "true",
	}
	require.False(t, createFailedMostRecently(other),
		"control: an unrelated annotation is not a create marker")
}

// Every assertion above exercises createFailedMostRecently directly, so all of them keep passing if the guard
// is never called from the branch that sets Available -- the helper would be correct and dead. That failure
// mode has already happened in this repo: a fix added an early return that bypassed the branch a later test
// meant to exercise, and the test passed while asserting nothing.
//
// So this reads the source and pins the ORDER: inside the `apiCall == nil` block of Observe, the guard must
// appear before the Available() that latches.
func TestTheLatchGuardIsReachedBeforeAvailableIsSet(t *testing.T) {
	src, err := os.ReadFile("restResources.go")
	require.NoError(t, err, "the file under test must be readable; a skipped check here is not a pass")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "restResources.go", src, 0)
	require.NoError(t, err)

	var observe *ast.FuncDecl
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "Observe" {
			observe = fn
			break
		}
	}
	require.NotNil(t, observe, "Observe not found: this test cannot say anything until it is")

	// Find the `if apiCall == nil` statement itself, rather than scanning all of Observe. An earlier
	// Available() in this function is a READ -- IsConditionSet(mg, condition.Available()) -- and comparing
	// against it made the first version of this test fail for the wrong reason.
	var block *ast.IfStmt
	ast.Inspect(observe, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok || block != nil {
			return true
		}
		bin, ok := ifs.Cond.(*ast.BinaryExpr)
		if !ok || bin.Op != token.EQL {
			return true
		}
		x, okX := bin.X.(*ast.Ident)
		y, okY := bin.Y.(*ast.Ident)
		if okX && okY && x.Name == "apiCall" && y.Name == "nil" {
			block = ifs
		}
		return true
	})
	require.NotNil(t, block,
		"the `if apiCall == nil` branch was not found in Observe: the structure this guard protects has "+
			"changed, so this test must be rewritten rather than left to pass vacuously")

	var guardPos, setPos token.Pos
	ast.Inspect(block, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if fun.Name == "createFailedMostRecently" && guardPos == token.NoPos {
				guardPos = call.Pos()
			}
		case *ast.SelectorExpr:
			// SetConditions is what PERSISTS the latch; Available() alone also appears as a read.
			if fun.Sel.Name == "SetConditions" && setPos == token.NoPos {
				setPos = call.Pos()
			}
		}
		return true
	})

	require.NotEqual(t, token.NoPos, guardPos,
		"createFailedMostRecently is never called inside the `apiCall == nil` branch, so the #347 guard is "+
			"dead code and every other test in this file passes while asserting nothing about the latch")
	require.NotEqual(t, token.NoPos, setPos,
		"no SetConditions call inside that branch: it no longer writes a condition, so this order "+
			"assertion is meaningless and must be rewritten rather than silently pass")

	require.Less(t, int(guardPos), int(setPos),
		"the #347 guard (line %d) comes AFTER the SetConditions that latches (line %d), so a create the "+
			"vendor refused still reaches Available and the CR still reports Ready=True for a resource "+
			"that was never created",
		fset.Position(guardPos).Line, fset.Position(setPos).Line)
}
