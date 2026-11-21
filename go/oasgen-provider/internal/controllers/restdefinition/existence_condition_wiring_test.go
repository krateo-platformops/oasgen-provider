//go:build unit || integration

package restdefinition

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
)

// TestTheConditionTheCRReceivesCarriesTheRightValuesInTheRightPLACES asserts on the value the CR
// actually consumes, which is the gap every other arm in this package leaves open.
//
// WHAT WAS UNTESTED, AND IT IS NOT A HYPOTHETICAL SHAPE. The decision passed THREE string parameters
// in a row:
//
//	existenceUnanswerableCondition(cr.Spec.Resource.Kind, param, source)
//	func existenceUnanswerableCondition(kind, param, source string)
//
// Swap `param` and `source` and the code compiles, the message names the source as the parameter and
// the parameter as the source, and NOT ONE of the fifteen existing arms fails -- because they call
// the predicate and the constructor separately, each with literals.
//
// This arm makes the swap detectable by asserting WHERE each value lands, not merely that all three
// appear somewhere in the message. A contains-check for "pushId" and "status.pushId" passes under the
// swap, since each string is still present.
func TestTheConditionTheCRReceivesCarriesTheRightValuesInTheRightPLACES(t *testing.T) {
	cr := &definitionv1alpha1.RestDefinition{
		Spec: definitionv1alpha1.RestDefinitionSpec{
			Resource: definitionv1alpha1.Resource{
				Kind: "GitPush",
				VerbsDescription: []definitionv1alpha1.VerbsDescription{
					{Action: "create", Method: "POST", Path: "/pushes"},
					{Action: "get", Method: "GET", Path: "/pushes/{pushId}",
						FieldMapping: []definitionv1alpha1.FieldMappingItem{
							{InPath: "pushId", InCustomResource: "status.pushId"},
						}},
				},
			},
		},
	}

	got := existenceConditionFor(cr)

	if got.Status != metav1.ConditionFalse {
		t.Fatalf("status = %q, want False -- this resource IS unanswerable", got.Status)
	}
	if got.Reason != ReasonExistenceUnanswerable {
		t.Fatalf("reason = %q, want %q", got.Reason, ReasonExistenceUnanswerable)
	}

	// THE ORDER ASSERTIONS. Each value is checked in the position it belongs to, so a swap of the
	// two similar-looking arguments fails here even though both strings remain present.
	msg := got.Message
	if !strings.HasPrefix(msg, "GitPush is generated") {
		t.Errorf("the message does not START with the KIND:\n%s", msg)
	}
	if !strings.Contains(msg, "needs {pushId}") {
		t.Errorf("the message does not name {pushId} as the PARAMETER -- a swapped argument would "+
			"put the source here instead, and both strings would still be present:\n%s", msg)
	}
	if !strings.Contains(msg, `mapped from "status.pushId"`) {
		t.Errorf("the message does not name status.pushId as the SOURCE:\n%s", msg)
	}

	// AND THE SWAP, SPELLED OUT: if param and source were exchanged these would appear, so asserting
	// their ABSENCE is what makes the arm discriminate rather than merely pass.
	if strings.Contains(msg, "needs {status.pushId}") {
		t.Errorf("param and source are SWAPPED: the message names the source as the parameter:\n%s", msg)
	}
	if strings.Contains(msg, `mapped from "pushId"`) {
		t.Errorf("param and source are SWAPPED: the message names the parameter as the source:\n%s", msg)
	}
}

// TestAnAnswerableResourceGetsTheClearedConditionFromTheSameCall pins the other branch at the same
// level, because a decision that only ever returns the warning is indistinguishable from a correct
// one on a document that is actually broken.
func TestAnAnswerableResourceGetsTheClearedConditionFromTheSameCall(t *testing.T) {
	cr := &definitionv1alpha1.RestDefinition{
		Spec: definitionv1alpha1.RestDefinitionSpec{
			Resource: definitionv1alpha1.Resource{
				Kind: "RepositoryPermission",
				VerbsDescription: []definitionv1alpha1.VerbsDescription{
					{Action: "get", Method: "GET", Path: "/repos/{projectId}/perms",
						FieldMapping: []definitionv1alpha1.FieldMappingItem{
							{InPath: "projectId", InCustomResource: "spec.projectId"},
						}},
				},
			},
		},
	}

	got := existenceConditionFor(cr)

	if got.Status != metav1.ConditionTrue {
		t.Fatalf("status = %q, want True -- this resource's get addresses from spec:\n%s", got.Status, got.Message)
	}
	if got.Reason != ReasonExistenceAnswerable {
		t.Errorf("reason = %q, want %q", got.Reason, ReasonExistenceAnswerable)
	}
	if got.Type != TypeExistenceAnswerable {
		t.Errorf("type = %q, want %q -- a warning on one type is never cleared by a True on another",
			got.Type, TypeExistenceAnswerable)
	}

	// SETUP CONTROL: the two branches must be distinguishable by more than Status, or a test that
	// got the wrong branch could still pass the Status check by coincidence.
	warn := existenceConditionFor(&definitionv1alpha1.RestDefinition{
		Spec: definitionv1alpha1.RestDefinitionSpec{Resource: definitionv1alpha1.Resource{
			Kind: "GitPush",
			VerbsDescription: []definitionv1alpha1.VerbsDescription{
				{Action: "get", Method: "GET", Path: "/p/{id}", FieldMapping: []definitionv1alpha1.FieldMappingItem{
					{InPath: "id", InCustomResource: "status.id"},
				}},
			},
		}},
	})
	if warn.Reason == got.Reason {
		t.Fatalf("both branches return reason %q, so this arm cannot tell them apart", got.Reason)
	}
	if warn.Message == got.Message {
		t.Fatalf("both branches return the same message, so this arm cannot tell them apart")
	}
}
