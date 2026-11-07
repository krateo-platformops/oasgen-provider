package restResources

import (
	"context"
	"fmt"
	"strconv"

	"github.com/krateo-platformops/plumbing/kubeutil/event"
	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A SECOND destination for a synthesised not-found, and the worse one (#485).
//
// Observe reaches "not found" from a findby that listed successfully and matched nothing, and that verdict
// has two destinations:
//
//	not Pending -> a create is attempted. reportInferredAbsence warns once per observe; the next reconcile
//	               sees the created resource and the matter resolves itself either way.
//	    Pending -> the latch at restResources.go:456 returns ResourceExists AND ResourceUpToDate without
//	               issuing anything further, so NO create is attempted and the resource is reported
//	               converged. Indefinitely, if the condition never clears.
//
// The second is what #485 exists to surface and the one its first implementation could not reach, because
// the report sits in the `else if`. A consumer measured the shape it produces: findby issued and SUCCEEDED
// (200, a full list), no item carried the identifier because no responseTransform existed, the listing was
// therefore unmatched, and with Pending set the latch reported exists-and-up-to-date for 318 CONSECUTIVE
// observes with 0 creates. Nothing failed anywhere -- which is precisely why nobody looked.
//
// Reporting on that path unconditionally is wrong: during a genuine async create, Pending plus a
// not-yet-visible resource is the NORMAL state, and an event per poll would be noise on every create.
// The latch cannot tell a create in flight from one that will never land -- but the two differ in whether
// they STOP, so a consecutive-repeat counter separates them where a single observation cannot.
const (
	// annotationLatchedAbsenceSignature identifies the latched state, so "the same latch again" can be told
	// from a fresh one. It holds the generation only -- there is no field or value to key on here, and a
	// generation bump is exactly "the author changed something in response", which must re-enable the count.
	annotationLatchedAbsenceSignature = "krateo.io/latched-absence-signature"
	// annotationLatchedAbsenceRepeats counts consecutive observes that took the latch with a synthesised
	// not-found. It stops being written once the report has fired -- see reportLatchedInferredAbsence.
	annotationLatchedAbsenceRepeats = "krateo.io/latched-absence-repeats"
	// maxLatchedAbsenceRepeats is how many consecutive latched observes are allowed before the report fires.
	//
	// Matches maxDriftRepeats, and for the same reason: it is a deadline for concluding "this is not going
	// to settle", not a retry budget. At the default 3m poll it is 15 minutes, which is long enough for an
	// async create to complete and far short of the days a genuinely stuck latch will sit for -- the
	// measured case held for 318 observes, so any threshold in this range catches it.
	maxLatchedAbsenceRepeats = 5
)

// latchedAbsenceSignature identifies the latched state for the purpose of "is this the same one again".
func latchedAbsenceSignature(mg *unstructured.Unstructured) string {
	if mg == nil {
		return ""
	}
	return fmt.Sprintf("gen=%d;latched-absence", mg.GetGeneration())
}

// latchedAbsenceRepeats reads the consecutive-latch count for sig, returning 0 when the recorded signature
// is a different one or when the value is unusable. Anything unparsable reads as 0 rather than failing a
// reconcile: this is an instrument, and a broken instrument must not wedge the controller.
func latchedAbsenceRepeats(mg *unstructured.Unstructured, sig string) int {
	if mg == nil || sig == "" {
		return 0
	}
	ann := mg.GetAnnotations()
	if ann[annotationLatchedAbsenceSignature] != sig {
		return 0
	}
	n, err := strconv.Atoi(ann[annotationLatchedAbsenceRepeats])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// reportLatchedInferredAbsence counts consecutive latched observes and emits ONE warning when the count
// reaches maxLatchedAbsenceRepeats. It reports whether the warning fired on this call.
//
// It deliberately stops writing once the report has fired. recordDriftRepeat patches unconditionally,
// including past its own threshold, which is an accepted cost there because a non-converging update
// eventually reaches a human. It is NOT acceptable here: a latched resource may sit for days, so an
// instrument that patched every poll would be writing to the stuck resource it is complaining about,
// forever. Precedent is an argument for accepting a cost already paid, not for choosing to add one.
//
// A counter must live in annotations rather than status: the latch returns before any status write on this
// path (the nearest preceding one is in the Get path, unreachable from here), so a status-backed count
// would never persist on the very path it exists for. patchAnnotations refreshes mg in place, so the next
// observe reads the incremented value rather than a stale copy.
func (h *handler) reportLatchedInferredAbsence(ctx context.Context, mg *unstructured.Unstructured, err error, log logging.Logger) bool {
	if !restclient.IsSynthesisedNotFound(err) {
		return false
	}
	sig := latchedAbsenceSignature(mg)
	n := latchedAbsenceRepeats(mg, sig)
	if n >= maxLatchedAbsenceRepeats {
		// Already reported for this generation. Do not patch again, and do not re-emit: one event per
		// latch, not one per poll.
		return false
	}
	n++
	if perr := h.patchAnnotations(ctx, mg, map[string]interface{}{
		annotationLatchedAbsenceSignature: sig,
		annotationLatchedAbsenceRepeats:   strconv.Itoa(n),
	}); perr != nil {
		// A failed count must not fail the reconcile: the latch's own verdict is unaffected, and losing
		// the instrument is strictly better than wedging the resource it watches.
		log.Debug("Could not record the latched-absence count", "error", perr)
		return false
	}
	if n < maxLatchedAbsenceRepeats {
		return false
	}
	h.eventRecorder.Event(mg, event.Warning(reasonAbsenceLatched, "Observe", fmt.Errorf(
		"%s has been reported up-to-date for %d consecutive checks WITHOUT being observed: its findby "+
			"listed successfully and no item matched, and because a create is pending the controller "+
			"concluded exists-and-up-to-date without issuing a create. If the create did complete, the "+
			"findby cannot see it -- most often because the response carries no field matching the "+
			"declared identifiers (check responseTransform and spec.resource.identifiers). Nothing is "+
			"failing, so this will not appear as an error",
		mg.GetName(), n)))
	log.Info("A pending create has been reported up-to-date without being observed for the whole budget; "+
		"the findby is listing successfully and matching nothing",
		"kind", mg.GetKind(), "name", mg.GetName(), "checks", n)
	return true
}

// clearLatchedAbsence removes the bookkeeping once an observe does NOT take the latch, so a resource whose
// create completes normally starts from zero if it is ever latched again. A no-op when nothing is recorded,
// which is the common case and must not cost a write per reconcile.
func (h *handler) clearLatchedAbsence(ctx context.Context, mg *unstructured.Unstructured) error {
	if mg == nil {
		return nil
	}
	ann := mg.GetAnnotations()
	if ann[annotationLatchedAbsenceSignature] == "" && ann[annotationLatchedAbsenceRepeats] == "" {
		return nil
	}
	return h.patchAnnotations(ctx, mg, map[string]interface{}{
		annotationLatchedAbsenceSignature: nil,
		annotationLatchedAbsenceRepeats:   nil,
	})
}
