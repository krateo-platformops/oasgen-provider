package restResources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/krateo-platformops/audit"
	"github.com/krateo-platformops/plumbing/kubeutil/event"
	customcondition "github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/builder"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/fieldmapping"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/snowplow"
	"github.com/krateo-platformops/unstructured-runtime/pkg/controller"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/krateo-platformops/unstructured-runtime/pkg/pluralizer"
	"github.com/krateo-platformops/unstructured-runtime/pkg/tools"
	unstructuredtools "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured"
	"github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

var _ controller.ExternalClient = (*handler)(nil)

// Event reasons emitted on the reconciled dynamic CR.
const (
	reasonCreated event.Reason = "ResourceCreated"
	reasonUpdated event.Reason = "ResourceUpdated"
	reasonDeleted event.Reason = "ResourceDeleted"
)

func NewHandler(cfg *rest.Config, log logging.Logger, swg getter.Getter, pluralizer pluralizer.PluralizerInterface, prettyJSONDebug bool) controller.ExternalClient {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Error(err, "Creating dynamic client.")
		return nil
	}

	dis, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		log.Error(err, "Creating discovery client.")
		return nil
	}

	return &handler{
		pluralizer:        pluralizer,
		logger:            log,
		dynamicClient:     dyn,
		discoveryClient:   dis,
		swaggerInfoGetter: swg,
		prettyJSONDebug:   prettyJSONDebug,
		// Default to a no-op recorder so events are always safe to emit even
		// when an API-backed recorder is not wired in (e.g. in tests). main.go
		// injects the real recorder via SetEventRecorder.
		eventRecorder: event.NewNopRecorder(),
	}
}

type handler struct {
	pluralizer        pluralizer.PluralizerInterface
	logger            logging.Logger
	dynamicClient     dynamic.Interface
	discoveryClient   *discovery.DiscoveryClient
	swaggerInfoGetter getter.Getter
	prettyJSONDebug   bool
	eventRecorder     event.Recorder
	// snowplowClient resolves observeApiRef RESTActions via snowplow's /call endpoint. nil when snowplow is
	// not configured; a resource that declares observeApiRef then fails Observe with a clear error.
	snowplowClient *snowplow.Client
	// selfServiceAccount is this controller's own identity, used as the RoleBinding subject when
	// self-provisioning secretRef RBAC (issue #31). main.go hard-fails at startup if it is unset, so by
	// the time Create/Update/Delete run it is always populated.
	selfServiceAccount types.NamespacedName
	// auditEmitter writes the canonical Krateo AuditEvent for each EXTERNAL write (C8, #253). Left nil
	// when -otel-logs-enabled is off; audit.Emitter is nil-safe by design, so every call site is an
	// unconditional Emit with no guard.
	auditEmitter *audit.Emitter
}

// SetEventRecorder wires a Kubernetes Event recorder used to emit Events on the
// reconciled dynamic CR. A nil recorder is ignored (the no-op default is kept).
func (h *handler) SetEventRecorder(rec event.Recorder) {
	if rec != nil {
		h.eventRecorder = rec
	}
}

// externalStatusCode extracts the external API's HTTP status from a native call's outcome.
//
// Two sources, because the value lives in different places depending on the outcome: a failure carries it
// on *restclient.StatusError, a success on the Response. Returning 0 when neither has it is deliberate --
// a transport error never reached a status, and audit.Emit omits a 0 rather than inventing one.
func externalStatusCode(resp *restclient.Response, callErr error) int {
	var se *restclient.StatusError
	if errors.As(callErr, &se) {
		return se.StatusCode
	}
	if callErr == nil {
		return resp.StatusCode()
	}
	return 0
}

// emitAudit writes one canonical Krateo AuditEvent for an EXTERNAL write (C8, #253).
//
// Called beside the existing eventRecorder.Event calls, because those already sit at exactly the points
// where an outcome is known -- so the audit record and the Kubernetes Event cannot disagree about whether
// a write succeeded, and a new write path that records an Event is adjacent to the place it must also
// audit.
//
// statusCode is 0 for a DELEGATED write, and that is deliberate rather than missing. A delegated call goes
// through snowplow's /call, so the status RDC can observe is snowplow's, not the vendor's: reporting it as
// http.response.status_code would state that the external API answered 200 when nothing of the sort has
// been established. audit.Emit omits a 0, which is the same principle the module applies to an unparseable
// server.address -- omit rather than write something untrue.
//
// No nil guard: audit.Emitter is nil-safe, so this is a no-op when -otel-logs-enabled is off.
// emitAudit records one external write in the C8 audit trail.
//
// PASS httpMethod == "" AND statusCode == 0 WHEN THIS COMPONENT DID NOT ISSUE THE REQUEST. Both are omitted
// from the event (audit's own builder skips empty strings, and documents `0 => omitted` for the code), which
// is the honest encoding for a DELEGATED write: the request is issued by snowplow's RESTAction stages, whose
// methods and status codes RDC never sees.
//
// The six delegated call sites previously passed http.MethodPost / MethodPut / MethodDelete -- a guess from
// the verb's semantics -- while correctly passing 0 for the status code they equally did not know. Same
// ignorance, two treatments: the code honestly omitted, the method invented. An audit record is a compliance
// artifact, so a field that is confidently WRONG is worse than an absent one: a reader reconciling it with
// snowplow's own record would try to match a PUT that never existed, and conclude the trail is unreliable
// about everything else too (#318).
//
// It also silently encoded "a resource update is a PUT". A peer spent three billable servers on that exact
// assumption in their own drift machinery: across their vendored OAS set, 29 item paths declare PUT and
// exactly one declares PATCH without it -- and that one was the resource they were driving. Their hardcoded
// PUT reached a create-like handler, which answered 400 asking for fields an update would never send, and it
// was filed as a vendor defect before the method was checked. We encoded the same assumption; we only
// reported it rather than sending it.
func (h *handler) emitAudit(ctx context.Context, mg *unstructured.Unstructured, serverURL, httpMethod string, statusCode int, callErr error) {
	outcome := audit.OutcomeSuccess
	msg := "external write succeeded"
	if callErr != nil {
		outcome = audit.OutcomeFailure
		msg = "external write failed: " + callErr.Error()
	}

	gvk := mg.GroupVersionKind()
	h.auditEmitter.Emit(ctx, audit.Event{
		Message: msg,
		// Option B's join key: uid plus the generation the reconcile acted on. snowplow audits the
		// apiserver write that created the CR; these two fields are what let that record and this one be
		// matched up, which is the entire point of C8.
		ObjectUID:             string(mg.GetUID()),
		ObjectGeneration:      mg.GetGeneration(),
		ObjectResourceVersion: mg.GetResourceVersion(),

		Namespace:  mg.GetNamespace(),
		Group:      gvk.Group,
		Version:    gvk.Version,
		Kind:       gvk.Kind,
		ObjectName: mg.GetName(),

		// Full URL. Emit reduces it to host[:port] centrally, so no caller has to remember that a vendor
		// path routinely carries an identifier and at least one carries a token-shaped segment.
		ServerAddress: serverURL,
		Action:        "rest.write",
		HTTPMethod:    httpMethod,
		StatusCode:    statusCode,
		Outcome:       outcome,
	})
}

// SetAuditEmitter wires the C8 audit emitter. A nil emitter is kept as nil rather than replaced with a
// stub: audit.Emitter's own methods are nil-safe, so the call sites need no guard either way, and
// pretending an emitter exists when OTLP logs are disabled would hide a misconfiguration that matters.
func (h *handler) SetAuditEmitter(e *audit.Emitter) {
	h.auditEmitter = e
}

// SetSnowplowClient wires the snowplow client used to resolve observeApiRef RESTActions. A nil client is
// ignored (observeApiRef resources then error at Observe with a "not configured" message).
func (h *handler) SetSnowplowClient(c *snowplow.Client) {
	if c != nil {
		h.snowplowClient = c
	}
}

// SetSelfServiceAccount records this controller's own ServiceAccount identity, used as the RoleBinding
// subject when self-provisioning secretRef RBAC (issue #31).
func (h *handler) SetSelfServiceAccount(sa types.NamespacedName) {
	h.selfServiceAccount = sa
}

// VersionLabel records which served CRD version an instance belongs to.
//
// MIRRORS oasgen-provider's crd/generation.VersionLabel, and the two must stay identical: that module
// generates the CRD whose printer column reads this label and whose pruning decides what to retire by it,
// while this module stamps and (from the per-version watch) selects on it. A rename on one side alone
// would silently stop both from meaning the same thing.
const VersionLabel = "krateo.io/oas-version"

