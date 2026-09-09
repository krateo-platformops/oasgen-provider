package restResources

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/builder"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/snowplow"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// mutateViaRestAction invokes a write-RESTAction (a create or delete sequence) via snowplow /call under the
// controller's identity, passing the resource's per-instance context — and, for create, the whole spec (the
// desired state) — as request extras. For create it clears stale status then projects any composed .status
// the RESTAction returns (e.g. a server-assigned identifier).
//
// It returns an error on a hard (non-2xx) snowplow failure. The controller does NOT verify per-CALL success
// WITHIN the sequence (snowplow may return 200 with a stage error), so:
//   - create relies on level-based convergence: it is re-invoked each reconcile until Observe reports the
//     resource exists, so the RESTAction MUST be idempotent AND the resource MUST have a get/findby observe
//     that can report non-existence (a createApiRef resource with neither converges to Available unverified).
//   - delete cannot rely on re-invocation (the finalizer is released once Delete returns nil), so the caller
//     VERIFIES the resource is actually gone (externalResourceStillExists) before releasing the finalizer.
func (h *handler) mutateViaRestAction(ctx context.Context, mg *unstructured.Unstructured, ref *getter.ApiRef, identifiers []string, action string, log logging.Logger) error {
	if h.snowplowClient == nil {
		return fmt.Errorf("resource declares %sApiRef %s/%s but no snowplow client is configured (set the snowplow/authn URLs)", action, ref.Namespace, ref.Name)
	}
	// writesDesiredState governs only whether the RESTAction's RESULT is projected into status: create and
	// update compose the observed resource, delete returns nothing to project. It deliberately no longer
	// governs what the RESTAction RECEIVES — buildExtras forwards the spec in every direction, because
	// locating a resource on a parent-scoped API needs spec fields that are not identifiers (issue #41).
	writesDesiredState := strings.EqualFold(action, "create") || strings.EqualFold(action, "update")
	extras := buildExtras(mg, ref.Extras, identifiers)
	result, err := h.snowplowClient.Resolve(ctx, snowplow.ApiRef{Name: ref.Name, Namespace: ref.Namespace}, extras)
	if err != nil {
		return fmt.Errorf("resolving %s RESTAction %s/%s: %w", action, ref.Namespace, ref.Name, err)
	}
	if writesDesiredState && len(result) > 0 {
		clearStatusFields(mg)
		if werr := writeObservedStatus(mg, result); werr != nil {
			return werr
		}
	}
	log.Debug("Mutated via RESTAction", "action", action, "restAction", ref.Namespace+"/"+ref.Name)
	return nil
}

// hasObserveVerb reports whether the resource declares a get or findby verb — the observe a createApiRef
// resource needs so level-based convergence can verify the create actually took effect.
func hasObserveVerb(verbs []getter.VerbsDescription) bool {
	for _, v := range verbs {
		if strings.EqualFold(v.Action, string(apiaction.Get)) || strings.EqualFold(v.Action, string(apiaction.FindBy)) {
			return true
		}
	}
	return false
}

// resolveDeleteOutcome is THE rule for whether a delete may release the finalizer. Every delete path --
// RESTAction-delegated and native, success and failure -- goes through here, so the decision exists in
// one place instead of being re-derived at each exit.
//
// That scattering was the defect behind three separate bugs (#77, #98, #101): the authoritative check
// sat at the END of Delete(), so any branch returning earlier decided without it, and each fix moved
// the check one branch earlier rather than making it the sole arbiter (#103).
//
// The rule, in priority order:
//
//	verifiably absent   -> release. Whatever the delete call said; a 404, a 400, or a 2xx are all just
//	                       proxies for this, and the observe verb is the ground truth.
//	verifiably present  -> hold and retry. A 2xx means the deletion was REQUESTED, not completed.
//	not verifiable      -> defer to deleteErr. No get verb, or the get itself failed. Absence cannot be
//	                       established, so "could not check" must mean retry, NEVER "assume gone" --
//	                       assuming would orphan the external resource, the failure #77 exists to
//	                       prevent. A successful delete is still trusted here, the documented limitation.
//
// Returns nil to release the finalizer, or the error to surface while holding it.
func (h *handler) resolveDeleteOutcome(ctx context.Context, cli restclient.UnstructuredClientInterface, clientInfo *getter.Info, mg *unstructured.Unstructured, deleteErr error, log logging.Logger) error {
	stillExists, verified, verr := h.externalResourceStillExists(ctx, cli, clientInfo, mg, log)
	if verr != nil {
		// The probe itself failed: we know nothing, so retry rather than guess.
		log.Error(verr, "Verifying deletion")
		return verr
	}

	if verified {
		if !stillExists {
			if deleteErr != nil {
				log.Info("Delete returned an error but the external resource is verifiably absent; releasing finalizer",
					"deleteError", deleteErr.Error())
			}
			return nil
		}
		log.Debug("External resource still present after delete; holding finalizer")
		// Phrasing is load-bearing: DeleteHoldsFinalizerWhileResourceLingers asserts on "still present"
		// so it cannot pass on an unrelated error, which is how an earlier delete test passed vacuously.
		return fmt.Errorf("delete of %s did not remove the external resource, which is still present; retrying", mg.GetName())
	}

	// Unverifiable: the delete result is the only signal there is.
	if deleteErr != nil {
		log.Debug("Delete failed and deletion cannot be verified; holding finalizer", "kind", mg.GetKind())
		return deleteErr
	}
	log.Debug("No get verb to verify deletion; trusting the successful delete")
	return nil
}

// externalResourceStillExists probes whether the external resource is still present via the get verb, so a
// RESTAction-delegated delete is only considered complete when the resource is verifiably gone — a RESTAction
// returns HTTP 200 even if a teardown stage failed, so a bare success is not proof of deletion. It returns
// (false, nil) when there is no usable get verb to check with, in which case the caller trusts the delete
// result (a documented limitation for resources with no get observe).
// It returns (stillExists, verified, err). `verified` distinguishes "the get verb answered and the
// resource is absent" from "there was no way to check" -- both of which previously returned
// (false, nil) and were indistinguishable to callers.
//
// That conflation is safe where a successful delete is being confirmed: no get verb means trusting
// the delete result, a documented limitation. It is NOT safe when the delete itself FAILED, where
// "could not check" must mean "retry", not "assume gone" -- otherwise any delete error on a resource
// without a get verb would release the finalizer and orphan the resource, which is the bug #77 exists
// to prevent (#101).
func (h *handler) externalResourceStillExists(ctx context.Context, cli restclient.UnstructuredClientInterface, clientInfo *getter.Info, mg *unstructured.Unstructured, log logging.Logger) (stillExists bool, verified bool, err error) {
	getCall, getInfo, err := builder.APICallBuilder(cli, clientInfo, apiaction.Get)
	if err != nil || getCall == nil || getInfo == nil {
		log.Debug("No get verb to verify deletion; trusting the delete result")
		return false, false, nil
	}
	getReq := builder.BuildCallConfig(getInfo, mg, clientInfo.ConfigurationSpec, nil)
	if getReq == nil {
		return false, false, nil
	}
	_, cerr := getCall(ctx, &http.Client{}, getInfo.Path, getReq)
	if restclient.IsNotFoundError(cerr) {
		return false, true, nil // verifiably gone
	}
	if cerr != nil {
		return false, false, fmt.Errorf("verifying deletion via get: %w", cerr)
	}
	return true, true, nil // get succeeded: verifiably still present
}
