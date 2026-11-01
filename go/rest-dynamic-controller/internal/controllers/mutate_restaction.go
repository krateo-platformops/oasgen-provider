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
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/pathparsing"
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
	result, failedStages, err := h.snowplowClient.Resolve(ctx, snowplow.ApiRef{Name: ref.Name, Namespace: ref.Namespace}, extras)
	if err != nil {
		return fmt.Errorf("resolving %s RESTAction %s/%s: %w", action, ref.Namespace, ref.Name, err)
	}
	if writesDesiredState && len(result) > 0 {
		clearStatusFields(mg)
		if werr := writeObservedStatus(mg, result); werr != nil {
			return werr
		}
	}
	// snowplow answers 200 even when a stage failed, so without this the only record of a failed teardown or
	// create stage was discarded with the response headers (#292). Warn rather than error: the stage result
	// does not decide the outcome -- for delete, the verification GET governs, and for create the next
	// Observe does -- but an operator reading why a resource did not change needs to see it.
	if failedStages != "" {
		log.Warn("RESTAction reported one or more FAILED stages; the external resource may not have been changed",
			"action", action, "restAction", ref.Namespace+"/"+ref.Name,
			"kind", mg.GetKind(), "failedStages", failedStages)
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
func (h *handler) resolveDeleteOutcome(ctx context.Context, cs *clientSet, clientInfo *getter.Info, mg *unstructured.Unstructured, deleteErr error, log logging.Logger) error {
	stillExists, verified, verr := h.externalResourceStillExists(ctx, cs, clientInfo, mg, log)
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
		//
		// deleteErr is WRAPPED rather than dropped. This branch is the most informative combination
		// available -- the vendor refused the delete AND the get verb confirms the resource is still
		// there -- and it was the one that discarded the vendor's reason. A consumer watching six CRs
		// sit in Terminating, each retrying a DELETE their API refuses because the node is in an active
		// state, read only "still present; retrying": true, and useless for deciding what to do.
		//
		// Same defect as #334, in the one place #334 did not reach: the explanation was in hand and
		// thrown away rather than withheld. %w keeps the error's identity, so restclient.HasStatusErr
		// and IsNotFoundError still see through it.
		if deleteErr != nil {
			return fmt.Errorf("delete of %s did not remove the external resource, which is still present; retrying: %w", mg.GetName(), deleteErr)
		}
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
func (h *handler) externalResourceStillExists(ctx context.Context, cs *clientSet, clientInfo *getter.Info, mg *unstructured.Unstructured, log logging.Logger) (stillExists bool, verified bool, err error) {
	// The GET verb's OWN document, not the delete verb's. Taking the set rather than a client is what makes
	// that possible: this read runs an action its caller did not name (#220).
	cli, err := cs.For(ctx, string(apiaction.Get))
	if err != nil {
		return false, false, fmt.Errorf("building the client for the verification get: %w", err)
	}
	getCall, getInfo, err := builder.APICallBuilder(cli, clientInfo, apiaction.Get)
	if err != nil || getCall == nil || getInfo == nil {
		log.Debug("No get verb to verify deletion; trusting the delete result")
		return false, false, nil
	}
	getReq := builder.BuildCallConfig(ctx, getInfo, mg, clientInfo.ConfigurationSpec, nil)
	if getReq == nil {
		return false, false, nil
	}

	// An unresolved {placeholder} in the GET path means the external resource is NOT ADDRESSABLE -- almost
	// always because the create failed, so no identifier was ever written to status. Report it unverifiable
	// (the same as the three cases above) rather than attempting the call and returning its error (#261).
	//
	// This is the difference between a TRANSIENT and a PERMANENT inability to check, and the caller's rule
	// -- "could not check" means retry -- is correct only for the transient kind. An id that was never
	// recorded can never appear, so returning an error here holds the finalizer on every retry forever: the
	// object sits in Deleting with no route out but a human stripping the finalizer by hand. Measured at
	// eight hours on a CloudServer whose create was refused by the vendor, while a Vpc in the same state
	// deleted immediately -- because the NATIVE delete path has had this guard all along
	// (restResources.go), and this path never consulted it.
	//
	// Uses pathparsing.UnresolvedPathParams, the same helper the native guard uses, deliberately: the two paths
	// must agree on what "not addressable" means, and a shared helper makes that true by construction
	// rather than by two implementations staying in step. Matching on the error string instead would couple
	// this to a message and silently restore the wedge the next time it is reworded.
	if missing := pathparsing.UnresolvedPathParams(getInfo.Path, getReq.Parameters); len(missing) > 0 {
		log.Warn("Cannot verify deletion: the external resource is not addressable, so the delete result governs",
			"kind", mg.GetKind(), "path", getInfo.Path, "unresolvedPathParams", strings.Join(missing, ","))
		return false, false, nil
	}

	body, cerr := getCall(ctx, &http.Client{}, getInfo.Path, getReq)
	if restclient.IsNotFoundError(cerr) {
		return false, true, nil // verifiably gone
	}
	if cerr != nil {
		return false, false, fmt.Errorf("verifying deletion via get: %w", cerr)
	}

	// A 200 does not always mean "still there". Some APIs answer GET for a deleted resource with a
	// TOMBSTONE -- 200 plus a record carrying status: Deleted -- rather than 404, so treating the status
	// code alone as presence wedges the CR in Deleting forever (#111, observed on Aruba security/Kmip
	// whose own list endpoint reports zero, including with includeDeleted=true).
	//
	// notFoundBody already exists for exactly this and Observe honours it; the delete verification did
	// not, which is the same shape as every other bug on this path: two code paths that must agree, and
	// one was not updated. Consulting it here makes the observe verb's notion of absence a single
	// definition rather than two.
	// body.ResponseBody, not body: getCall is an APIFuncDef returning the restclient.Response WRAPPER, and
	// jqengine.Program.Run canonicalizes its output but not its input, so handing it the struct gives gojq
	// something it cannot index and the predicate errors every time (#218). The observe call site gets this
	// right; this one did not, which made the whole tombstone check dead code from the commit that added it.
	if prog := notFoundBodyForAction(clientInfo.Resource.VerbsDescription, string(apiaction.Get)); prog != nil {
		gone, perr := evalNotFoundBody(ctx, prog, body.ResponseBody)
		if perr != nil {
			// A mistyped predicate must not read as "still present" and hold the finalizer forever, nor
			// as "gone" and orphan the resource: report it unverifiable so the delete result governs.
			log.Warn("notFoundBody predicate failed while verifying deletion; treating as unverifiable", "error", perr.Error())
			return false, false, nil
		}
		if gone {
			log.Debug("Get returned 200 but notFoundBody reports the resource absent; treating as deleted")
			return false, true, nil
		}
	}

	return true, true, nil // get succeeded and no tombstone predicate matched: verifiably still present
}
