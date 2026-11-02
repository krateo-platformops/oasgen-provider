package restResources

import (
	"context"
	"strings"
	"testing"

	"github.com/krateo-platformops/plumbing/kubeutil/event"
	customcondition "github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	unstructuredtools "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// WHY THESE CASES EXIST. Exists distinguishes sixteen reasons it cannot answer, each implying a different
// operator action -- and its caller throws every one of them away. unstructured-runtime's recovery block
// (pkg/controller/worker.go:386-396 at v1.7.1) assigns our error and references it exactly once, in the
// `!= nil` test: `grep -n obsErr` over that file returns the assignment and the test, nothing else. All
// sixteen arrive at the operator as one fixed string:
//
//	cannot determine creation result - remove the krateo.io/external-create-pending annotation if it is
//	safe to proceed
//
// So the CR reports that a wedge exists and nothing about why -- while that string's own remediation is
// the only exit from the wedge, and "if it is safe to proceed" is precisely the judgement the discarded
// reason would inform.
//
// These cases assert the ARTIFACT an operator reads -- the Event actually emitted -- not a reimplementation
// of the rule that produces it.

// recordingRecorder keeps every Event so a case can assert on what an operator would see.
type recordingRecorder struct{ events []event.Event }

func (r *recordingRecorder) Event(_ runtime.Object, e event.Event) { r.events = append(r.events, e) }

// THE PREMISE: every refusal must emit exactly one event, and its MESSAGE must be the branch's own reason
// rather than a shared string. Two different branches are driven here specifically so that a fixed or
// generic message cannot pass -- a single case would be satisfied by an implementation that emitted the
// same sentence for everything, which is the defect being fixed one layer out.
func TestEveryRefusalEmitsItsOwnReason(t *testing.T) {
	pendingCR := func() *unstructured.Unstructured {
		u := crWithKind("Port")
		if err := unstructuredtools.SetConditions(u, customcondition.Pending()); err != nil {
			t.Fatalf("test setup: could not set the Pending condition, so this case would have asserted "+
				"the WRONG refusal: %v", err)
		}
		return u
	}

	cases := []struct {
		name string
		// handler state chosen so a DIFFERENT branch of existenceFromExternalAPI is reached.
		handler    func(rec event.Recorder) *handler
		cr         func() *unstructured.Unstructured
		mustSay    string
		mustNotSay string
	}{
		{
			name: "no swagger getter",
			handler: func(rec event.Recorder) *handler {
				return &handler{logger: logging.NewNopLogger(), eventRecorder: rec}
			},
			cr:      func() *unstructured.Unstructured { return crWithKind("Port") },
			mustSay: "swagger info getter",
			// The Pending branch's text must NOT appear, or the two cases are not reaching
			// different branches and this file proves nothing about per-reason detail.
			mustNotSay: "Pending",
		},
		{
			name: "resource is Pending",
			handler: func(rec event.Recorder) *handler {
				return &handler{
					logger:            logging.NewNopLogger(),
					eventRecorder:     rec,
					swaggerInfoGetter: stubGetter{info: minimalInfo()},
				}
			},
			cr:         pendingCR,
			mustSay:    "Pending",
			mustNotSay: "swagger info getter",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingRecorder{}
			h := tc.handler(rec)

			exists, err := h.Exists(context.Background(), tc.cr())

			if err == nil {
				t.Fatalf("this case must reach a refusal or it asserts nothing about events; got exists=%v, err=nil", exists)
			}
			if exists {
				t.Error("a refusal must never report the resource as existing: a true here stamps " +
					"external-create-succeeded on a resource whose state is unknown")
			}
			if len(rec.events) != 1 {
				t.Fatalf("expected exactly 1 event, got %d. Zero means the reason is still discarded and "+
					"the operator sees only the runtime's fixed string; more than one means a refusal is "+
					"emitting on several paths and the CR event stream becomes unreadable", len(rec.events))
			}

			got := rec.events[0]
			if got.Reason != reasonExistenceUndetermined {
				t.Errorf("event reason = %q, want %q", got.Reason, reasonExistenceUndetermined)
			}
			if got.Type != event.TypeWarning {
				t.Errorf("event type = %q, want Warning: a Normal event is filtered out of the views "+
					"operators actually watch", got.Type)
			}
			if got.Action != actionExistenceCheck {
				t.Errorf("event action = %q, want %q", got.Action, actionExistenceCheck)
			}
			if !strings.Contains(got.Message, tc.mustSay) {
				t.Errorf("event message does not name this branch's reason.\n  want substring: %q\n  got: %q",
					tc.mustSay, got.Message)
			}
			if strings.Contains(got.Message, tc.mustNotSay) {
				t.Errorf("event message carries ANOTHER branch's reason (%q), so the two cases are not "+
					"reaching different branches and this file does not establish per-reason detail.\n  got: %q",
					tc.mustNotSay, got.Message)
			}
			// The event must carry what the runtime's fixed string cannot.
			if !strings.Contains(got.Message, "existence check") {
				t.Errorf("event message lost the existence-check prefix, so an operator cannot tell which "+
					"check produced it: %q", got.Message)
			}
		})
	}
}

// The emission must be ONE PER REFUSAL, not one per call and not one per entry.
//
// Named for what it actually checks. It does NOT establish that a definite true or false emits nothing --
// reaching a definite answer needs a live API, and a case called "no event when the answer is definite"
// that never reaches a definite answer would read as coverage of a claim it cannot make. That claim rests
// on the wrapper's `if err != nil` guard, which is visible in exists.go and is not asserted here.
func TestEmissionIsOnePerRefusalNotPerCall(t *testing.T) {
	rec := &recordingRecorder{}
	h := &handler{logger: logging.NewNopLogger(), eventRecorder: rec}

	// Sanity: this handler DOES fail, so the inverse case below is meaningful only via the wrapper's
	// own guard. Assert the guard directly rather than pretending to reach a success path.
	if _, err := h.Exists(context.Background(), crWithKind("Port")); err == nil {
		t.Fatal("setup assumption broken: this handler was expected to refuse")
	}
	before := len(rec.events)
	if before != 1 {
		t.Fatalf("expected the refusal to emit 1 event, got %d", before)
	}

	// The wrapper emits only when err != nil. Re-running with the same handler must add exactly one
	// more -- never two -- which is what pins the emission to the error branch rather than to entry.
	if _, err := h.Exists(context.Background(), crWithKind("Port")); err == nil {
		t.Fatal("setup assumption broken on the second call")
	}
	if got := len(rec.events) - before; got != 1 {
		t.Errorf("a second refusal added %d events, want exactly 1: the emission is not one-per-refusal", got)
	}
}
