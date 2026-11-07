package restResources

import (
	"errors"
	"net/http"
	"testing"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func absenceReportCR() *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{}}
	u.SetAPIVersion("example.com/v1")
	u.SetKind("Widget")
	u.SetName("w1")
	u.SetNamespace("default")
	return u
}

// BOTH HALVES, and the second is the one that matters.
//
// The whole point of #485's fix is to distinguish a 404 THIS CONTROLLER SYNTHESISED from one the VENDOR
// returned. A test that only asserted "an event is emitted on a synthesised 404" passes just as well
// against an unconditional emission -- which would put a warning on every ordinary first create of every
// findby-only kind, 37 of which were measured across two consumers. The vendor-404 arm is what makes the
// synthesised arm mean anything.
//
// The third arm covers the shape that produced this issue's first WRONG diagnosis: IsNotFoundError is
// true for both kinds, so a predicate keyed on it rather than on the Synthesised marker would report
// every not-found alike.
func TestOnlyASynthesised404IsReportedAsInferredAbsence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantReport bool
		why        string
	}{
		{
			name:       "synthesised 404 from an unmatched listing",
			err:        &restclient.StatusError{StatusCode: http.StatusNotFound, Synthesised: true},
			wantReport: true,
			why: "a findby that listed successfully and matched nothing is the case this reports: the " +
				"matching can fail for reasons unrelated to existence",
		},
		{
			name:       "vendor 404",
			err:        &restclient.StatusError{StatusCode: http.StatusNotFound},
			wantReport: false,
			why: "the API itself said the resource is missing, which is ordinary and is how every " +
				"findby-only kind reaches its FIRST create -- reporting it would warn on healthy creates",
		},
		{
			name:       "a non-404 error",
			err:        errors.New("connection refused"),
			wantReport: false,
			why:        "not a not-found verdict at all; this branch is not even reached for it",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evs := &recordingEvents{}
			h := &handler{eventRecorder: evs, logger: logging.NewNopLogger()}

			got := h.reportInferredAbsence(absenceReportCR(), tc.err, logging.NewNopLogger())

			require.Equal(t, tc.wantReport, got, tc.why)
			require.Len(t, evs.all(), map[bool]int{true: 1, false: 0}[tc.wantReport],
				"the return value and the emitted events must agree: %s", tc.why)

			if tc.wantReport {
				e := evs.all()[0]
				require.Equal(t, reasonAbsenceInferred, e.Reason,
					"the reason is what a consumer greps for; it must not drift silently")
				require.Contains(t, e.Message, "listed successfully and no item matched",
					"the message must say WHY absence was concluded, not merely that it was -- a reader "+
						"seeing a create they did not expect needs the cause")
				require.Contains(t, e.Message, "A create will be attempted",
					"it must say what happens next, because nothing refuses: the create proceeds")
			}
		})
	}
}

// THE CALL SITE IS NOT COVERED HERE, and saying so is the point.
//
// Observe's findby branch is only reachable through the full handler -- a clientset, a document getter and
// an HTTP endpoint -- and the tests that drive it are integration-tagged and need a cluster. So this file
// proves the DECISION and not the WIRING, exactly the gap that let status.schemaWarnings ship unpersisted
// in 0.54.0 while three tests for it passed (#492).
//
// Two things stand in for it, deliberately named rather than implied:
//  1. the call site is a single unconditional line in the one branch that can produce a synthesised 404
//     (Observe's findby not-found path; the get path takes a vendor 404 and can never synthesise one), so
//     there is no condition on it to get wrong;
//  2. a consumer with a reproduction -- a findby blinded by a bad offset on a live fleet -- has been asked
//     to confirm the event appears on the real path.
//
// If (2) comes back negative, this test will still pass, and that is the limitation to remember.
func TestTheCallSiteIsCoveredByInspectionNotByThisFile(t *testing.T) {
	t.Skip("documentation of a known coverage gap; see the comment above")
}
