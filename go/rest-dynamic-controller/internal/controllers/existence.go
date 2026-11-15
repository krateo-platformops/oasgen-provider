package restResources

import (
	"context"
	"fmt"
	"strings"

	customcondition "github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
	"github.com/krateo-platformops/unstructured-runtime/pkg/meta"
	unstructuredtools "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured"
	"github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/jqengine"
)

// createHasBeenAttempted reports whether anything has already tried to create this resource, read off
// the Ready condition the CONTROLLER itself wrote on a previous pass.
//
// WHY THIS IS A NAMED FUNCTION AND NOT THREE NEGATIONS INLINE. It used to be
// `!IsConditionSet(Creating()) && !IsConditionSet(Available())` at its one call site, and I added a
// THIRD Ready reason (NoIdentifierRecorded, issue 682) without adding it here. The consequence was not
// a missed diagnostic: because Ready holds exactly ONE condition, writing the new reason REPLACED
// Creating, the guard then read the CR as never-created, returned {ResourceExists: false} with a NIL
// error, and unstructured-runtime's handleObserve enqueues a fresh Create on exactly that input. So the
// refusal CAUSED the create it was written to prevent -- measured as 5 of 5 duplicates at a vendor with
// the extras orphaned on CR delete, and as three POSTs in five minutes on another (issue 692).
//
// Both log lines at the call site are Debug, so the pass that does this is SILENT at default level.
// Neither consumer saw anything; one of them reported "either Observe ran cleanly and still chose
// Create without logging, or Create was re-entered without an Observe that logs anything", which was
// right and unanswerable from their side.
//
// So the list of reasons is the load-bearing part, and it must live where someone adding a fourth one
// will see it. THE RULE: any Ready reason this controller writes AFTER a create has been attempted
// belongs here. Forgetting one does not degrade a message -- it recreates an external resource.
//
// TWO MEASURED ROUTES INTO THE SAME GUARD, and the second is why the conditions alone cannot do it.
//
//	github-provider-kog   the refusal fires and writes Ready=False/NoIdentifierRecorded, DISPLACING
//	                      Creating. Their streaming watch has it at rv=961, persisting 1.86s until the
//	                      create handler overwrites it with Creating -- which is why every sampled P3
//	                      count was 0. Three POSTs in five minutes.
//	azuredevops-provider-kog  the refusal is NEVER reached (0 occurrences of its log.Info line against 6
//	                      Info lines from the same file as a control). Their status write lost a race, so
//	                      Creating was never PERSISTED at all, and the guard saw nothing. 5 of 5
//	                      duplicated at the vendor, extras orphaned on CR delete.
//
// Their session caught this: a fix that only stopped the refusal from displacing Creating would not have
// stopped their duplicates, because on their path no condition was ever written. The guard predates
// #687 -- same line in 0.66.0, 0.68.0 and 0.69.0 -- so #687 added a route rather than creating the hole.
//
// WHY A CONDITION AT ALL, since a condition is an output of the previous reconcile rather than evidence
// about the external world. For a kind with neither get nor findby there is nothing else to read about
// the external state: it is unreadable by construction. That reasoning is #347's and it still holds.
// What was wrong was reading a channel the triggering failure destroys.
func createHasBeenAttempted(mg *unstructured.Unstructured) bool {
	// THE MARKERS FIRST, and they are the reason this function is correct where three conditions were
	// not. krateo.io/external-create-{pending,succeeded,failed} are ANNOTATIONS: the runtime writes them
	// with tools.Update, against the main resource, while the conditions go to the STATUS subresource in
	// a different request. github-provider-kog's managedFields show the split directly -- `controller`
	// Update owns the two annotations, `controller` Update/status owns the conditions.
	//
	// That split is the whole point. The trigger for issue 692 is a status write that LOST A RACE, so on
	// that path no Ready condition is ever persisted and every condition-based test reads "nothing has
	// happened here". The annotation survives, because it was never part of the write that failed.
	if !meta.GetExternalCreateSucceeded(mg).IsZero() ||
		!meta.GetExternalCreatePending(mg).IsZero() ||
		!meta.GetExternalCreateFailed(mg).IsZero() ||
		!meta.GetExternalCreateNotRequested(mg).IsZero() {
		return true
	}

	// The conditions, kept as a second answer rather than removed. A marker is written by the runtime
	// around a create it dispatched; a condition is written by this controller. They can disagree -- an
	// adopted resource reaches Available with no marker ever set -- and only one of them being true is
	// still a yes.
	return unstructuredtools.IsConditionSet(mg, condition.Creating()) ||
		unstructuredtools.IsConditionSet(mg, condition.Available()) ||
		unstructuredtools.IsConditionSet(mg, customcondition.NoIdentifierRecorded(""))
}

// declaresVerb reports whether the resource declares a verb for action.
//
// Used to tell two cases apart that IsResourceKnown collapses into one `false`: a kind with NO get verb
// (unobservable by declaration) and a kind WITH a get verb whose identifier is missing (observable in
// principle, and we have lost the handle). Only the second is a defect, and only the second must refuse
// to answer (issue 682).
func declaresVerb(verbs []getter.VerbsDescription, action string) bool {
	for _, v := range verbs {
		if strings.EqualFold(v.Action, action) {
			return true
		}
	}
	return false
}

// notFoundBodyForAction returns the NotFoundBody predicate declared on the verb matching action, or nil.
func notFoundBodyForAction(verbs []getter.VerbsDescription, action string) *getter.JQProgram {
	for _, v := range verbs {
		if strings.EqualFold(v.Action, action) {
			return v.NotFoundBody
		}
	}
	return nil
}

// evalNotFoundBody runs the NotFoundBody gojq predicate against the (raw) observe-response body and reports
// whether the external resource should be treated as not existing.
func evalNotFoundBody(ctx context.Context, prog *getter.JQProgram, body interface{}) (bool, error) {
	return evalBoolPredicate(ctx, prog, body, "notFoundBody")
}

// evalBoolPredicate runs a gojq boolean predicate against body and returns its result. A nil program, or one
// with no inline source, yields (false, nil). The program must return a boolean; any other result is an
// error so a mistyped predicate fails loudly rather than silently reading as false. label names the field in
// error messages.
func evalBoolPredicate(ctx context.Context, prog *getter.JQProgram, body interface{}, label string) (bool, error) {
	if prog == nil {
		return false, nil
	}
	if prog.Inline == "" {
		// A module ref (configmap:// / http(s)://) is materialized into Inline by the definitiongetter at
		// load time, so a still-present Ref here means resolution was skipped/failed — fail loudly.
		if prog.Ref != "" {
			return false, fmt.Errorf("%s jq module %q was not resolved to inline source", label, prog.Ref)
		}
		return false, nil
	}
	p, err := jqengine.Compile(prog.Inline)
	if err != nil {
		return false, fmt.Errorf("compiling %s: %w", label, err)
	}
	out, err := p.Run(ctx, body)
	if err != nil {
		return false, fmt.Errorf("running %s: %w", label, err)
	}
	b, ok := out.(bool)
	if !ok {
		return false, fmt.Errorf("%s must return a boolean, got %T", label, out)
	}
	return b, nil
}
