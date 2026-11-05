//go:build unit

package restclient

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// #461. StatusError already distinguishes a status this package INVENTED from one the vendor sent --
// Synthesised is set only on the findby no-match paths, and exists.go branches on it BEFORE the plain
// not-found case, because IsNotFoundError is true for both. Error() threw that distinction away: an
// invented 404 rendered as `unexpected status: 404: item not found`, identical to a vendor 404.
//
// The message is the only thing an operator has when deciding whether to clear
// krateo.io/external-create-pending, and clearing it wrongly lets the reconciler create a resource that
// already exists. Measured on a live fleet by the openstack-ironic session: the resource this message
// called "item not found" was active, powered on, with no error, and no request had reached the endpoint
// at all. The token was FALSE about the world, not merely unproven.

func TestASynthesisedStatusDoesNotReadLikeAVendorResponse(t *testing.T) {
	err := &StatusError{StatusCode: http.StatusNotFound, Inner: fmt.Errorf("item not found"), Synthesised: true}
	got := err.Error()

	if !strings.Contains(got, "synthesised by this controller") {
		t.Errorf("Error() = %q\nIt must say the status is this controller's, not the vendor's", got)
	}
	if !strings.Contains(got, "NOT received from the vendor") {
		t.Errorf("Error() = %q\nIt must deny vendor provenance explicitly: a reader who sees only a 404 "+
			"concludes the resource is absent and clears the create-pending annotation", got)
	}
	if strings.Contains(got, "unexpected status") {
		t.Errorf("Error() = %q\nThis is the vendor-status phrasing, and it is what made an invented 404 "+
			"indistinguishable from a received one", got)
	}
}

// THE CONTROL. A real vendor status must render EXACTLY as before, byte for byte. Eight existing
// assertions across this package read that form, and none of them is wrong -- only the synthesised
// branch was. Without this arm the change above could silently rewrite every error message in the
// module.
func TestARealVendorStatusRendersUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *StatusError
		want string
	}{
		{
			name: "with an inner error",
			err:  &StatusError{StatusCode: 400, Inner: fmt.Errorf("invalid status code: 400")},
			want: "unexpected status: 400: invalid status code: 400",
		},
		{
			name: "without one",
			err:  &StatusError{StatusCode: 500},
			want: "unexpected status: 500:",
		},
		{
			name: "a VENDOR 404 is still a plain unexpected status",
			err:  &StatusError{StatusCode: http.StatusNotFound, Inner: fmt.Errorf("item not found")},
			want: "unexpected status: 404: item not found",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q -- existing callers and tests read this exact form", got, tc.want)
			}
		})
	}
}

// Rendering is not classification. Changing the message must not move a single caller's branch: a
// synthesised 404 is still a not-found to IsNotFoundError (which is why exists.go has to check the
// synthesised case FIRST), and a vendor 404 must not start reading as synthesised.
func TestTheMessageChangeDoesNotMoveAnyClassification(t *testing.T) {
	synth := &StatusError{StatusCode: http.StatusNotFound, Inner: fmt.Errorf("item not found"), Synthesised: true}
	vendor := &StatusError{StatusCode: http.StatusNotFound, Inner: fmt.Errorf("item not found")}

	for _, tc := range []struct {
		name            string
		err             error
		notFound, synth bool
	}{
		{"synthesised 404", synth, true, true},
		{"vendor 404", vendor, true, false},
		{"wrapped synthesised 404", fmt.Errorf("existence check: %w", synth), true, true},
		{"wrapped vendor 404", fmt.Errorf("existence check: %w", vendor), true, false},
		{"a non-status error", errors.New("boom"), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNotFoundError(tc.err); got != tc.notFound {
				t.Errorf("IsNotFoundError = %v, want %v", got, tc.notFound)
			}
			if got := IsSynthesisedNotFound(tc.err); got != tc.synth {
				t.Errorf("IsSynthesisedNotFound = %v, want %v", got, tc.synth)
			}
		})
	}
}

// Unwrap must keep working, or errors.Is/As against the inner cause stops matching for callers that
// never asked about the message at all.
func TestUnwrapStillReachesTheInnerCause(t *testing.T) {
	inner := fmt.Errorf("item not found")
	err := &StatusError{StatusCode: http.StatusNotFound, Inner: inner, Synthesised: true}
	if !errors.Is(err, inner) {
		t.Error("errors.Is no longer reaches the inner cause")
	}
}