// The fallback that used to live here -- needsVersionLabel/ensureVersionLabel, stamping VersionLabel at
// the top of Observe when an instance carried none -- has been REMOVED, and deliberately so.
//
// It was reachable only while this controller watched every instance. Under version-scoped watching the
// selector is exact equality on VersionLabel, so an unlabelled instance matches no watch, is never
// delivered to Observe, and could never have reached that code. Keeping it would have been worse than
// useless: it would read as a safety net for precisely the case it cannot catch.
//
// What covers that case instead, in order: the MutatingAdmissionPolicy stamps the label at admission
// (required now, hence the chart's 1.36 floor), and oasgen-provider backfills instances that predate the
// policy before deploying the controller that selects on it. Neither runs here.
//
// The merge-patch reasoning that lived in the old comment is preserved in the provider's backfill, which
// is where that write now happens.
//
// The handler's own `version` field went with it. It outlived the fallback by several releases: assigned
// from -version at construction and never read again, while its doc went on claiming the label "is
// stamped onto instances that reach it without one". The -version flag itself is still needed (the watch
// selector, the GVR and the logger all use it), so only the handler's unread copy is gone.

func (h *handler) Observe(ctx context.Context, mg *unstructured.Unstructured) (controller.ExternalObservation, error) {
	if mg == nil {
		return controller.ExternalObservation{}, fmt.Errorf("custom resource is nil")
	}

	log := h.logger.WithValues("op", "Observe").
		WithValues("apiVersion", mg.GetAPIVersion()).
		WithValues("kind", mg.GetKind()).
		WithValues("name", mg.GetName()).
		WithValues("namespace", mg.GetNamespace())

	if h.swaggerInfoGetter == nil {
		return controller.ExternalObservation{}, fmt.Errorf("swagger file info getter must be specified")
	}
	clientInfo, err := h.swaggerInfoGetter.Get(mg)
	if err != nil {
		log.Error(err, "Getting REST client info")
		return controller.ExternalObservation{}, err
	}
	if clientInfo == nil {
		log.Error(fmt.Errorf("swagger info is nil"), "Getting REST client info")
		return controller.ExternalObservation{}, fmt.Errorf("swagger info is nil")
	}
	// Record which served version this instance belongs to, if nothing has yet.
	//
	// BEFORE the Update below, and that ordering is load-bearing: tools.Update REBINDS mg to the object it
	// returns, so anything done after it writes into a local copy and never reaches the caller's object.
	// The caller goes on using what it passed in -- the integration test does Get -> Observe -> Create on
	// one object -- so a resourceVersion bump it never sees makes its next write fail.

	mg, err = tools.Update(ctx, mg, tools.UpdateOptions{
		Pluralizer:    h.pluralizer,
		DynamicClient: h.dynamicClient,
	})
	if err != nil {
		log.Error(err, "Updating CR")
		return controller.ExternalObservation{}, err
	}

	// If observation is delegated to a Snowplow RESTAction, resolve it and project the composed result into
	// status. This replaces the get/findby observe entirely (no OAS call is made for this resource).
	if obs, handled, oerr := h.observeViaRestAction(ctx, mg, clientInfo.Resource.ObserveApiRef, clientInfo.Resource.Identifiers, log); handled {
		return obs, oerr
	}

	// One set per reconcile, resolving each verb to the document IT declares, with the per-reconcile client
	// configuration applied inside it (#220).
	//
	// Observe needs more than one client in the general case, which is why nothing here resolves a single
	// "the client" up front: IsResourceKnown below runs the GET verb, the observe branch runs get XOR
	// findby, and the dispatch and async paths run their own verbs. A resource declaring get and findby in
	// different documents is served correctly only because each use asks for its own action.
	cs := newClientSet(h.dynamicClient, clientInfo, mg, h.prettyJSONDebug)

	// A dispatch resource is observed by looking its correlation token up in the declared collection, which
	// replaces the get/findby observe entirely (#199). BEFORE the async check, because the two are mutually
	// exclusive by construction: async extracts a handle from the trigger response, and a dispatch has no
	// handle to extract -- that absence is what the correlation token exists to replace. CEL refuses async
	// on a dispatch verb, so reaching the poll below with a dispatch declared would mean acting on a
	// handle no dispatch could have produced.
	if dcli, derr := cs.For(ctx, string(apiaction.Dispatch)); derr != nil {
		log.Error(derr, "Building REST client for the dispatch verb")
		return controller.ExternalObservation{}, derr
	} else if obs, handled, oerr := h.observeDispatch(ctx, mg, dcli, &http.Client{}, clientInfo, log); handled {
		return obs, oerr
	}

	// If a requeue-based (Model B) async operation is in flight, poll it once before the normal existence
	// checks: stay pending until it terminates, then (on success) fall through to observe the real state.
	if opID, action := asyncOperationInFlight(mg); opID != "" {
		obs, handled, aerr := h.driveAsyncRequeue(ctx, cs, clientInfo, mg, opID, action, log)
		if handled {
			return obs, aerr
		}
	}

	var response restclient.Response
	// Tries to tries to build the `get` action API Call, with the given statusFields and specFields values.
	// If it is able to validate the `get` action request, returns true
	// A broken request-direction declaration must NOT read as "identity not known". That routes to findby
	// and, with no findby declared, to assume-needs-creating -- a create issued because a jq program was
	// wrong (#117). Surface it instead; the reconcile retries once the RestDefinition is corrected.
	// IsResourceKnown runs the GET verb, which is NOT necessarily the verb this observe will use: it is
	// asked before the get-or-findby decision below. So it gets the get verb's own client.
	gcli, gerr := cs.For(ctx, string(apiaction.Get))
	if gerr != nil {
		log.Error(gerr, "Building REST client for the get verb")
		return controller.ExternalObservation{}, gerr
	}
	isKnown, knownErr := builder.IsResourceKnown(ctx, gcli, clientInfo, mg)
	if knownErr != nil {
		log.Error(knownErr, "Building the get request from the RestDefinition")
		return controller.ExternalObservation{}, fmt.Errorf("building the get request: %w", knownErr)
	}
	// The observed body comes from exactly one verb (get XOR findby); response normalization must use only
	// that verb's fieldMapping, never both, or the other verb's transforms would be misapplied to it.
	observeAction := "get"
	if isKnown {
		// Resource is known: getting the external resource by its identifier (e.g GET /resources/{id}).
		apiCall, callInfo, err := builder.APICallBuilder(gcli, clientInfo, apiaction.Get)
		if apiCall == nil || callInfo == nil {
			log.Error(fmt.Errorf("API action get not found"), "action", apiaction.Get)
			return controller.ExternalObservation{}, fmt.Errorf("API action get not found for %s", apiaction.Get)
		}
		if err != nil {
			log.Error(err, "Building API call")
			return controller.ExternalObservation{}, err
		}
		reqConfiguration := builder.BuildCallConfig(ctx, callInfo, mg, clientInfo.ConfigurationSpec, nil)
		if reqConfiguration == nil {
			return controller.ExternalObservation{}, fmt.Errorf("error building call configuration")
		}
		response, err = apiCall(ctx, &http.Client{}, callInfo.Path, reqConfiguration)
		notfound := restclient.IsNotFoundError(err)
		if notfound && unstructuredtools.IsConditionSet(mg, customcondition.Pending()) {
			log.Debug("External resource exist but is in pending state", "kind", mg.GetKind())
			// We can stop here if the resource is not found and it is in pending state
			// because it means that the resource is being created.
			return controller.ExternalObservation{
				ResourceExists:   true,
				ResourceUpToDate: true,
			}, nil
		} else if notfound {
			log.Debug("External resource not found", "kind", mg.GetKind())
			return controller.ExternalObservation{
				ResourceExists:   false,
				ResourceUpToDate: false,
			}, nil
		}
		if err != nil {
			log.Error(err, "Performing REST call")
			return controller.ExternalObservation{}, err
		}
	} else {
		observeAction = "findby"
		// Resource is not known, we try to find it by its identifiers fields with a `findby` action,
		// typically searching in the items returned by a "list" API call (e.g GET /resources).
		// This is typically used when the resource does not have an server-side generated identifier (e.g., ID, UUID) yet,
		// for instance before creation (in the first ever reconcile loop).

		// This branch is also hit when the resource does not support a `get` action at all but supports only a `findby` action for the observe phase.
		fcli, ferr := cs.For(ctx, string(apiaction.FindBy))
		if ferr != nil {
			log.Error(ferr, "Building REST client for the findby verb")
			return controller.ExternalObservation{}, ferr
		}
		apiCall, callInfo, err := builder.APICallBuilder(fcli, clientInfo, apiaction.FindBy)
		if apiCall == nil {
			if !unstructuredtools.IsConditionSet(mg, condition.Creating()) && !unstructuredtools.IsConditionSet(mg, condition.Available()) {
				log.Debug("No get or findby action found for the resource.")
				log.Debug("External resource is being created", "kind", mg.GetKind())
				return controller.ExternalObservation{}, nil
			}
			// A FAILED create leaves Creating set, so reaching here with Creating is NOT evidence that
			// anything exists. Measured against a real Zendesk (#347): a create rejected with 422
			// ("Private groups are not enabled for this account") left Creating set, and ~2 minutes later
			// this branch read that condition as grounds to assume the resource was fine, set Available,
			// and LATCHED -- a CR permanently Ready=True with no identifier, for a resource that was never
			// created. Nothing recovers from it, because the observation that would clear Available is the
			// one this kind cannot make.
			//
			// The runtime distinguishes the two outcomes and this is the only place that has to care:
			//
			//	create succeeded  worker.go:823   Synced = ReconcileSuccess   (Creating still set)
			//	create failed     worker.go:760   Synced = ReconcileError     (Creating still set)
			//
			// So Ready/Creating cannot separate them and Synced can. A condition is an output of the
			// PREVIOUS reconcile, never evidence about the external world -- but "the previous reconcile
			// errored" is a fact about us, which is exactly what is needed here.
			//
			// Reporting not-exists sends the runtime back to Create, which fails again and re-sets
			// Creating/ReconcileError. That is the honest steady state for a vendor that is refusing the
			// create: visibly stuck and retrying, rather than falsely Available. It is also why this does
			// not error -- an error here would replace the vendor's own explanation with ours.
			if lastReconcileErrored(mg) {
				log.Debug("No get or findby action found for the resource.")
				log.Info("The last reconcile errored, so this resource is not assumed to be up to date",
					"kind", mg.GetKind(), "name", mg.GetName())
				return controller.ExternalObservation{}, nil
			}
			log.Debug("API call not found", "action", apiaction.FindBy)
			log.Debug("No get or findby action found for the resource.")
			log.Debug("Resource is assumed to be up-to-date.")
			cond := condition.Available()
			cond.Message = "Resource is assumed to be up-to-date. API call not found for FindBy."
			err = unstructuredtools.SetConditions(mg, cond)
			if err != nil {
				log.Error(err, "Setting condition")
				return controller.ExternalObservation{}, err
			}

			_, err = tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
				Pluralizer:    h.pluralizer,
				DynamicClient: h.dynamicClient,
			})

			return controller.ExternalObservation{
				ResourceExists:   true,
				ResourceUpToDate: true,
			}, err
		}
		if err != nil {
			log.Error(err, "Building API call")
			return controller.ExternalObservation{}, err
		}
		reqConfiguration := builder.BuildCallConfig(ctx, callInfo, mg, clientInfo.ConfigurationSpec, nil)
		if reqConfiguration == nil {
			log.Error(fmt.Errorf("error building call configuration"), "Building call configuration")
			return controller.ExternalObservation{}, fmt.Errorf("error building call configuration")
		}
		log.Debug("Performing FindBy API call", "path", callInfo.Path)
		response, err = apiCall(ctx, &http.Client{}, callInfo.Path, reqConfiguration)
		notfound := restclient.IsNotFoundError(err)
		if notfound && unstructuredtools.IsConditionSet(mg, customcondition.Pending()) {
			log.Debug("External resource exist but is in pending state", "kind", mg.GetKind())
			// We can stop here if the resource is not found and it is in pending state because it means that the resource is being created.
			return controller.ExternalObservation{
				ResourceExists:   true,
				ResourceUpToDate: true,
			}, nil
		} else if notfound {
			log.Debug("External resource not found", "kind", mg.GetKind())
			return controller.ExternalObservation{
				ResourceExists:   false,
				ResourceUpToDate: false,
			}, nil
		}
		if err != nil {
			log.Error(err, "Performing REST call")
			return controller.ExternalObservation{}, err
		}
	}

	// Body-based absence: some APIs signal "not found" with a successful (2xx) body rather than a 404/other
	// status code (which notFoundCodes already handles). If a NotFoundBody predicate is declared for the
	// observe verb and it matches, treat the resource as not existing so the reconciler (re)creates it. The
	// predicate reads the RAW response (before normalization), so it must be written against the API shape:
	//   - get:    the whole GET body — e.g. a wrapper with an empty list (.items|length==0), a {"deleted":
	//             true} flag, or a state field ({"status":"NOT_FOUND"}).
	//   - findby: the SINGLE matched item (a no-match already returned not-found above via the client's 404,
	//             so this block is only reached on a match) — use it for a tombstone/soft-deleted match such
	//             as .status == "deleted"; a list-shaped predicate is meaningless against a single item.
	// It is skipped while the resource is Pending: during an async create the trigger has fired and the GET
	// may return a transient provisioning body the predicate could read as absent, which would re-trigger
	// Create in a loop — the same reason the notfound handlers above defer to the Pending condition.
	if prog := notFoundBodyForAction(clientInfo.Resource.VerbsDescription, observeAction); prog != nil &&
		response.ResponseBody != nil && !unstructuredtools.IsConditionSet(mg, customcondition.Pending()) {
		absent, perr := evalNotFoundBody(ctx, prog, response.ResponseBody)
		if perr != nil {
			log.Error(perr, "Evaluating notFoundBody predicate")
			return controller.ExternalObservation{}, perr
		}
		if absent {
			log.Debug("External resource treated as not found by notFoundBody predicate", "kind", mg.GetKind())
			return controller.ExternalObservation{
				ResourceExists:   false,
				ResourceUpToDate: false,
			}, nil
		}
	}

	// Response can be nil if the API does not return anything with a proper status code (204 No Content, 304 Not Modified) on an Observe call.
	// In this case, we assume the resource is up-to-date.
	if response.ResponseBody == nil {
		cond := condition.Available()
		cond.Message = "Resource is assumed to be up-to-date. Returned body is nil."
		err = unstructuredtools.SetConditions(mg, cond)
		if err != nil {
			log.Error(err, "Setting condition")
			return controller.ExternalObservation{}, err
		}
		_, err = tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
			Pluralizer:    h.pluralizer,
			DynamicClient: h.dynamicClient,
		})
		if err != nil {
			log.Error(err, "Updating status")
			return controller.ExternalObservation{}, err
		}
		log.Debug("Resource is assumed to be up-to-date. Returned body is nil.")
		return controller.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: true,
		}, nil
	}
	// If we have a response body, we try to populate status fields and check if the resource is up-to-date by comparing spec vs remote resource.
	b, ok := response.ResponseBody.(map[string]interface{})
	if !ok {
		log.Error(fmt.Errorf("body is not an object"), "Performing REST call")
		return controller.ExternalObservation{}, fmt.Errorf("body is not an object")
	}
	// Normalize the observed body into the CR-domain shape (response fieldMapping) before it feeds both
	// status population and drift comparison, so those keep working unchanged.
	//
	// UNLESS findby already did it. findby must normalize before it matches, because the match compares
	// the item against the CR and therefore needs it in the CR's shape (#145) -- so the body it returns is
	// already normalized. Running the transforms again is not a harmless repeat: whole-document jq is not
	// guaranteed idempotent, and a per-entry fieldMapping lifts a value and then REMOVES its source, so a
	// second pass looks for a source that is no longer there.
	if response.Normalized {
		log.Debug("Response already normalized by findby; skipping the observe-path normalization",
			"action", observeAction)
	} else if err := fieldmapping.NormalizeResponseBody(ctx, clientInfo.Resource.VerbsDescription, []string{observeAction}, b); err != nil {
		log.Error(err, "Normalizing response body (fieldMapping)")
		return controller.ExternalObservation{}, err
	}
	// Record whether this observe had a body to populate status FROM. A declared status field absent after
	// a body WAS obtained means the API did not send it; absent after NO body means nothing was read, and
	// the absence is not evidence about the external resource (#233). Those were indistinguishable from
	// the CR, and the second is the one that silently hides a resource's failure reason.
	//
	// Set inside each branch rather than once above, because the two branches persist differently: the
	// body path already writes status below, while the bodyless path did not write at all. A condition set
	// on the bodyless path without its own write would be computed, applied to an in-memory object and
	// dropped -- the same way the drift reason was lost in #213.
	if b == nil {
		changed, cerr := setObservedIfChanged(mg, customcondition.NotObserved(
			fmt.Sprintf("observe action %q returned no body", observeAction)))
		if cerr != nil {
			log.Error(cerr, "Setting the Observed condition")
			return controller.ExternalObservation{}, cerr
		}
		// Only write when the condition actually transitioned. This path has no other reason to write, so
		// an unguarded write here would issue one API update per reconcile, forever, for any resource
		// whose observe never returns a body.
		if changed {
			if _, uerr := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
				Pluralizer:    h.pluralizer,
				DynamicClient: h.dynamicClient,
			}); uerr != nil {
				log.Error(uerr, "Updating status")
				return controller.ExternalObservation{}, uerr
			}
		}
	}

	if b != nil {
		// No write guard needed here -- this path writes status unconditionally below for the populated
		// fields -- but the change check still matters, so the timestamp marks the transition rather than
		// the latest reconcile.
		if _, cerr := setObservedIfChanged(mg, customcondition.Observed()); cerr != nil {
			log.Error(cerr, "Setting the Observed condition")
			return controller.ExternalObservation{}, cerr
		}
		err = populateStatusFields(clientInfo, mg, b)
		if err != nil {
			log.Error(err, "Populating status fields (identifiers and additionalStatusFields)")
			return controller.ExternalObservation{}, err
		}

		mg, err = tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
			Pluralizer:    h.pluralizer,
			DynamicClient: h.dynamicClient,
		})
		if err != nil {
			log.Error(err, "Updating status")
			return controller.ExternalObservation{}, err
		}

		// scopeFields = identifiers + additionalStatusFields, consulted only when compareScope restricts drift
		// to that set. Built as a fresh slice so appending never aliases the Resource's identifier slice.
		scopeFields := make([]string, 0, len(clientInfo.Resource.Identifiers)+len(clientInfo.Resource.AdditionalStatusFields))
		scopeFields = append(scopeFields, clientInfo.Resource.Identifiers...)
		scopeFields = append(scopeFields, clientInfo.Resource.AdditionalStatusFields...)

		// compareScope: updatable narrows drift to the fields the UPDATE verb's request body can express.
		// Anything outside that set cannot be fixed by an update, so comparing it would loop: difference
		// found -> update issued that cannot carry the field -> nothing changes -> difference found again.
		if clientInfo.Resource.CompareScope == compareScopeUpdatable {
			var updatable []string
			var uerr error
			for _, v := range clientInfo.Resource.VerbsDescription {
				if !strings.EqualFold(v.Action, string(apiaction.Update)) {
					continue
				}
				// The UPDATE verb's document: compareScope "updatable" asks which spec fields that verb's
				// body can express, so asking the observe verb's document would answer about the wrong
				// operation (#220).
				var ucli restclient.UnstructuredClientInterface
				ucli, uerr = cs.For(ctx, string(apiaction.Update))
				if uerr != nil {
					break
				}
				updatable, uerr = ucli.UpdatableBodyPaths(v.Method, v.Path)
				break
			}
			if uerr != nil {
				log.Error(uerr, "Deriving updatable fields for drift comparison")
				return controller.ExternalObservation{}, uerr
			}
			// No update verb, or one without a JSON body, means nothing about this resource is
			// fixable by an update -- so nothing should be reported as drift.
			scopeFields = updatable
		}
		res, err := isCRUpdated(mg, b, clientInfo.Resource.CompareScope, scopeFields)
		if err != nil {
			log.Error(err, "Checking if CR is updated")
			return controller.ExternalObservation{}, err
		}
		if !res.IsEqual {
			field := res.Reason.FieldPath()
			// The VALUES go to the log; the condition gets the field and the kind of difference. A
			// condition is readable by anyone who can get the resource, and the response side of a
			// comparison is not otherwise visible there, so putting observed values in it would publish
			// them -- and an array or object value would publish a lot of them.
			log.Debug("External resource not up-to-date", "kind", mg.GetKind(), "field", field, "reason", res.String())

			sig := driftSignature(mg, res)
			repeats, rerr := h.recordDriftRepeat(ctx, mg, sig)
			if rerr != nil {
				return controller.ExternalObservation{}, rerr
			}

			if repeats >= maxDriftRepeats {
				// The same difference, this many times running, means the update issued for it is not
				// resolving it -- so issuing it again only repeats a write that has already been shown
				// not to work. Reported as not-Ready with the field named, and NOT as drift, because
				// reporting drift is what makes the reconciler call Update.
				//
				// This is the only place the controller declines to act on a difference it can see. It
				// is recoverable without operator action on the CR: observe still runs every poll, and
				// a different difference, or an edited spec (generation), produces a new signature and
				// a fresh attempt.
				detail := fmt.Sprintf("%s; the update has been issued %d times without resolving it, so it will not be issued again for this difference. "+
					"The usual cause is a request/response shape divergence in the API description, fixable with responseTransform on the observe verb",
					res.Reason.Reason, repeats)
				if serr := unstructuredtools.SetConditions(mg, customcondition.DriftNotConverging(field, detail)); serr != nil {
					log.Error(serr, "Setting condition")
					return controller.ExternalObservation{}, serr
				}
				h.eventRecorder.Event(mg, event.Warning(reasonUpdated, "Update", fmt.Errorf(
					"not updating %s again: field %s has differed for %d consecutive checks and the update does not resolve it",
					mg.GetName(), field, repeats)))
				log.Info("Drift is not converging; no longer issuing the update for it",
					"kind", mg.GetKind(), "field", field, "checks", repeats)
				// Persisted here, not left to the caller. The runtime RE-FETCHES the object as soon as
				// Observe returns, so a condition that is only set in memory is discarded whichever
				// branch it then takes -- which is the other half of why the drift reason never reached
				// a CR (#213).
				if _, uerr := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
					Pluralizer:    h.pluralizer,
					DynamicClient: h.dynamicClient,
				}); uerr != nil {
					log.Error(uerr, "Updating status")
					return controller.ExternalObservation{}, uerr
				}
				// exists=true, upToDate=true so no Update is queued. Not a claim that the resource
				// matches its spec -- the Ready condition just persisted says it does not.
				return controller.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				}, nil
			}

			cond := condition.Unavailable()
			cond.Reason = "ResourceNotUpToDate"
			cond.Message = fmt.Sprintf("field %s: %s", field, res.Reason.Reason)
			if field == "" {
				cond.Message = res.Reason.Reason
			}
			if serr := unstructuredtools.SetConditions(mg, cond); serr != nil {
				log.Error(serr, "Setting condition")
				return controller.ExternalObservation{}, serr
			}
			// Persisted here, because the caller does not: the runtime answers !ResourceUpToDate by
			// queueing an Update and returning, with no status write on that path. Without this the
			// reason was computed, set on an in-memory object and dropped, so a resource looping here
			// showed only whatever the Update leg last wrote (#213).
			if _, uerr := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
				Pluralizer:    h.pluralizer,
				DynamicClient: h.dynamicClient,
			}); uerr != nil {
				log.Error(uerr, "Updating status")
				return controller.ExternalObservation{}, uerr
			}
			return controller.ExternalObservation{
				ResourceExists:   true,
				ResourceUpToDate: false,
			}, nil
		}
		// The comparison agrees, so any recorded non-convergence is stale.
		if cerr := h.clearDriftRepeats(ctx, mg); cerr != nil {
			return controller.ExternalObservation{}, cerr
		}
	}
	log.Debug("Setting condition", "kind", mg.GetKind())
	err = unstructuredtools.SetConditions(mg, condition.Available())
	if err != nil {
		log.Error(err, "Setting condition")
		return controller.ExternalObservation{}, err
	}
	mg, err = tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
		Pluralizer:    h.pluralizer,
		DynamicClient: h.dynamicClient,
	})
	if err != nil {
		log.Error(err, "Updating status")
		return controller.ExternalObservation{}, err
	}

	log.Debug("External resource up-to-date", "kind", mg.GetKind())

	return controller.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true,
	}, nil
}

