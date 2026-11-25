//go:build unit || integration

package restdefinition

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	rtv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
)

func verb(action, path string, mappings ...definitionv1alpha1.FieldMappingItem) definitionv1alpha1.VerbsDescription {
	return definitionv1alpha1.VerbsDescription{Action: action, Method: "GET", Path: path, FieldMapping: mappings}
}

func mapTo(inPath, inCR string) definitionv1alpha1.FieldMappingItem {
	return definitionv1alpha1.FieldMappingItem{InPath: inPath, InCustomResource: inCR}
}

// TestUnanswerableExistence pins both directions over the shapes two consumers actually ship.
//
// THE FALSE-POSITIVE DIRECTION MATTERS MORE than the false-negative one: a consumer reads this
// condition across 25 healthy RestDefinitions, and a warning on a kind that is fine is how a channel
// stops being read. Measured on one chart: ten findby-less kinds split SIX exposed and FOUR not,
// and listing all ten would have sent operators to do manual work on four kinds that need none.
func TestUnanswerableExistence(t *testing.T) {
	cases := []struct {
		name       string
		res        definitionv1alpha1.Resource
		wantParam  string
		wantSource string
		wantFlag   bool
	}{
		{
			// THE MEASURED CASE: Azure DevOps GitPush, GitHub Issue. The get needs an id the create
			// response was supposed to supply, and there is no findby to fall back to.
			name: "get needs a status id and no findby -- EXPOSED",
			res: definitionv1alpha1.Resource{Kind: "GitPush", VerbsDescription: []definitionv1alpha1.VerbsDescription{
				verb("create", "/pushes"),
				verb("get", "/pushes/{pushId}", mapTo("pushId", "status.pushId")),
			}},
			wantParam: "pushId", wantSource: "status.pushId", wantFlag: true,
		},
		{
			// NOT EXPOSED: the same shape plus a findby, which is the runtime's fallback.
			name: "a findby makes it answerable",
			res: definitionv1alpha1.Resource{Kind: "Issue", VerbsDescription: []definitionv1alpha1.VerbsDescription{
				verb("get", "/issues/{number}", mapTo("number", "status.number")),
				verb("findby", "/issues"),
			}},
			wantFlag: false,
		},
		{
			// NOT EXPOSED: the four kinds on one consumer's chart whose get addresses from spec, so
			// IsResourceKnown is true at reconcile 1 and findby is never consulted.
			name: "get addresses from spec -- NOT exposed",
			res: definitionv1alpha1.Resource{Kind: "RepositoryPermission", VerbsDescription: []definitionv1alpha1.VerbsDescription{
				verb("get", "/repos/{projectId}/perms", mapTo("projectId", "spec.projectId")),
			}},
			wantFlag: false,
		},
		{
			// NOT THIS CASE: no get at all is a different defect, reported elsewhere. Claiming
			// "existence unanswerable" here would be true for the wrong reason.
			name: "no get verb -- a different defect",
			res: definitionv1alpha1.Resource{Kind: "Trigger", VerbsDescription: []definitionv1alpha1.VerbsDescription{
				verb("create", "/triggers"),
			}},
			wantFlag: false,
		},
		{
			// DELIBERATE FALSE NEGATIVE: an unmapped parameter may still resolve by name from the
			// generated spec at step 3. Deciding that here would mean re-deriving the rendered
			// schema, and an approximation of the real resolver is what produced a vacuous gate
			// elsewhere in this repo today.
			name: "an unmapped parameter is NOT flagged",
			res: definitionv1alpha1.Resource{Kind: "Widget", VerbsDescription: []definitionv1alpha1.VerbsDescription{
				verb("get", "/widgets/{name}"),
			}},
			wantFlag: false,
		},
		{
			name: "no path parameters at all",
			res: definitionv1alpha1.Resource{Kind: "Settings", VerbsDescription: []definitionv1alpha1.VerbsDescription{
				verb("get", "/settings"),
			}},
			wantFlag: false,
		},
		{
			// SEVERAL PARAMETERS, ONE FROM STATUS: it only takes one to make the get unaddressable.
			name: "one of three parameters comes from status",
			res: definitionv1alpha1.Resource{Kind: "CheckTask", VerbsDescription: []definitionv1alpha1.VerbsDescription{
				verb("get", "/orgs/{org}/checks/{checkId}/tasks/{taskId}",
					mapTo("org", "spec.org"), mapTo("checkId", "status.checkId"), mapTo("taskId", "spec.taskId")),
			}},
			wantParam: "checkId", wantSource: "status.checkId", wantFlag: true,
		},
		{
			// ACTION CASING: the CRD does not constrain case here, and a definition written `Get`
			// must not slip past the check.
			name: "action casing does not matter",
			res: definitionv1alpha1.Resource{Kind: "Run", VerbsDescription: []definitionv1alpha1.VerbsDescription{
				{Action: "GET", Method: "GET", Path: "/runs/{runId}",
					FieldMapping: []definitionv1alpha1.FieldMappingItem{mapTo("runId", "status.runId")}},
			}},
			wantParam: "runId", wantSource: "status.runId", wantFlag: true,
		},
		{
			// FINDBY CASING too, or a `FindBy` definition would be reported as exposed when it is not.
			name: "findby casing does not matter",
			res: definitionv1alpha1.Resource{Kind: "Issue", VerbsDescription: []definitionv1alpha1.VerbsDescription{
				verb("get", "/issues/{number}", mapTo("number", "status.number")),
				{Action: "FindBy", Method: "GET", Path: "/issues"},
			}},
			wantFlag: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			param, source, flag := unanswerableExistence(tc.res, map[string]bool{})
			if flag != tc.wantFlag {
				t.Fatalf("unanswerable = %v, want %v (param=%q source=%q)", flag, tc.wantFlag, param, source)
			}
			if !flag {
				return
			}
			if param != tc.wantParam {
				t.Errorf("param = %q, want %q", param, tc.wantParam)
			}
			if source != tc.wantSource {
				t.Errorf("source = %q, want %q", source, tc.wantSource)
			}
		})
	}
}

