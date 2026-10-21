package condition

import (
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
