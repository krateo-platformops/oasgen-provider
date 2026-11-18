package restResources

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	unstructuredtools "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured"
	"github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"
)

// DefaultCreateRefusalReportAfter is how long a create may go on being refused before the controller
// says so in a condition rather than only in a log line.
//
// ONE HOUR, AND THE UNIT IS DELIBERATELY WALL-CLOCK. A retry COUNT would mean different things per
// install: the resync interval is a flag, so "20 cycles" is an hour at the 180s default and twenty
// minutes if an operator sets 60s. A duration means the same thing everywhere.
const DefaultCreateRefusalReportAfter = time.Hour

// createRefusedLongerThan reports whether this resource has been not-Ready for at least d, using the
// Ready condition's lastTransitionTime as the anchor.
//
// WHY THAT IS A VALID ANCHOR, and it is the only part of this worth checking carefully. The runtime's
// condition.Upsert holds the timestamp while the STATUS is unchanged:
//
//	if el.Status == co.Status && !el.LastTransitionTime.IsZero() {
//		// Same status: this is not a transition. Keep the moment it became true.
//		co.LastTransitionTime = el.LastTransitionTime
//	}
//
// A create that keeps being refused stays at Ready=False, so the stamp holds at the moment it first
// became False. No new field, no annotation, nothing to migrate.
//
// AND THE CONDITION THIS GATES IS ITSELF Ready=False, which is what makes the arrangement stable rather
// than lucky: setting it does not change the status, so it does not reset the anchor it just read. A
// condition that moved its own anchor would re-arm the timer every cycle and never fire twice.
//
// THE TWO STAMPS THAT LOOK RIGHT AND ARE NOT. krateo.io/external-create-pending is written as
// `SetExternalCreatePending(el, time.Now())` before every create attempt, and external-create-failed is
// re-set on every failure, so both move each cycle. Measured in unstructured-runtime v1.28.1. Anchoring
// on either would mean the deadline never arrives.
//
// THE EDGE, STATED BECAUSE THE MESSAGE MUST NOT CLAIM OTHERWISE: Upsert compares Status, not Reason. A
// resource that was already Ready=False for an unrelated reason before its create began failing carries
// an anchor earlier than its first create attempt, so this can fire after less than d of actual create
// refusals. That is why the condition says "not Ready" rather than "retrying" -- see
// customcondition.CreateRefusedPersistently, whose message names the measured quantity and not an
// inferred one.
func createRefusedLongerThan(mg *unstructured.Unstructured, d time.Duration, now time.Time) (time.Duration, bool) {
	if mg == nil || d <= 0 {
		return 0, false
	}

	for _, c := range unstructuredtools.GetConditions(mg) {
		if c.Type != condition.TypeReady {
			continue
		}
		// A Ready=True resource is not failing, whatever its age.
		if c.Status != metav1.ConditionFalse {
			return 0, false
		}
		// A zero stamp is missing data, not an instant in 0001. Treating it as a real time would make
		// every such resource instantly over the deadline, which is the "plausible value" failure the
		// runtime's own IsZero guard exists to avoid.
		if c.LastTransitionTime.IsZero() {
			return 0, false
		}
		age := now.Sub(c.LastTransitionTime.Time)
		return age, age >= d
	}

	// No Ready condition at all: nothing has been observed yet, so there is nothing to report.
	return 0, false
}
