//go:build unit || integration

package condition

import (
	"strings"
	"testing"
	"time"
)

// TestTheRefusalMessageDoesNotClaimTheControllerStopped pins the SEMANTIC of a user-facing string
// that had no test at all, which is how it shipped saying the opposite of what the code does.
//
// The message used to end "The retry budget is spent, so the controller is no longer re-sending
// within each cycle". That was written on a reading of the limiter which a consumer's measurement
// falsified: a full retry series runs in EVERY resync cycle, indefinitely, because the limiter keys
// its failure map on the boxed Event value and Event carries a QueuedAt timestamp. A new enqueue is
// a new key, so the count restarts.
//
// A consumer maps this reason to a UI pill. The old sentence would have had them tell an operator
// the system had given up on a request it is in fact still retrying every three minutes.
//
// IT ASSERTS ON THE MESSAGE VALUE, NOT ON THE FILE. A grep-the-source check would fail on the
// comment that documents the correction -- a correction has to quote what it corrects, so any
// absence check over the source fires on the fix itself.
func TestTheRefusalMessageDoesNotClaimTheControllerStopped(t *testing.T) {
	msg := CreateRefusedPersistently(62*time.Minute, 422, "unprocessable entity").Message

	// CONTROL: the message is non-trivial, so the absence assertions below are not vacuous.
	if len(msg) < 40 {
		t.Fatalf("message is implausibly short (%d chars), so the checks below measure nothing: %q", len(msg), msg)
	}

	// THE SUBJECT: it must not say the controller has stopped.
	for _, forbidden := range []string{
		"no longer re-sending",
		"no longer retrying",
		"has stopped retrying",
		"gave up",
		"will not retry",
	} {
		if strings.Contains(msg, forbidden) {
			t.Errorf("the message claims the controller stopped (%q), which is FALSE -- the retry "+
				"budget is per enqueued event and resets on every resync:\n%s", forbidden, msg)
		}
	}

	// And it must positively say that it will try again, or an operator is left to infer it.
	if !strings.Contains(msg, "next resync") {
		t.Errorf("the message does not say the controller will try again on the next resync, so a "+
			"reader cannot tell whether it is still trying:\n%s", msg)
	}
	if !strings.Contains(msg, "NOT stopped") {
		t.Errorf("the message does not state plainly that retrying has NOT stopped:\n%s", msg)
	}
}

// TestTheRefusalMessageCarriesTheCodeButNotTheVendorBody documents the provenance split a consumer
// asked about, so a later change that starts copying vendor text into the condition has to change
// this test deliberately.
//
// StatusError.Error() carries only our own words and the status code; the vendor's response text
// lives in a BodySnippet field that no caller passes to this constructor. That is deliberate -- a
// condition is in .status, readable by anyone who can get the CR, and a refusal body can echo
// submitted content.
func TestTheRefusalMessageCarriesTheCodeButNotTheVendorBody(t *testing.T) {
	msg := CreateRefusedPersistently(90*time.Minute, 422, "unexpected status: 422").Message

	if !strings.Contains(msg, "422") {
		t.Errorf("the status code is missing, and it is the one vendor fact this message is meant to "+
			"carry:\n%s", msg)
	}
	if !strings.Contains(msg, "1h30m0s") {
		t.Errorf("the duration is missing or not rounded as expected:\n%s", msg)
	}

	// The zero-code and empty-detail forms must not emit dangling punctuation -- this message is
	// shown verbatim in a UI tooltip.
	bare := CreateRefusedPersistently(time.Hour, 0, "").Message
	if strings.Contains(bare, "(last status 0)") {
		t.Errorf("a zero status code reached the message:\n%s", bare)
	}
	if strings.Contains(bare, ": .") || strings.Contains(bare, " :") {
		t.Errorf("empty detail left dangling punctuation:\n%s", bare)
	}
	if !strings.Contains(bare, "NOT stopped") {
		t.Errorf("the bare form lost the retry statement:\n%s", bare)
	}
}