// lastReconcileErrored reports whether the PREVIOUS reconcile of this object ended in an error, by reading
// the runtime's Synced condition.
//
// Used by the one observe path that has no way to look at the external resource, to avoid treating its own
// Ready/Creating condition as evidence that a create succeeded. The runtime sets Creating on BOTH outcomes
// and distinguishes them only on Synced (#347).
//
// Compares Type and Reason only. condition.ReconcileError takes the error as an argument and puts it in the
// Message, so constructing one to hand to IsConditionSet would require inventing an error whose text must
// match -- and would silently stop matching the moment the runtime reworded it.
func lastReconcileErrored(mg *unstructured.Unstructured) bool {
	for _, c := range unstructuredtools.GetConditions(mg) {
		if c.Type == condition.TypeSynced {
			return c.Reason == condition.ReasonReconcileError
		}
	}
	return false
}

// stopOnAuthorizationDenied reports whether err is a permanent authorisation refusal, and if so records it
// on the CR so the refusal is visible without reading logs.
//
// Returns true when the caller must STOP rather than return the error. Returning the error would retry the
// same verb with the same credential on every reconcile, forever: a 403 with no rate-limit signal is a
// decision, and nothing the controller does next can change it (#313).
//
// NOT USED BY Delete, deliberately, and that asymmetry is the point. A refused delete leaves the external
// resource in place; stopping there would let the finalizer be removed and the CR disappear while the thing
// it represents still exists and still costs money. #261 already settled that an unverifiable delete HOLDS
// the finalizer, and a denied delete is unverifiable in exactly that sense. Retrying a denied delete
// forever is wasteful; orphaning the resource is worse, and only one of the two is recoverable by fixing
// the credential.
//
// A 401 is not in scope here either: see restclient.IsAuthorizationDenied. Credential rotation is delegated
// to a secret manager, and retrying a 401 is the mechanism by which a rotation mid-reconcile is survived.
func (h *handler) stopOnAuthorizationDenied(ctx context.Context, mg *unstructured.Unstructured, verb string, err error, log logging.Logger) bool {
	if !restclient.IsAuthorizationDenied(err) {
		return false
	}
	if serr := unstructuredtools.SetConditions(mg, customcondition.AuthorizationDenied(verb)); serr != nil {
		log.Error(serr, "Setting condition")
		return false
	}
	h.eventRecorder.Event(mg, event.Warning(reasonUpdated, event.Action(verb), fmt.Errorf(
		"the API refused the %s request for %s; the credential is not permitted to perform it, so it will not be retried",
		verb, mg.GetName())))
	// The vendor's explanation reaches the log and not the condition (#334): a 403 body often names the
	// required scope, and a condition is readable by anyone with `get` on the CR.
	log.Info("Authorization denied; no longer issuing this verb",
		"kind", mg.GetKind(), "verb", verb, "error", err.Error())
	if _, uerr := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
		Pluralizer:    h.pluralizer,
		DynamicClient: h.dynamicClient,
	}); uerr != nil {
		log.Error(uerr, "Updating status")
		return false
	}
	return true
}

