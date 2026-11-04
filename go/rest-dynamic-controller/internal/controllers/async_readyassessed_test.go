package restResources

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// The OutcomeFailed branch of driveAsyncRequeue sets Ready=Unavailable and PERSISTS it, then returns an
// error. Without a ReadyAssessed wrap, unstructured-runtime's handleObserve replaces that Unavailable with
// the generic ObserveFailed (worker.go:649) -- which a composition rollup buckets as CONVERGING, so a
// terminal async failure presents as an install still in progress, indefinitely.
//
// WHY THIS IS A SOURCE-LEVEL TEST AND WHAT THAT COSTS, stated rather than glossed. Driving
// driveAsyncRequeue behaviourally needs a clientSet, a client info, a seeded async record and a vendor
// endpoint; that lives in the integration suite. This asserts the narrower property that the branch
// contains no UNWRAPPED error return, which is the regression actually likely: someone adds an early
// return to that branch and does not know the wrap exists.
//
// It cannot catch a wrap that is present but broken by a %v three frames up -- only a behavioural
// assertion on the persisted condition does that, and that is what the #436 tests do for the Observe path.
// So this is a guard against OMISSION, not against breakage, and it is the cheaper half of the pair.
func TestEveryErrorReturnInTheAsyncFailureBranchIsReadyAssessed(t *testing.T) {
	src, err := os.ReadFile("async_requeue.go")
	require.NoError(t, err, "the file under test must be readable; a skipped check here is not a pass")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "async_requeue.go", src, 0)
	require.NoError(t, err)

	var branch *ast.CaseClause
	ast.Inspect(f, func(n ast.Node) bool {
		cc, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, e := range cc.List {
			if sel, ok := e.(*ast.SelectorExpr); ok && sel.Sel.Name == "OutcomeFailed" {
				branch = cc
			}
		}
		return true
	})

	// PREMISE: if the branch cannot be found, the walk proves nothing and must fail loudly. A rename of
	// the outcome constant or a restructure of the switch lands here rather than passing vacuously.
	require.NotNil(t, branch,
		"could not find the `case async.OutcomeFailed` clause in async_requeue.go: either the constant was "+
			"renamed or the switch was restructured, and this test cannot say anything until it is found")

	// THE BOUNDARY IS THE PERSIST, NOT THE BRANCH. The returns BEFORE `*mg = *updated` fire when
	// SetConditions or UpdateStatus itself failed -- so Ready=Unavailable was never written, and wrapping
	// those would tell the runtime the handler owns a condition that does not exist. The generic
	// ObserveFailed is the honest outcome there. Only returns AFTER the persist succeeded may be wrapped.
	//
	// This test asserted "every return in the branch" on its first draft and failed against correct code,
	// which is how the boundary got found: the pre-persist returns are deliberately bare.
	persistIdx := -1
	for i, stmt := range branch.Body {
		if as, ok := stmt.(*ast.AssignStmt); ok && len(as.Lhs) == 1 {
			if star, ok := as.Lhs[0].(*ast.StarExpr); ok {
				if id, ok := star.X.(*ast.Ident); ok && id.Name == "mg" {
					persistIdx = i
				}
			}
		}
	}
	require.GreaterOrEqual(t, persistIdx, 0,
		"could not find the `*mg = *updated` write that marks the persist succeeding, so this test cannot "+
			"tell a pre-persist return from a post-persist one")

	var unwrapped, wrappedTooEarly []int
	wrapped := 0
	for i, stmt := range branch.Body {
		afterPersist := i > persistIdx
		ast.Inspect(stmt, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 3 {
				return true
			}
			errExpr := ret.Results[2]
			if id, ok := errExpr.(*ast.Ident); ok && id.Name == "nil" {
				return true
			}
			isWrapped := false
			if call, ok := errExpr.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ReadyAssessed" {
					isWrapped = true
				}
			}
			line := fset.Position(ret.Pos()).Line
			switch {
			case afterPersist && isWrapped:
				wrapped++
			case afterPersist && !isWrapped:
				unwrapped = append(unwrapped, line)
			case !afterPersist && isWrapped:
				wrappedTooEarly = append(wrappedTooEarly, line)
			}
			return true
		})
	}

	require.Greater(t, wrapped, 0,
		"no ReadyAssessed-wrapped return found after the persist, so finding no unwrapped ones says "+
			"nothing: the walk is not seeing this branch's returns")

	require.Empty(t, wrappedTooEarly,
		"async_requeue.go lines %v wrap an error with ReadyAssessed BEFORE the Unavailable condition was "+
			"persisted. Those returns fire when SetConditions or UpdateStatus itself failed, so the wrap "+
			"claims the handler owns a Ready condition that was never written -- and the runtime then "+
			"writes no Ready at all, leaving whatever was in etcd", wrappedTooEarly)

	require.Empty(t, unwrapped,
		"async_requeue.go lines %v return an error AFTER the persist WITHOUT "+
			"controller.ReadyAssessed. That branch has already set and persisted Ready=Unavailable, and an "+
			"unwrapped error makes the runtime overwrite it with ObserveFailed -- which rolls up as "+
			"converging, so a terminal async failure reads as an install still in progress", unwrapped)
}
