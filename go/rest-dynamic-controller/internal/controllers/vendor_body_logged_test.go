package restResources

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// #433 moved the vendor's response text off the returned error, because a returned error lands verbatim in
// the Synced condition message -- and on Ready's too under unstructured-runtime -- readable by anyone with
// `get` on the resource. The text is not discarded: it goes to the log, at Info, via logVendorBody.
//
// SO THE TWO HALVES ONLY WORK TOGETHER. Remove the text from the error and forget to log it, and the
// explanation is gone -- which is exactly the defect #334 was filed for, a measured 34-minute
// investigation whose entire answer was the sentence the controller threw away. This asserts that every
// site surfacing a failed call also logs the vendor's half.
//
// WHY THIS IS A SOURCE-LEVEL TEST, stated rather than glossed: the log LEVEL cannot be asserted here. This
// package's test logger funnels every level into one slice, so an assertion that the line exists would
// pass against a Debug-only version -- and Debug reaches nobody on a default rest-dynamic-controller
// install, which is the whole reason the level matters. A behavioural test would therefore assert the one
// thing that is not in question and miss the one that is.
//
// So this is a guard against OMISSION, and the level is held by the comment on logVendorBody plus the
// measurement behind it. That is weaker than a test and it is the honest best available.
func TestEverySiteThatSurfacesAFailedCallAlsoLogsTheVendorBody(t *testing.T) {
	src, err := os.ReadFile("restResources.go")
	require.NoError(t, err, "the file under test must be readable; a skipped check here is not a pass")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "restResources.go", src, 0)
	require.NoError(t, err)

	// Find each `log.Error(err, "Performing REST call")` and require a logVendorBody call within the same
	// enclosing block, so a site added without one is visible.
	var surfaced, logged []int
	ast.Inspect(f, func(n ast.Node) bool {
		blk, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for _, stmt := range blk.List {
			es, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := es.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			line := fset.Position(call.Pos()).Line
			// The first argument must be the identifier `err`, not just any error. The same log message
			// is also used for `log.Error(fmt.Errorf("body is not an object"), "Performing REST call")`,
			// which is OUR error about OUR parsing and carries no vendor snippet -- counting those made
			// this test demand 7 logVendorBody calls where only 4 sites can supply one. Matching the
			// message alone was too loose, and the over-count is how that surfaced.
			if sel.Sel.Name == "Error" && len(call.Args) == 2 {
				lit, isLit := call.Args[1].(*ast.BasicLit)
				id, isErrIdent := call.Args[0].(*ast.Ident)
				if isLit && lit.Value == `"Performing REST call"` && isErrIdent && id.Name == "err" {
					surfaced = append(surfaced, line)
				}
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "logVendorBody" {
				logged = append(logged, line)
			}
		}
		// logVendorBody is a bare identifier call, not a selector, so catch it separately.
		for _, stmt := range blk.List {
			es, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			if call, ok := es.X.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "logVendorBody" {
					logged = append(logged, fset.Position(call.Pos()).Line)
				}
			}
		}
		return true
	})

	// PREMISE: if no surfacing site is found, the walk proves nothing. A reworded log message lands here
	// rather than passing vacuously -- and the message IS the thing being matched on, so a reword is the
	// likeliest way this stops working.
	require.NotEmpty(t, surfaced,
		`no 'log.Error(err, "Performing REST call")' site found in restResources.go: the message was `+
			`reworded or the sites were restructured, and this test cannot say anything until one is found`)

	require.Equal(t, len(surfaced), len(logged),
		"found %d sites surfacing a failed REST call (lines %v) but %d logVendorBody calls (lines %v). "+
			"A site that surfaces the error without logging the vendor's body DISCARDS the explanation "+
			"rather than relocating it, because #433 removed it from the error string -- which is the "+
			"defect #334 was filed for", len(surfaced), surfaced, len(logged), logged)
}
