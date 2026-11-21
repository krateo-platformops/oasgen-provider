package restdefinition

import (
	"fmt"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	rtv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
)

// ReasonExistenceUnanswerable marks a declared resource whose existence can NEVER be read, so a
// create whose response is lost strands its CR permanently.
const ReasonExistenceUnanswerable rtv1.ConditionReason = "ExistenceUnanswerable"

// TypeExistenceAnswerable is the condition type this is reported on.
//
// NOT status.schemaWarnings, WHICH IS WHERE THIS WOULD OTHERWISE GO. That field is a plain []string
// and issue 708 measured it non-empty on 23 of 25 HEALTHY RestDefinitions -- a new entry there is a
// warning nobody reads, because 92% of working objects already carry something in it. A condition is
// visible in `describe`, greppable by reason, and the same channel the management-policy fault uses,
// so one kind of "this is misconfigured" has one shape.
//
// NOT rtv1.TypeSynced either, for the reason documented at TypeManagementPolicyValid: this module
// pins provider-runtime v1.8.2, whose reconciler writes Synced after Observe in the same pass.
const TypeExistenceAnswerable rtv1.ConditionType = "ExistenceAnswerable"

// pathParamPattern matches an OAS path template parameter: {id}, {issue_number}, {pushId}.
var pathParamPattern = regexp.MustCompile(`\{([^{}/]+)\}`)

// statusSourcePrefix is the convention for a mapping that reads from status. Measured across this
// repo's examples and fixtures: inCustomResource values carry an explicit `spec.` or `status.`
// prefix (`spec.name`, `spec.projectId`, `spec.node_id`, `status.id`), never a bare field name.
const statusSourcePrefix = "status."

// unanswerableExistence reports a get path parameter that can only come from status, on a resource
// that declares no findby.
//
// WHY THIS IS THE PREDICATE. rest-dynamic-controller's existence check picks its read verb by
// ADDRESSABILITY, not by which verbs exist:
//
//	isKnown, _ := builder.IsResourceKnown(...)   // a dry run of the GET
//	action := apiaction.Get
//	if !isKnown { action = apiaction.FindBy }    // -> no findby -> "existence cannot be read"
//
// A get parameter sourced from `status.<id>` is empty at reconcile 1, so the GET does not validate,
// the action becomes findby, there is none, and the check returns an error ON EVERY PASS FOREVER --
// because the only thing that would fill `status.<id>` is the create response that was lost.
// Measured: 4 of 6 CRs in one consumer's chart stranded on a CLEAN install, and 2 more on another
// consumer's chart over ~2h. See unstructured-runtime issue 119.
//
// IT IS DELIBERATELY NARROW, and the narrowness is what makes it exact rather than approximate:
//
//   - it warns ONLY where a get path parameter is EXPLICITLY mapped from `status.*`. A parameter with
//     no mapping at all may still resolve by name from the generated spec at step 3, and deciding
//     that here would mean re-deriving the rendered schema -- an approximation of the real resolver,
//     which is exactly the kind of stand-in that produced a vacuous gate elsewhere in this repo
//     today.
//   - a resource with no get verb is NOT this case. It has a different defect and other checks
//     report it; saying "existence unanswerable" about it would be true for the wrong reason.
//   - a declared findby makes the resource answerable regardless of the get, because findby is the
//     fallback the runtime reaches for.
//
// SO A FALSE NEGATIVE IS POSSIBLE AND A FALSE POSITIVE IS NOT, which is the right way round for a
// warning that a consumer reads on 25 healthy objects.
func unanswerableExistence(res definitionv1alpha1.Resource) (param, source string, unanswerable bool) {
	var get *definitionv1alpha1.VerbsDescription
	for i := range res.VerbsDescription {
		switch strings.ToLower(res.VerbsDescription[i].Action) {
		case "findby":
			// A findby makes it answerable; nothing else matters.
			return "", "", false
		case "get":
			get = &res.VerbsDescription[i]
		}
	}
	if get == nil {
		return "", "", false
	}

	for _, m := range pathParamPattern.FindAllStringSubmatch(get.Path, -1) {
		name := m[1]
		if src, ok := statusMappedSource(get, name); ok {
			return name, src, true
		}
	}
	return "", "", false
}

// statusMappedSource reports the `status.*` source a get path parameter is mapped from, if any.
//
// BOTH MAPPING FIELDS ARE READ. requestFieldMapping is deprecated and fieldMapping is current, and a
// definition may carry either -- a check that read only the current one would miss every definition
// written before the rename, which is most of the ones in the wild.
func statusMappedSource(get *definitionv1alpha1.VerbsDescription, param string) (string, bool) {
	for _, m := range get.FieldMapping {
		if m.InPath == param && strings.HasPrefix(m.InCustomResource, statusSourcePrefix) {
			return m.InCustomResource, true
		}
	}
	for _, m := range get.RequestFieldMapping {
		if m.InPath == param && strings.HasPrefix(m.InCustomResource, statusSourcePrefix) {
			return m.InCustomResource, true
		}
	}
	return "", false
}

// existenceUnanswerableCondition is the warning.
//
// IT WARNS AND THE RESOURCE IS STILL GENERATED. Refusing would take six live kinds out of service on
// one consumer's chart at upgrade -- a regression delivered as a fix. The warning appears at apply
// time, which is when someone can still add a findby to the document.
func existenceUnanswerableCondition(kind, param, source string) rtv1.Condition {
	return rtv1.Condition{
		Type:               TypeExistenceAnswerable,
		Status:             metav1.ConditionFalse,
		LastTransitionTime: metav1.Now(),
		Reason:             ReasonExistenceUnanswerable,
		Message: fmt.Sprintf(
			"%s is generated, but its existence can NEVER be read: the get path needs {%s}, which is "+
				"mapped from %q, and no findby verb is declared. If a create's response is ever lost, "+
				"the CR strands at Ready=False/CreateOutcomeUnknown and cannot be deleted without "+
				"first annotating krateo.io/deletion-policy=orphan. Declare a findby verb, or source "+
				"{%s} from spec, to make it recoverable",
			kind, param, source, param,
		),
	}
}

// existenceAnswerableCondition is the cleared state, so the condition does not linger after a
// document is fixed.
func existenceAnswerableCondition() rtv1.Condition {
	return rtv1.Condition{
		Type:               TypeExistenceAnswerable,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Reason:             ReasonExistenceAnswerable,
	}
}

// ReasonExistenceAnswerable is the cleared counterpart.
const ReasonExistenceAnswerable rtv1.ConditionReason = "ExistenceAnswerable"
