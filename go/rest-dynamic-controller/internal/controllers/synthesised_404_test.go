package restResources

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
)

// A findby no-match SYNTHESISES a 404 (restclient.go:646, :667, and the paginated Exhausted branch). The
// existence checker treated any 404 as "a definite negative ... the ONLY path that may return false", so a
// listing that returned nothing matching became proof of absence — and its own comment names the stake:
// "Returning false here is what would recreate a live resource."
//
// Consequence: after a create whose response was never recorded, a findby that cannot match makes the
// runtime re-create, DUPLICATING A LIVE VENDOR RESOURCE (#371).
//
// Both callers must keep their own answer, which is why the distinction is a field rather than a change to
// IsNotFoundError:
//
//	Observe  -> not-exists is correct; creating is the intent, no duplicate is possible yet
//	Exists   -> "nothing matched" is not determinable; it must error
func TestSynthesisedAndVendor404AreDistinguishable(t *testing.T) {
	vendor := &restclient.StatusError{StatusCode: http.StatusNotFound, Inner: fmt.Errorf("not found")}
	ours := &restclient.StatusError{StatusCode: http.StatusNotFound, Inner: fmt.Errorf("item not found"), Synthesised: true}

	// Every existing caller must keep its behaviour: IsNotFoundError stays true for BOTH.
	if !restclient.IsNotFoundError(vendor) || !restclient.IsNotFoundError(ours) {
		t.Fatal("IsNotFoundError must remain true for both, or Observe's not-exists conclusion changes — " +
			"which is the one place the old behaviour was correct")
	}

	if restclient.IsSynthesisedNotFound(vendor) {
		t.Error("a vendor 404 was reported as synthesised. Marking a real absence as undeterminable would " +
			"strand a CR whose resource genuinely does not exist — the opposite failure, and not better")
	}
	if !restclient.IsSynthesisedNotFound(ours) {
		t.Error("our own no-match 404 was not recognised, so the existence checker still reads it as " +
			"definite absence and a create-recovery still duplicates")
	}
}

// The marker must not leak onto other statuses: a synthesised flag on a 403 or 500 would be meaningless,
// and a predicate that ignored the code would spread "undeterminable" to failures that are already
// handled correctly.
func TestIsSynthesisedNotFoundRequiresA404(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusInternalServerError, http.StatusOK} {
		err := &restclient.StatusError{StatusCode: code, Synthesised: true}
		if restclient.IsSynthesisedNotFound(err) {
			t.Errorf("status %d with Synthesised reported as a synthesised NOT-FOUND; the predicate must "+
				"require both", code)
		}
	}
	if restclient.IsSynthesisedNotFound(nil) {
		t.Error("nil must not be a synthesised not-found")
	}
	if restclient.IsSynthesisedNotFound(fmt.Errorf("plain error")) {
		t.Error("a non-StatusError must not be a synthesised not-found")
	}
}

// THE PREMISE TEST. The two cases above exercise the predicate directly, so both pass whether or not
// exists.go consults it — which is exactly the defect being fixed, a distinction that exists and is not
// reached. That gap has shipped in this repo three times.
//
// Order matters and is the easy thing to get wrong: IsNotFoundError is true for a synthesised 404 too, so
// if the vendor case were checked first it would return false and the fix would be inert.
func TestExistsChecksTheSynthesisedCaseBeforeTheVendorCase(t *testing.T) {
	src, err := os.ReadFile("exists.go")
	if err != nil {
		t.Fatalf("exists.go must be readable; a skipped check here is not a pass: %v", err)
	}
	text := string(src)

	synth := strings.Index(text, "restclient.IsSynthesisedNotFound(callErr)")
	vendor := strings.Index(text, "restclient.IsNotFoundError(callErr)")

	if synth < 0 {
		t.Fatal("exists.go never calls IsSynthesisedNotFound, so a findby no-match is still read as " +
			"definite absence and #371 is unfixed — the predicate exists and nothing reaches it")
	}
	if vendor < 0 {
		t.Fatal("exists.go no longer calls IsNotFoundError: a genuine vendor 404 must still return a " +
			"clean false, or every absent resource becomes undeterminable")
	}
	if synth > vendor {
		t.Errorf("IsSynthesisedNotFound is checked AFTER IsNotFoundError (offsets %d vs %d). "+
			"IsNotFoundError is true for a synthesised 404 as well, so the vendor branch matches first "+
			"and returns a clean false — the fix is present and inert", synth, vendor)
	}
}
