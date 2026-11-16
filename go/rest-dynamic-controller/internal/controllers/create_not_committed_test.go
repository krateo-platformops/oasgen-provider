package restResources

import (
	"net/http"
	"testing"
)

// TestCreateRefusedWithoutCreating pins the EXACT set, because the two directions have very different
// costs and only one of them is recoverable.
//
// An unclassified error costs one existence check. A wrongly classified one costs a DUPLICATE external
// resource at a vendor -- the defect the v1.22.0 bump exists to stop. So the test names every code it
// refuses individually: a table that only checked the two accepted codes would pass under
// `return true`, which is the catastrophic direction.
func TestCreateRefusedWithoutCreating(t *testing.T) {
	classified := []int{
		http.StatusBadRequest,          // 400 -- malformed; nothing created
		http.StatusUnprocessableEntity, // 422 -- understood and refused on content
	}
	for _, code := range classified {
		if !createRefusedWithoutCreating(code) {
			t.Errorf("%d must be classified as not-committed: the vendor refused the request, so a "+
				"confirm-the-negative existence check before retrying is a wasted round trip -- and on a "+
				"kind whose existence cannot be answered it ends in a CreateOutcomeUnknown refusal that "+
				"needs a human", code)
		}
	}

	refused := map[int]string{
		http.StatusConflict: "some vendors answer 409 BECAUSE the resource now exists, so 'not committed' " +
			"is false for exactly the case that would duplicate",
		http.StatusNotFound: "usually a missing parent, but a vendor answering 404 from a route that " +
			"nevertheless wrote something would be unrecoverable; the price of leaving it out is one " +
			"existence check",
		http.StatusInternalServerError: "the vendor may have COMMITTED and lost the acknowledgement -- " +
			"this is the case the recovery exists for",
		http.StatusServiceUnavailable: "same as 500: a create must never blindly retry this",
		http.StatusGatewayTimeout:     "same as 500",
		http.StatusForbidden: "never reaches the caller -- IsAuthorizationDenied is handled earlier and " +
			"returns a nil error, so classifying it here would be dead code",
		http.StatusUnauthorized: "401 is retryable by design: credential rotation is delegated to a " +
			"secret manager and a request failing against a token being replaced must be retried",
		http.StatusTooManyRequests: "throttling says nothing about whether the write landed",
		0:                          "externalStatusCode's answer when there is no HTTP status to read: the request may never have completed",
		http.StatusCreated:         "not an error path at all; must not be classified if it ever arrives here",
		http.StatusOK:              "likewise",
	}
	for code, why := range refused {
		if createRefusedWithoutCreating(code) {
			t.Errorf("%d must NOT be classified as not-committed: %s", code, why)
		}
	}
}

// TestCreateRefusedWithoutCreatingIsExhaustive is the guard against the list growing by accident.
//
// Every status code from 100 to 599 is asked, and exactly two may answer true. A new code added to the
// switch without a decision fails here, which is the point: widening this list is how a duplicate gets
// created, and it must not be possible to do it quietly.
func TestCreateRefusedWithoutCreatingIsExhaustive(t *testing.T) {
	var got []int
	for code := 100; code < 600; code++ {
		if createRefusedWithoutCreating(code) {
			got = append(got, code)
		}
	}
	want := []int{http.StatusBadRequest, http.StatusUnprocessableEntity}
	if len(got) != len(want) {
		t.Fatalf("classified %v, want exactly %v.\nEvery addition to this set risks a duplicate external "+
			"resource and needs its own argument in the doc comment.", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("classified %v, want exactly %v", got, want)
		}
	}
}
