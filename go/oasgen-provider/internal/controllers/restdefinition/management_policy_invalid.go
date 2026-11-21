package restdefinition

import (
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	rtv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
	"github.com/krateo-platformops/provider-runtime/pkg/meta"
)

// ReasonManagementPolicyInvalid marks a RestDefinition whose krateo.io/management-policy annotation
// is set to something no policy recognises.
//
// THE STRING IS DELIBERATELY THE SAME ONE core-provider USES for the same fault on generated kinds
// (their #117, unstructured-runtime v1.31.0). The annotation key is shared between the two runtimes,
// so one symptom gets one name -- an operator grepping for ManagementPolicyInvalid finds it whether
// the misspelling is on a RestDefinition or on one of its instances.
const ReasonManagementPolicyInvalid rtv1.ConditionReason = "ManagementPolicyInvalid"

// TypeManagementPolicyValid is the condition TYPE this is reported on, and it is deliberately NOT
// rtv1.TypeSynced.
//
// MEASURED IN provider-runtime v1.8.2, which is what this module pins: the reconciler sets
// Synced=TRUE on the very path an unrecognised policy takes --
//
//	reconciler.go ~1278
//	    // skip the update if the management policy is set to ignore updates
//	    if !meta.ShouldUpdate(managed) {
//	        managed.SetConditions(prv1.ReconcileSuccess())   // TypeSynced / ConditionTrue
//	        return reconcile.Result{RequeueAfter: reconcileAfter}, nil
//	    }
//
// ReconcileSuccess() is Synced=True, and it is written AFTER a provider's Observe in the same
// reconcile. A Synced=False condition set here would be overwritten every pass -- not fragile,
// overwritten by design. There are five ReconcileSuccess() sites in that file and this is the one
// reached when the policy denies the action.
//
// WHAT SUPERSEDES THIS. provider-runtime reports the same fault on Synced from v1.21.0 (their #115
// plus a writeStatus that rewrites a Synced ReconcileSuccess before writing), matching
// unstructured-runtime v1.31.0's #117 for generated kinds. When this module's provider-runtime pin
// reaches that version, DELETE this file and its tests: the runtime's condition is the one that
// should survive, because the annotation key is shared and one symptom should have one name.
//
// At the time of writing that is not close for THIS module: #115 is open, release/v1.21 does not
// exist, the latest tag is v1.20.1, and this module pins v1.8.2 -- twelve minors behind the tag that
// would carry it. The silent window is real in the meantime, which is why this ships now.
const TypeManagementPolicyValid rtv1.ConditionType = "ManagementPolicyValid"

// maxReportedPolicyLength bounds what reaches status. An annotation value is user-controlled and may
// be very large; a status message is read by humans and stored on every RestDefinition, so a runaway
// value must not be copied into it wholesale.
const maxReportedPolicyLength = 64

// knownManagementPolicies is the complete set provider-runtime v1.8.2 accepts, read from its
// constants rather than retyped -- see pkg/meta/meta.go. A value outside this set makes
// meta.IsActionAllowed return FALSE FOR EVERY ACTION: create, update and delete alike.
func knownManagementPolicies() []string {
	return []string{
		meta.ManagementPolicyDefault,
		meta.ManagementPolicyObserve,
		meta.ManagementPolicyObserveCreateUpdate,
		meta.ManagementPolicyObserveDelete,
	}
}

// managementPolicyIsUnrecognised reports whether the annotation is SET to a value no policy knows,
// and returns that value.
//
// AN ABSENT ANNOTATION IS NOT A FAULT: provider-runtime substitutes the default policy, which is
// full management, and that is the overwhelmingly common case. Only a value that is present and
// unrecognised is reported -- otherwise every RestDefinition in existence would carry the condition.
func managementPolicyIsUnrecognised(cr interface {
	GetAnnotations() map[string]string
}) (string, bool) {
	v, ok := cr.GetAnnotations()[meta.AnnotationKeyManagementPolicy]
	if !ok || v == "" {
		return "", false
	}
	for _, known := range knownManagementPolicies() {
		if v == known {
			return "", false
		}
	}
	return v, true
}

// managementPolicyInvalidCondition is the condition for that state.
//
// WHY A CONDITION AND NOT A LOG LINE. meta.IsActionAllowed returns false for create, update AND
// delete when the value is unrecognised, and each of the three call sites in this controller then
// logs at Debug and `return nil`. A nil return reads as SUCCESS to every layer above, so the
// RestDefinition reports healthy while managing nothing at all -- and the log line it does emit,
// "External resource should not be created by provider", is WORD FOR WORD the line a DELIBERATE
// `observe` policy produces. Nothing in the object distinguishes "the operator asked me not to act"
// from "the operator misspelled `default`".
//
// AND NOTHING CATCHES IT EARLIER: krateo.io/management-policy is an ANNOTATION, not a field in this
// CRD, so there is no schema to validate against and no admission refusal. Measured: zero
// occurrences of "managementPolicy" in the shipped RestDefinition CRD. This condition is the only
// signal that exists.
func managementPolicyInvalidCondition(value string) rtv1.Condition {
	reported := value
	if len(reported) > maxReportedPolicyLength {
		reported = reported[:maxReportedPolicyLength] + "..."
	}

	accepted := knownManagementPolicies()
	sort.Strings(accepted)

	return rtv1.Condition{
		Type:               TypeManagementPolicyValid,
		Status:             metav1.ConditionFalse,
		LastTransitionTime: metav1.Now(),
		Reason:             ReasonManagementPolicyInvalid,
		Message: fmt.Sprintf(
			"the %s annotation is %q, which is not one of %s -- NOTHING is being managed: "+
				"create, update and delete are all refused while this value is set",
			meta.AnnotationKeyManagementPolicy,
			reported,
			strings.Join(accepted, ", "),
		),
	}
}
