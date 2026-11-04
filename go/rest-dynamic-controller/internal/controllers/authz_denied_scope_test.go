package restResources

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDeleteNeverStopsOnAuthorizationDenied pins a DELIBERATE ASYMMETRY that reads like an oversight, which
// is exactly why it needs a test rather than a comment.
//
// Create and Update stop on a denied 403: retrying a decision cannot change it, and the resource simply
// stays not-ready with the reason named. Delete must NOT stop. Returning nil from Delete lets the finalizer
// be removed, so the CR disappears while the external resource it represents still exists -- and still
// costs money. #261 already settled that an unverifiable delete HOLDS the finalizer, and a denied delete is
// unverifiable in precisely that sense.
//
// So the two failure modes are not symmetric in cost:
//
//	denied create/update, stopped   -> resource not ready, reason visible, fix the credential and it proceeds
//	denied delete, stopped          -> CR gone, external resource orphaned, nothing left to retry FROM
//
// Only the first is recoverable. A future refactor tidying "all three write verbs" into one shared guard
// would look like an improvement and would introduce a silent leak, so this asserts at the source level
// that the guard is absent from Delete.
func TestDeleteNeverStopsOnAuthorizationDenied(t *testing.T) {
	src, err := os.ReadFile("restResources.go")
	require.NoError(t, err, "the file under test must be readable; a skipped check here is not a pass")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "restResources.go", src, 0)
	require.NoError(t, err)

	const guard = "stopOnAuthorizationDenied"
	callers := map[string]int{}

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := fn.Name.Name
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != guard {
				return true
			}
			callers[name]++
			return true
		})
	}

	// Premise first: if the guard is not called anywhere, this test proves nothing about Delete and must
	// fail loudly rather than pass vacuously. A rename or a removal lands here.
	require.NotEmpty(t, callers,
		"%s is called nowhere in restResources.go: either it was renamed or the #313 guard was removed, "+
			"and this test cannot say anything about Delete until it is reached", guard)

	require.Contains(t, callers, "Create",
		"Create does not guard a denied 403, so it retries a refused create forever (#313)")
	require.Contains(t, callers, "Update",
		"Update does not guard a denied 403, so it retries a refused update forever (#313)")

	require.NotContains(t, callers, "Delete",
		"Delete calls %s. Stopping there removes the finalizer and ORPHANS the external resource -- "+
			"retrying a denied delete forever is wasteful, orphaning is unrecoverable, and #261 settled "+
			"that an unverifiable delete holds the finalizer", guard)
}

// TestDeleteDoesNotReachTheGuardINDIRECTLY closes a hole that #436 opened in the test above.
//
// That test asserts Delete does not call stopOnAuthorizationDenied DIRECTLY. #436 added
// observeAuthorizationDenied, a wrapper that calls the guard and adds the ReadyAssessed wrap -- so from
// that point on there are two names a caller could use, and the direct-call assertion only sees one of
// them. A future Delete routed through the wrapper would orphan resources exactly as before and the
// existing test would stay green, because the thing it greps for is no longer the only way in.
//
// So this computes TRANSITIVE reachability over the file's own methods and asserts on that instead. It
// survives any number of future wrappers, because a wrapper is itself a node on the path rather than a
// bypass.
//
// Worth stating why this is not just a stricter version of the test above: the one above pins the DIRECT
// call set, which is still what a reader wants to know. This one pins the PROPERTY ("a denied delete is
// never stopped"), which is what #261 actually settled. Both are kept because a failure in each says
// something different -- the first says "someone wired Delete to the guard", the second says "someone
// found a path to it".
func TestDeleteDoesNotReachTheGuardINDIRECTLY(t *testing.T) {
	src, err := os.ReadFile("restResources.go")
	require.NoError(t, err, "the file under test must be readable; a skipped check here is not a pass")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "restResources.go", src, 0)
	require.NoError(t, err)

	// calls[fn] is every method name fn invokes on a receiver, within this file.
	calls := map[string]map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		out := map[string]bool{}
		ast.Inspect(fn, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					out[sel.Sel.Name] = true
				}
			}
			return true
		})
		calls[fn.Name.Name] = out
	}

	// Fixpoint: a method reaches the guard if it calls it, or calls anything that reaches it.
	const guard = "stopOnAuthorizationDenied"
	reaches := map[string]bool{guard: true}
	for changed := true; changed; {
		changed = false
		for fn, callees := range calls {
			if reaches[fn] {
				continue
			}
			for callee := range callees {
				if reaches[callee] {
					reaches[fn] = true
					changed = true
					break
				}
			}
		}
	}

	// PREMISE, so a renamed guard or a broken walk fails loudly instead of passing vacuously: the two
	// verbs that are SUPPOSED to reach it must be found by this same traversal.
	require.True(t, reaches["Create"],
		"the transitive walk does not find Create reaching %s, so it cannot say anything about Delete: "+
			"either the guard was renamed or this traversal is broken", guard)
	require.True(t, reaches["Update"],
		"the transitive walk does not find Update reaching %s; same premise failure as Create", guard)

	// And the property itself.
	require.False(t, reaches["Delete"],
		"Delete reaches %s through %v. Stopping a denied delete removes the finalizer and ORPHANS the "+
			"external resource; #261 settled that an unverifiable delete holds it. If a wrapper made this "+
			"path look harmless, it is not -- the wrapper still stops.", guard, calls["Delete"])

	// Observe SHOULD reach it as of #436, via observeAuthorizationDenied. Asserted so that the wiring
	// cannot be silently removed: without it a permanent 403 on a read falls back to the runtime's generic
	// ObserveFailed, which a composition rollup buckets as CONVERGING -- a credential that cannot read
	// then presents as an install still in progress, indefinitely.
	require.True(t, reaches["Observe"],
		"Observe no longer reaches %s, so a permanent 403 on a read is reported as a generic observe "+
			"failure and rolls up as converging rather than failed (#436)", guard)
}
