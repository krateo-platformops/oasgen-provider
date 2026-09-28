package restResources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/krateo-platformops/plumbing/kubeutil/event"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/async"
	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/builder"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/fieldmapping"
	"github.com/krateo-platformops/unstructured-runtime/pkg/controller"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/krateo-platformops/unstructured-runtime/pkg/tools"
	unstructuredtools "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured"
	"github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// Annotations that persist an in-flight requeue-based (Model B) async operation across reconciles, so it can
// be polled to completion without re-triggering the mutating verb.
//
// NOTE (idempotency): the operation is triggered by the mutating verb BEFORE these annotations are persisted,
// so — exactly as for the blocking Model A driver — the trigger must be idempotent. If the controller
// restarts (or the metadata write fails non-transiently) in the narrow window between the trigger returning
// and the handle being recorded, the verb may re-run. Transient conflicts on the write are retried.
const (
	annotationAsyncOperationID     = "krateo.io/async-operation-id"
	annotationAsyncOperationAction = "krateo.io/async-operation-action"
	// annotationAsyncOperationParams stores the trigger call's resolved path params and query (JSON) so the
	// poll — issued in a later reconcile where the trigger request no longer exists — re-uses the exact same
	// shared inputs (e.g. {organization}, ?api-version) rather than reconstructing them from the get verb,
	// which may resolve different params. Auth headers/cookies are deliberately NOT stored; the client
	// re-applies auth at poll time.
	annotationAsyncOperationParams = "krateo.io/async-operation-params"
)

const reasonAsyncFailed event.Reason = "AsyncOperationFailed"

// pollInputs is the subset of a trigger RequestConfiguration the poll needs to reproduce (no secrets).
type pollInputs struct {
	Parameters map[string]string `json:"parameters,omitempty"`
	Query      map[string]string `json:"query,omitempty"`
}

// asyncRequeueEnabled reports whether the async config selects the requeue-based driver (Model B).
func asyncRequeueEnabled(cfg *getter.AsyncConfig) bool {
	return cfg != nil && strings.EqualFold(cfg.Mode, "requeue")
}

// asyncOperationInFlight returns the recorded operation handle and originating action for a requeue-based
// operation awaiting completion, or ("","") if none is recorded.
func asyncOperationInFlight(mg *unstructured.Unstructured) (operationID, action string) {
	ann := mg.GetAnnotations()
	if ann == nil {
		return "", ""
	}
	return ann[annotationAsyncOperationID], ann[annotationAsyncOperationAction]
}

// patchAnnotations applies a JSON merge patch to the object's annotations, refreshing mg in place. A merge
// patch carries no resourceVersion, so it never conflicts with a concurrent writer (e.g. another controller
// adding a finalizer between the trigger firing and the handle being recorded) — set a key to a string to
// add/replace it, or to nil to remove it, leaving all other annotations untouched.
func (h *handler) patchAnnotations(ctx context.Context, mg *unstructured.Unstructured, annotations map[string]interface{}) error {
	gvr, err := h.pluralizer.GVKtoGVR(mg.GroupVersionKind())
	if err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]interface{}{"metadata": map[string]interface{}{"annotations": annotations}})
	if err != nil {
		return err
	}
	updated, err := h.dynamicClient.Resource(gvr).Namespace(mg.GetNamespace()).
		Patch(ctx, mg.GetName(), types.MergePatchType, raw, metav1.PatchOptions{})
	if err != nil {
		return err
	}
	*mg = *updated
	return nil
}

// recordAsyncOperation extracts the operation handle from the trigger response and persists it (with the
// originating action and the trigger's resolved poll inputs) as annotations, so subsequent reconciles poll
// the operation to completion without re-triggering it (Model B). It updates metadata in place.
func (h *handler) recordAsyncOperation(ctx context.Context, mg *unstructured.Unstructured, cfg *getter.AsyncConfig, action string, triggerResp restclient.Response, triggerReq *restclient.RequestConfiguration, log logging.Logger) error {
	body, _ := triggerResp.ResponseBody.(map[string]interface{})
	if strings.EqualFold(cfg.OperationRef.In, "body") && body == nil {
		return fmt.Errorf("async(requeue) %s trigger returned no object body to extract the operation handle from", action)
	}
	opID, err := async.ExtractOperationHandle(ctx, body, triggerResp.Headers, cfg.OperationRef)
	if err != nil {
		return fmt.Errorf("recording async operation handle: %w", err)
	}
	patch := map[string]interface{}{
		annotationAsyncOperationID:     opID,
		annotationAsyncOperationAction: action,
	}
	if triggerReq != nil {
		if raw, merr := json.Marshal(pollInputs{Parameters: triggerReq.Parameters, Query: triggerReq.Query}); merr == nil {
			patch[annotationAsyncOperationParams] = string(raw)
		}
	}
	if err := h.patchAnnotations(ctx, mg, patch); err != nil {
		return fmt.Errorf("persisting async operation handle: %w", err)
	}
	log.Debug("Recorded async operation for requeue-based polling", "operationId", opID, "action", action)
	return nil
}

