// Dispatch verb support (#199): a fire-and-forget endpoint made safe in a level-based reconciler.
//
// A dispatch-style endpoint (ArgoCD sync, GitHub workflow_dispatch, Jenkins build) fits none of the CRUD
// five. Two POSTs are two runs, so it is not idempotent, and the canonical case answers 204 with an empty
// body, so nothing comes back to key on -- which is also why the async machinery cannot rescue it:
// AsyncConfig/OperationRef extract a handle FROM THE TRIGGER RESPONSE, and there is no handle.
//
// What closes the loop instead is a CR-supplied correlation token that the API echoes back in some
// observable collection. The token IS the resource's identity, which makes observation an ordinary
// lookup: token found => the invocation happened => Ready, and nothing fires; token absent => the
// resource does not exist => the dispatch fires. Exactly once, because its own run is then what stops it.
// Changing the token is therefore a new invocation, not drift.
package restResources

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/krateo-platformops/plumbing/kubeutil/event"
	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/builder"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/unstructured-runtime/pkg/controller"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/krateo-platformops/unstructured-runtime/pkg/tools"
	unstructuredtools "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured"
	"github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	// annotationDispatchedToken records the correlation token this controller has SUCCESSFULLY POSTed for.
	// It is what makes "fire exactly once per token" enforceable rather than merely intended: Observe
	// refuses to report not-exists for a token it already fired, so a dispatch cannot be repeated however
	// long the invocation takes to appear -- or whether it ever appears at all.
	annotationDispatchedToken = "krateo.io/dispatched-token"
	// annotationDispatchObserveMisses counts reconciles since the dispatch fired in which the token has not
	// yet turned up in the collection. Eventual consistency is normal and this tolerates it; a persistent
	// miss is a declaration defect and is eventually reported as such rather than waited on forever.
	annotationDispatchObserveMisses = "krateo.io/dispatch-observe-misses"
	// maxDispatchObserveMisses is where "still appearing" stops being the likelier explanation than "the
	// echo path does not match". Deliberately a diagnosis deadline and not a retry budget -- nothing is
	// re-POSTed when it passes.
	maxDispatchObserveMisses = 5
)

// dispatchVerb returns the resource's dispatch verb, or nil when it declares none. At most one is
// admitted (CEL), so the first match is the only one.
func dispatchVerb(info *getter.Info) *getter.VerbsDescription {
	if info == nil {
		return nil
	}
	for i := range info.Resource.VerbsDescription {
		if strings.EqualFold(info.Resource.VerbsDescription[i].Action, apiaction.Dispatch.String()) {
			return &info.Resource.VerbsDescription[i]
		}
	}
	return nil
}

// correlationToken reads the token the CR supplies, as a string.
//
// Compared as a STRING rather than with DeepEqual against the echoed value, unlike findby identifier
// matching. A token's whole job is to be a round-tripped opaque label, and it crosses a JSON boundary on
// the way out and back: a number the CR holds as int64 returns as float64, and an identity comparison
// would then miss its own token and fire the dispatch again. Stringifying both sides is what makes the
// round trip survivable.
func correlationToken(mg *unstructured.Unstructured, fromSpec string) (string, bool) {
	if mg == nil || fromSpec == "" {
		return "", false
	}
	path := append([]string{"spec"}, strings.Split(fromSpec, ".")...)
	v, found, err := unstructured.NestedFieldNoCopy(mg.Object, path...)
	if err != nil || !found || v == nil {
		return "", false
	}
	return fmt.Sprintf("%v", v), true
}

// echoedToken reads the token back out of ONE item of the correlation collection.
func echoedToken(item map[string]interface{}, echoedAt string) (string, bool) {
	if item == nil || echoedAt == "" {
		return "", false
	}
	// A leading dot is the documented dialect for these paths (".inputs.correlationId"), and writing it is
	// natural enough that accepting both spellings costs nothing and prevents a silent never-match.
	path := strings.Split(strings.TrimPrefix(echoedAt, "."), ".")
	v, found, err := unstructured.NestedFieldNoCopy(item, path...)
	if err != nil || !found || v == nil {
		return "", false
	}
	return fmt.Sprintf("%v", v), true
}

