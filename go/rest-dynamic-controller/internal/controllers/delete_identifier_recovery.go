package restResources

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/krateo-platformops/plumbing/kubeutil/event"
	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/builder"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/pathparsing"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/krateo-platformops/unstructured-runtime/pkg/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// reasonPossibleOrphan is the Event reason an alert keys on. It names the SYMPTOM an operator cares about
// -- an external object that may still exist -- rather than the mechanism, because whoever reads it has
// diagnosed nothing yet.
const reasonPossibleOrphan event.Reason = "OrphanedExternalResourcePossible"

// createOutcome is what the create-tracking annotations say about whether an external object plausibly
// exists. It exists so the delete path can stop treating "the identifier is missing" as one situation.
type createOutcome int

const (
	// createNeverAttempted: no pending marker was ever set, so no external call was made under this CR.
	createNeverAttempted createOutcome = iota
	// createSucceeded: a create landed. Something probably exists at the vendor.
	createSucceeded
	// createFailed: the last create attempt failed, so nothing was left behind by it.
	createFailed
	// createIncomplete: a create was attempted and neither outcome was recorded.
	//
	// UNREACHABLE FROM THE DELETE PATH, and that is a fact about unstructured-runtime's ORDERING rather
	// than about this code: worker.go's recovery block runs BEFORE the Create/Update/Delete dispatch and
	// RESOLVES this state -- on a readable external resource it writes SetExternalCreateSucceeded, on a
	// definitely-absent one it removes the pending marker, and on an unreadable one it returns early so no
	// delete is dispatched at all. So by the time Delete runs the state is always determinate.
	//
	// Kept as a named case anyway, because "the runtime handles it" is exactly the kind of premise that
	// silently stops being true on a dependency bump. A test asserts the ordering.
	createIncomplete
)

// classifyCreateOutcome reads the three create-tracking annotations as one answer.
//
// The annotations are second-resolution timestamps and all three can coexist, so PRESENCE is not the
// test -- ORDER is. meta.ExternalCreateIncomplete already encodes that comparison; this adds the
// distinction between "never started" and "finished, one way or the other", which it folds together.
func classifyCreateOutcome(mg *unstructured.Unstructured) createOutcome {
	pending := meta.GetExternalCreatePending(mg)
	if pending.IsZero() {
		return createNeverAttempted
	}
	if meta.ExternalCreateIncomplete(mg) {
		return createIncomplete
	}
	if meta.GetExternalCreateFailed(mg).After(meta.GetExternalCreateSucceeded(mg)) {
		return createFailed
	}
	return createSucceeded
}

// String names the outcome in a log line. A bare integer would make the one field that explains WHY the
// finalizer was released unreadable without the source.
func (o createOutcome) String() string {
	switch o {
	case createNeverAttempted:
		return "neverAttempted"
	case createSucceeded:
		return "succeeded"
	case createFailed:
		return "failed"
	case createIncomplete:
		return "incomplete"
	}
	return "unknown"
}

// shouldReportPossibleOrphan decides whether releasing the finalizer leaves an external object that
// someone needs to hear about.
//
// EXTRACTED so the decision can be exercised exhaustively. It is four lines of branching inside a function
// that needs a live API, an OAS document and a dynamic client to reach, and the cost of that is a decision
// nobody can test cheaply -- which is how the single honest behaviour it replaces survived.
//
// The pairing that matters is the third row. A SYNTHESISED 404 (findby listed and nothing matched) arrives
// as a non-nil error and warns; the VENDOR's own 404 arrives as (false, nil) and does not. Collapsing them
// would either cry wolf on a resource proven absent -- training an operator to ignore the one case that
// matters -- or stay silent on one that may exist, which is the leak itself.
//
//	outcome          recoveryErr   report?   because
//	succeeded        non-nil       YES       a create landed and we could not address or re-derive it
//	succeeded        nil           no        the vendor said not-found. Proven absent; nothing survives.
//	neverAttempted   any           no        no create was made under this CR, so nothing was left behind
//	failed           any           no        the last create did not land
//	incomplete       any           no        unreachable; see classifyCreateOutcome
func shouldReportPossibleOrphan(outcome createOutcome, recoveryErr error) bool {
	return outcome == createSucceeded && recoveryErr != nil
}

