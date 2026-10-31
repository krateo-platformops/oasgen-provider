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
	// ReasonAuthorizationDenied is set when the API answered 403 to a verb and the response carried no
	// rate-limit signal, so the refusal is a decision about this credential rather than a limit that
	// clears. The controller stops issuing that verb; the resource stays NOT ready.
	//
	// Distinct from a 401, which is deliberately NOT in this family: a 401 is what a credential being
	// rotated under us looks like, and retrying it is how rotation is tolerated (#313).
	ReasonAuthorizationDenied string = "AuthorizationDenied"
	// ReasonResponseObtained and ReasonNoResponseBody are the two states of TypeObserved below.
	ReasonResponseObtained string = "ResponseObtained"
	ReasonNoResponseBody   string = "NoResponseBody"
)

// TypeObserved reports whether the last observe obtained a response body to populate status FROM, which is
// a different question from whether the resource is Ready.
//
// It exists because a CR could not distinguish three situations that look identical from outside -- a
// declared status field simply absent in all of them (#233):
//
//  1. the API returned a body and genuinely had no diagnostic to report -- healthy;
//  2. the API returned a body and the diagnostic was at a path the RestDefinition does not name --
//     mis-declared;
//  3. the observe obtained no body at all -- the diagnostic exists and is NOT being read.
//
// Case 3 is the one with consequences: a resource whose failure reason is being dropped looks exactly
// like a healthy one. TypeObserved separates it from the other two without claiming anything about any
// individual field, which matters because absence is the COMMON case: measured across five live Aruba
// resources, every healthy payload omitted both failureReason and error even though both are declared in
// the vendor's published specs. A per-field warning would therefore fire constantly on correct input,
// which is the mistake #228 was -- a warning about a shape the verb is defined not to have.
//
// It is deliberately a condition that TRANSITIONS rather than a marker that is only ever set. A condition
// whose status never changes carries a lastTransitionTime from the first time it was set, forever, so an
// always-True "Observed" would answer "populated from a response obtained WHEN?" with the wrong date. By
// flipping to False when no body is obtained, the timestamp means what a reader assumes it means.
const TypeObserved string = "Observed"

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

// AuthorizationDenied returns a condition indicating that the API refused a verb for this credential, so
// the controller has stopped issuing it.
//
// Deliberately NOT Ready and deliberately not an error, matching DriftNotConverging: the resource does not
// match its spec, so Ready would be false, and nothing is retryable because the only action available has
// been refused by a decision the controller cannot influence. The remedy is a credential or policy change,
// which is a human's, so this condition's job is to name the verb and stop.
//
// THE VENDOR'S EXPLANATION IS NOT IN THE MESSAGE, deliberately. A 403 body routinely names the required
// scope or role, which is exactly what an operator wants -- but a condition is readable by anyone with
// `get` on the CR while an error reaches logs and events, which are operator-only. #334 made that call for
// error bodies generally and this follows it rather than carving out an exception for the one body most
// likely to describe someone's permission model. The message says where to look instead.
func AuthorizationDenied(verb string) metav1.Condition {
	msg := "the API refused this request for the configured credential; see the controller log for the API's explanation"
	if verb != "" {
		msg = "the API refused the " + verb + " request for the configured credential; see the controller log for the API's explanation"
	}
	return metav1.Condition{
		Type:               condition.TypeReady,
		Status:             metav1.ConditionFalse,
		LastTransitionTime: metav1.Now(),
		Reason:             ReasonAuthorizationDenied,
		Message:            msg,
	}
}

// Observed reports that the last observe obtained a response body, so the declared status fields were
// populated from it. A status field still absent after this is absent because the API did not send it.
func Observed() metav1.Condition {
	return metav1.Condition{
		Type:               TypeObserved,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Reason:             ReasonResponseObtained,
	}
}

// NotObserved reports that the last observe obtained NO response body, so nothing was populated and the
// absence of a declared status field says nothing about the external resource.
//
// Not expressed as Ready=False: the resource may well exist and be fine, and the observe may have decided
// that from a status code alone. What is unknown is the CONTENT of its status, which is a different claim
// from the resource not being ready, and conflating them would make every bodyless observe look like a
// failed one.
func NotObserved(detail string) metav1.Condition {
	msg := "the observe obtained no response body, so declared status fields were not populated and their absence is not evidence about the external resource"
	if detail != "" {
		msg = msg + ": " + detail
	}
	return metav1.Condition{
		Type:               TypeObserved,
		Status:             metav1.ConditionFalse,
		LastTransitionTime: metav1.Now(),
		Reason:             ReasonNoResponseBody,
		Message:            msg,
	}
}
