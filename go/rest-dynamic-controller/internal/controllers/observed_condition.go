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
// condition.Upsert replaces an existing entry WHOLESALE, and the constructors stamp
// LastTransitionTime: metav1.Now() on every call. Setting unconditionally would therefore rewrite the
// timestamp on every reconcile, which breaks exactly the property this condition was chosen for: a
// metav1.Condition's lastTransitionTime is supposed to mark when the status last CHANGED, and a reader
// who sees it move every few seconds learns nothing from it. Comparing first makes it mean what it says.
//
// And tools.UpdateStatus issues an unconditional API write -- it does not diff. Since the stamped
// timestamp differs every call, an unguarded set would make every write a real one, so a resource whose
// observe never returns a body would get a status update on every single reconcile for as long as it
// exists. The caller uses the returned bool to skip the write when there is nothing to record.
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
