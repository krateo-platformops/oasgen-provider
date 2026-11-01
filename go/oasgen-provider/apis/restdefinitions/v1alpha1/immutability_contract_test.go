package v1alpha1

import (
	"os"
	"strings"
	"testing"
)

// The immutability contract is a PUBLISHED PROMISE: consumers plan upgrades around which RestDefinition
// fields they can change in place. docs/api.md stated it, and stated it WRONG -- it listed
// `additionalStatusFields` as strictly immutable when the marker enforces append-only, then contradicted
// itself two lines later (#290).
//
// Wrong in the direction that costs work: it told readers they could not append a status field, when
// appending is exactly what the rule permits, and is what unblocked an installer roll.
//
// Nothing could have caught that. The markers are the authority and the prose is a hand-written copy, so
// this pins the SHAPE of the contract: if a rule is added, removed, or changed in kind, this fails and
// whoever changed it has to revisit the page. It deliberately does not parse the doc -- a test that
// checked prose against markers would be asserting one hand-written thing against another.
//
// Counting by grepping the rendered CRD for `oldSelf` OVER-COUNTS: two matches live inside a
// `description` rather than a `rule`. The markers in this file are the only honest source.
func TestImmutabilityContractShape(t *testing.T) {
	src, err := os.ReadFile("types.go")
	if err != nil {
		t.Fatalf("types.go must be readable; a skipped check here is not a pass: %v", err)
	}
	text := string(src)

	const (
		strictRule     = `rule="self == oldSelf"`
		appendOnlyRule = `rule="size(self) >= size(oldSelf)`
	)

	strict := strings.Count(text, strictRule)
	appendOnly := strings.Count(text, appendOnlyRule)

	// The numbers documented in docs/api.md. Changing the contract means changing both.
	const wantStrict, wantAppendOnly = 5, 2

	if strict != wantStrict {
		t.Errorf("strictly-immutable fields = %d, want %d.\n\n"+
			"docs/api.md names them explicitly (resourceGroup, kind, identifiers, excludedSpecFields, "+
			"configurationFields). If you changed the contract deliberately, update that page in the same "+
			"commit -- it was wrong about this once already, and a consumer plans upgrades around it",
			strict, wantStrict)
	}
	if appendOnly != wantAppendOnly {
		t.Errorf("append-only fields = %d, want %d.\n\n"+
			"docs/api.md names them (additionalStatusFields, includedSpecFields). The distinction is not "+
			"cosmetic: append-only permits adding an entry in place, and telling a reader otherwise sends "+
			"them to delete and recreate a RestDefinition they did not need to touch",
			appendOnly, wantAppendOnly)
	}

	if total := strict + appendOnly; total != 7 {
		t.Errorf("total immutability rules = %d, want 7: the page states seven and a count is the one "+
			"thing a reader can check at a glance", total)
	}
}