// identifierSummary renders the CR's declared identifiers and their values, for the Event that names a
// possible orphan.
//
// FROM THE CR, NEVER FROM THE VENDOR. These paths are already readable by anyone who can `get` the
// resource, so naming them in an Event discloses nothing new -- which is NOT true of a vendor response
// body, and is why none of one appears here. A returned error reaches a condition message verbatim
// through ReconcileError, and a vendor body can carry things the CR does not.
//
// An identifier with no value is still NAMED, as `<name>=<unset>`. An alert that says which field was
// missing is more use than one that silently drops it, and the absence is itself the diagnosis.
func identifierSummary(clientInfo *getter.Info, mg *unstructured.Unstructured) string {
	if clientInfo == nil || mg == nil || len(clientInfo.Resource.Identifiers) == 0 {
		return "no identifiers declared"
	}
	parts := make([]string, 0, len(clientInfo.Resource.Identifiers))
	for _, id := range clientInfo.Resource.Identifiers {
		val := "<unset>"
		// status first: for a server-assigned identifier that is where it lands. spec second: a
		// user-supplied identifier such as a username lives there, and it is the case that makes this
		// recovery possible at all.
		if s, ok, _ := unstructured.NestedString(mg.Object, "status", id); ok && s != "" {
			val = s
		} else if s, ok, _ := unstructured.NestedString(mg.Object, "spec", id); ok && s != "" {
			val = s
		}
		parts = append(parts, fmt.Sprintf("%s=%s", id, val))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// recoverDeleteIdentifier tries to make an unaddressable resource addressable again, by running the
// kind's own findby and writing what it returns into the CR's status IN MEMORY (#548).
//
// WHY THIS IS NOT A NEW CAPABILITY. The comment this fix replaces dismissed the orphan case because
// "nothing can re-derive the identifier". That is false twice over. The kind declares findby, whose
// identifiers routinely live on spec and are still there at delete time -- and more directly, THIS
// CONTROLLER HAS ALREADY DONE IT: unstructured-runtime's incomplete-create recovery calls
// externalResourceExists, which prefers our ExistenceChecker, whose implementation resolves the
// not-addressable case by running findby, and which on success causes SetExternalCreateSucceeded. So in
// the window this issue is about, the controller resolved the resource by findby, wrote down that it had,
// and the delete path then released the finalizer without consulting either the note or the mechanism.
//
// Returns (true, nil) only when the status now carries enough for the caller to rebuild an addressable
// delete request. Everything else is (false, ...) and the caller reports rather than retries: holding a
// finalizer here turns one vendor hiccup into a CR that cannot be deleted and a namespace that cannot be
// emptied, which a consumer measured as needing a human.
func (h *handler) recoverDeleteIdentifier(
	ctx context.Context,
	cs *clientSet,
	clientInfo *getter.Info,
	mg *unstructured.Unstructured,
	log logging.Logger,
) (bool, error) {
	fcli, ferr := cs.For(ctx, string(apiaction.FindBy))
	if ferr != nil {
		return false, fmt.Errorf("building the findby client: %w", ferr)
	}

	apiCall, callInfo, berr := builder.APICallBuilder(fcli, clientInfo, apiaction.FindBy)
	if berr != nil {
		return false, fmt.Errorf("building the findby call: %w", berr)
	}
	if apiCall == nil || callInfo == nil {
		// No findby verb. This is the case the original comment was written for and it is still honest:
		// there is genuinely no second way to address the resource.
		return false, fmt.Errorf("this kind declares no findby verb, so the identifier cannot be re-derived")
	}

	reqConfiguration := builder.BuildCallConfig(ctx, callInfo, mg, clientInfo.ConfigurationSpec, nil)
	if reqConfiguration == nil {
		return false, fmt.Errorf("could not build the findby call configuration")
	}
	if reqConfiguration.BuildErr != nil {
		return false, fmt.Errorf("assembling the findby request: %w", reqConfiguration.BuildErr)
	}
	if missing := pathparsing.UnresolvedPathParams(callInfo.Path, reqConfiguration.Parameters); len(missing) > 0 {
		return false, fmt.Errorf("the findby path %q is itself unresolved (%v)", callInfo.Path, missing)
	}

	log.Debug("Delete: resolving the identifier through findby before releasing", "path", callInfo.Path)
	response, callErr := apiCall(ctx, &http.Client{}, callInfo.Path, reqConfiguration)

	switch {
	case callErr == nil:
		// fall through to the status write below
	case restclient.IsSynthesisedNotFound(callErr):
		// A 404 WE INVENTED: findby listed successfully and nothing matched. Checked BEFORE the vendor-404
		// case, because IsNotFoundError is true for both.
		//
		// "Nothing matched" is NOT proof of absence, and on this path believing it is the whole defect
		// repeated. The matching can fail from a truncated listing, an envelope resolving to the wrong
		// collection, or identifiers absent from the item shape -- none of which is absence. Treating it as
		// "already gone" releases the finalizer on a resource that exists, which is exactly the leak this
		// fix exists to stop, and is the mirror of #371.
		return false, fmt.Errorf("findby listed successfully but nothing matched, which is not proof of absence: %w", callErr)
	case restclient.IsNotFoundError(callErr):
		// The VENDOR said not-found. That IS proof of absence, so there is nothing to orphan and nothing
		// to warn about -- releasing is correct and quiet. Distinguished from the synthesised case above
		// precisely so this path does not cry wolf: an Event on a resource that provably does not exist
		// trains an operator to ignore the one that matters.
		log.Info("Delete: the vendor reports the external resource does not exist; releasing the finalizer",
			"kind", mg.GetKind(), "name", mg.GetName())
		return false, nil
	default:
		logVendorBody(log, "findby", mg, callErr)
		return false, fmt.Errorf("findby failed: %w", callErr)
	}

	body, ok := response.ResponseBody.(map[string]interface{})
	if !ok || body == nil {
		return false, fmt.Errorf("findby succeeded but returned no object to take the identifier from")
	}
	if perr := populateStatusFields(clientInfo, mg, body); perr != nil {
		return false, fmt.Errorf("writing the resolved identifier into status: %w", perr)
	}
	return true, nil
}