// clearAsyncOperation removes the in-flight async-operation annotations (via a merge patch that sets them to
// null), updating mg in place. It is a no-op when no operation is recorded.
func (h *handler) clearAsyncOperation(ctx context.Context, mg *unstructured.Unstructured) error {
	ann := mg.GetAnnotations()
	if ann == nil {
		return nil
	}
	if _, ok := ann[annotationAsyncOperationID]; !ok {
		return nil
	}
	return h.patchAnnotations(ctx, mg, map[string]interface{}{
		annotationAsyncOperationID:     nil,
		annotationAsyncOperationAction: nil,
		annotationAsyncOperationParams: nil,
	})
}

// pollBaseFromRecord reconstructs the poll's shared inputs: the trigger's persisted params/query if present
// (faithful), otherwise the get verb's resolved config (best-effort fallback), otherwise nil.
func pollBaseFromRecord(ctx context.Context, cli restclient.UnstructuredClientInterface, clientInfo *getter.Info, mg *unstructured.Unstructured) *restclient.RequestConfiguration {
	if ann := mg.GetAnnotations(); ann != nil {
		if raw, ok := ann[annotationAsyncOperationParams]; ok && raw != "" {
			var in pollInputs
			if err := json.Unmarshal([]byte(raw), &in); err == nil {
				return &restclient.RequestConfiguration{Parameters: in.Parameters, Query: in.Query}
			}
		}
	}
	if getCall, getInfo, gerr := builder.APICallBuilder(cli, clientInfo, apiaction.Get); gerr == nil && getCall != nil && getInfo != nil {
		return builder.BuildCallConfig(ctx, getInfo, mg, clientInfo.ConfigurationSpec, nil)
	}
	return nil
}

// pollAsyncOnce issues a single poll of the operations endpoint for a requeue-based operation and classifies
// the observed status, binding the recorded shared inputs plus the extracted {operationId}.
func pollAsyncOnce(ctx context.Context, cli restclient.UnstructuredClientInterface, clientInfo *getter.Info, mg *unstructured.Unstructured, cfg *getter.AsyncConfig, operationID string) (async.PollOutcome, string, error) {
	base := pollBaseFromRecord(ctx, cli, clientInfo, mg)
	pollReq := buildPollRequest(cfg.Poll.Method, cfg.Poll.HandleParam, operationID, base)
	resp, err := cli.Call(ctx, &http.Client{Timeout: async.RequestTimeout}, cfg.Poll.Path, pollReq)
	if err != nil {
		return async.OutcomePending, "", err
	}
	pb, ok := resp.ResponseBody.(map[string]interface{})
	if !ok {
		return async.OutcomePending, "", fmt.Errorf("poll response body is not an object")
	}
	status, err := extractPollStatus(pb, cfg.Poll.StatusPath)
	if err != nil {
		return async.OutcomePending, "", err
	}
	return async.ClassifyStatus(cfg.Poll, status), status, nil
}

// reReadStatusOnAsyncSuccess re-reads the resource after a successful requeue-based operation and populates
// status from it (mirroring Model A's postGet), so an identifier the operation assigned is captured before
// the fall-through observe — otherwise a resource observable only by that identifier would be re-created. It
// is a no-op when no get verb exists.
func (h *handler) reReadStatusOnAsyncSuccess(ctx context.Context, cli restclient.UnstructuredClientInterface, clientInfo *getter.Info, mg *unstructured.Unstructured) error {
	getCall, getInfo, gerr := builder.APICallBuilder(cli, clientInfo, apiaction.Get)
	if gerr != nil || getCall == nil || getInfo == nil {
		return nil
	}
	getReq := builder.BuildCallConfig(ctx, getInfo, mg, clientInfo.ConfigurationSpec, nil)
	resp, err := getCall(ctx, &http.Client{Timeout: async.RequestTimeout}, getInfo.Path, getReq)
	if err != nil {
		return err
	}
	b, ok := resp.ResponseBody.(map[string]interface{})
	if !ok {
		return nil
	}
	if err := fieldmapping.NormalizeResponseBody(ctx, clientInfo.Resource.VerbsDescription, []string{"get"}, b); err != nil {
		return err
	}
	if err := populateStatusFields(clientInfo, mg, b); err != nil {
		return err
	}
	updated, err := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{Pluralizer: h.pluralizer, DynamicClient: h.dynamicClient})
	if err != nil {
		return err
	}
	*mg = *updated
	return nil
}

