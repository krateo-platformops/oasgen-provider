package restResources

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/krateo-platformops/unstructured-runtime/pkg/meta"
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

// annotationCreateRefusedSince records WHEN a create was first refused, as RFC3339.
//
// IT EXISTS BECAUSE Ready's lastTransitionTime is not ours (issue 929). The anchor this function used
// to read was valid for every write THIS component makes: a refused create stays Ready=False, and
// setting the gated condition does not change the status, so it could not move the stamp it had just
// read. Both were verified. What was not considered is a write the component does NOT make --
// unstructured-runtime's handleObserve sets Ready=Unknown/ObserveFailed on ANY observe error, and
// False -> Unknown -> False is two transitions, so one transient vendor error resets the clock and
// buys another full reporting period of silence. Measured by a consumer on released 0.91.1 and
// 0.93.0: twice from unprovoked ADO 503s, once from a deliberate 400.
//
// THE SAME INCIDENT LEFT drift-repeats UNTOUCHED, which is the argument for this shape rather than an
// argument from symmetry: across one 503 the drift counter read 4 before and reached 5 at the next
// resync, latching on schedule, while Ready's stamp reset. The counter that survives is the one whose
// state the runtime does not write.
//
// NOT THE TWO RUNTIME STAMPS THAT LOOK RIGHT: external-create-pending is written before every create
// attempt and external-create-failed is re-set on every failure, so both move each cycle -- measured
// in unstructured-runtime v1.28.1 and recorded in this file before this change. Anchoring on either
// would mean the deadline never arrives.
//
// WRITTEN ONCE, NEVER REWRITTEN, NEVER CLEARED. A clear can be missed -- a failed write, or a path
// that returns early -- and a missed clear leaves a stamp that reports too EARLY, which is the failure
// this issue is about. It is read against krateo.io/external-create-succeeded instead: a success later
// than the stamp makes the stamp historical. existence.go:54 already compares those two stamps the
// same way.
const annotationCreateRefusedSince = "krateo.io/create-refused-since"

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

	// A READY=TRUE RESOURCE NEVER REPORTS, whatever its stamp says. This is a VETO, not the clock --
	// the interval still comes from the annotation below, so the defect this change fixes does not
	// return through it. It is kept because losing it would be a regression of my own making: the
	// external-create-succeeded comparison further down covers the cases I can enumerate (a create
	// that succeeded, and an adoption, which delete_identifier_recovery records as also setting that
	// stamp), and this covers the ones I cannot. Reporting a persistent refusal on a working resource
	// is a false alarm an operator cannot dismiss.
	for _, c := range unstructuredtools.GetConditions(mg) {
		if c.Type == condition.TypeReady && c.Status == metav1.ConditionTrue {
			return 0, false
		}
	}

	raw := mg.GetAnnotations()[annotationCreateRefusedSince]
	if raw == "" {
		// ABSENT MEANS "NOT YET", NOT "fall back to Ready". A fallback to the condition's stamp would
		// reintroduce the defect for exactly the resources carrying this annotation -- a guard undone
		// by the layer beneath it. The cost is explicit: on upgrade, a resource already refused for
		// hours restarts its period once, which DELAYS a true report by one period and never
		// suppresses one.
		return 0, false
	}
	since, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		// Unparsable reads as "not yet", for the reason drift-repeats reads a corrupt count as 0: this
		// is a brake, and a corrupt brake must fail open rather than wedge the reconcile.
		return 0, false
	}
	// A SUCCESS LATER THAN THE STAMP MAKES IT HISTORICAL, which is why nothing clears the annotation.
	if ok := meta.GetExternalCreateSucceeded(mg); !ok.IsZero() && ok.After(since) {
		return 0, false
	}
	age := now.Sub(since)
	return age, age >= d
}
