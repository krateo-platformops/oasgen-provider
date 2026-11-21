//go:build unit

package restResources

import (
	"context"
	"strings"
	"testing"

	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
)

// BEHAVIOURAL, not source-level: it drives reportUnmodelledKeys through a recording recorder, so what it
// reads is the event an operator would see.
//
// The old wording asserted that an update "would DELETE" these keys. Measured on Azure DevOps across four
// kinds and nine key rows, SIX were kept by the vendor -- so the assertion was wrong about two thirds of
// the cases that produced it, and it sent operators to model keys the endpoint was never going to drop
// (issue 757). Whether an omitted key is dropped is the endpoint's behaviour and is not observable here.
func TestTheUnmodelledKeyWarningDoesNotAssertTheVendorsBehaviour(t *testing.T) {
	mg := driftMG(1, nil)
	h, evs := latchedHandler(t, mg)

	found := map[string][]string{"settings": {"manualQueueOnly"}, "type": {"displayName", "url"}}
	// A NIL PARTIAL SET, deliberately: this test is about the non-partial wording, and passing nil keeps
	// it asserting exactly the case issue 757 measured. The partial wording has its own test, and the two
	// must not share a fixture -- a single input exercising both would let either sentence satisfy an
	// assertion meant for the other.
	if !h.reportUnmodelledKeys(context.Background(), mg, found, nil, logging.NewNopLogger()) {
		t.Fatal("reportUnmodelledKeys returned false, so nothing was reported and this test asserts nothing")
	}

	all := evs.all()
	if len(all) != 1 {
		t.Fatalf("got %d events, want exactly 1", len(all))
	}
	msg := all[0].Message

	// The REASON is read alone, in a REASON column, with no message beside it -- so it must not assert the
	// removal either. Renamed from UnmodelledKeysWouldBeRemoved; a consumer's filter on the old name stops
	// matching, which is the stated cost of saying something true.
	if got := string(all[0].Reason); got != "UnmodelledKeysPresent" {
		t.Errorf("event reason = %q, want %q. The reason is the channel most often read without the "+
			"message, so a name that predicts the vendor's behaviour is the assertion issue 757 is about",
			got, "UnmodelledKeysPresent")
	}

	// The specific claim that was false two thirds of the time. Case-insensitive: the old text shouted it.
	lower := strings.ToLower(msg)
	for _, forbidden := range []string{
		"would delete",
		"would remove",
		"so issuing one would",
	} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("the warning still says %q. Whether the vendor drops keys it was not sent is that "+
				"endpoint's behaviour; measured on one vendor, 6 of 9 key rows were KEPT", forbidden)
		}
	}

	// What IS observable must still be said, or the warning stops being actionable: we send the field whole.
	// The PHRASE, not the word: the message also ends with "own the whole field", so a bare search for
	// "whole" is satisfied by a sentence that says nothing about what the update sends. A control that
	// deleted the real clause passed against the loose version of this check.
	if !strings.Contains(lower, "sends each of these fields whole") {
		t.Error("the warning no longer says the update SENDS EACH OF THESE FIELDS WHOLE. That is this " +
			"controller's own behaviour, it is why the keys are at risk at all, and it is the half that " +
			"is observable from here")
	}
	// And it must point the reader at the thing that decides the outcome.
	if !strings.Contains(lower, "not observable") && !strings.Contains(lower, "semantics") {
		t.Error("the warning does not tell the reader the outcome depends on the endpoint's own semantics, " +
			"so an operator has no way to know the message is not a prediction")
	}
	// The detail must survive the reword: without the field list the warning names nothing to act on.
	for _, want := range []string{"spec.settings", "manualQueueOnly", "spec.type", "displayName"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the warning no longer names %q; the key list is what an operator acts on", want)
		}
	}
}