// driveAsyncRequeue advances an in-flight requeue-based (Model B) operation by polling it once. It returns
// handled=true (with the observation the caller should return) while the operation is still pending or has
// terminated in failure; it returns handled=false once the operation has terminally succeeded (annotation
// cleared) so the caller falls through to a normal observe that reads the resource's real state.
func (h *handler) driveAsyncRequeue(ctx context.Context, cli restclient.UnstructuredClientInterface, clientInfo *getter.Info, mg *unstructured.Unstructured, operationID, action string, log logging.Logger) (controller.ExternalObservation, bool, error) {
	cfg := asyncConfigForAction(clientInfo.Resource.VerbsDescription, action)
	if cfg == nil {
		// The verb no longer declares async — a stale annotation. Clear it and observe normally.
		if err := h.clearAsyncOperation(ctx, mg); err != nil {
			return controller.ExternalObservation{}, true, err
		}
		return controller.ExternalObservation{}, false, nil
	}

	outcome, status, err := pollAsyncOnce(ctx, cli, clientInfo, mg, cfg, operationID)
	if err != nil {
		// Transient poll error: keep the resource pending and requeue (surface the error for backoff).
		log.Debug("Async(requeue) poll failed; will retry", "operationId", operationID, "error", err.Error())
		return controller.ExternalObservation{}, true, fmt.Errorf("polling async %s operation %q: %w", action, operationID, err)
	}

	switch outcome {
	case async.OutcomeSucceeded:
		log.Debug("Async(requeue) operation reached terminal success", "operationId", operationID, "action", action)
		// Capture the resource's real (post-operation) state before clearing the handle, so an identifier the
		// operation assigned is recorded (mirrors Model A's postGet). Keep the annotation on failure so this
		// is re-driven next reconcile rather than falling into a re-create.
		if cfg.PostGet {
			if rerr := h.reReadStatusOnAsyncSuccess(ctx, cli, clientInfo, mg); rerr != nil {
				return controller.ExternalObservation{}, true, fmt.Errorf("async %s postGet after success: %w", action, rerr)
			}
		}
		if cerr := h.clearAsyncOperation(ctx, mg); cerr != nil {
			return controller.ExternalObservation{}, true, cerr
		}
		// Fall through to a normal observe so status/drift reflect the resource's real state.
		return controller.ExternalObservation{}, false, nil

	case async.OutcomeFailed:
		log.Error(fmt.Errorf("async operation reached terminal failure status %q", status), "operationId", operationID, "action", action)
		// Move off Pending and persist it BEFORE clearing the handle, propagating any error: if this
		// transition is interrupted the annotation is still present, so the Model B branch re-drives it next
		// reconcile instead of leaving a not-found resource stuck as "up-to-date" (the notfound+Pending guard).
		if serr := unstructuredtools.SetConditions(mg, condition.Unavailable()); serr != nil {
			return controller.ExternalObservation{}, true, serr
		}
		updated, uerr := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{Pluralizer: h.pluralizer, DynamicClient: h.dynamicClient})
		if uerr != nil {
			return controller.ExternalObservation{}, true, uerr
		}
		*mg = *updated
		if cerr := h.clearAsyncOperation(ctx, mg); cerr != nil {
			return controller.ExternalObservation{}, true, cerr
		}
		h.eventRecorder.Event(mg, event.Warning(reasonAsyncFailed, event.Action(action), fmt.Errorf("async %s operation %q reached terminal failure status %q", action, operationID, status)))
		return controller.ExternalObservation{}, true, fmt.Errorf("async %s operation %q reached terminal failure status %q", action, operationID, status)

	default: // pending — stay put and let the next reconcile poll again.
		log.Debug("Async(requeue) operation still pending", "operationId", operationID, "status", status)
		return controller.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, true, nil
	}
}
