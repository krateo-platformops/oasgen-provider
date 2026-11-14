package restResources

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
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

	// TWO ENTRY POINTS TO ONE GUARD, since 0.66.0. Create no longer calls stopOnAuthorizationDenied:
	// that helper PERSISTS the condition, which Observe depends on, and persisting from inside Create
	// would be a second write of what the runtime already writes from a CreateResult. So Create
	// classifies with restclient.IsAuthorizationDenied and returns createAuthorizationDeniedResult.
	//
	// Both are entry points to the same refusal guard, so this unions them. That makes the assertion
	// STRONGER than the single-name version it replaces: Delete reaching the guard by EITHER route now
	// fails, where before only one route was watched.
	guardEntryPoints := []string{"stopOnAuthorizationDenied", "createAuthorizationDeniedResult"}
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
			if !ok {
				return true
			}
			for _, g := range guardEntryPoints {
				if sel.Sel.Name == g {
					callers[name]++
				}
			}
			return true
		})
	}

	// Premise first: if the guard is not called anywhere, this test proves nothing about Delete and must
	// fail loudly rather than pass vacuously. A rename or a removal lands here.
	require.NotEmpty(t, callers,
		"none of %v is called anywhere in restResources.go: either they were renamed or the #313 guard "+
			"was removed, and this test cannot say anything about Delete until one is reached",
		guardEntryPoints)

	require.Contains(t, callers, "CreateWithResult",
		"Create does not guard a denied 403, so it retries a refused create forever (#313)")
	require.Contains(t, callers, "Update",
		"Update does not guard a denied 403, so it retries a refused update forever (#313)")

	require.NotContains(t, callers, "Delete",
		"Delete calls one of %v. Stopping there removes the finalizer and ORPHANS the external resource "+
			"-- retrying a denied delete forever is wasteful, orphaning is unrecoverable, and #261 "+
			"settled that an unverifiable delete holds the finalizer", guardEntryPoints)
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

	// BOTH stop-guards, because #42 added a second one and Delete must reach NEITHER for the same
	// reason: stopping a delete lets the finalizer be removed while the external resource still exists
	// (#261). Enumerated here rather than discovered, so adding a third guard without adding it to this
	// list is a visible omission in a diff rather than a silent gap.
	// A refusal CLASS can have more than one entry point. Since 0.66.0 the authorization guard has two:
	// Update reaches it via stopOnAuthorizationDenied, Create via createAuthorizationDeniedResult (which
	// does not persist, because the runtime writes a CreateResult's conditions itself). Reachability is
	// therefore "reaches ANY entry point of this class".
	//
	// Enumerated rather than discovered, so adding a third guard without declaring its class and its
	// expected reachers is a visible omission in a diff rather than a silent gap.
	classes := []struct {
		name        string
		entryPoints []string
	}{
		{"authorization refusal", []string{"stopOnAuthorizationDenied", "createAuthorizationDeniedResult"}},
		{"endpoint refusal", []string{"stopOnEndpointRefusal"}},
	}
	for _, c := range classes {
		t.Run(c.name, func(t *testing.T) { assertDeleteDoesNotReach(t, fset, calls, c.entryPoints) })
	}
}

func assertDeleteDoesNotReach(t *testing.T, fset *token.FileSet, calls map[string]map[string]bool, entryPoints []string) {
	t.Helper()
	_ = fset
	// Fixpoint: a method reaches the class if it calls any entry point, or calls anything that does.
	reaches := map[string]bool{}
	for _, g := range entryPoints {
		reaches[g] = true
	}
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
	require.True(t, reaches["CreateWithResult"],
		"the transitive walk does not find Create reaching %v, so it cannot say anything about Delete: "+
			"either the guard was renamed or this traversal is broken", entryPoints)
	require.True(t, reaches["Update"],
		"the transitive walk does not find Update reaching %v; same premise failure as Create", entryPoints)

	// And the property itself.
	require.False(t, reaches["Delete"],
		"Delete reaches one of %v through %v. Stopping a denied delete removes the finalizer and ORPHANS "+
			"the external resource; #261 settled that an unverifiable delete holds it. If a wrapper made "+
			"this path look harmless, it is not -- the wrapper still stops.", entryPoints, calls["Delete"])

	// Observe SHOULD reach the 403 guard as of #436, via observeAuthorizationDenied. Asserted so the
	// wiring cannot be silently removed: without it a permanent 403 on a read falls back to the runtime's
	// generic ObserveFailed, which a composition rollup buckets as CONVERGING -- a credential that cannot
	// read then presents as an install still in progress, indefinitely.
	//
	// Only for that guard: an endpoint refusal on the OBSERVE path is a separate decision nobody has
	// taken, and asserting it here would pin a behaviour that does not exist.
	if slices.Contains(entryPoints, "stopOnAuthorizationDenied") {
		require.True(t, reaches["Observe"],
			"Observe no longer reaches %v, so a permanent 403 on a read is reported as a generic observe "+
				"failure and rolls up as converging rather than failed (#436)", entryPoints)
	}
}
