package restResources

import (
	"os"
	"strings"
	"testing"
)

// TestNoCreateResultAfterTheRequestClaimsNothingWasSent is the guard for issue 746, and it is a
// SOURCE-LEVEL test on purpose.
//
// The defect: every error return in CreateWithResult after the POST returned a zero-value
// CreateResult, whose Requested is false. The runtime's gate is
// `createCommittedNot(actionErr) || !created.Requested`, so a 5xx, a transport failure, a timeout or
// a lost response all read as "nothing was sent" -- external-create-failed was stamped,
// ExternalCreateIncomplete went false, the existence-confirm recovery never ran, and the next pass
// POSTed again. v1.22.0's fix was unreachable from this controller.
//
// WHY A BEHAVIOURAL TEST WOULD NOT HAVE CAUGHT IT. Every individual return is syntactically fine and
// each returns a correct ERROR; what is wrong is one bool, on one side of a line that is 170 lines
// away in another repo. A unit test would have to construct a vendor failure for each of twelve
// sites and assert a field nobody thought to assert -- which is exactly why nobody did. The
// invariant is positional: "after the request leaves this process, no error return may claim nothing
// was sent." That is a property of the SOURCE, so the source is what this reads.
//
// Same shape as TestNoBodySendingCallSiteBypassesTheHelper (#722) and
// TestEveryBackfillRBACFunctionHasACallSite (#742), both of which caught real omissions.
func TestNoCreateResultAfterTheRequestClaimsNothingWasSent(t *testing.T) {
	b, err := os.ReadFile("restResources.go")
	if err != nil {
		t.Fatalf("reading restResources.go: %v", err)
	}
	lines := strings.Split(string(b), "\n")

	fnAt, postAt, endAt := -1, -1, -1
	for i, l := range lines {
		switch {
		case strings.Contains(l, "func (h *handler) CreateWithResult"):
			fnAt = i
		case fnAt >= 0 && postAt < 0 &&
			strings.Contains(l, "response, err := apiCall(ctx, &http.Client{}, callInfo.Path, reqConfiguration)"):
			postAt = i
		case postAt >= 0 && endAt < 0 && strings.HasPrefix(l, "func "):
			endAt = i
		}
	}
	// CALIBRATION: if any of the three anchors moved, this test silently covers nothing. Fail loudly
	// instead -- a guard that cannot find its own subject must not report success.
	if fnAt < 0 || postAt < 0 || endAt < 0 {
		t.Fatalf("anchors not found (func=%d post=%d end=%d). CreateWithResult or its apiCall line "+
			"was renamed; re-point this guard rather than deleting it", fnAt, postAt, endAt)
	}
	if !(fnAt < postAt && postAt < endAt) {
		t.Fatalf("anchors out of order: func=%d post=%d end=%d", fnAt, postAt, endAt)
	}

	var bad []int
	for i := postAt; i < endAt; i++ {
		if strings.Contains(lines[i], "return controller.CreateResult{},") {
			bad = append(bad, i+1)
		}
	}
	if len(bad) > 0 {
		t.Errorf("restResources.go:%v return a zero-value CreateResult AFTER the create request is "+
			"sent (line %d).\n"+
			"  Requested is false there, and the runtime's gate is\n"+
			"      createCommittedNot(actionErr) || !created.Requested\n"+
			"  so the error reads as \"nothing was sent\": external-create-failed is stamped, the\n"+
			"  existence-confirm recovery is skipped, and the next pass creates the resource AGAIN.\n"+
			"  Use controller.CreateResult{Requested: true} for any error return past the request --\n"+
			"  including the ones after a SUCCESSFUL create, where the resource demonstrably exists\n"+
			"  and a status-handling failure must not report otherwise. Issue 746.", bad, postAt+1)
	}

	// POSITIVE CONTROL: the setup failures BEFORE the request must still be allowed to report
	// false, or this guard would be pushing every return to Requested:true and asserting nothing
	// about the distinction it exists to protect.
	var presend int
	for i := fnAt; i < postAt; i++ {
		if strings.Contains(lines[i], "return controller.CreateResult{},") {
			presend++
		}
	}
	if presend == 0 {
		t.Error("no zero-value CreateResult remains BEFORE the request. Either the pre-send setup " +
			"failures were changed too -- which would claim a request was sent when none was -- or " +
			"this guard is no longer distinguishing the two halves at all")
	}
}