func (h *handler) Create(ctx context.Context, mg *unstructured.Unstructured) error {
	log := h.logger.WithValues("op", "Create").
		WithValues("apiVersion", mg.GetAPIVersion()).
		WithValues("kind", mg.GetKind()).
		WithValues("name", mg.GetName()).
		WithValues("namespace", mg.GetNamespace())

	// REFUSE TO CREATE AN OBJECT THAT IS BEING DELETED (#279).
	//
	// A Create event queued before the object acquired a deletionTimestamp is still dispatched after it:
	// unstructured-runtime's worker consults WasDeleted only to decide whether to ADD a finalizer
	// (worker.go:454 in v1.6.1) and not to gate the dispatch switch below it, so a restart or requeue
	// mid-delete runs handleCreate. Measured on a live cluster: three creates at 13:58:33, 13:58:53 and
	// 13:59:22 against a CR whose deletionTimestamp was 13:58:27, interleaved with the delete attempts --
	// it re-dispatches for as long as the delete is in flight, not once.
	//
	// Harmless when the vendor refuses the create. Where it SUCCEEDS: the create lands, the delete
	// completes, the finalizer is released, the CR is reaped, and the external resource remains with
	// nothing referencing it -- its identifier possibly never persisted, because the create's status write
	// races the deletion. On billable infrastructure that is one orphan per reconcile for the duration of
	// the delete, and no reconcile will ever notice.
	//
	// Fixed upstream in unstructured-runtime #25 (merged, untagged as of writing). This guard is kept
	// REGARDLESS, and not as a stopgap: a controller should not create external infrastructure it is in
	// the middle of deleting, whoever asked. It also covers every deployment pinned below whatever version
	// carries that fix, which is all of them today.
	//
	// Create ONLY. Update is deliberately not gated: an update during deletion is wasteful rather than
	// destructive, and the runtime's create-recovery path intentionally reconciles annotations on objects
	// in odd states, so gating the whole dispatch would break it.
	//
	// Returns nil, not an error: the object is going away, so there is no later moment at which creating
	// becomes correct and nothing is served by requeueing.
	if !mg.GetDeletionTimestamp().IsZero() {
		log.Info("Refusing to create: the resource is being deleted",
			"deletionTimestamp", mg.GetDeletionTimestamp().String())
		h.eventRecorder.Event(mg, event.Warning(reasonCreated, "Create",
			fmt.Errorf("refused to create an external resource for a custom resource that is being "+
				"deleted: creating now would leave it orphaned once the delete completes")))
		return nil
	}

	if h.swaggerInfoGetter == nil {
		return fmt.Errorf("swagger info getter must be specified")
	}

	clientInfo, err := h.swaggerInfoGetter.Get(mg)
	if err != nil {
		log.Error(err, "Getting REST client info")
		return err
	}

	// One set per reconcile, resolving each verb to the document IT declares (#220). Configuration lives
	// in the set, so every client it hands out carries SetAuth -- a second client for a second document
	// would otherwise go out unauthenticated.
	cs := newClientSet(h.dynamicClient, clientInfo, mg, h.prettyJSONDebug)
	cli, err := cs.For(ctx, string(apiaction.Create))
	if err != nil {
		log.Error(err, "Building REST client")
		return err
	}

	// If create is delegated to a RESTAction, invoke the provisioning sequence instead of the create verb
	// and mark the resource Creating; convergence is by re-invocation (the RESTAction must be idempotent)
	// until Observe reports the resource exists — which requires a get/findby observe that can report
	// non-existence. Without one, the resource is marked Available after a single unverified invocation.
	if ref := clientInfo.Resource.CreateApiRef; ref != nil {
		if !hasObserveVerb(clientInfo.Resource.VerbsDescription) {
			log.Warn("createApiRef is set but the resource has no get/findby verb: create success cannot be verified and the resource will be marked Available after one invocation", "restAction", ref.Namespace+"/"+ref.Name)
		}
		if err := h.mutateViaRestAction(ctx, mg, ref, clientInfo.Resource.Identifiers, "create", log); err != nil {
			log.Error(err, "Creating via RESTAction")
			h.eventRecorder.Event(mg, event.Warning(reasonCreated, "Create", err))
			// statusCode 0: delegated, so the vendor's status is snowplow's to report, not ours.
			h.emitAudit(ctx, mg, cli.ServerURL(), "", 0, err)
			return err
		}
		h.emitAudit(ctx, mg, cli.ServerURL(), "", 0, nil)
		if err := unstructuredtools.SetConditions(mg, condition.Creating()); err != nil {
			return err
		}
		if _, err := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{Pluralizer: h.pluralizer, DynamicClient: h.dynamicClient}); err != nil {
			log.Error(err, "Updating status")
			h.eventRecorder.Event(mg, event.Warning(reasonCreated, "Create", err))
			return err
		}
		h.eventRecorder.Event(mg, event.Normal(reasonCreated, "Create", fmt.Sprintf("Invoked create RESTAction for: %s", mg.GetName())))
		return nil
	}

	// A dispatch resource has no create verb -- the dispatch IS its create -- so this must come before the
	// create builder, which would otherwise find nothing and report a missing action for a resource that
	// correctly declares none (#199).
	if handled, derr := h.createDispatch(ctx, mg, cli, &http.Client{}, clientInfo, log); handled {
		// A dispatch IS an external write -- the dispatch is this resource's create -- so it is audited
		// like one. Leaving it out would make the audit trail silently incomplete for exactly the
		// resources that have no create verb.
		h.emitAudit(ctx, mg, cli.ServerURL(), "", 0, derr)
		if derr != nil {
			log.Error(derr, "Dispatching")
			h.eventRecorder.Event(mg, event.Warning(reasonCreated, "Dispatch", derr))
			return derr
		}
		// Creating, not Available: the invocation is not confirmed until the next Observe finds the token
		// in the collection, which is the only evidence a 204-empty trigger leaves behind.
		if err := unstructuredtools.SetConditions(mg, condition.Creating()); err != nil {
			return err
		}
		if _, err := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{Pluralizer: h.pluralizer, DynamicClient: h.dynamicClient}); err != nil {
			log.Error(err, "Updating status")
			return err
		}
		return nil
	}

	apiCall, callInfo, err := builder.APICallBuilder(cli, clientInfo, apiaction.Create)
	if err != nil {
		log.Error(err, "Building API call")
		return err
	}
	if apiCall == nil || callInfo == nil {
		log.Error(fmt.Errorf("API action create not found"), "action", apiaction.Create)
		return nil
	}

	// First provisioning: a missing secretRef RBAC Role is normal here (expectExisting=false).
	resolved, err := h.ensureSecretRefRBACAndResolve(ctx, cs, clientInfo, callInfo.FieldMapping, mg, false)
	if err != nil {
		// A match that found no item, on a lookup declaring onMissing: requeue, is the expected state while
		// the CR defining the referent is still converging -- not a failure. Report Pending and return
		// cleanly so the next reconcile retries; returning the error instead would surface a permanent
		// failure for an ordering race that resolves itself.
		if isLookupReferentMissing(err) {
			log.Debug("A dependent lookup's referent does not exist yet; waiting", "kind", mg.GetKind(), "reason", err.Error())
			if cerr := unstructuredtools.SetConditions(mg, customcondition.Pending()); cerr != nil {
				log.Error(cerr, "Setting condition")
				return cerr
			}
			_, uerr := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
				Pluralizer:    h.pluralizer,
				DynamicClient: h.dynamicClient,
			})
			return uerr
		}
		log.Error(err, "Provisioning secretRef RBAC / resolving field mapping resolvers")
		return err
	}

	reqConfiguration := builder.BuildCallConfig(ctx, callInfo, mg, clientInfo.ConfigurationSpec, resolved)
	// Whole-document requestTransform, applied to the assembled body immediately before the call — after the
	// per-field mappings have composed it, so the program sees the finished article. A failure fails the
	// reconcile rather than sending a partially transformed body.
	if reqConfiguration != nil {
		tb, terr := fieldmapping.ApplyRequestTransform(ctx, callInfo.RequestTransform, reqConfiguration.Body)
		if terr != nil {
			log.Error(terr, "Applying requestTransform")
			return terr
		}
		reqConfiguration.Body = tb
	}
	response, err := apiCall(ctx, &http.Client{}, callInfo.Path, reqConfiguration)
	// NATIVE Create, so the vendor's own status is in hand -- unlike the delegated path, which can
	// only see snowplow's. On failure it travels on *restclient.StatusError; on success it is on
	// the Response. Either way this is the real external status code, not a stand-in (#253).
	h.emitAudit(ctx, mg, cli.ServerURL(), callInfo.Method, externalStatusCode(&response, err), err)
	if err != nil {
		if h.stopOnAuthorizationDenied(ctx, mg, "create", err, log) {
			return nil
		}
		log.Error(err, "Performing REST call")
		return err
	}

	// If create is asynchronous, drive the long-running operation. Two modes:
	//   - Model A (blocking, default): poll to completion inline here. If the controller restarts mid-poll the
	//     create verb re-runs, so the trigger must be idempotent (tolerate a duplicate via successCodes/409
	//     or a server-side upsert).
	//   - Model B (requeue): record the operation handle and return; the resource stays Pending and each
	//     subsequent Observe polls the operation once until it terminates — no worker is blocked, and the
	//     create is not re-triggered on restart.
	asyncRequeued := false
	if cfg := asyncConfigForAction(clientInfo.Resource.VerbsDescription, string(apiaction.Create)); cfg != nil {
		if asyncRequeueEnabled(cfg) {
			if err := h.recordAsyncOperation(ctx, mg, cfg, string(apiaction.Create), response, reqConfiguration, log); err != nil {
				log.Error(err, "Recording async create operation")
				return err
			}
			asyncRequeued = true
		} else {
			response, err = driveAsync(ctx, cli, clientInfo, mg, cfg, string(apiaction.Create), response, reqConfiguration, log)
			if err != nil {
				log.Error(err, "Driving async create operation")
				return err
			}
		}
	}

	// Clear status before populating with new values to ensure no stale values remain.
	// This prevents, for instance, using outdated identifiers (e.g., status.id from a previously deleted
	// external resource) that would cause reconciliation deadlock on subsequent Observe operations.
	clearStatusFields(mg)
	log.Debug("Cleared status before populating with create response", "kind", mg.GetKind())

	if response.ResponseBody != nil {
		body := response.ResponseBody
		b, ok := body.(map[string]interface{})
		if !ok {
			log.Error(fmt.Errorf("body is not an object"), "Performing REST call")
			return fmt.Errorf("body is not an object")
		}

		if err := fieldmapping.NormalizeResponseBody(ctx, clientInfo.Resource.VerbsDescription, []string{"create"}, b); err != nil {
			log.Error(err, "Normalizing response body (fieldMapping)")
			return err
		}

		err = populateStatusFields(clientInfo, mg, b)
		if err != nil {
			log.Error(err, "Populating status fields (identifiers and additionalStatusFields)")
			return err
		}
	} else {
		log.Debug("Create response has no body, status will remain empty until discovered by next observe", "kind", mg.GetKind())
	}
	log.Debug("Creating external resource", "kind", mg.GetKind())

	// Set condition for pending responses to indicate async creation operation in progress. A Model B
	// (requeue) operation is always pending on create — its handle was recorded and Observe will poll it.
	if response.IsPending() || asyncRequeued {
		log.Debug("External resource is pending", "kind", mg.GetKind())
		err = unstructuredtools.SetConditions(mg, customcondition.Pending())
		if err != nil {
			log.Error(err, "Setting condition")
			return err
		}
	} else { // Set creating condition if not pending
		err = unstructuredtools.SetConditions(mg, condition.Creating())
		if err != nil {
			log.Error(err, "Setting condition")
			return err
		}
	}

	_, err = tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
		Pluralizer:    h.pluralizer,
		DynamicClient: h.dynamicClient,
	})
	if err != nil {
		log.Error(err, "Updating status")
		h.eventRecorder.Event(mg, event.Warning(reasonCreated, "Create", err))
		return err
	}

	h.eventRecorder.Event(mg, event.Normal(reasonCreated, "Create", fmt.Sprintf("Created external resource: %s", mg.GetName())))
	return nil
}