// findCorrelatedItem returns the item of the collection whose echoed value equals the CR's token.
//
// A match must be UNIQUE. Two items echoing one token means the token is not identifying a single
// invocation, which is the premise the whole verb rests on -- and the remedy for "not found" is to POST
// again, so quietly taking the first (apiLookup's items[0], the defect that got it removed) would turn an
// ambiguous collection into a duplicate-run loop. An ambiguous match is therefore a hard error, not a
// miss and not a pick.
func findCorrelatedItem(items []interface{}, echoedAt, token string) (map[string]interface{}, bool, error) {
	var found map[string]interface{}
	matches := 0
	for _, raw := range items {
		item, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if echoed, ok := echoedToken(item, echoedAt); ok && echoed == token {
			matches++
			if found == nil {
				found = item
			}
		}
	}
	switch {
	case matches == 0:
		return nil, false, nil
	case matches > 1:
		return nil, false, fmt.Errorf(
			"correlation is ambiguous: %d items in the collection echo the token %q at %q, so it does not "+
				"identify a single invocation; narrow the collection (a query parameter on correlation.observe.path) "+
				"or choose a token that is unique per invocation", matches, token, echoedAt)
	default:
		return found, true, nil
	}
}

// observeDispatch observes a dispatch resource by looking its correlation token up in the declared
// collection. handled=false means the resource declares no dispatch verb, so the caller falls through to
// the ordinary get/findby observe.
//
// A resource with BOTH a dispatch and a get/findby is observed here, not there: the dispatch's token is
// what decides whether this invocation happened, which is the only question whose answer can make the
// reconciler POST.
func (h *handler) observeDispatch(ctx context.Context, mg *unstructured.Unstructured, cli restclient.UnstructuredClientInterface, httpCli *http.Client, clientInfo *getter.Info, log logging.Logger) (controller.ExternalObservation, bool, error) {
	verb := dispatchVerb(clientInfo)
	if verb == nil {
		return controller.ExternalObservation{}, false, nil
	}
	if verb.Correlation == nil {
		// Unreachable through admission (CEL requires correlation on a dispatch), so this is a
		// defence-in-depth error rather than a tolerated state: without it the resource would report
		// not-found forever and POST on every reconcile.
		return controller.ExternalObservation{}, true, fmt.Errorf(
			"resource declares a dispatch verb without correlation, so an invocation could never be observed")
	}
	c := verb.Correlation

	token, ok := correlationToken(mg, c.FromSpec)
	if !ok || token == "" {
		// No token in the spec. NOT an error and NOT "does not exist": firing a dispatch whose invocation
		// can never be found again is the one thing this verb is designed to prevent, so the resource is
		// held instead, with the reason on the CR rather than only in the log.
		log.Debug("Dispatch resource has no correlation token in spec; not dispatching",
			"fromSpec", c.FromSpec)
		cond := condition.Unavailable()
		cond.Reason = "CorrelationTokenMissing"
		cond.Message = fmt.Sprintf("spec.%s is empty, so a dispatched invocation could never be correlated; set it to a value unique to this invocation", c.FromSpec)
		if err := unstructuredtools.SetConditions(mg, cond); err != nil {
			return controller.ExternalObservation{}, true, err
		}
		if _, err := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
			Pluralizer:    h.pluralizer,
			DynamicClient: h.dynamicClient,
		}); err != nil {
			return controller.ExternalObservation{}, true, err
		}
		return controller.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, true, nil
	}

	params, query, headers, cookies, err := cli.RequestedParams(c.Observe.Method, c.Observe.Path)
	if err != nil {
		return controller.ExternalObservation{}, true, fmt.Errorf("correlation.observe: retrieving requested params: %w", err)
	}
	callInfo := &builder.CallInfo{
		Path:   c.Observe.Path,
		Method: c.Observe.Method,
		// FindBy, because the collection read is one: it must receive the configuration fields scoped to a
		// read, not to the dispatch's own POST.
		Action: apiaction.FindBy,
		ReqParams: &builder.RequestedParams{
			Parameters: params,
			Query:      query,
			Headers:    headers,
			Cookies:    cookies,
		},
		IdentifierFields: clientInfo.Resource.Identifiers,
	}
	conf := builder.BuildCallConfig(ctx, callInfo, mg, clientInfo.ConfigurationSpec, nil)
	if conf == nil {
		return controller.ExternalObservation{}, true, fmt.Errorf("correlation.observe: could not build the call configuration")
	}

	response, err := cli.Call(ctx, httpCli, c.Observe.Path, conf)
	if err != nil {
		if restclient.IsNotFoundError(err) {
			// The collection itself is absent, which is not the same as the token being absent. Treating it
			// as not-found would dispatch against an endpoint that just told us it is not there.
			return controller.ExternalObservation{}, true, fmt.Errorf(
				"correlation.observe: collection %s %s not found: %w", c.Observe.Method, c.Observe.Path, err)
		}
		return controller.ExternalObservation{}, true, err
	}

	items, err := restclient.ExtractItemsFromResponse(response.ResponseBody, c.Observe.ItemsPath)
	if err != nil {
		return controller.ExternalObservation{}, true, fmt.Errorf("correlation.observe: %w", err)
	}

	item, found, err := findCorrelatedItem(items, c.EchoedAt, token)
	if err != nil {
		return controller.ExternalObservation{}, true, err
	}
	if !found {
		// Already fired for THIS token? Then it must not be fired again, and that is the whole guarantee.
		//
		// Returning not-exists here is what the reconciler acts on by calling Create, so doing it a second
		// time for one token is the duplicate run this verb exists to prevent -- and it would happen on
		// every reconcile, forever, for a cause as small as a mistyped echoedAt. Generation now refuses the
		// echo paths it can check, but it cannot check a path into a free-form object, so this is the
		// backstop for the case that gets through.
		//
		// A FAILED post deliberately leaves no annotation (createDispatch records it only on success), so a
		// dispatch that never actually happened is still retried. Fire-once applies to invocations that
		// occurred, not to attempts.
		if alreadyDispatched(mg, token) {
			misses := dispatchObserveMisses(mg) + 1
			if err := h.patchAnnotations(ctx, mg, map[string]interface{}{
				annotationDispatchObserveMisses: strconv.Itoa(misses),
			}); err != nil {
				return controller.ExternalObservation{}, true, err
			}

			cond := condition.Creating()
			if misses >= maxDispatchObserveMisses {
				cond = condition.Unavailable()
				cond.Reason = "DispatchedButNotObserved"
				cond.Message = fmt.Sprintf(
					"dispatched with correlation token %q but it has not appeared at %q in %s %s after %d checks; "+
						"the most likely cause is that correlation.echoedAt does not match where this API echoes the "+
						"token. NOT re-dispatching: the invocation may well have happened",
					token, c.EchoedAt, c.Observe.Method, c.Observe.Path, misses)
				log.Info("Dispatched invocation never appeared; not re-dispatching",
					"token", token, "echoedAt", c.EchoedAt, "checks", misses)
			} else {
				cond.Message = fmt.Sprintf("dispatched with correlation token %q; waiting for it to appear in %s %s",
					token, c.Observe.Method, c.Observe.Path)
				log.Debug("Dispatched invocation not yet visible", "token", token, "checks", misses)
			}
			if err := unstructuredtools.SetConditions(mg, cond); err != nil {
				return controller.ExternalObservation{}, true, err
			}
			if _, err := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
				Pluralizer:    h.pluralizer,
				DynamicClient: h.dynamicClient,
			}); err != nil {
				return controller.ExternalObservation{}, true, err
			}
			// exists=true so Create is NOT called again. upToDate=true because there is no update that
			// could help: the only action available would be to re-POST.
			return controller.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, true, nil
		}

		log.Debug("Correlated invocation not found; the dispatch will fire", "token", token)
		return controller.ExternalObservation{ResourceExists: false, ResourceUpToDate: false}, true, nil
	}

	// Found. The invocation exists, so the resource is up-to-date BY DEFINITION: the token identifies it,
	// and no other part of the spec can be reconciled towards a run that has already been triggered. This
	// is why compareScope is refused on a dispatch resource -- any other scope would report drift whose
	// only available remedy is to POST again.
	log.Debug("Correlated invocation found", "token", token)
	if dispatchObserveMisses(mg) > 0 {
		if err := h.patchAnnotations(ctx, mg, map[string]interface{}{annotationDispatchObserveMisses: nil}); err != nil {
			return controller.ExternalObservation{}, true, err
		}
	}
	if err := unstructuredtools.SetConditions(mg, condition.Available()); err != nil {
		return controller.ExternalObservation{}, true, err
	}
	// The matched ITEM, not the collection, feeds status -- the same shape findby projects, so identifiers
	// and additionalStatusFields resolve against the run that was correlated. This is also why generation
	// builds a dispatch kind's status schema from an item of this collection: both sides must describe the
	// same object or the CRD documents fields the controller never writes.
	if err := populateStatusFields(clientInfo, mg, item); err != nil {
		return controller.ExternalObservation{}, true, fmt.Errorf("populating status from the correlated item: %w", err)
	}
	if _, err := tools.UpdateStatus(ctx, mg, tools.UpdateOptions{
		Pluralizer:    h.pluralizer,
		DynamicClient: h.dynamicClient,
	}); err != nil {
		return controller.ExternalObservation{}, true, err
	}
	return controller.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, true, nil
}

