package restResources

import (
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
	for _, existing := range unstructuredtools.GetConditions(mg) {
		if existing.Type != customcondition.TypeObserved {
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
