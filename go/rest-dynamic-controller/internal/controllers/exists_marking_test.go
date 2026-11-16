package restResources

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	ucontroller "github.com/krateo-platformops/unstructured-runtime/pkg/controller"
)

// TestExactlyTheFiveTransientRefusalsAreMarked is a CENSUS, not a sample, and it is source-level because
// the property is "which of sixteen returns is marked" -- a question no behavioural test can ask without
// constructing sixteen vendor states.
//
// unstructured-runtime v1.24.0 lets a handler mark an existence refusal TRANSIENT, so the runtime
// requeues instead of writing Ready=False/CreateOutcomeUnknown. The default is structural, deliberately:
// marking a refusal that can NEVER resolve makes the resource requeue forever reporting Creating, which
// core-provider's rollup reads as CONVERGING -- a parent sitting in childConverging indefinitely with
// nothing saying why. That is the defect CreateOutcomeUnknown was added to fix.
//
// So the marking has to be per-site and deliberate, and this test is what makes "deliberate" checkable:
// adding a sixth mark, or dropping one of the five, fails here and names the count.
func TestExactlyTheFiveTransientRefusalsAreMarked(t *testing.T) {
	const wantMarked, wantUnmarked = 5, 11

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "exists.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing exists.go: %v", err)
	}

	var marked, unmarked int
	ast.Inspect(f, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 2 {
			return true
		}
		// Only the refusals: `return false, <err>`. A `return false, nil` is the definite negative and
		// a `return true, nil` is a hit; neither is a refusal.
		lit, ok := ret.Results[0].(*ast.Ident)
		if !ok || lit.Name != "false" {
			return true
		}
		call, ok := ret.Results[1].(*ast.CallExpr)
		if !ok {
			return true // `return false, nil`
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ExistenceUndetermined" {
			marked++
		} else {
			unmarked++
		}
		return true
	})

	if marked != wantMarked || unmarked != wantUnmarked {
		t.Errorf("marked=%d unmarked=%d, want %d/%d.\n"+
			"  A NEW mark: is that refusal really one the vendor or cluster LEAVES? Marking a refusal\n"+
			"  that can never resolve makes the resource requeue forever reporting Creating, which the\n"+
			"  cdc rolls up as converging -- a parent stuck with nothing explaining why.\n"+
			"  A REMOVED mark: that refusal goes back to CreateOutcomeUnknown, which tells an operator a\n"+
			"  human is needed for something that resolves itself.\n"+
			"  Either may be right. Update the counts here with the argument in the commit message.",
			marked, unmarked, wantMarked, wantUnmarked)
	}

	// CALIBRATION: if the AST walk found no refusals at all, the two numbers above could both be zero
	// and the test would be asserting nothing. 16 is the population this census covers.
	if marked+unmarked != 16 {
		t.Fatalf("found %d refusal returns, expected 16. Either exists.go was restructured or this "+
			"walk no longer recognises its returns -- a census that cannot find its population must "+
			"not report a count", marked+unmarked)
	}
}

// TestMarkedRefusalsStayTransparent: the wrap must not change what a caller sees. The runtime reads the
// marker, but every log line, condition message and errors.Is in this repo reads the cause.
func TestMarkedRefusalsStayTransparent(t *testing.T) {
	cause := errForTest("the vendor answered 503")
	marked := ucontroller.ExistenceUndetermined(cause)

	if !ucontroller.IsExistenceUndetermined(marked) {
		t.Error("the marker is not detectable, so the runtime will treat this as structural")
	}
	if got := marked.Error(); !strings.Contains(got, "503") {
		t.Errorf("Error() = %q, which lost the cause: the condition message is built from this", got)
	}
	// And an UNMARKED error must not read as marked, or the structural default is gone.
	if ucontroller.IsExistenceUndetermined(cause) {
		t.Error("an unmarked error reads as transient -- the default must stay structural")
	}
}

type errForTest string

func (e errForTest) Error() string { return string(e) }
