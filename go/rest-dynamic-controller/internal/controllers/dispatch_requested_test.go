//go:build unit

package restResources

import (
	"os"
	"strings"
	"testing"
)

// TestEveryPostSendDispatchErrorIsMarked and TestTheDispatchCallerClassifiesBeforeClaimingNothingWasSent
// are the guard for issue 776, and they exist because the issue-746 guard next door COULD NOT SEE this
// path.
//
// WHY THAT GUARD MISSED IT, which is the finding worth keeping. It anchors on CreateWithResult's own
// `response, err := apiCall(ctx, &http.Client{}, callInfo.Path, reqConfiguration)` line and checks every
// zero-value return PAST it. The dispatch block sits at ~1604, the anchor at ~1760 -- so the dispatch
// path is before the anchor and outside the guard's coverage BY CONSTRUCTION. The dispatch's own send
// happens inside createDispatch, in a different function, which no positional check on CreateWithResult
// can reach.
//
// So a positional guard needs its scope stated as carefully as its assertion: "no zero-value return
// after the send" was true of the lines it read and silently false for a send it could not see. Measured
// by the acmp session on a real cluster: a fake that stores the object and then answers 500 produced SIX
// dispatches of one CR, with external-create-failed stamped at the same instant as
// external-create-pending on every attempt.

func dispatchSrc(t *testing.T, file string) string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	return string(raw)
}

// createDispatch's POST-SEND errors must carry the sentinel; a pre-send error must not. The caller
// cannot tell them apart any other way: a pre-send failure and a connection dropped mid-flight both
// arrive with no status code.
func TestEveryPostSendDispatchErrorIsMarked(t *testing.T) {
	s := dispatchSrc(t, "dispatch.go")

	const marker = "func (h *handler) createDispatch("
	start := strings.Index(s, marker)
	if start < 0 {
		t.Fatalf("dispatch.go no longer declares %s; this test must follow it", marker)
	}
	body := s[start+len(marker):]
	if end := strings.Index(body, "\nfunc "); end >= 0 {
		body = body[:end]
	}

	if !strings.Contains(s, "var errDispatchReachedTheVendor = errors.New(") {
		t.Fatal("dispatch.go no longer declares errDispatchReachedTheVendor. Without it the caller cannot " +
			"distinguish a dispatch that reached the vendor from one that never went out, and a lost " +
			"answer is reported as 'nothing was sent' (issue 776)")
	}

	// The send, and everything textually after it inside this function, is post-send.
	send := strings.Index(body, "apiCall(ctx, httpCli, callInfo.Path, conf)")
	if send < 0 {
		t.Fatal("createDispatch no longer calls apiCall with the expected argument list; this test cannot " +
			"locate the send and therefore measures nothing")
	}
	after := body[send:]

	// Every error return after the send must be marked. Count returns and marked returns separately so a
	// NEW unmarked one is caught rather than hidden by the marked ones.
	// SCANNED BY STATEMENT, NOT BY LINE. gofmt wraps a long return across lines, and the first run of
	// this test reported "1 of 2 marked" against correct code: the sentinel sat on the CONTINUATION line.
	// A line-based check on a multi-line construct is the same defect class the test exists to catch.
	// Continuation lines are indented further than the `return`, so that is the bound.
	lines := strings.Split(after, "\n")
	returns, marked := 0, 0
	for i, line := range lines {
		l := strings.TrimSpace(line)
		if !strings.HasPrefix(l, "return true, ") || strings.Contains(l, "return true, nil") {
			continue
		}
		returns++
		indent := len(line) - len(strings.TrimLeft(line, "\t"))
		stmt := line
		for j := i + 1; j < len(lines); j++ {
			nxt := lines[j]
			if strings.TrimSpace(nxt) == "" {
				break
			}
			if len(nxt)-len(strings.TrimLeft(nxt, "\t")) <= indent {
				break
			}
			stmt += " " + strings.TrimSpace(nxt)
		}
		if strings.Contains(stmt, "errDispatchReachedTheVendor") {
			marked++
		}
	}
	if returns == 0 {
		t.Fatal("no error returns found after createDispatch's send; the anchors must have moved, so this " +
			"test measures nothing")
	}
	if marked != returns {
		t.Errorf("%d of %d error returns after createDispatch's send carry errDispatchReachedTheVendor. "+
			"An unmarked one is reported to the runtime as 'nothing was requested', so the "+
			"existence-confirm gate never engages and the next pass dispatches again (issue 776)",
			marked, returns)
	}
}

// And the caller must TEST the sentinel before it returns a zero-value CreateResult.
func TestTheDispatchCallerClassifiesBeforeClaimingNothingWasSent(t *testing.T) {
	s := dispatchSrc(t, "restResources.go")

	d := strings.Index(s, "h.createDispatch(")
	if d < 0 {
		t.Fatal("CreateWithResult no longer calls createDispatch; this test must follow it")
	}
	// Scope to the dispatch block: from the call to the end of that if-statement's body, which the next
	// top-level `apiCall` line safely bounds.
	block := s[d:]
	if end := strings.Index(block, "response, err := apiCall("); end >= 0 {
		block = block[:end]
	}

	sent := strings.Index(block, "errors.Is(derr, errDispatchReachedTheVendor)")
	zero := strings.Index(block, "return controller.CreateResult{}, derr")

	if sent < 0 {
		t.Fatal("the dispatch block does not test errors.Is(derr, errDispatchReachedTheVendor). Every " +
			"dispatch error is then reported with Requested false, including one whose request reached " +
			"the vendor (issue 776)")
	}
	if zero < 0 {
		t.Fatal("the dispatch block no longer has a zero-value CreateResult return at all. That is the " +
			"correct answer for a PRE-SEND failure, so this test cannot confirm the ordering; it needs " +
			"updating rather than relaxing")
	}
	if sent > zero {
		t.Errorf("the dispatch block returns the zero CreateResult (offset %d) BEFORE it tests whether the "+
			"request reached the vendor (offset %d). A lost answer is then reported as 'nothing was sent' "+
			"(issue 776)", zero, sent)
	}

	// Post-send must mirror the ordinary create path rather than invent a second policy.
	if !strings.Contains(block, "controller.CreateResult{Requested: true}") {
		t.Error("the dispatch block never returns Requested: true. A dispatch that reached the vendor is a " +
			"requested create, and reporting otherwise re-dispatches a call that landed")
	}
	if !strings.Contains(block, "createRefusedWithoutCreating(") {
		t.Error("the dispatch block does not consult createRefusedWithoutCreating. The ordinary create path " +
			"uses it to let the runtime retry DIRECTLY on a 400/422 instead of confirming the negative; " +
			"a second, divergent policy for dispatch is how the two drift apart")
	}
}
