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

// The defect this guards, measured against a real Zendesk (#347).
//
// The runtime sets Creating on BOTH create outcomes and separates them only on Synced:
//
//	create succeeded   worker.go:823   Synced = ReconcileSuccess
//	create failed      worker.go:760   Synced = ReconcileError
//
// So an observe path with no get and no findby, reading Ready/Creating alone, cannot tell a resource that
// was created from one the vendor REFUSED to create -- and the measured consequence was a CR permanently
// Ready=True with no identifier for a group that never existed.
func TestAFailedCreateIsNotMistakenForASuccessfulOne(t *testing.T) {
	failed := withConditions(
		condition.Creating(),
		metav1.Condition{Type: condition.TypeSynced, Status: metav1.ConditionFalse,
			Reason: condition.ReasonReconcileError, Message: "create failed: 422"},
	)
	if !lastReconcileErrored(failed) {
		t.Error("a CR whose last reconcile errored was not recognised: the no-findby observe path would " +
			"read its own Creating condition as evidence of a create that in fact was refused")
	}

	succeeded := withConditions(condition.Creating(), condition.ReconcileSuccess())
	if lastReconcileErrored(succeeded) {
		t.Error("a successful create was reported as errored: write-only kinds -- no get, no findby -- " +
			"would never reach Available and would be re-created on every reconcile")
	}
}

// A brand-new CR has no conditions at all. It must not look like a failure, because the branch upstream of
// this helper already handles "no conditions" correctly by reporting not-exists, and that path is the
// control acmp measured as working (first observe -> create).
func TestAConditionlessCRIsNotTreatedAsAFailure(t *testing.T) {
	if lastReconcileErrored(&unstructured.Unstructured{Object: map[string]any{}}) {
		t.Error("an object with no status reported as errored")
	}
	if lastReconcileErrored(withConditions()) {
		t.Error("an object with an empty condition list reported as errored")
	}
}

// Synced is read by Type, so an unrelated Ready reason must not be mistaken for it. This is the assertion
// that would fail if someone keyed the helper on Reason alone -- ReasonReconcileError is a Synced reason,
// but a future Ready reason could collide.
func TestOnlyTheSyncedConditionDecides(t *testing.T) {
	// Ready=False for an unrelated reason, with Synced explicitly successful.
	mg := withConditions(
		metav1.Condition{Type: condition.TypeReady, Status: metav1.ConditionFalse, Reason: "Updating"},
		condition.ReconcileSuccess(),
	)
	if lastReconcileErrored(mg) {
		t.Error("a non-Synced condition decided the outcome; only Synced distinguishes the two create results")
	}

	// And with no Synced condition present at all, there is no evidence of an error.
	onlyReady := withConditions(metav1.Condition{
		Type: condition.TypeReady, Status: metav1.ConditionFalse, Reason: condition.ReasonReconcileError,
	})
	if lastReconcileErrored(onlyReady) {
		t.Error("a Ready condition carrying the ReconcileError reason was read as the Synced one")
	}
}

// TestTheLatchGuardIsReachedBeforeAvailableIsSet asserts the PREMISE the tests above depend on.
//
// Every assertion above exercises lastReconcileErrored directly, so all of them keep passing if the guard
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
			if fun.Name == "lastReconcileErrored" && guardPos == token.NoPos {
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
		"lastReconcileErrored is never called inside the `apiCall == nil` branch, so the #347 guard is "+
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
