package restResources

import (
	"context"
	"fmt"
	"net/http"

	customcondition "github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/builder"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/pathparsing"
	ucontroller "github.com/krateo-platformops/unstructured-runtime/pkg/controller"
	unstructuredtools "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Exists implements unstructured-runtime's ExistenceChecker, which incomplete-create recovery prefers over
// Observe.
//
// WHY THIS EXISTS AT ALL. Without it, recovery falls through to Observe -- and our Observe writes the status
// subresource (SetConditions + UpdateStatus) and calls tools.Update on its very first statement. That bumps
// the object's resourceVersion server-side, and the recovery block's own bare tools.Update then conflicts on
// every pass, so the krateo.io/external-create-pending annotation is never reconciled and the resource stays
// wedged. Measured upstream at 51 consecutive failures and 0 recoveries over 20 minutes on one BaremetalNode,
// with the damage surfacing two layers away as a deploy rejected for a field the spec never got to push.
// Implementing this removes the conflict at its source rather than surviving it (#317).
//
// THE CONTRACT IS NOT "RETURN YOUR BEST GUESS". From unstructured-runtime's own doc comment: Exists "must not
// attempt to converge, repair or otherwise mutate anything". So this function performs ONE read and writes
// nothing -- no conditions, no status, no tools.Update.
//
// AND `false` MEANS "I LOOKED AND IT IS DEFINITELY NOT THERE". It must never mean "I could not look". The
// runtime's recovery does this with each answer:
//
//	true                        -> SetExternalCreateSucceeded, done
//	false, within  2 minutes    -> requeue after 15s, marker KEPT          (safe)
//	false, past    2 minutes    -> RemoveAnnotations(pending) -> dispatch -> CREATE RUNS AGAIN
//	error                       -> Creating + ReconcileError, marker UNTOUCHED
//
// The grace period narrows the window; it does not close it. An external API unreadable for longer than two
// minutes, answered with a cheerful `false`, recreates the resource -- for a baremetal provider, a duplicate
// physical server. The error path is strictly safer and already does the right thing: it just has to be
// reached by returning an error rather than a plausible boolean.
//
// This is the third place in this codebase where "I could not determine this" must be its own answer, after
// liveInstanceCount (a LIST failure must never read as zero instances) and the delete guard (unverifiable
// must not read as deleted). All three are recovery or teardown paths, where the plausible value is also the
// destructive one.
func (h *handler) Exists(ctx context.Context, mg *unstructured.Unstructured) (bool, error) {
	log := h.logger.WithValues("kind", mg.GetKind(), "name", mg.GetName())

	if h.swaggerInfoGetter == nil {
		return false, fmt.Errorf("existence check: swagger info getter must be specified")
	}
	clientInfo, err := h.swaggerInfoGetter.Get(mg)
	if err != nil {
		return false, fmt.Errorf("existence check: getting REST client info: %w", err)
	}
	if clientInfo == nil {
		return false, fmt.Errorf("existence check: no REST client info for %s", mg.GetKind())
	}

	// Paths where a single cheap read CANNOT answer the question. Each returns an error, deliberately, and
	// each has a reason that is not "unimplemented":
	//
	//   - a delegated observe (observeApiRef) resolves a RESTAction, and snowplow answers 200 even when a
	//     stage FAILED (#292). The composed .status would then be partial, and reading existence out of a
	//     partial status is "could not look" wearing the shape of an answer.
	//   - an async operation in flight means the create's outcome is genuinely not yet determined. That is
	//     the textbook "could not look", and the runtime's own grace period exists for exactly this.
	//   - a Pending condition is the same thing recorded on the object. Observe's get path treats
	//     notfound+Pending as EXISTS (restResources.go:324), which is right for drift detection and WRONG
	//     here: during recovery the open question is whether the create landed, so answering `true` would
	//     stamp external-create-succeeded on a resource that may not exist.
	//
	//     THE CONDITION CARRIES NO INFORMATION ABOUT EXISTENCE IN EITHER DIRECTION, which is a stronger
	//     reason than the one above and comes from a measured trace rather than from reasoning. The
	//     openstack-ironic-operator-kog session's annotation history on one physical node:
	//
	//       blade01  external-create-succeeded  08:58:57
	//       blade01  external-create-pending    08:59:56   <- back to pending, a minute after succeeding
	//       blade01  external-create-failed     09:00:26
	//
	//     That node's Ironic-side object existed throughout -- it was `active` on disk -- and a chart-driven
	//     de-enrol/re-create walked it back through Pending. So a REAL resource transits Pending, and an
	//     Exists that answered `true` from the condition would have been right there and wrong in the
	//     phantom-create case above; one that answered `false` would be wrong in exactly the opposite pair.
	//     Both booleans are sometimes wrong, so neither is available: refusing is not caution here, it is
	//     the only answer that is never false.
	if clientInfo.Resource.ObserveApiRef != nil {
		return false, fmt.Errorf("existence check: observation is delegated to RESTAction %s/%s, which cannot be read without risking a partial status",
			clientInfo.Resource.ObserveApiRef.Namespace, clientInfo.Resource.ObserveApiRef.Name)
	}
	if opID, action := asyncOperationInFlight(mg); opID != "" {
		return false, fmt.Errorf("existence check: async %s operation %s is still in flight, so whether the resource exists is not yet determined", action, opID)
	}
	if unstructuredtools.IsConditionSet(mg, customcondition.Pending()) {
		return false, fmt.Errorf("existence check: the resource is Pending, so whether the create landed is not yet determined")
	}

	cs := newClientSet(h.dynamicClient, clientInfo, mg, h.prettyJSONDebug)

	gcli, gerr := cs.For(ctx, string(apiaction.Get))
	if gerr != nil {
		return false, fmt.Errorf("existence check: building the REST client for get: %w", gerr)
	}

	isKnown, knownErr := builder.IsResourceKnown(ctx, gcli, clientInfo, mg)
	if knownErr != nil {
		return false, fmt.Errorf("existence check: deciding whether the resource is addressable: %w", knownErr)
	}

	action := apiaction.Get
	if !isKnown {
		// Not addressable by identifier yet, which is the normal state for a create whose response we never
		// recorded. findby is the only way to answer, and it is a read.
		action = apiaction.FindBy
	}

	cli := gcli
	if action == apiaction.FindBy {
		fcli, ferr := cs.For(ctx, string(apiaction.FindBy))
		if ferr != nil {
			return false, fmt.Errorf("existence check: building the REST client for findby: %w", ferr)
		}
		cli = fcli
	}

	apiCall, callInfo, berr := builder.APICallBuilder(cli, clientInfo, action)
	if berr != nil {
		return false, fmt.Errorf("existence check: building the %s call: %w", action, berr)
	}
	if apiCall == nil || callInfo == nil {
		// No verb to read with. NOT false: a kind with no get and no findby is unobservable, and reporting
		// "definitely absent" for it would let recovery recreate on every grace-period expiry.
		return false, fmt.Errorf("existence check: this kind declares no %s verb, so existence cannot be read", action)
	}

	reqConfiguration := builder.BuildCallConfig(ctx, callInfo, mg, clientInfo.ConfigurationSpec, nil)
	if reqConfiguration == nil {
		return false, fmt.Errorf("existence check: could not build the %s call configuration", action)
	}
	if reqConfiguration.BuildErr != nil {
		return false, fmt.Errorf("existence check: assembling the %s request: %w", action, reqConfiguration.BuildErr)
	}
	if missing := pathparsing.UnresolvedPathParams(callInfo.Path, reqConfiguration.Parameters); len(missing) > 0 {
		// The same rule as the delete guard: a resource that is not addressable is UNVERIFIABLE, not absent.
		return false, fmt.Errorf("existence check: %s path %q has unresolved parameters (%v), so the resource is not addressable", action, callInfo.Path, missing)
	}

	log.Debug("Existence check: reading external state", "action", action, "path", callInfo.Path)
	_, callErr := apiCall(ctx, &http.Client{}, callInfo.Path, reqConfiguration)

	switch {
	case callErr == nil:
		// A definite affirmative.
		return true, nil
	case restclient.IsNotFoundError(callErr):
		// A definite negative: the API answered, and answered that it is not there. This is the ONLY path
		// that may return false.
		log.Debug("Existence check: the external resource is definitely absent", "action", action)
		return false, nil
	default:
		// Transport failure, 5xx, timeout, 401/403, an unevaluable findby query -- every one of these is
		// "I could not look". Returning false here is what would recreate a live resource.
		return false, fmt.Errorf("existence check: reading external state via %s: %w", action, callErr)
	}
}

// Compile-time assertion against the runtime's OWN named interface, not a hand-written copy of its shape.
//
// This is load-bearing rather than tidy. The runtime reaches this code through a type assertion:
//
//	if checker, ok := c.externalClient.(ExistenceChecker); ok { return checker.Exists(ctx, el) }
//	obs, err := c.externalClient.Observe(ctx, el)   // <- silent fallback
//
// So if the signature ever drifts -- a renamed method, a changed parameter, an upstream interface change --
// the assertion simply fails, recovery falls back to Observe, and the wedge this file exists to prevent
// returns with NO error and NO log line anywhere. Nothing else in the build would notice.
//
// Asserting the hand-written shape instead would be the same mistake one layer out: it would keep compiling
// while the interface it is meant to mirror moved.
var _ ucontroller.ExistenceChecker = (*handler)(nil)
