package restResources

import (
	"context"
	"fmt"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/pathparsing"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/snowplow"
	"github.com/krateo-platformops/unstructured-runtime/pkg/controller"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/krateo-platformops/unstructured-runtime/pkg/tools"
	unstructuredtools "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured"
	"github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// observeViaRestAction delegates observation to a Snowplow RESTAction: it resolves the referenced RESTAction
// (via snowplow /call, under the controller's own identity) with the resource's per-instance context as
// request extras, then projects the RESTAction's composed .status into this resource's status and marks it
// Available. It returns handled=true so the caller returns immediately (this replaces the get/findby observe);
// handled=false only when no observeApiRef is declared, so the caller falls through to the normal observe.
//
// With no notFoundExpr/upToDateExpr the RESTAction composes the observed state each reconcile and the
// resource is reported existing + up-to-date (observe-only projection). When notFoundExpr / upToDateExpr are
// declared, the delegated observe reports non-existence (=> create) / drift (=> update), so it composes with
// create/update/delete RESTActions. The referenced RESTAction is trusted platform configuration: its
// composed .status is projected into this resource's status (except the runtime-managed conditions /
// observedGeneration), so it must be authored to return the intended shape.
func (h *handler) observeViaRestAction(ctx context.Context, mg *unstructured.Unstructured, ref *getter.ApiRef, identifiers []string, log logging.Logger) (controller.ExternalObservation, bool, error) {
	if ref == nil {
		return controller.ExternalObservation{}, false, nil
	}
	if h.snowplowClient == nil {
		return controller.ExternalObservation{}, true, fmt.Errorf("resource declares observeApiRef %s/%s but no snowplow client is configured (set the snowplow/authn URLs)", ref.Namespace, ref.Name)
	}
	// If a Model B async operation is in flight (from an OAS create/update verb), defer to the normal observe
	// so it is polled to completion, rather than short-circuiting to the RESTAction observe.
	if opID, _ := asyncOperationInFlight(mg); opID != "" {
		return controller.ExternalObservation{}, false, nil
	}

	extras := buildExtras(mg, ref.Extras, identifiers)
	result, failedStages, err := h.snowplowClient.Resolve(ctx, snowplow.ApiRef{Name: ref.Name, Namespace: ref.Namespace}, extras)
	if err != nil {
		log.Error(err, "Resolving observe RESTAction", "restAction", ref.Namespace+"/"+ref.Name)
		return controller.ExternalObservation{}, true, fmt.Errorf("resolving observe RESTAction %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	// A failed OBSERVE stage is more insidious than a failed write stage, so it gets its own message rather
	// than sharing the mutate path's wording.
	//
	// snowplow still answers 200 and still composes a .status -- just one missing whatever the failed stage
	// would have contributed. That partial status then feeds BOTH predicates below: a notFoundExpr can read
	// a missing field as "absent" and trigger a create for a resource that exists, and the drift comparison
	// can read it as a difference and update forever (the #309 shape). Neither failure mentions snowplow.
	//
	// Not an error: a partial observation is still the best information available, and erroring here would
	// wedge the resource on a transient stage failure. But it must be visible, because every symptom it
	// causes appears somewhere else (#292).
	if failedStages != "" {
		log.Warn("Observe RESTAction reported one or more FAILED stages; the composed status may be INCOMPLETE, which can present as spurious drift or as a missing resource",
			"restAction", ref.Namespace+"/"+ref.Name, "kind", mg.GetKind(), "failedStages", failedStages)
	}

	// The existence / drift predicates evaluate against {spec, status}, where status is the composed result.
	predCtx := map[string]interface{}{"status": result}
	if spec, found, _ := unstructured.NestedMap(mg.Object, "spec"); found {
		predCtx["spec"] = spec
	}

	// Existence: if the observe reports the resource absent, return not-exists so the controller creates it
	// (composing with createApiRef). Do not project status / mark Available in that case.
	if notFound, ferr := evalBoolPredicate(ctx, ref.NotFoundExpr, predCtx, "observeApiRef.notFoundExpr"); ferr != nil {
		return controller.ExternalObservation{}, true, fmt.Errorf("evaluating observeApiRef.notFoundExpr: %w", ferr)
	} else if notFound {
		log.Debug("Observe RESTAction reports the resource absent", "restAction", ref.Namespace+"/"+ref.Name)
		return controller.ExternalObservation{ResourceExists: false, ResourceUpToDate: false}, true, nil
	}

	// Drift: default up-to-date unless a SOURCED upToDateExpr reports otherwise (composing with updateApiRef).
	// A declared-but-source-less predicate is treated as "no drift signal" (up-to-date) rather than reading
	// as false, which would thrash Update every reconcile.
	upToDate := true
	if ex := ref.UpToDateExpr; ex != nil && (ex.Inline != "" || ex.Ref != "") {
		utd, derr := evalBoolPredicate(ctx, ex, predCtx, "observeApiRef.upToDateExpr")
		if derr != nil {
			return controller.ExternalObservation{}, true, fmt.Errorf("evaluating observeApiRef.upToDateExpr: %w", derr)
		}
		upToDate = utd
	}

	if err := writeObservedStatus(mg, result); err != nil {
		return controller.ExternalObservation{}, true, err
	}
	// Available when up-to-date; report Unavailable while drifted (an update will be driven).
	cond := condition.Available()
	if !upToDate {
		cond = condition.Unavailable()
		cond.Message = "drift reported by observeApiRef.upToDateExpr"
	}
	if err := unstructuredtools.SetConditions(mg, cond); err != nil {
		return controller.ExternalObservation{}, true, err
	}
	updated, uerr := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{Pluralizer: h.pluralizer, DynamicClient: h.dynamicClient})
	if uerr != nil {
		return controller.ExternalObservation{}, true, fmt.Errorf("updating status from observe RESTAction: %w", uerr)
	}
	*mg = *updated
	log.Debug("Observed via RESTAction", "restAction", ref.Namespace+"/"+ref.Name, "upToDate", upToDate)

	// The RESTAction composes the observed state each reconcile. With no notFoundExpr/upToDateExpr the
	// resource is reported existing + up-to-date (observe-only); with them, non-existence/drift compose with
	// create/update RESTActions.
	return controller.ExternalObservation{ResourceExists: true, ResourceUpToDate: upToDate}, true, nil
}

// buildExtras builds the request extras passed to snowplow: the RESTAction's static extras form the base,
// and the per-instance context is layered on top and WINS on conflict. The per-instance context is the
// resource's name/namespace/uid, the values of its declared IDENTIFIERS (resolved from spec, else status),
// and the WHOLE spec under "spec".
//
// The spec goes to EVERY direction, not just create/update. It used to be withheld from observe and delete
// on the reasoning that they "only need to locate the resource" — but locating it is exactly what needs the
// spec: an API whose URLs are parent-scoped (/projects/{projectId}/...) keeps that scope in a plain spec
// field, which is not an identifier (making it one would corrupt findby matching under the default OR
// policy) and is not derivable from name/namespace/uid.
//
// Withholding it produced a silent deadlock rather than an error (issue #41). A delete RESTAction written
// naturally against .spec.* — mirroring its own create/update siblings, where .spec.* is legitimate — saw
// nulls, its guard iterators yielded nothing, every step skipped, snowplow returned 200, and
// mutateViaRestAction returned nil. The caller's externalResourceStillExists check then found the resource
// alive and never released the finalizer, with no error naming the cause. The CR hung in Deleting forever.
//
// The CR exists in all three directions — on delete the finalizer guarantees it — so there is nothing to
// withhold. Payload size is the only cost, and a spec that is too large to forward would already be too
// large to reconcile.
func buildExtras(mg *unstructured.Unstructured, static map[string]interface{}, identifiers []string) map[string]any {
	out := make(map[string]any, len(static)+len(identifiers)+4)
	for k, v := range static {
		out[k] = v
	}
	out["name"] = mg.GetName()
	out["namespace"] = mg.GetNamespace()
	out["uid"] = string(mg.GetUID())
	for _, id := range identifiers {
		segs, err := pathparsing.ParsePath(id)
		if err != nil || len(segs) == 0 {
			continue
		}
		if v, found, _ := unstructured.NestedFieldNoCopy(mg.Object, append([]string{"spec"}, segs...)...); found {
			out[id] = v
			continue
		}
		if v, found, _ := unstructured.NestedFieldNoCopy(mg.Object, append([]string{"status"}, segs...)...); found {
			out[id] = v
		}
	}
	if spec, found, _ := unstructured.NestedMap(mg.Object, "spec"); found {
		out["spec"] = spec
	}
	return out
}

// writeObservedStatus merges the RESTAction's composed status map into the resource's status, leaving the
// runtime-managed conditions untouched.
func writeObservedStatus(mg *unstructured.Unstructured, result map[string]interface{}) error {
	status, _, err := unstructured.NestedMap(mg.Object, "status")
	if err != nil {
		return fmt.Errorf("reading status: %w", err)
	}
	if status == nil {
		status = map[string]interface{}{}
	}
	for k, v := range result {
		// Never let the composed result clobber runtime-managed status fields.
		if k == "conditions" || k == "observedGeneration" {
			continue
		}
		status[k] = v
	}
	if err := unstructured.SetNestedMap(mg.Object, status, "status"); err != nil {
		return fmt.Errorf("writing observed status: %w", err)
	}
	return nil
}