func (h *handler) Update(ctx context.Context, mg *unstructured.Unstructured) error {
	log := h.logger.WithValues("op", "Update").
		WithValues("apiVersion", mg.GetAPIVersion()).
		WithValues("kind", mg.GetKind()).
		WithValues("name", mg.GetName()).
		WithValues("namespace", mg.GetNamespace())

	log.Debug("Handling custom resource values update.")
	if h.swaggerInfoGetter == nil {
		return fmt.Errorf("swagger info getter must be specified")
	}

	clientInfo, err := h.swaggerInfoGetter.Get(mg)
	if err != nil {
		log.Error(err, "Getting REST client info")
		return err
	}

	// One set per reconcile, resolving each verb to the document IT declares (#220). Configuration lives
	// in the set, so every client it hands out carries SetAuth -- a second client for a second document
	// would otherwise go out unauthenticated.
	cs := newClientSet(h.dynamicClient, clientInfo, mg, h.prettyJSONDebug)
	cli, err := cs.For(ctx, string(apiaction.Update))
	if err != nil {
		log.Error(err, "Building REST client")
		return err
	}

	// If update is delegated to a RESTAction, invoke the re-apply sequence instead of the update verb (it
	// forwards the whole spec — the desired state — and must be idempotent), project any composed result,
	// and return.
	if ref := clientInfo.Resource.UpdateApiRef; ref != nil {
		if err := h.mutateViaRestAction(ctx, mg, ref, clientInfo.Resource.Identifiers, "update", log); err != nil {
			log.Error(err, "Updating via RESTAction")
			h.eventRecorder.Event(mg, event.Warning(reasonUpdated, "Update", err))
			h.emitAudit(ctx, mg, cli.ServerURL(), "", 0, err)
			return err
		}
		h.emitAudit(ctx, mg, cli.ServerURL(), "", 0, nil)
		if err := unstructuredtools.SetConditions(mg, condition.Available()); err != nil {
			return err
		}
		if _, err := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{Pluralizer: h.pluralizer, DynamicClient: h.dynamicClient}); err != nil {
			log.Error(err, "Updating status")
			h.eventRecorder.Event(mg, event.Warning(reasonUpdated, "Update", err))
			return err
		}
		h.eventRecorder.Event(mg, event.Normal(reasonUpdated, "Update", fmt.Sprintf("Invoked update RESTAction for: %s", mg.GetName())))
		return nil
	}

	apiCall, callInfo, err := builder.APICallBuilder(cli, clientInfo, apiaction.Update)
	if err != nil {
		log.Error(err, "Building API call")
		return err
	}
	if apiCall == nil || callInfo == nil {
		log.Error(fmt.Errorf("API action update not found"), "action", apiaction.Update)
		return nil
	}

	// The Role should already exist from Create (expectExisting=true): if it's unexpectedly missing, that's
	// a hard error (decision D), not a silent recreate. Full replace of the granted secret-name set.
	resolved, err := h.ensureSecretRefRBACAndResolve(ctx, cs, clientInfo, callInfo.FieldMapping, mg, true)
	if err != nil {
		// A match that found no item, on a lookup declaring onMissing: requeue, is the expected state while
		// the CR defining the referent is still converging -- not a failure. Report Pending and return
		// cleanly so the next reconcile retries; returning the error instead would surface a permanent
		// failure for an ordering race that resolves itself.
		if isLookupReferentMissing(err) {
			log.Debug("A dependent lookup's referent does not exist yet; waiting", "kind", mg.GetKind(), "reason", err.Error())
			if cerr := unstructuredtools.SetConditions(mg, customcondition.Pending()); cerr != nil {
				log.Error(cerr, "Setting condition")
				return cerr
			}
			_, uerr := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
				Pluralizer:    h.pluralizer,
				DynamicClient: h.dynamicClient,
			})
			return uerr
		}
		log.Error(err, "Refreshing secretRef RBAC / resolving field mapping resolvers")
		return err
	}

	reqConfiguration := builder.BuildCallConfig(ctx, callInfo, mg, clientInfo.ConfigurationSpec, resolved)
	// Whole-document requestTransform, applied to the assembled body immediately before the call — after the
	// per-field mappings have composed it, so the program sees the finished article. A failure fails the
	// reconcile rather than sending a partially transformed body.
	if reqConfiguration != nil {
		tb, terr := fieldmapping.ApplyRequestTransform(ctx, callInfo.RequestTransform, reqConfiguration.Body)
		if terr != nil {
			log.Error(terr, "Applying requestTransform")
			return terr
		}
		reqConfiguration.Body = tb
	}
	response, err := apiCall(ctx, &http.Client{}, callInfo.Path, reqConfiguration)
	// NATIVE Update, so the vendor's own status is in hand -- unlike the delegated path, which can
	// only see snowplow's. On failure it travels on *restclient.StatusError; on success it is on
	// the Response. Either way this is the real external status code, not a stand-in (#253).
	h.emitAudit(ctx, mg, cli.ServerURL(), callInfo.Method, externalStatusCode(&response, err), err)
	if err != nil {
		if h.stopOnAuthorizationDenied(ctx, mg, "update", err, log) {
			return nil
		}
		log.Error(err, "Performing REST call")
		return err
	}

	// If update is asynchronous, drive the long-running operation. Model A (blocking) polls to completion
	// inline; Model B (requeue) records the operation handle and returns, letting each subsequent Observe
	// poll it once until terminal (see the create path for the full rationale).
	asyncRequeued := false
	if cfg := asyncConfigForAction(clientInfo.Resource.VerbsDescription, string(apiaction.Update)); cfg != nil {
		if asyncRequeueEnabled(cfg) {
			if err := h.recordAsyncOperation(ctx, mg, cfg, string(apiaction.Update), response, reqConfiguration, log); err != nil {
				log.Error(err, "Recording async update operation")
				return err
			}
			asyncRequeued = true
		} else {
			response, err = driveAsync(ctx, cli, clientInfo, mg, cfg, string(apiaction.Update), response, reqConfiguration, log)
			if err != nil {
				log.Error(err, "Driving async update operation")
				return err
			}
		}
	}

	// Clear status before populating with new values, unless the response has empty body.
	// If response body is empty (e.g., 204 responses), we keep the existing status
	// since the API indicates success without returning data (edge case).
	if response.ResponseBody != nil {
		clearStatusFields(mg)
		log.Debug("Cleared status before populating with update response", "kind", mg.GetKind())

		body := response.ResponseBody
		b, ok := body.(map[string]interface{})
		if !ok {
			log.Error(fmt.Errorf("body is not an object"), "Performing REST call")
			return fmt.Errorf("body is not an object")
		}

		if err := fieldmapping.NormalizeResponseBody(ctx, clientInfo.Resource.VerbsDescription, []string{"update"}, b); err != nil {
			log.Error(err, "Normalizing response body (fieldMapping)")
			return err
		}

		err = populateStatusFields(clientInfo, mg, b)
		if err != nil {
			log.Error(err, "Populating status fields (identifiers and additionalStatusFields)")
			return err
		}
	} else {
		log.Debug("Update response has no body, keeping existing status", "kind", mg.GetKind())
	}
	log.Debug("Updating external resource", "kind", mg.GetKind())

	// Set condition for pending responses to indicate async update operation in progress. A Model B
	// (requeue) operation is always pending on update — its handle was recorded and Observe will poll it.
	if response.IsPending() || asyncRequeued {
		log.Debug("External resource update is pending", "kind", mg.GetKind())
		err = unstructuredtools.SetConditions(mg, customcondition.Pending())
		if err != nil {
			log.Error(err, "Setting condition")
			return err
		}
	} else {
		// Updating, not Creating. A resource that has existed for days is not being created, and saying
		// it is sends whoever reads the condition looking for a creation that already happened -- which
		// is exactly how a permanent drift loop came to look like a slow create (#213).
		err = unstructuredtools.SetConditions(mg, customcondition.Updating())
		if err != nil {
			log.Error(err, "Setting condition")
			return err
		}
	}

	mg, err = tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
		Pluralizer:    h.pluralizer,
		DynamicClient: h.dynamicClient,
	})
	if err != nil {
		log.Error(err, "Updating status")
		h.eventRecorder.Event(mg, event.Warning(reasonUpdated, "Update", err))
		return err
	}

	log.Debug("Custom resource values updated", "kind", mg.GetKind())

	h.eventRecorder.Event(mg, event.Normal(reasonUpdated, "Update", fmt.Sprintf("Updated external resource: %s", mg.GetName())))
	return nil
}

