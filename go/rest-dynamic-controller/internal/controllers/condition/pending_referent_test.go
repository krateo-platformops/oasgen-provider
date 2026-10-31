package condition

import (
	"testing"

	"github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// THE LOAD-BEARING ASSERTION. PendingReferent must remain indistinguishable from Pending to
// IsConditionSet, which compares Type, Status and Reason and ignores Message.
//
// Observe guards on `notfound && IsConditionSet(mg, Pending())`. If a well-meaning change gave this its
// own Reason -- which is what a reader would expect a new constructor to do -- that guard would stop
// matching, and a resource waiting on a referent would be reported as NOT EXISTING instead of waiting.
// The CR would then be re-created or re-attempted on a different path, and the only visible symptom would
// be behaviour, not a failing test.
//
// So this pins the sameness, not the difference.
func TestPendingReferentIsIndistinguishableFromPendingExceptInMessage(t *testing.T) {
	plain := Pending()
	withDetail := PendingReferent(`dependent lookup "repo" found no item matching "krateo/foo"`)

	if withDetail.Type != plain.Type {
		t.Errorf("Type = %q, want %q: the Observe guard matches on Type", withDetail.Type, plain.Type)
	}
	if withDetail.Status != plain.Status {
		t.Errorf("Status = %q, want %q: the Observe guard matches on Status", withDetail.Status, plain.Status)
	}
	if withDetail.Reason != plain.Reason {
		t.Errorf("Reason = %q, want %q. A distinct Reason stops `IsConditionSet(mg, Pending())` matching, "+
			"so a resource waiting on a referent gets reported as not existing -- a behaviour change "+
			"wearing the clothes of a diagnostic improvement", withDetail.Reason, plain.Reason)
	}
	if withDetail.Message == "" {
		t.Error("Message is empty: the whole point is that the CR names what it is waiting for")
	}
	if plain.Message != "" {
		t.Error("Pending() now carries a message; this test can no longer show that the detail is the " +
			"only difference between the two")
	}
}

// And it must still be Ready=False. #323 was filed claiming a waiting resource "reports healthy"; it does
// not, precisely because Pending is Ready=False and the runtime's up-to-date branch sets ReconcileSuccess
// without setting Available. That property is what makes this a diagnosability fix rather than a
// correctness one, so it is asserted rather than assumed.
func TestPendingReferentIsNotReady(t *testing.T) {
	c := PendingReferent("anything")

	if c.Type != condition.TypeReady {
		t.Errorf("Type = %q, want %q", c.Type, condition.TypeReady)
	}
	if c.Status != metav1.ConditionFalse {
		t.Errorf("Status = %q, want False: a resource waiting on a referent it may never get is not ready", c.Status)
	}
	if c.Reason != ReasonPending {
		t.Errorf("Reason = %q, want %q", c.Reason, ReasonPending)
	}
}

// The message is passed through verbatim, because its structure is the useful part:
// errLookupReferentMissing already names the lookup and the value it was matching.
func TestPendingReferentPassesTheDetailThrough(t *testing.T) {
	const detail = `dependent lookup "orgId" found no item matching "acme"`

	if got := PendingReferent(detail).Message; got != detail {
		t.Errorf("Message = %q, want %q verbatim: the lookup name and the matched value are what an "+
			"operator needs, and reformatting them here would drift from the error that produced them",
			got, detail)
	}

	// An empty detail must not produce a message that implies one is coming.
	if got := PendingReferent("").Message; got != "" {
		t.Errorf("Message = %q for an empty detail, want empty", got)
	}
}
