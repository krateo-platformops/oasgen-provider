package restResources

import (
	"fmt"

	customcondition "github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
	unstructuredtools "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// setObservedIfChanged applies the Observed condition only when it would actually change, and reports
// whether it did.
//
// Both halves of that matter, and neither is an optimisation.
//
// CORRECTED 2026-11-14. This comment used to say that condition.Upsert "replaces an existing entry
// WHOLESALE", so an unconditional set would rewrite lastTransitionTime on every reconcile. THAT WAS TRUE
// WHEN WRITTEN AND IS NOT TRUE OF ANY VERSION THIS MODULE HAS EVER SHIPPED AGAINST. Measured across the
// cached versions:
//
//	v1.7.1   overwrites wholesale   <- what the original claim described
//	v1.9.0   PRESERVES
//	v1.14.0  PRESERVES
//	v1.17.1  PRESERVES
//	v1.18.0  PRESERVES              <- what we ship
//
// unstructured-runtime's #40 added the guard, in v1.7.5 by core-provider's account; the boundary this
// repo can see is between v1.7.1 and v1.9.0. pkg/tools/unstructured/condition/condition.go:276 now reads:
//
//	if el.Status == co.Status && !el.LastTransitionTime.IsZero() {
//	    co.LastTransitionTime = el.LastTransitionTime   // not a transition: keep when it became true
//	}
//
// WHY THE STALE VERSION COST SOMETHING. The openstack-ironic-operator-kog session quoted this comment as
// evidence that core-provider's #498 stall clock could not work -- a reader cannot date a comment, and a
// claim about a dependency reads as current forever while the dependency moves underneath it. The
// refutation was one grep away in the module cache. See also the `default:` arm this file's package has
// had corrected for the same reason.
//
// WHAT IS STILL TRUE, and why this function stays. tools.UpdateStatus issues an UNCONDITIONAL API write:
// it does not diff. So a resource whose observe never returns a body would get a status API round trip on
// every single reconcile for as long as it exists. What that costs is now a wasted request rather than a
// real write -- with the timestamp preserved, the object the apiserver receives is identical, so
// resourceVersion does not move and no watcher is woken. The guard is a request-rate guard, not a
// correctness guard, and the returned bool is what the caller uses to skip the write.
//
// Status AND reason are compared, not status alone: the two states carry different reasons, and a future
// third reason on the same status would otherwise be swallowed. Note what that means NOW -- a
// reason-only change still sets, and Upsert keeps the old lastTransitionTime because the status did not
// transition. That is the correct outcome and it is worth stating, because the guard and Upsert key on
// different things and only their combination gets it right.
//
// Status AND reason are compared, not status alone: the two states carry different reasons, and a future
// third reason on the same status would otherwise be swallowed.
func setObservedIfChanged(mg *unstructured.Unstructured, want metav1.Condition) (bool, error) {
	if want.Type != customcondition.TypeObserved {
		// A caller that passed some other type would get the right behaviour from
		// setConditionIfChanged anyway, but silently: this keeps the name honest.
		return false, fmt.Errorf("setObservedIfChanged called with condition type %q", want.Type)
	}
	return setConditionIfChanged(mg, want)
}

// setConditionIfChanged applies `want` only when it would actually change the condition of that
// TYPE, and reports whether it did. It is the general form of the rule documented on
// setObservedIfChanged above, and every word of that rationale applies to any handler-owned
// condition type: the constructors stamp LastTransitionTime on every call, and tools.UpdateStatus
// does not diff, so setting unconditionally turns every reconcile into a real API write and makes
// lastTransitionTime mean "the last time we looked" instead of "the last time this changed".
//
// It keys on want.Type rather than on a constant, so a second handler-owned type cannot drift from
// this logic by copying it.
func setConditionIfChanged(mg *unstructured.Unstructured, want metav1.Condition) (bool, error) {
	for _, existing := range unstructuredtools.GetConditions(mg) {
		if existing.Type != want.Type {
			continue
		}
		if existing.Status == want.Status && existing.Reason == want.Reason {
			return false, nil
		}
		break
	}
	if err := unstructuredtools.SetConditions(mg, want); err != nil {
		return false, err
	}
	return true, nil
}