func (h *handler) Delete(ctx context.Context, mg *unstructured.Unstructured) error {
	log := h.logger.WithValues("op", "Delete").
		WithValues("apiVersion", mg.GetAPIVersion()).
		WithValues("kind", mg.GetKind()).
		WithValues("name", mg.GetName()).
		WithValues("namespace", mg.GetNamespace())

	log.Debug("Handling custom resource values deletion.")

	if h.swaggerInfoGetter == nil {
		return fmt.Errorf("swagger info getter must be specified")
	}

	clientInfo, err := h.swaggerInfoGetter.Get(mg)
	if err != nil {
		// Only proceed to delete the CR without teardown when the RestDefinition is GENUINELY absent. A
		// transient failure to list/read definitions must HOLD the finalizer and retry, otherwise a
		// momentary control-plane blip during a delete reconcile would orphan the external resource.
		if !errors.Is(err, getter.ErrDefinitionNotFound) {
			log.Error(err, "Getting REST client info; holding finalizer and retrying")
			return err
		}
		log.Debug("Getting REST client info", "error", err)
		log.Info("RestDefinition not found, CR will be deleted but real external resource will not be deleted due to missing information")
		// We can still delete the CR, but we cannot delete the external resource.
		err = unstructuredtools.SetConditions(mg, condition.Deleting())
		if err != nil {
			log.Error(err, "Setting condition")
			return err
		}
		// Teardown of RDC's own self-granted secretRef RBAC never depends on the RestDefinition existing.
		// Best-effort: a failure here must not block releasing the CR's finalizer.
		if derr := h.deleteSecretRefRBAC(ctx, mg); derr != nil {
			log.Warn("secretRef RBAC teardown failed, continuing", "error", derr)
		}
		return nil
	}

	// One set per reconcile, resolving each verb to the document IT declares. Delete is the clearest case
	// for why a single client per entry point will not do: it verifies itself with the GET verb, so the
	// verification read must address the get verb's document even when the delete verb declares another
	// (#220).
	cs := newClientSet(h.dynamicClient, clientInfo, mg, h.prettyJSONDebug)
	cli, err := cs.For(ctx, string(apiaction.Delete))
	if err != nil {
		log.Error(err, "Building REST client")
		return err
	}

	// A dispatch resource's delete is a no-op that releases the finalizer: the invocation its token names
	// is immutable history, not an object, so there is nothing to tear down and nothing to verify gone.
	// Before the delegated and OAS delete paths, both of which would look for a delete verb a dispatch
	// kind does not declare (#199).
	if handled := h.deleteDispatch(mg, clientInfo, log); handled {
		if err := unstructuredtools.SetConditions(mg, condition.Deleting()); err != nil {
			log.Warn("Setting condition", "error", err)
		}
		return nil
	}

	// If delete is delegated to a RESTAction, invoke the teardown sequence, then VERIFY the resource is
	// actually gone before releasing the finalizer — a RESTAction returns 200 even if a teardown stage
	// failed, so a bare success is not proof of deletion. Hold the finalizer (return the error) on a
	// transport failure or while the resource still exists, so the teardown is retried; release it (return
	// nil) only once verified gone.
	if ref := clientInfo.Resource.DeleteApiRef; ref != nil {
		if err := h.mutateViaRestAction(ctx, mg, ref, clientInfo.Resource.Identifiers, "delete", log); err != nil {
			log.Error(err, "Deleting via RESTAction")
			h.eventRecorder.Event(mg, event.Warning(reasonDeleted, "Delete", err))
			h.emitAudit(ctx, mg, cli.ServerURL(), "", 0, err)
			return err
		}
		h.emitAudit(ctx, mg, cli.ServerURL(), "", 0, nil)
		// One arbiter for every delete path (see resolveDeleteOutcome). The RESTAction reported success,
		// so deleteErr is nil: an unverifiable resource falls back to trusting that result, as before.
		if outcome := h.resolveDeleteOutcome(ctx, cs, clientInfo, mg, nil, log); outcome != nil {
			h.eventRecorder.Event(mg, event.Warning(reasonDeleted, "Delete", outcome))
			return outcome
		}
		if err := unstructuredtools.SetConditions(mg, condition.Deleting()); err != nil {
			log.Warn("Setting condition", "error", err)
		}
		if derr := h.deleteSecretRefRBAC(ctx, mg); derr != nil {
			log.Warn("secretRef RBAC teardown failed, continuing", "error", derr)
		}
		return nil
	}

	apiCall, callInfo, err := builder.APICallBuilder(cli, clientInfo, apiaction.Delete)
	if err != nil {
		log.Error(err, "Building API call")
		return err
	}
	if apiCall == nil || callInfo == nil {
		// A RestDefinition that declares no delete verb is a legitimate, supported shape: a kind whose
		// external resource is deliberately NOT removed by Krateo. This branch IS that outcome -- the
		// finalizer is released and the external resource is left alone on purpose -- so it is logged at
		// Info, naming the deliberate consequence.
		//
		// It used to log at ERROR ("API action delete not found") on this same path. An operator deleting a
		// batch of such CRs saw one ERROR line per resource at exactly the moment their correct instinct was
		// to stop at the first error, and had to be told in advance to ignore them (#291).
		log.Info("No delete verb is declared for this kind, so the external resource is intentionally left in place; releasing the finalizer",
			"kind", mg.GetKind(), "action", apiaction.Delete)
		if derr := h.deleteSecretRefRBAC(ctx, mg); derr != nil {
			log.Warn("secretRef RBAC teardown failed, continuing", "error", derr)
		}
		return nil
	}

	// expectExisting=false: the Role may or may not exist yet (e.g. a CR deleted without ever having gone
	// through Update), and either way EnsureSecretRole's create-or-update is correct here.
	//
	// A failure here is a WARNING on the delete path, not a hard stop. Resolver sources live on the CR's
	// spec, and a CR that never created successfully routinely carries a spec the resolvers cannot read —
	// making this fatal would hold the finalizer for a resource that does not exist. Whether the resource
	// is still addressable is decided below, from the path parameters; if it is, the delete proceeds
	// without the unresolved values, which for a delete request are almost never part of the call.
	resolved, err := h.ensureSecretRefRBACAndResolve(ctx, cs, clientInfo, callInfo.FieldMapping, mg, false)
	if err != nil {
		log.Warn("Provisioning secretRef RBAC / resolving field mapping resolvers, continuing", "error", err)
		resolved = nil
	}

	reqConfiguration := builder.BuildCallConfig(ctx, callInfo, mg, clientInfo.ConfigurationSpec, resolved)
	if reqConfiguration == nil {
		log.Error(fmt.Errorf("error building call configuration"), "Building call configuration")
		return fmt.Errorf("building call configuration")
	}

	// If the delete path still has unpopulated {placeholder}s, the external resource is not addressable —
	// typically because the create never succeeded, so no identifier was ever written to status. Sending the
	// request is impossible (it is rejected before it leaves, "missing path parameter: id"), and returning
	// that error would hold the finalizer on EVERY retry: the CR would sit in Deleting forever and block its
	// namespace with it. Release the finalizer instead.
	//
	// Warn rather than debug: the identifier can also go missing on a resource that DOES exist (e.g. status
	// was lost), and that case orphans the external resource. Holding the finalizer would not save it —
	// nothing can re-derive the identifier — so the operator needs to see it, not be blocked by it.
	if missing := builder.UnresolvedPathParams(callInfo.Path, reqConfiguration.Parameters); len(missing) > 0 {
		log.Warn("External resource is not addressable, releasing finalizer without calling the API",
			"kind", mg.GetKind(), "path", callInfo.Path, "unresolvedPathParams", strings.Join(missing, ","))
		if serr := unstructuredtools.SetConditions(mg, condition.Deleting()); serr != nil {
			log.Warn("Setting condition", "error", serr)
		}
		if derr := h.deleteSecretRefRBAC(ctx, mg); derr != nil {
			log.Warn("secretRef RBAC teardown failed, continuing", "error", derr)
		}
		return nil
	}

	tb, terr := fieldmapping.ApplyRequestTransform(ctx, callInfo.RequestTransform, reqConfiguration.Body)
	if terr != nil {
		log.Error(terr, "Applying requestTransform")
		h.eventRecorder.Event(mg, event.Warning(reasonDeleted, "Delete", terr))
		return terr
	}
	reqConfiguration.Body = tb

	// A 404 on DELETE is the SUCCESS condition, not a failure: it means the resource is already gone.
	// Observe() a few hundred lines above already treats not-found this way; the delete path did not, and
	// that inconsistency was harmless only while the delete was issued exactly once. Holding the finalizer
	// until the resource is verified gone (#77) introduced the retry that reaches it: the first DELETE
	// succeeds, the resource disappears asynchronously, and every retry then 404s, returns an error, and
	// never releases the finalizer -- so the CR hangs in Deleting forever and the resource cannot be
	// deleted through Kubernetes at all (#98). Reported against 0.22.1 on an API with no async block
	// declared for delete.
	// The delete call is now BEST EFFORT: its status code no longer decides the finalizer's fate on its
	// own. Whatever it returns, resolveDeleteOutcome below asks the observe verb and applies one rule.
	response, err := apiCall(ctx, &http.Client{}, callInfo.Path, reqConfiguration)
	// NATIVE Delete, so the vendor's own status is in hand -- unlike the delegated path, which can
	// only see snowplow's. On failure it travels on *restclient.StatusError; on success it is on
	// the Response. Either way this is the real external status code, not a stand-in (#253).
	h.emitAudit(ctx, mg, cli.ServerURL(), callInfo.Method, externalStatusCode(&response, err), err)
	alreadyGone := false
	var deleteErr error
	if err != nil {
		if restclient.IsNotFoundError(err) {
			// Not a special case so much as the common one: every retry after a delete that took effect
			// lands here. Recorded so the async block below is skipped -- there is no operation to poll
			// and `response` holds the 404, not a handle.
			alreadyGone = true
			log.Debug("External resource already absent on delete; treating as deleted", "kind", mg.GetKind())
		} else {
			// Do NOT decide here. A failed delete is not proof the resource survived, any more than a
			// successful one is proof it is gone -- both are proxies. Record the error and let the single
			// arbiter weigh it against what the observe verb reports.
			log.Debug("Delete call failed; deferring to the observe verb", "kind", mg.GetKind(), "error", err.Error())
			deleteErr = err
		}
	}

	// If delete is asynchronous, poll the operation to completion so the resource is actually gone before
	// we report success. PostGet is not meaningful for delete (the resource no longer exists).
	//
	// Skipped when the resource was already absent: there is no operation to poll, and `response` carries
	// the 404 rather than an operation handle, so driving async over it would fail on a delete that has in
	// fact completed.
	if cfg := asyncConfigForAction(clientInfo.Resource.VerbsDescription, string(apiaction.Delete)); cfg != nil && !alreadyGone {
		if _, aerr := driveAsync(ctx, cli, clientInfo, mg, cfg, string(apiaction.Delete), response, reqConfiguration, log); aerr != nil {
			log.Error(aerr, "Driving async delete operation")
			h.eventRecorder.Event(mg, event.Warning(reasonDeleted, "Delete", aerr))
			return aerr
		}
	}

	// VERIFY the resource is actually gone before releasing the finalizer, exactly as the RESTAction branch
	// above does. A 2xx on DELETE means the deletion was REQUESTED, not that it completed: an API that
	// deletes asynchronously and declares no pollable operation in its OAS answers 200 (or 204) immediately
	// and keeps the resource in a "Deleting" state afterwards. Releasing on that response makes the CR
	// disappear while the resource still exists, and if the deletion then fails there is nothing left to
	// retry — the resource is orphaned silently and, for billable kinds, indefinitely.
	//
	// Holding the finalizer (returning the error) means the delete is retried until the resource is
	// verifiably absent. Where an async operation IS declared, driveAsync above has already polled it to
	// completion and this check simply confirms it. Where the API has no get verb to probe with,
	// resolveDeleteOutcome reports "not verifiable" and we trust the delete result, as before.
	// THE decision, for every native delete: success, 404, or any other error. resolveDeleteOutcome asks
	// the observe verb and applies one rule, so no branch above needs to reason about the finalizer.
	if outcome := h.resolveDeleteOutcome(ctx, cs, clientInfo, mg, deleteErr, log); outcome != nil {
		h.eventRecorder.Event(mg, event.Warning(reasonDeleted, "Delete", outcome))
		return outcome
	}

	log.Debug("Setting condition", "kind", mg.GetKind())

	err = unstructuredtools.SetConditions(mg, condition.Deleting())
	if err != nil {
		log.Error(err, "Setting condition")
		return err
	}

	h.eventRecorder.Event(mg, event.Normal(reasonDeleted, "Delete", fmt.Sprintf("Deleted external resource: %s", mg.GetName())))
	if derr := h.deleteSecretRefRBAC(ctx, mg); derr != nil {
		log.Warn("secretRef RBAC teardown failed, continuing", "error", derr)
	}
	return nil
}
