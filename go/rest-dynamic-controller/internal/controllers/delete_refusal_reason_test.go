package restResources

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
)

// The branch under test is the most informative one available: the vendor REFUSED the delete and the get
// verb confirms the resource is still there. It was also the one branch that discarded the vendor's reason.
//
// Measured consequence, reported by a consumer: six CRs sat in Terminating, each retrying a DELETE their
// API refuses because the node is in an active state, and the CR said only "still present; retrying".
// True, and useless for deciding what to do -- the actionable sentence was in hand and dropped.
//
// This asserts on the composed error rather than calling resolveDeleteOutcome, because reaching that
// function needs a clientSet, a getter.Info and a live probe; the thing that regressed is whether
// deleteErr survives into the message, and that is what is checked.
func TestARefusedDeleteKeepsTheVendorsReason(t *testing.T) {
	vendor := &restclient.StatusError{
		StatusCode: http.StatusConflict,
		Inner:      fmt.Errorf(`invalid status code: 409: {"error":"Node 9f2c is in active state; deactivate it first"}`),
		Header:     http.Header{},
	}

	composed := fmt.Errorf("delete of %s did not remove the external resource, which is still present; retrying: %w", "bmn-9f2c", vendor)

	// The existing phrasing assertion must keep holding: DeleteHoldsFinalizerWhileResourceLingers keys on
	// it so that test cannot pass on an unrelated error.
	if !strings.Contains(composed.Error(), "still present") {
		t.Error(`"still present" is gone from the message; DeleteHoldsFinalizerWhileResourceLingers keys on it`)
	}

	// The vendor's actionable sentence must survive.
	if !strings.Contains(composed.Error(), "deactivate it first") {
		t.Error("the vendor's explanation was dropped: this is the one branch where it is both available " +
			"and decisive, and without it the CR says only that the resource is still there")
	}

	// %w, not %v: the error's identity must survive so status-code checks still see through the wrapper.
	// With %v they silently stop matching, which would break the 404 short-circuit on the delete path.
	if !restclient.HasStatusErr(composed, http.StatusConflict) {
		t.Error("HasStatusErr cannot see the 409 through the wrapper: the error was formatted with a " +
			"non-wrapping verb, so every status-code check on this path silently stops matching")
	}
	se := new(restclient.StatusError)
	if !errors.As(composed, &se) {
		t.Fatal("errors.As cannot unwrap to *StatusError")
	}
	if se.StatusCode != http.StatusConflict {
		t.Errorf("unwrapped StatusCode = %d, want 409", se.StatusCode)
	}
}

// And with no vendor error -- a delete that reported success while the resource lingers -- the message must
// stay exactly as it was, with no dangling separator inviting a reader to look for a reason that does not
// exist.
func TestALingeringResourceWithNoVendorErrorIsUnchanged(t *testing.T) {
	plain := fmt.Errorf("delete of %s did not remove the external resource, which is still present; retrying", "bmn-9f2c")

	if strings.HasSuffix(plain.Error(), ":") || strings.Contains(plain.Error(), "retrying: ") {
		t.Error("a trailing separator with nothing after it: the message implies an explanation follows")
	}
	if !strings.Contains(plain.Error(), "still present") {
		t.Error(`"still present" must remain`)
	}
}

// TestResolveDeleteOutcomeActuallyWrapsTheVendorError closes the gap the tests above leave open.
//
// Both of them build the composed error themselves, so both keep passing if resolveDeleteOutcome never
// wraps anything -- they assert the shape intended, not the shape produced. Driving the real function needs
// a clientSet, a getter.Info and a live probe, so this reads the source instead and pins the two properties
// that matter: deleteErr appears in the still-present branch, and it is wrapped with a verb that preserves
// error identity.
//
// The %v/%w distinction is not cosmetic here. With %v the message looks identical and
// restclient.HasStatusErr stops seeing the status code through the wrapper -- which would silently disable
// the 404 short-circuit that lets a delete of an already-absent resource release its finalizer.
func TestResolveDeleteOutcomeActuallyWrapsTheVendorError(t *testing.T) {
	src, err := os.ReadFile("mutate_restaction.go")
	require.NoError(t, err, "the file under test must be readable; a skipped check here is not a pass")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "mutate_restaction.go", src, 0)
	require.NoError(t, err)

	var fn *ast.FuncDecl
	for _, decl := range f.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if ok && d.Name.Name == "resolveDeleteOutcome" {
			fn = d
			break
		}
	}
	require.NotNil(t, fn, "resolveDeleteOutcome not found: this test cannot say anything until it is")

	// Collect every Errorf format string in the function that mentions the load-bearing phrase.
	var stillPresentFormats []string
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Errorf" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if strings.Contains(lit.Value, "still present") {
			stillPresentFormats = append(stillPresentFormats, lit.Value)
		}
		return true
	})

	require.NotEmpty(t, stillPresentFormats,
		`no "still present" Errorf found in resolveDeleteOutcome: the branch these tests describe has `+
			`moved, so they must be rewritten rather than left to pass vacuously`)

	var wrapping int
	for _, format := range stillPresentFormats {
		if strings.Contains(format, "%w") {
			wrapping++
		}
	}
	require.Equal(t, 1, wrapping,
		"expected exactly one wrapping (%%w) \"still present\" format in resolveDeleteOutcome and found "+
			"%d among %d. Zero means the vendor's refusal reason is dropped again -- the CR goes back to "+
			"saying only that the resource is still there. More than one suggests the plain variant was "+
			"replaced rather than kept, and a delete that reported success while the resource lingers has "+
			"no vendor error to wrap",
		wrapping, len(stillPresentFormats))
}
