package hostpolicy

import (
	"strings"
	"testing"
)

// #403: a document server URL that cannot be parsed is DISCARDED from the permitted set, and until this
// fix the discard left no trace anywhere -- while the comment beside it said "Not fatal, and not silent."
// The first clause was true and deliberate; the second was false.
//
// THE TEST ASSERTS THE TRACE, not that Parse returned a shorter entry list. The old behaviour already
// returned a nil error and a shorter list, so a test shaped that way passes with the discard still
// silent -- which is the whole defect.
func TestAnUnparseableDocumentServerURLIsReportedRatherThanOnlyDropped(t *testing.T) {
	p, discarded, err := Parse(
		[]string{"example.com"},
		[]string{"ftp://nope.example", "https://ok.example", "//also-bad"},
		false,
	)

	// The non-fatal half must not regress: one malformed `servers:` entry cannot make a whole kind
	// unreconcilable, so Parse still succeeds and the good host is still permitted.
	if err != nil {
		t.Fatalf("a bad DOCUMENT host must not be fatal, or one malformed servers: entry makes the kind "+
			"unreconcilable: %v", err)
	}
	if p == nil {
		t.Fatal("no policy returned")
	}

	if len(discarded) != 2 {
		t.Fatalf("discarded = %d entries, want 2. Without this the skip is invisible: the caller cannot "+
			"tell a document with three good servers from one with two bad ones, and an operator meets "+
			"the difference later as a refusal naming a host", len(discarded))
	}

	// The RAW string must come back verbatim, because that is what the operator has to find and fix.
	var got []string
	for _, d := range discarded {
		got = append(got, d.URL)
		if d.Reason == nil {
			t.Errorf("discarded %q carries no Reason; a report that cannot say WHY sends the operator "+
				"back to guessing", d.URL)
		}
	}
	for _, want := range []string{"ftp://nope.example", "//also-bad"} {
		if !contains(got, want) {
			t.Errorf("discarded = %v, want it to include %q verbatim", got, want)
		}
	}
	if contains(got, "https://ok.example") {
		t.Error("a PARSEABLE server URL was reported as discarded. Reporting a good host would send an " +
			"operator to fix something that works, which is worse than silence")
	}
}

// A document whose server URLs all parse must report NOTHING, or the report becomes noise an operator
// learns to ignore -- and an always-firing warning is indistinguishable from none.
func TestNothingIsReportedWhenEveryDocumentServerURLParses(t *testing.T) {
	_, discarded, err := Parse(nil, []string{"https://api.github.com", "http://ghe.corp.example.com:8443"}, false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(discarded) != 0 {
		t.Errorf("discarded = %v, want none", discarded)
	}
}

// The asymmetry is deliberate and must stay: a bad OPERATOR entry is FATAL with the allowlist wrapping,
// where a bad DOCUMENT host is reported and skipped. Pinned because nothing asserted it, and because
// swapping the two would be a plausible "consistency" change that breaks the design in both directions
// at once -- a typo in chart values silently ignored, and a cosmetic document defect taking a kind down.
func TestABadOperatorEntryIsFatalWhileABadDocumentHostIsReported(t *testing.T) {
	if _, _, err := Parse([]string{"ftp://nope.example"}, nil, false); err == nil {
		t.Error("a malformed allowlist entry must be fatal: degrading into permit-nothing or " +
			"permit-everything surfaces later, in opposite and equally confusing ways")
	} else if !strings.Contains(err.Error(), "endpoint allowlist entry") {
		t.Errorf("error = %q, want it to name the allowlist so an operator knows which input to fix",
			err.Error())
	}

	if _, discarded, err := Parse(nil, []string{"ftp://nope.example"}, false); err != nil {
		t.Errorf("the identical string as a DOCUMENT host must not be fatal: %v", err)
	} else if len(discarded) != 1 {
		t.Errorf("discarded = %d, want 1: the document half is reported, not fatal and not silent",
			len(discarded))
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
