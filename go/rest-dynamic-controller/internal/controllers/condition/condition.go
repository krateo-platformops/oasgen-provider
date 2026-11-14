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
	// ReasonCreateUnreachable is set when a resource's create is its ONLY write verb and its findby
	// matches, so the runtime can never queue that create -- it queues one only on !ResourceExists.
	// The resource's single effect cannot fire, and no reconcile will change that.
	//
	// IT REQUIRES THAT NO CREATE HAS EVER SUCCEEDED, which is what separates it from the healthy
	// terminal state. After a successful create the external object exists, so findby matches
	// forever -- correctly. Keying on a matched findby alone would report every provisioned resource
	// as broken (#629, and the false-positive family of #625).
	ReasonCreateUnreachable string = "CreateUnreachable"

	// ReasonAuthorizationDenied is set when the API answered 403 to a verb and the response carried no
	// rate-limit signal, so the refusal is a decision about this credential rather than a limit that
	// clears. The controller stops issuing that verb; the resource stays NOT ready.
	//
	// Distinct from a 401, which is deliberately NOT in this family: a 401 is what a credential being
	// rotated under us looks like, and retrying it is how rotation is tolerated (#313).
	ReasonAuthorizationDenied string = "AuthorizationDenied"
	// ReasonCreateFailed and ReasonUpdateFailed say the verb was attempted and failed in a way that
	// retrying cannot change -- today, an endpoint the host policy refuses, which no reconcile will
	// permit until an operator edits the allowlist or the document.
	//
	// THEY EXIST BECAUSE "Creating" READS AS PROGRESS. The runtime sets Creating on both create outcomes
	// and separates them only on Synced, so a permanently stuck resource reported Ready=False/Creating --
	// and a composition rollup, a portal pill and an operator all read that as "still working on it".
	// The distinction was living in the MESSAGE, which consumers cannot branch on because it is ours to
	// reword freely; a reason is a contract and a message is prose (#42, requested by the portal).
	//
	// Named so they cannot be read as transient: "CreateError" would imply a retry is coming, which is
	// the one thing that is not true here. Mapped to childFailed by core-provider#323.
	//
	// There is deliberately NO DeleteFailed. A refused delete holds its finalizer (#261) and a
	// terminal-looking Ready reason there invites the finalizer removal that orphans a resource which
	// still exists and still costs money.
	ReasonCreateFailed string = "CreateFailed"
	ReasonUpdateFailed string = "UpdateFailed"
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