// createDispatch fires the dispatch verb. handled=false when the resource declares none, so the caller
// falls through to the ordinary create.
//
// There is no convergence check to do afterwards and none is attempted: the next Observe looks the token
// up in the collection, and that is the only evidence that the invocation happened. A dispatch that
// returns 204 with an empty body gives this call nothing else to verify against, which is the entire
// premise of the verb.
func (h *handler) createDispatch(ctx context.Context, mg *unstructured.Unstructured, cli restclient.UnstructuredClientInterface, httpCli *http.Client, clientInfo *getter.Info, log logging.Logger) (bool, error) {
	verb := dispatchVerb(clientInfo)
	if verb == nil {
		return false, nil
	}

	// Refuse to fire without a token. Observe already holds such a resource, so this is unreachable by
	// that route -- but Create is also entered directly after a not-found, and a dispatch fired with no
	// correlatable token can never be observed again, so it would be re-fired on every single reconcile.
	// That is the duplicate-run loop this verb exists to make impossible; it must not be reachable by any
	// path.
	if verb.Correlation == nil {
		return true, fmt.Errorf("resource declares a dispatch verb without correlation, so it must not be fired")
	}
	token, ok := correlationToken(mg, verb.Correlation.FromSpec)
	if !ok || token == "" {
		return true, fmt.Errorf(
			"refusing to dispatch: spec.%s carries no correlation token, so the invocation could never be "+
				"observed and would be re-fired on every reconcile", verb.Correlation.FromSpec)
	}

	apiCall, callInfo, err := builder.APICallBuilder(cli, clientInfo, apiaction.Dispatch)
	if err != nil {
		return true, fmt.Errorf("building the dispatch call: %w", err)
	}
	if apiCall == nil {
		return true, fmt.Errorf("no dispatch API call could be built for the declared verb")
	}
	conf := builder.BuildCallConfig(ctx, callInfo, mg, clientInfo.ConfigurationSpec, nil)
	if conf == nil {
		return true, fmt.Errorf("could not build the dispatch call configuration")
	}

	log.Debug("Dispatching", "method", callInfo.Method, "path", callInfo.Path, "token", token)
	if _, err := apiCall(ctx, httpCli, callInfo.Path, conf); err != nil {
		return true, err
	}
	// Record the token ONLY now, after the POST succeeded. This is what Observe reads to refuse a second
	// dispatch for the same token; writing it before the call would strand a resource whose dispatch never
	// actually happened, and writing it on failure would do the same.
	if err := h.patchAnnotations(ctx, mg, map[string]interface{}{
		annotationDispatchedToken:       token,
		annotationDispatchObserveMisses: nil,
	}); err != nil {
		return true, fmt.Errorf("recording the dispatched correlation token (the dispatch itself succeeded): %w", err)
	}
	h.eventRecorder.Event(mg, event.Normal(reasonCreated, "Dispatch", fmt.Sprintf("Dispatched %s %s for: %s (correlation token %s)", callInfo.Method, callInfo.Path, mg.GetName(), token)))
	return true, nil
}

