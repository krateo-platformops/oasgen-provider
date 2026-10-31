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
