package condition

import (
	"testing"

	condition "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestAssumedUpToDateIsTrueWithItsOwnReason pins the CROSS-REPO contract, which is the only reason
// these three fields matter together.
//
// core-provider's krateoReady (PR #538) maps Ready=True + reason AssumedUpToDate to childUnevaluated,
// and they corrected themselves explicitly on the point this test exists for: the arm matches status
// TRUE ONLY. Ready=Unknown/AssumedUpToDate would miss it and fall to their default, holding every
// parent composition at Ready=False/Creating forever.
//
// So "status True" is not a stylistic choice here and must not be tidied into Unknown by someone who
// reads Unknown as more honest. It is more honest, and it is also a cross-repo change.
func TestAssumedUpToDateIsTrueWithItsOwnReason(t *testing.T) {
	c := AssumedUpToDate("because nothing looked")

	if c.Type != "Ready" {
		t.Errorf("Type = %q, want Ready: the rollup reads the child's Ready condition", c.Type)
	}
	if c.Status != metav1.ConditionTrue {
		t.Errorf(`Status = %q, want True.

core-provider's arm matches status True ONLY. Unknown would fall to krateoReady's default and hold
every parent at Ready=False/Creating for as long as the assumed path lasts -- swapping a false-healthy
for a permanent false-converging. Changing this is a cross-repo change, not a local tidy-up.`, c.Status)
	}
	// Two distinct assertions, not one: that the constructor uses the constant, AND that the constant
	// holds the literal core-provider matches. My first version wrote them as a single `||`, which go vet
	// correctly flagged as always-true when the two differ -- i.e. a check that could not fail in the one
	// case it existed for.
	if c.Reason != ReasonAssumedUpToDate {
		t.Errorf("Reason = %q, want ReasonAssumedUpToDate", c.Reason)
	}
	if ReasonAssumedUpToDate != "AssumedUpToDate" {
		t.Errorf(`ReasonAssumedUpToDate = %q, want "AssumedUpToDate".

core-provider's krateoReady compares the reason as a STRING literal (childhealth.go), not by importing
this constant, so renaming it here silently un-maps their arm.`, ReasonAssumedUpToDate)
	}
	if c.Message != "because nothing looked" {
		t.Errorf("Message = %q, want the caller's message carried through", c.Message)
	}
}

// TestAssumedUpToDateIsDistinguishableFromAvailable is the point of the change: before it, both states
// were Ready=True/Available and no machine reader could tell them apart.
func TestAssumedUpToDateIsDistinguishableFromAvailable(t *testing.T) {
	assumed := AssumedUpToDate("nothing looked")
	observed := condition.Available()

	if assumed.Status != observed.Status {
		t.Fatal("premise of this test: both are Ready=True, which is why the REASON has to differ")
	}
	if assumed.Reason == observed.Reason {
		t.Fatalf("both carry reason %q, so an asserted state is still indistinguishable from an "+
			"observed one -- which is the whole of issue 709", assumed.Reason)
	}
}
