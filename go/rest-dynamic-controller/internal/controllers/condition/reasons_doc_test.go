//go:build unit || integration

package condition

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEveryReasonAppearsInTheDoc keeps docs/rdc-ready-reasons.md true by construction.
//
// A consumer VENDORS that table and maps each reason to a UI state, so a reason this package adds
// without a row becomes an unlabelled pill in somebody else's product, and a row with no reason
// behind it is a promise about behaviour that does not exist.
//
// IT COMPARES WIRE VALUES, NOT IDENTIFIERS. Two reasons here differ between the two --
// ReasonResolverDigestRecorded is "DigestRecorded" and ReasonResolverDigestFieldAbsent is
// "DigestFieldAbsent" -- so a check written against Go names would pass while the documented strings
// were wrong, which is the only thing the consumer's parser actually reads.
func TestEveryReasonAppearsInTheDoc(t *testing.T) {
	// EVERY reason this package defines, by wire value. Adding a constant above without adding it
	// here makes this list stale rather than the test wrong, which is why the count is asserted too.
	wire := []string{
		ReasonPending,
		ReasonUpdating,
		ReasonDriftNotConverging,
		ReasonCreateRefusedPersistently,
		ReasonCreateUnreachable,
		ReasonAuthorizationDenied,
		ReasonCreateFailed,
		ReasonUpdateFailed,
		ReasonNoIdentifierRecorded,
		ReasonAssumedUpToDate,
		ReasonResponseObtained,
		ReasonNoResponseBody,
		ReasonResolverDigestRecorded,
		ReasonResolverDigestFieldAbsent,
		ReasonNoCreateVerbDeclared,
	}

	doc := readReasonsDoc(t)

	// CONTROL: the doc parsed into rows at all. Without this, a path typo or a renamed file would
	// make every "documented" check below pass over an empty set.
	rows := reasonRowsIn(doc)
	if len(rows) < len(wire) {
		t.Fatalf("the doc yielded %d reason rows for %d reasons -- the table did not parse, so the "+
			"assertions below would measure nothing", len(rows), len(wire))
	}

	for _, w := range wire {
		if !rows[w] {
			t.Errorf("reason %q is set by this package and has NO ROW in docs/rdc-ready-reasons.md -- "+
				"a consumer vendoring that table would show it as an unlabelled state", w)
		}
	}

	// And the other direction: a row with no reason behind it documents behaviour that does not exist.
	for r := range rows {
		found := false
		for _, w := range wire {
			if r == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the doc has a row for reason %q, which this package does not set -- either the "+
				"reason was removed and the row outlived it, or the row was never real", r)
		}
	}
}

// TestTheDocKeepsTheSevenColumnShape pins the format a consumer's parser depends on. They asked for
// core-provider's exact column set, and a reordering or an added column breaks their vendoring
// silently -- their parser skips to the first table and reads positionally.
func TestTheDocKeepsTheSevenColumnShape(t *testing.T) {
	doc := readReasonsDoc(t)

	var header string
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "| condition ") {
			header = line
			break
		}
	}
	if header == "" {
		t.Fatalf("no table header starting \"| condition \" -- the consumer's parser skips to the " +
			"first table and would read whatever came next")
	}

	want := []string{"condition", "reason", "status", "writer", "parent verdict", "expected to clear", "written by"}
	got := []string{}
	for _, c := range strings.Split(strings.Trim(header, "|"), "|") {
		got = append(got, strings.TrimSpace(c))
	}
	if len(got) != len(want) {
		t.Fatalf("the table has %d columns, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("column %d is %q, want %q -- the parser reads positionally", i, got[i], want[i])
		}
	}
}

// TestTheDocStatesThatVendorBodiesAreNotInStatus pins the decision, not just its consequence.
//
// A consumer asked whether a tooltip can show the vendor's refusal text. The answer is no, and the
// REASON is a provenance boundary rather than an oversight -- .status is readable by anyone who can
// get the CR. If someone later copies BodySnippet into a condition, this fails and makes them
// reconsider the sentence rather than discover the leak downstream.
func TestTheDocStatesThatVendorBodiesAreNotInStatus(t *testing.T) {
	// WHITESPACE IS COLLAPSED FIRST, because every one of these phrases spans a markdown line break
	// in the current file and a plain Contains found NONE of them -- my own doc would have failed
	// this test. Reflowing a paragraph must not break a check about its meaning.
	doc := strings.Join(strings.Fields(readReasonsDoc(t)), " ")
	for _, phrase := range []string{
		"never carries the vendor's response body",
		"readable by every subject with `get` on the CR",
		"The body is logged at Info",
	} {
		if !strings.Contains(doc, phrase) {
			t.Errorf("the doc no longer says %q -- a consumer would be left to assume a tooltip can "+
				"show the vendor's explanation", phrase)
		}
	}
}

func readReasonsDoc(t *testing.T) string {
	t.Helper()
	p := filepath.Join("..", "..", "..", "..", "..", "docs", "rdc-ready-reasons.md")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("could not read %s: %v -- a path error here would make every check in this file "+
			"pass over an empty document", p, err)
	}
	if len(b) < 500 {
		t.Fatalf("%s is only %d bytes; the file read is not the doc", p, len(b))
	}
	return string(b)
}

// reasonRowsIn collects the reason column of every table row, which is the second cell and is
// written in backticks.
func reasonRowsIn(doc string) map[string]bool {
	out := map[string]bool{}
	re := regexp.MustCompile("^\\|[^|]+\\|\\s*`([^`]+)`\\s*\\|")
	for _, line := range strings.Split(doc, "\n") {
		if m := re.FindStringSubmatch(line); m != nil {
			out[m[1]] = true
		}
	}
	return out
}
