//go:build unit || integration

package restdefinition

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	rtv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
	"github.com/krateo-platformops/provider-runtime/pkg/meta"
)

type annotated map[string]string

func (a annotated) GetAnnotations() map[string]string { return a }

// TestOnlyAnUnrecognisedPolicyIsReported pins both directions, because the wrong one is worse:
// reporting a fault on every correctly-configured RestDefinition would make the condition noise and
// get it ignored.
func TestOnlyAnUnrecognisedPolicyIsReported(t *testing.T) {
	cases := []struct {
		name        string
		annotations map[string]string
		wantBad     string
	}{
		// AN ABSENT ANNOTATION IS NOT A FAULT: provider-runtime substitutes the default policy, and
		// this is the overwhelmingly common case.
		{"absent", nil, ""},
		{"empty map", map[string]string{}, ""},
		{"empty value", map[string]string{meta.AnnotationKeyManagementPolicy: ""}, ""},
		{"other annotations only", map[string]string{"krateo.io/deletion-policy": "orphan"}, ""},

		// All four values provider-runtime v1.8.2 accepts, read from its constants rather than typed.
		{"default", map[string]string{meta.AnnotationKeyManagementPolicy: meta.ManagementPolicyDefault}, ""},
		{"observe", map[string]string{meta.AnnotationKeyManagementPolicy: meta.ManagementPolicyObserve}, ""},
		{"observe-create-update", map[string]string{meta.AnnotationKeyManagementPolicy: meta.ManagementPolicyObserveCreateUpdate}, ""},
		{"observe-delete", map[string]string{meta.AnnotationKeyManagementPolicy: meta.ManagementPolicyObserveDelete}, ""},

		// THE SUBJECT. A misspelling, a case change, padding, and a verb set that a newer runtime
		// understands and v1.8.2 does not -- every one of which makes IsActionAllowed deny all three
		// actions silently.
		{"misspelled default", map[string]string{meta.AnnotationKeyManagementPolicy: "defualt"}, "defualt"},
		{"wrong case", map[string]string{meta.AnnotationKeyManagementPolicy: "Default"}, "Default"},
		{"padded", map[string]string{meta.AnnotationKeyManagementPolicy: " default "}, " default "},
		{"verb set", map[string]string{meta.AnnotationKeyManagementPolicy: "observe,create"}, "observe,create"},
		{"unknown verb in a set", map[string]string{meta.AnnotationKeyManagementPolicy: "observe,upsert"}, "observe,upsert"},
		{"plausible but wrong", map[string]string{meta.AnnotationKeyManagementPolicy: "observe-create"}, "observe-create"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad, unrecognised := managementPolicyIsUnrecognised(annotated(tc.annotations))
			if tc.wantBad == "" {
				if unrecognised {
					t.Fatalf("reported %q as unrecognised; this value must NOT raise a condition", bad)
				}
				return
			}
			if !unrecognised {
				t.Fatalf("did not report %q as unrecognised -- IsActionAllowed denies create, update "+
					"AND delete for it, and nothing else would say so", tc.wantBad)
			}
			if bad != tc.wantBad {
				t.Errorf("reported value = %q, want %q", bad, tc.wantBad)
			}
		})
	}
}