// deleteDispatch is the no-op delete of a dispatch resource. handled=false when the resource declares no
// dispatch verb.
//
// Deliberately a no-op that lets the finalizer be released. The invocation the token names is immutable
// history -- a triggered run, not an object -- so there is nothing to tear down, and a delete that tried
// to would be inventing a semantic the vendor does not have. Returning an error instead would strand the
// CR undeletable for a resource whose deletion is meaningful locally and meaningless remotely.
func (h *handler) deleteDispatch(mg *unstructured.Unstructured, clientInfo *getter.Info, log logging.Logger) bool {
	if dispatchVerb(clientInfo) == nil {
		return false
	}
	log.Debug("Dispatch resource deleted; the dispatched invocation is history and is left untouched")
	h.eventRecorder.Event(mg, event.Normal(reasonDeleted, "Delete", fmt.Sprintf("Removed dispatch resource %s; the dispatched invocation is history and was left untouched", mg.GetName())))
	return true
}

// dispatchObserveMisses reads the miss counter, treating anything unparsable as zero -- the counter is a
// diagnosis aid, so a corrupt value must not be able to fail a reconcile.
func dispatchObserveMisses(mg *unstructured.Unstructured) int {
	n, err := strconv.Atoi(mg.GetAnnotations()[annotationDispatchObserveMisses])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// alreadyDispatched reports whether this controller has successfully POSTed for exactly this token.
//
// The token comparison is what makes the guarantee per-INVOCATION rather than per-resource: changing the
// token in the spec is a new invocation and must fire, while leaving it alone must never fire twice. An
// empty token never counts as dispatched, so a resource whose token was removed cannot inherit the
// previous one's fired state.
func alreadyDispatched(mg *unstructured.Unstructured, token string) bool {
	if mg == nil || token == "" {
		return false
	}
	return mg.GetAnnotations()[annotationDispatchedToken] == token
}
