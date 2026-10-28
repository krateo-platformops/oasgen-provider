package condition

import (
	"strings"
	"testing"
	"time"

	"github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPending(t *testing.T) {
	beforeCall := time.Now()
	result := Pending()
	afterCall := time.Now()

	// Test Type
	if result.Type != condition.TypeReady {
		t.Errorf("Expected Type to be %s, got %s", condition.TypeReady, result.Type)
	}

	// Test Status
	if result.Status != metav1.ConditionFalse {
		t.Errorf("Expected Status to be %s, got %s", metav1.ConditionFalse, result.Status)
	}

	// Test Reason
	if result.Reason != ReasonPending {
		t.Errorf("Expected Reason to be %s, got %s", ReasonPending, result.Reason)
	}

	// Test LastTransitionTime is within reasonable bounds
	transitionTime := result.LastTransitionTime.Time
	if transitionTime.Before(beforeCall) || transitionTime.After(afterCall) {
		t.Errorf("Expected LastTransitionTime to be between %v and %v, got %v", beforeCall, afterCall, transitionTime)
	}
}

func TestReasonPendingConstant(t *testing.T) {
	expected := "Pending"
	if ReasonPending != expected {
		t.Errorf("Expected ReasonPending to be %s, got %s", expected, ReasonPending)
	}
}

func TestUpdatingIsNotReadyAndSaysUpdating(t *testing.T) {
	c := Updating()

	if c.Type != condition.TypeReady {
		t.Errorf("Type = %q, want %q", c.Type, condition.TypeReady)
	}
	if c.Status != metav1.ConditionFalse {
		t.Errorf("Status = %q, want False: an issued update is not evidence the resource matches its spec", c.Status)
	}
	if c.Reason != ReasonUpdating {
		t.Errorf("Reason = %q, want %q", c.Reason, ReasonUpdating)
	}
	if c.Reason == "Creating" {
		t.Error("a post-update state must not report Creating; that is what made a drift loop look like a slow create")
	}
}

func TestDriftNotConvergingNamesTheFieldAndIsNotReady(t *testing.T) {
	c := DriftNotConverging("nodePools[0].instance", "values differ; tried 5 times")

	if c.Status != metav1.ConditionFalse {
		t.Errorf("Status = %q, want False: the resource genuinely does not match its spec", c.Status)
	}
	if c.Reason != ReasonDriftNotConverging {
		t.Errorf("Reason = %q, want %q", c.Reason, ReasonDriftNotConverging)
	}
	if want := "field nodePools[0].instance: values differ; tried 5 times"; c.Message != want {
		t.Errorf("Message = %q, want %q", c.Message, want)
	}
}

func TestDriftNotConvergingOmitsAnEmptyField(t *testing.T) {
	c := DriftNotConverging("", "slice lengths differ")

	if c.Message != "slice lengths differ" {
		t.Errorf("Message = %q, want the bare detail with no empty field prefix", c.Message)
	}
}

// TestObservedIsItsOwnConditionTypeNotReady pins that TypeObserved is distinct from TypeReady.
//
// It has to be: "did the last observe obtain a body to read status from" is a different question from
// "is this resource ready", and a bodyless observe does not mean the resource is unhealthy — the observe
// may have decided existence from a status code alone. Collapsing the two would make every bodyless
// observe look like a failure (#233).
func TestObservedIsItsOwnConditionTypeNotReady(t *testing.T) {
	for _, c := range []metav1.Condition{Observed(), NotObserved("")} {
		if c.Type != TypeObserved {
			t.Errorf("expected Type %q, got %q", TypeObserved, c.Type)
		}
		if c.Type == condition.TypeReady {
			t.Error("TypeObserved must not be TypeReady: a bodyless observe is not an unready resource")
		}
	}
}

// TestObservedTransitionsSoItsTimestampMeansSomething is the reason this is a condition that flips rather
// than a marker that is only ever set.
//
// metav1.Condition carries lastTransitionTime, and a condition whose status never changes keeps the
// timestamp of the FIRST time it was set, forever. An always-True "Observed" would therefore answer
// "populated from a response obtained when?" with the wrong date on every reconcile after the first. The
// two states must differ in Status, not merely in Reason, or the transition never happens.
func TestObservedTransitionsSoItsTimestampMeansSomething(t *testing.T) {
	yes, no := Observed(), NotObserved("")

	if yes.Status != metav1.ConditionTrue {
		t.Errorf("Observed must be True, got %s", yes.Status)
	}
	if no.Status != metav1.ConditionFalse {
		t.Errorf("NotObserved must be False, got %s", no.Status)
	}
	if yes.Status == no.Status {
		t.Error("the two states must differ in Status, or lastTransitionTime never transitions")
	}
	if yes.Reason != ReasonResponseObtained {
		t.Errorf("expected reason %q, got %q", ReasonResponseObtained, yes.Reason)
	}
	if no.Reason != ReasonNoResponseBody {
		t.Errorf("expected reason %q, got %q", ReasonNoResponseBody, no.Reason)
	}
}

// TestNotObservedSaysWhatTheAbsenceDoesNotMean checks the message, because the whole point of #233 is that
// a reader was drawing a wrong conclusion from an absent field. The message has to say that absence is not
// evidence, not merely that something was missing.
func TestNotObservedSaysWhatTheAbsenceDoesNotMean(t *testing.T) {
	msg := NotObserved("").Message
	if msg == "" {
		t.Fatal("NotObserved must carry a message: the condition exists to correct a wrong inference")
	}
	for _, want := range []string{"no response body", "not evidence"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message should contain %q, got %q", want, msg)
		}
	}

	// Observed carries no message: there is nothing to warn about, and a message on the healthy state
	// would appear on every resource forever.
	if Observed().Message != "" {
		t.Errorf("Observed should carry no message, got %q", Observed().Message)
	}
}

// TestNotObservedAppendsTheDetail pins that the caller's detail survives, since that is what names WHICH
// observe action came back empty.
func TestNotObservedAppendsTheDetail(t *testing.T) {
	msg := NotObserved(`observe action "findby" returned no body`).Message
	if !strings.Contains(msg, `observe action "findby" returned no body`) {
		t.Errorf("the caller's detail must survive, got %q", msg)
	}
	if !strings.Contains(msg, "not evidence") {
		t.Errorf("the detail must be appended to the explanation, not replace it, got %q", msg)
	}
}