// TestThePaddedCaseAgreesWithTheRuntime is separate because " default " is the one case where this
// helper and a FUTURE runtime could legitimately disagree.
//
// provider-runtime v1.8.2 compares the annotation literally, so " default " is unrecognised and all
// three actions are denied -- which is what this helper reports. A later version that trims padding
// would accept it, and then this condition would be wrong. The assertion below is therefore against
// the RUNTIME's own answer rather than against a hardcoded expectation: if meta.IsActionAllowed ever
// starts permitting a padded value, this fails and the helper has to follow it.
func TestThePaddedCaseAgreesWithTheRuntime(t *testing.T) {
	for _, v := range []string{" default ", "defualt", "Default", "observe,create", "observe-create"} {
		obj := &metav1.ObjectMeta{Annotations: map[string]string{meta.AnnotationKeyManagementPolicy: v}}

		anyAllowed := meta.IsActionAllowed(obj, meta.ActionCreate) ||
			meta.IsActionAllowed(obj, meta.ActionUpdate) ||
			meta.IsActionAllowed(obj, meta.ActionDelete)

		_, weReport := managementPolicyIsUnrecognised(annotated(obj.Annotations))

		// The contract: we report exactly when the runtime permits NOTHING.
		if anyAllowed == weReport {
			t.Errorf("policy %q: runtime permits something = %v, we report it as unrecognised = %v -- "+
				"these must be opposites, or the condition says the resource is unmanaged while the "+
				"runtime is in fact managing it (or the reverse)", v, anyAllowed, weReport)
		}
	}

	// POSITIVE CONTROL: a value the runtime DOES accept must come out the other way on both sides,
	// or the loop above would pass by always disagreeing.
	ok := &metav1.ObjectMeta{Annotations: map[string]string{meta.AnnotationKeyManagementPolicy: meta.ManagementPolicyDefault}}
	if !meta.IsActionAllowed(ok, meta.ActionCreate) {
		t.Fatalf("CONTROL FAILED: the runtime refuses create under the default policy, so this test's " +
			"notion of agreement is wrong")
	}
	if _, weReport := managementPolicyIsUnrecognised(annotated(ok.Annotations)); weReport {
		t.Errorf("CONTROL FAILED: we report the default policy as unrecognised")
	}
}

// TestTheConditionIsNotOnSynced is the whole reason this condition exists on a type of its own, and
// it would be silently undone by a well-meaning change to "match core-provider".
//
// provider-runtime v1.8.2's reconciler sets Synced=TRUE on the policy-denied path:
//
//	reconciler.go ~1278
//	    if !meta.ShouldUpdate(managed) {
//	        managed.SetConditions(prv1.ReconcileSuccess())   // TypeSynced / ConditionTrue
//	        return ...
//	    }
//
// A Synced condition set during Observe is overwritten in the same reconcile.
func TestTheConditionIsNotOnSynced(t *testing.T) {
	c := managementPolicyInvalidCondition("defualt")

	if c.Type == rtv1.TypeSynced {
		t.Fatalf("the condition is on TypeSynced, which provider-runtime v1.8.2 OVERWRITES with " +
			"ReconcileSuccess on the very path an unrecognised policy takes -- it would never survive " +
			"a pass. See reconciler.go's !ShouldUpdate branch.")
	}
	if c.Type != TypeManagementPolicyValid {
		t.Errorf("condition type = %q, want %q", c.Type, TypeManagementPolicyValid)
	}
	if c.Status != metav1.ConditionFalse {
		t.Errorf("status = %q, want False", c.Status)
	}
	if c.Reason != ReasonManagementPolicyInvalid {
		t.Errorf("reason = %q, want %q", c.Reason, ReasonManagementPolicyInvalid)
	}
}

// TestTheMessageNamesTheValueAndTheConsequence is what makes the condition actionable rather than
// merely present.
func TestTheMessageNamesTheValueAndTheConsequence(t *testing.T) {
	c := managementPolicyInvalidCondition("defualt")

	if !strings.Contains(c.Message, `"defualt"`) {
		t.Errorf("the message does not quote the offending value, so an operator cannot see the "+
			"typo:\n%s", c.Message)
	}
	if !strings.Contains(c.Message, "NOTHING is being managed") {
		t.Errorf("the message does not say that nothing is managed -- that is the consequence, and "+
			"without it this reads like a warning rather than an outage:\n%s", c.Message)
	}
	for _, accepted := range []string{
		meta.ManagementPolicyDefault,
		meta.ManagementPolicyObserve,
		meta.ManagementPolicyObserveCreateUpdate,
		meta.ManagementPolicyObserveDelete,
	} {
		if !strings.Contains(c.Message, accepted) {
			t.Errorf("the message does not list the accepted value %q, so the operator has to go "+
				"looking:\n%s", accepted, c.Message)
		}
	}

	// A runaway annotation must not be copied into status wholesale: .status is stored on every
	// RestDefinition and read by humans.
	long := strings.Repeat("x", 500)
	c = managementPolicyInvalidCondition(long)
	if strings.Contains(c.Message, long) {
		t.Errorf("a 500-char annotation value reached the message unbounded (%d chars)", len(c.Message))
	}
	if !strings.Contains(c.Message, "...") {
		t.Errorf("the truncated value is not marked with an ellipsis, so it reads as the whole value:\n%s", c.Message)
	}
}
