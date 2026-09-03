package restResources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

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
	"github.com/krateo-platformops/unstructured-runtime/pkg/meta"
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
}

// SetEventRecorder wires a Kubernetes Event recorder used to emit Events on the
// reconciled dynamic CR. A nil recorder is ignored (the no-op default is kept).
func (h *handler) SetEventRecorder(rec event.Recorder) {
	if rec != nil {
		h.eventRecorder = rec
	}
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

	cli, err := restclient.BuildClient(ctx, h.dynamicClient, clientInfo.URL)
	if err != nil {
		log.Error(err, "Building REST client")
		return controller.ExternalObservation{}, err
	}

	// Set client properties
	cli.Debug = meta.IsVerbose(mg)
	cli.PrettyJSON = h.prettyJSONDebug
	cli.Resource = mg
	cli.SetAuth = clientInfo.SetAuth
	cli.IdentifierFields = clientInfo.Resource.Identifiers // TODO: probably redundant since we pass the resource too (`cli.Resource = mg`)
	// Loop VerbsDescription, if `findby` action set, then check if IdentifiersMatchPolicy is set, if so, set it in the client
	for _, verb := range clientInfo.Resource.VerbsDescription {
		if verb.Action == string(apiaction.FindBy) && verb.IdentifiersMatchPolicy != "" {
			cli.IdentifiersMatchPolicy = verb.IdentifiersMatchPolicy
			log.Debug("Found findby action and a IdentifiersMatchPolicy configured in RestDefinition", "policy", cli.IdentifiersMatchPolicy)
			break
		}
	}

	// If a requeue-based (Model B) async operation is in flight, poll it once before the normal existence
	// checks: stay pending until it terminates, then (on success) fall through to observe the real state.
	if opID, action := asyncOperationInFlight(mg); opID != "" {
		obs, handled, aerr := h.driveAsyncRequeue(ctx, cli, clientInfo, mg, opID, action, log)
		if handled {
			return obs, aerr
		}
	}

	var response restclient.Response
	// Tries to tries to build the `get` action API Call, with the given statusFields and specFields values.
	// If it is able to validate the `get` action request, returns true
	isKnown := builder.IsResourceKnown(cli, clientInfo, mg)
	// The observed body comes from exactly one verb (get XOR findby); response normalization must use only
	// that verb's fieldMapping, never both, or the other verb's transforms would be misapplied to it.
	observeAction := "get"
	if isKnown {
		// Resource is known: getting the external resource by its identifier (e.g GET /resources/{id}).
		apiCall, callInfo, err := builder.APICallBuilder(cli, clientInfo, apiaction.Get)
		if apiCall == nil || callInfo == nil {
			log.Error(fmt.Errorf("API action get not found"), "action", apiaction.Get)
			return controller.ExternalObservation{}, fmt.Errorf("API action get not found for %s", apiaction.Get)
		}
		if err != nil {
			log.Error(err, "Building API call")
			return controller.ExternalObservation{}, err
		}
		reqConfiguration := builder.BuildCallConfig(callInfo, mg, clientInfo.ConfigurationSpec, nil)
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
		apiCall, callInfo, err := builder.APICallBuilder(cli, clientInfo, apiaction.FindBy)
		if apiCall == nil {
			if !unstructuredtools.IsConditionSet(mg, condition.Creating()) && !unstructuredtools.IsConditionSet(mg, condition.Available()) {
				log.Debug("No get or findby action found for the resource.")
				log.Debug("External resource is being created", "kind", mg.GetKind())
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
		reqConfiguration := builder.BuildCallConfig(callInfo, mg, clientInfo.ConfigurationSpec, nil)
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
	if err := fieldmapping.NormalizeResponseBody(ctx, clientInfo.Resource.VerbsDescription, []string{observeAction}, b); err != nil {
		log.Error(err, "Normalizing response body (fieldMapping)")
		return controller.ExternalObservation{}, err
	}
	if b != nil {
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
				updatable, uerr = cli.UpdatableBodyPaths(v.Method, v.Path)
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
			cond := condition.Unavailable()
			cond.Reason = fmt.Sprintf("Resource is not up-to-date due to %s", res.String())
			unstructuredtools.SetConditions(mg, cond)
			log.Debug("External resource not up-to-date", "kind", mg.GetKind(), "reason", res.String())
			return controller.ExternalObservation{
				ResourceExists:   true,
				ResourceUpToDate: false,
			}, nil
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

func (h *handler) Create(ctx context.Context, mg *unstructured.Unstructured) error {
	log := h.logger.WithValues("op", "Create").
		WithValues("apiVersion", mg.GetAPIVersion()).
		WithValues("kind", mg.GetKind()).
		WithValues("name", mg.GetName()).
		WithValues("namespace", mg.GetNamespace())

	if h.swaggerInfoGetter == nil {
		return fmt.Errorf("swagger info getter must be specified")
	}

	clientInfo, err := h.swaggerInfoGetter.Get(mg)
	if err != nil {
		log.Error(err, "Getting REST client info")
		return err
	}

	cli, err := restclient.BuildClient(ctx, h.dynamicClient, clientInfo.URL)
	if err != nil {
		log.Error(err, "Building REST client")
		return err
	}
	cli.Debug = meta.IsVerbose(mg)
	cli.PrettyJSON = h.prettyJSONDebug
	cli.SetAuth = clientInfo.SetAuth

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
			return err
		}
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
	resolved, err := h.ensureSecretRefRBACAndResolve(ctx, clientInfo, callInfo.FieldMapping, mg, false)
	if err != nil {
		log.Error(err, "Provisioning secretRef RBAC / resolving field mapping resolvers")
		return err
	}

	reqConfiguration := builder.BuildCallConfig(callInfo, mg, clientInfo.ConfigurationSpec, resolved)
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
	if err != nil {
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

	cli, err := restclient.BuildClient(ctx, h.dynamicClient, clientInfo.URL)
	if err != nil {
		log.Error(err, "Building REST client")
		return err
	}
	cli.Debug = meta.IsVerbose(mg)
	cli.PrettyJSON = h.prettyJSONDebug
	cli.SetAuth = clientInfo.SetAuth

	// If update is delegated to a RESTAction, invoke the re-apply sequence instead of the update verb (it
	// forwards the whole spec — the desired state — and must be idempotent), project any composed result,
	// and return.
	if ref := clientInfo.Resource.UpdateApiRef; ref != nil {
		if err := h.mutateViaRestAction(ctx, mg, ref, clientInfo.Resource.Identifiers, "update", log); err != nil {
			log.Error(err, "Updating via RESTAction")
			h.eventRecorder.Event(mg, event.Warning(reasonUpdated, "Update", err))
			return err
		}
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
	resolved, err := h.ensureSecretRefRBACAndResolve(ctx, clientInfo, callInfo.FieldMapping, mg, true)
	if err != nil {
		log.Error(err, "Refreshing secretRef RBAC / resolving field mapping resolvers")
		return err
	}

	reqConfiguration := builder.BuildCallConfig(callInfo, mg, clientInfo.ConfigurationSpec, resolved)
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
	if err != nil {
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
	} else { // Set creating condition if not pending (using Creating condition for updates as well since no Update condition exists)
		err = unstructuredtools.SetConditions(mg, condition.Creating())
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

	cli, err := restclient.BuildClient(ctx, h.dynamicClient, clientInfo.URL)
	if err != nil {
		log.Error(err, "Building REST client")
		return err
	}
	cli.Debug = meta.IsVerbose(mg)
	cli.PrettyJSON = h.prettyJSONDebug
	cli.SetAuth = clientInfo.SetAuth

	// If delete is delegated to a RESTAction, invoke the teardown sequence, then VERIFY the resource is
	// actually gone before releasing the finalizer — a RESTAction returns 200 even if a teardown stage
	// failed, so a bare success is not proof of deletion. Hold the finalizer (return the error) on a
	// transport failure or while the resource still exists, so the teardown is retried; release it (return
	// nil) only once verified gone.
	if ref := clientInfo.Resource.DeleteApiRef; ref != nil {
		if err := h.mutateViaRestAction(ctx, mg, ref, clientInfo.Resource.Identifiers, "delete", log); err != nil {
			log.Error(err, "Deleting via RESTAction")
			h.eventRecorder.Event(mg, event.Warning(reasonDeleted, "Delete", err))
			return err
		}
		// `verified` is ignored here on purpose: this branch runs after the RESTAction reported success,
		// so no get verb means trusting that result -- the documented limitation, unchanged.
		stillExists, _, verr := h.externalResourceStillExists(ctx, cli, clientInfo, mg, log)
		if verr != nil {
			h.eventRecorder.Event(mg, event.Warning(reasonDeleted, "Delete", verr))
			return verr
		}
		if stillExists {
			err := fmt.Errorf("delete RESTAction %s/%s completed but the external resource is still present; retrying", ref.Namespace, ref.Name)
			h.eventRecorder.Event(mg, event.Warning(reasonDeleted, "Delete", err))
			return err
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
		log.Error(fmt.Errorf("API action delete not found"), "action", apiaction.Delete)
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
	resolved, err := h.ensureSecretRefRBACAndResolve(ctx, clientInfo, callInfo.FieldMapping, mg, false)
	if err != nil {
		log.Warn("Provisioning secretRef RBAC / resolving field mapping resolvers, continuing", "error", err)
		resolved = nil
	}

	reqConfiguration := builder.BuildCallConfig(callInfo, mg, clientInfo.ConfigurationSpec, resolved)
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
	response, err := apiCall(ctx, &http.Client{}, callInfo.Path, reqConfiguration)
	alreadyGone := false
	if err != nil {
		if restclient.IsNotFoundError(err) {
			alreadyGone = true
			log.Debug("External resource already absent on delete; treating as deleted", "kind", mg.GetKind())
		} else {
			// The status code of a DELETE is a PROXY for whether the resource is gone; the observe verb is
			// the ground truth, and the controller already knows how to ask it. Returning here without
			// asking is what left the existence check below unreachable on any non-404 error, so an API
			// that answers a delete with, say, 400 for a resource it has already removed hung the CR in
			// Deleting forever (#101 -- observed on Aruba security/Kms, where DELETE returned 400
			// "Some kms keys are not deleted" while a direct GET returned 404).
			//
			// Only an AFFIRMATIVE, verified absence releases here. If there is no get verb, or the get
			// itself failed, `verified` is false and the original delete error stands: "could not check"
			// must mean retry, never "assume gone" -- assuming would orphan the resource, which is the
			// failure #77 exists to prevent.
			stillExists, verified, verr := h.externalResourceStillExists(ctx, cli, clientInfo, mg, log)
			if verr == nil && verified && !stillExists {
				alreadyGone = true
				log.Info("Delete returned an error but the external resource is verifiably absent; releasing finalizer",
					"kind", mg.GetKind(), "deleteError", err.Error())
			} else {
				log.Error(err, "Performing REST call")
				h.eventRecorder.Event(mg, event.Warning(reasonDeleted, "Delete", err))
				return err
			}
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
	// externalResourceStillExists reports (false, nil) and we trust the delete result, as before.
	// `verified` ignored: reached only when the DELETE itself succeeded, so an unverifiable resource
	// falls back to trusting that success, as before.
	stillExists, _, verr := h.externalResourceStillExists(ctx, cli, clientInfo, mg, log)
	if verr != nil {
		log.Error(verr, "Verifying deletion")
		h.eventRecorder.Event(mg, event.Warning(reasonDeleted, "Delete", verr))
		return verr
	}
	if stillExists {
		err := fmt.Errorf("delete of %s returned success but the external resource is still present; retrying", mg.GetName())
		log.Debug("External resource still present after delete; holding finalizer", "kind", mg.GetKind())
		h.eventRecorder.Event(mg, event.Warning(reasonDeleted, "Delete", err))
		return err
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