// PendingReferent returns the SAME condition as Pending, with a message naming what the resource is
// waiting for.
//
// Identical Type, Status and Reason, deliberately. `IsConditionSet` compares those three and NOT the
// message, so the `notfound && IsConditionSet(mg, Pending())` guard in Observe keeps matching and no
// control flow changes -- this adds information to the CR and nothing else. A distinct Reason would stop
// that guard matching and silently turn a waiting resource into one reported as not existing, which is a
// behaviour change masquerading as a diagnostic improvement.
//
// It exists because `Pending` alone cannot be acted on (#323). Four code paths set it, and two of them are
// not async at all: a dependentLookup whose referent is missing sets Pending with NO operation handle, so
// nothing in the async machinery will ever move it off. If that referent never appears -- a typo in a
// lookup, a CR nobody created -- the resource waits forever, correctly reporting Ready=False/Pending, and
// an operator cannot tell it apart from an async create that is thirty seconds in and about to succeed.
//
// The detail is already structured where it is produced: errLookupReferentMissing names the lookup and the
// value it was matching. It reached only a Debug log, which is off by default.
func PendingReferent(detail string) metav1.Condition {
	c := Pending()
	c.Message = detail
	return c
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

// CreateUnreachable returns a condition indicating that this resource's create is its only write verb
// and can never be queued, so its one effect will never fire.
//
// Deliberately NOT Ready, and deliberately not an error, matching DriftNotConverging: nothing failed
// and nothing is retryable. The runtime queues a Create only on !ResourceExists, so a findby that
// matches closes the only door. The remedy is a declaration change -- identifiers the findby response
// does not carry, or expressing the action as a dispatch verb -- which is a human's decision.
//
// WHY THIS CONDITION EXISTS AT ALL, rather than just the event that accompanies it. The measured
// symptom (#629) is an INVERTED health signal: the stuck resource reported Ready=True/Synced while a
// working one reported Ready=False/Pending, so triaging by CR health picked the broken one. An event
// does not appear in `kubectl get`, so an event alone would leave that table unchanged -- it would
// close the issue on the half it did not complain about.
//
// Note both now read Ready=False, for different reasons; the REASON is what distinguishes a stuck
// resource from one with a create in flight. That is the deliberate cost of making the stuck one
// visible at all.
func CreateUnreachable(detail string) metav1.Condition {
	return metav1.Condition{
		Type:               condition.TypeReady,
		Status:             metav1.ConditionFalse,
		LastTransitionTime: metav1.Now(),
		Reason:             ReasonCreateUnreachable,
		Message:            detail,
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
// CreateFailed and UpdateFailed report a verb that failed permanently, carrying the cause in the message
// while the REASON carries the only distinction a consumer can key on: this is not converging.
//
// The detail goes in the message rather than into more reasons on purpose. A refused endpoint, a malformed
// request and a rejected payload are all "attempted, failed, will not self-correct" to every consumer of
// the rollup, and splitting them would multiply a cross-repo contract to say something only a human reads.
func CreateFailed(detail string) metav1.Condition {
	return permanentVerbFailure(ReasonCreateFailed, "create", detail)
}

func UpdateFailed(detail string) metav1.Condition {
	return permanentVerbFailure(ReasonUpdateFailed, "update", detail)
}

func permanentVerbFailure(reason, verb, detail string) metav1.Condition {
	msg := "the " + verb + " request will not be retried because it cannot succeed as declared"
	if detail != "" {
		msg = msg + ": " + detail
	}
	return metav1.Condition{
		Type:               condition.TypeReady,
		Status:             metav1.ConditionFalse,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            msg,
	}
}

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

// TypeResolverDigestRecorded reports whether this controller can actually RECORD the secretRef
// resolver digest it uses to detect a rotated Secret (#598) -- which is a different question from
// whether the resource is Ready, and must not be answered on TypeReady.
//
// WHY IT NEEDS A TYPE OF ITS OWN. SetConditions upserts by Type and replaces the entry wholesale, so
// anything written on TypeReady here would fight the one place Ready is decided on the observe path
// and whichever ran last would win. That is the defect #637 shipped: a condition set on a path the
// runtime later overwrites is inert, and inert in a way no test on the condition's constructor can
// see. A resource can be perfectly Ready and still have an undetectable rotation, so the two facts
// need two channels.
//
// IT SURVIVES EXACTLY WHERE THE DIGEST FIELD DOES NOT, which is the whole reason a condition is the
// carrier. Measured on a rendered CRD: status is a closed schema whose properties are
// [conditions, html_url, id, name, observedGeneration] with no preserve-unknown-fields, so an
// undeclared digest field is pruned -- but `conditions.items.properties.type` is a plain string with
// no enum, so a NEW condition type is storable on that same un-regenerated CRD. The report therefore
// gets through on precisely the clusters where the thing it is reporting about is broken.
const TypeResolverDigestRecorded string = "ResolverDigestRecorded"

// The two states of TypeResolverDigestRecorded.
const (
	ReasonResolverDigestRecorded    string = "DigestRecorded"
	ReasonResolverDigestFieldAbsent string = "DigestFieldAbsent"
)

// ResolverDigestRecorded says the digest was written and read back from the live object, so a
// rotation of a referenced Secret will be detected.
func ResolverDigestRecorded() metav1.Condition {
	return metav1.Condition{
		Type:               TypeResolverDigestRecorded,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Reason:             ReasonResolverDigestRecorded,
		Message: "the secretRef resolver digest is recorded on this resource, so a change to a " +
			"referenced Secret will be detected and pushed on the next reconcile",
	}
}

// ResolverDigestFieldAbsent says the digest was written and came back absent, so the apiserver
// pruned it and rotation detection cannot work for this resource.
//
// LOUD RATHER THAN SILENT IS THE ENTIRE POINT. A pruned write exits 0 and returns no error, so
// without this the feature would be exactly as inert as the defect it was built to fix, with nothing
// anywhere saying so. It does NOT make the resource un-Ready: the resource may be in perfect shape
// and this says only that one detector is unavailable for it.
//
// Not retryable, and deliberately not phrased as transient: the remedy is to regenerate the CRD,
// which is an operator's action.
func ResolverDigestFieldAbsent() metav1.Condition {
	return metav1.Condition{
		Type:               TypeResolverDigestRecorded,
		Status:             metav1.ConditionFalse,
		LastTransitionTime: metav1.Now(),
		Reason:             ReasonResolverDigestFieldAbsent,
		Message: "this resource's CRD does not declare status." + "resolvedResolversDigest" + ", so the " +
			"apiserver discarded it. A change to a referenced Secret will NOT be detected for this " +
			"resource until its CRD is regenerated -- re-apply its RestDefinition, or upgrade the " +
			"provider that generated it, so the field is declared",
	}
}

// ReasonNoCreateVerbDeclared is set when the runtime asked for a create and the definition declares
// no create action at all -- no create verb, no dispatch, no createApiRef.
//
// REMOVED IN 0.65.0 (#645) AND REVIVED HERE, and the round trip is the useful part of its history.
//
// It was written on TypeReady inside Create, persisted, and then replaced by the runtime's
// create-failure branch in the same reconcile -- so it existed for under a second and never reached a
// settled CR. #645 removed it on the grounds that a write which cannot win costs a status write per
// reconcile to produce a transient nobody can key on, and a reason whose visibility depends on poll
// timing is worse for an alert rule than no reason at all.
//
// What changed is not the condition but who owns the write. Under unstructured-runtime's
// ResultCreator (v1.17.0), a handler returning CreateResult{Requested: false} with a nil error has
// its conditions UPSERT and KEPT: setCreateConditions stamps the default Synced=ReconcileSuccess only
// when the handler returns no TypeSynced of its own. So the reason survives, and #645's argument no
// longer applies.
//
// THE COST #645 DID NOT ACCOUNT FOR, which is why reviving it matters rather than merely being
// possible: with no specific reason, such a resource settles at Ready=False/Creating, and
// core-provider's rollup classifies `Creating` as childConverging -- so a composition containing a
// kind that can never be created reports Ready=False/Creating FOREVER while nothing progresses
// (#660). That hole is not closable in the rollup, because `Creating` is genuinely ambiguous between
// a create in flight and a create that is impossible, and no arm can separate what the reason does
// not distinguish. Only a specific reason from this side closes it. core-provider's arm mapping this
// exact string to childFailed is merged ahead of its arrival.
//
// DISTINCT FROM ReasonCreateUnreachable on purpose, and both are mapped. There the create exists and
// cannot be queued, and the remedy is to change the identifiers or use a dispatch verb. Here there is
// nothing to queue, and the remedy is to declare a create verb or accept that the Kind only observes.
// Folding them together would give an operator one reason and two incompatible fixes.
const ReasonNoCreateVerbDeclared string = "NoCreateVerbDeclared"

// NoCreateVerbDeclaredConditions returns BOTH conditions a refusal must carry, for a handler
// returning CreateResult{Requested: false}.
//
// TWO, NOT ONE, and the Synced half is deliberate. The runtime stamps Synced=ReconcileSuccess when a
// handler returns no TypeSynced -- which is correct for a WAIT (the reconcile did its job and the
// resource may yet exist) and wrong for a REFUSAL, where reporting a successful sync on a resource
// that can never exist tells a reader the opposite of the truth. So a refusal returns its own Synced
// and the runtime leaves it.
//
// Not Ready-only, and not an error either: an error return would take the runtime's error path, which
// is what used to overwrite the reason, and would also reintroduce a rate-limited retry of something
// no retry can change.
func NoCreateVerbDeclaredConditions() []metav1.Condition {
	const msg = "the runtime asked for this resource to be created, but its definition declares no " +
		"create action: no create verb, no dispatch verb and no createApiRef. Nothing will be " +
		"created for it. If the vendor exposes a creation endpoint, declare it in " +
		"spec.resource.verbsDescription; if it does not, this Kind can only observe"
	now := metav1.Now()
	return []metav1.Condition{
		{
			Type:               condition.TypeReady,
			Status:             metav1.ConditionFalse,
			LastTransitionTime: now,
			Reason:             ReasonNoCreateVerbDeclared,
			Message:            msg,
		},
		{
			Type:               condition.TypeSynced,
			Status:             metav1.ConditionFalse,
			LastTransitionTime: now,
			Reason:             ReasonNoCreateVerbDeclared,
			Message:            msg,
		},
	}
}
