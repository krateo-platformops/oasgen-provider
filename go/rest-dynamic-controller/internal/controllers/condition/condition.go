package condition

import (
	"github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Reasons a resource is or is not ready.
const (
	ReasonPending string = "Pending"
	// ReasonUpdating is set after an update has been issued, until the next observe decides whether it
	// converged. It replaces the use of the runtime's "Creating" reason on that path: a resource that has
	// existed for days is not being created, and reporting that it is sent whoever reads the condition
	// looking for a creation that already happened (#213).
	ReasonUpdating string = "Updating"
	// ReasonDriftNotConverging is set when the same difference has been reported, and the same update
	// issued, enough times in a row that the update is evidently not able to fix it. The controller stops
	// issuing that update; the resource stays NOT ready, with the field named.
	ReasonDriftNotConverging string = "DriftNotConverging"
)

// Pending returns a metav1.Condition indicating that a resource is pending. This means that the resource has been created or updated, but is not yet ready for use.
func Pending() metav1.Condition {
	return metav1.Condition{
		Type:               condition.TypeReady,
		Status:             metav1.ConditionFalse,
		LastTransitionTime: metav1.Now(),
		Reason:             ReasonPending,
	}
}

// Updating returns a condition indicating that an update has been issued and its effect is not yet
// observed. Not Ready: the resource is known to differ from its spec, and whether the update fixed that is
// exactly what the next observe decides.
func Updating() metav1.Condition {
	return metav1.Condition{
		Type:               condition.TypeReady,
		Status:             metav1.ConditionFalse,
		LastTransitionTime: metav1.Now(),
		Reason:             ReasonUpdating,
	}
}

// DriftNotConverging returns a condition indicating that a difference has been reported repeatedly and the
// update issued for it has not resolved it, so the controller has stopped issuing that update.
//
// Deliberately NOT Ready and deliberately not an error. The resource genuinely does not match its spec, so
// claiming Ready would be false; but nothing is retryable either, because the one action available has been
// tried and changed nothing. The remedy is a declaration change (usually a responseTransform reconciling a
// request/response shape divergence), which is a human's decision, so the condition's job is to say which
// field and stop.
func DriftNotConverging(field, detail string) metav1.Condition {
	msg := detail
	if field != "" {
		msg = "field " + field + ": " + detail
	}
	return metav1.Condition{
		Type:               condition.TypeReady,
		Status:             metav1.ConditionFalse,
		LastTransitionTime: metav1.Now(),
		Reason:             ReasonDriftNotConverging,
		Message:            msg,
	}
}