// TestTheDeprecatedMappingFieldIsReadToo. requestFieldMapping is deprecated and fieldMapping is
// current; a check that read only the current one would miss every definition written before the
// rename, which is most of the ones in the wild.
func TestTheDeprecatedMappingFieldIsReadToo(t *testing.T) {
	res := definitionv1alpha1.Resource{Kind: "Legacy", VerbsDescription: []definitionv1alpha1.VerbsDescription{
		{Action: "get", Method: "GET", Path: "/things/{thingId}",
			RequestFieldMapping: []definitionv1alpha1.RequestFieldMappingItem{
				{InPath: "thingId", InCustomResource: "status.thingId"},
			}},
	}}
	param, source, flag := unanswerableExistence(res, map[string]bool{})
	if !flag {
		t.Fatalf("a status mapping on the DEPRECATED field was not seen -- every definition written "+
			"before the rename would go unwarned (param=%q source=%q)", param, source)
	}
	if source != "status.thingId" {
		t.Errorf("source = %q, want status.thingId", source)
	}
}

// TestTheConditionNamesTheKindTheParamAndTheWayOut. A warning an operator cannot act on is noise,
// and this one lands in a channel a consumer reads across every RestDefinition.
func TestTheConditionNamesTheKindTheParamAndTheWayOut(t *testing.T) {
	c := existenceUnanswerableCondition("GitPush", "pushId", "status.pushId")

	if c.Type == rtv1.TypeSynced {
		t.Fatalf("the condition is on TypeSynced, which provider-runtime v1.8.2 overwrites after " +
			"Observe in the same pass -- see TypeExistenceAnswerable for the measurement")
	}
	if c.Type != TypeExistenceAnswerable {
		t.Errorf("type = %q, want %q", c.Type, TypeExistenceAnswerable)
	}
	if c.Status != metav1.ConditionFalse {
		t.Errorf("status = %q, want False", c.Status)
	}
	if c.Reason != ReasonExistenceUnanswerable {
		t.Errorf("reason = %q, want %q", c.Reason, ReasonExistenceUnanswerable)
	}

	for _, want := range []string{"GitPush", "{pushId}", `"status.pushId"`, "findby", "deletion-policy=orphan"} {
		if !strings.Contains(c.Message, want) {
			t.Errorf("the message does not contain %q, so an operator cannot act on it:\n%s", want, c.Message)
		}
	}

	// AND IT MUST SAY THE RESOURCE IS STILL GENERATED, or a reader concludes generation failed.
	if !strings.Contains(c.Message, "is generated") {
		t.Errorf("the message does not say the resource IS generated; a reader would think it was "+
			"refused:\n%s", c.Message)
	}

	// The cleared counterpart must be the same TYPE, or the warning never goes away.
	ok := existenceAnswerableCondition()
	if ok.Type != c.Type {
		t.Errorf("the cleared condition is type %q and the warning is %q -- a warning on one type is "+
			"never cleared by a True on another", ok.Type, c.Type)
	}
	if ok.Status != metav1.ConditionTrue {
		t.Errorf("the cleared condition status = %q, want True", ok.Status)
	}
}
