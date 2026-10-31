package restResources

import (
	"context"
	"strings"
	"testing"

	customcondition "github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	unstructuredtools "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// --- #317: Exists must distinguish "definitely absent" from "could not look" ---
//
// The decision table these cases pin, and why the error column is the whole point. The runtime's
// incomplete-create recovery does this with each answer:
//
//	true                      -> SetExternalCreateSucceeded, done
//	false, within 2 minutes   -> requeue after 15s, marker KEPT        (safe)
//	false, past   2 minutes   -> clear the marker -> dispatch -> CREATE RUNS AGAIN
//	error                     -> Creating + ReconcileError, marker UNTOUCHED
//
// So a `false` returned for a reason other than a definite negative recreates the external resource once the
// 120-second grace period lapses. For a baremetal provider that is a duplicate physical server. Every case
// below that expects an error is a case where the plausible answer is destructive.

// stubGetter returns a fixed Info, so the early refusals AFTER the getter are actually reachable.
//
// Without it the Pending subtest below never reached its own check: Exists refuses on the nil getter first,
// so the case passed while asserting the WRONG refusal. The test name claimed one thing and the assertion
// checked another, which is worse than no test -- it reads as coverage of the Pending decision.
type stubGetter struct{ info *getter.Info }

func (s stubGetter) Get(_ *unstructured.Unstructured) (*getter.Info, error) { return s.info, nil }

// minimalInfo is enough Info to get past the nil checks and reach the state-based refusals.
func minimalInfo() *getter.Info {
	return &getter.Info{}
}

// existsHandler builds a handler with no swagger getter unless one is supplied, which is enough to reach the
// early refusals without a live API.
func existsHandler(g getter.Getter) *handler {
	return &handler{logger: logging.NewNopLogger(), swaggerInfoGetter: g}
}

func crWithKind(kind string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{}}
	u.SetKind(kind)
	u.SetName("thing-1")
	u.SetNamespace("default")
	return u
}

func TestExistsRefusesRatherThanGuessing(t *testing.T) {
	cases := []struct {
		name string
		// getter, when set, lets the case reach the refusals that come AFTER the getter lookup.
		getter getter.Getter
		// mutate prepares the CR into the state under test.
		mutate func(*unstructured.Unstructured)
		// wantErrContains is a fragment of the message, so a future reword that preserves meaning does not
		// fail the test while a change of MEANING does.
		wantErrContains string
	}{
		{
			// Not a config problem to paper over: with no getter there is no way to read anything, and
			// `false` would be a claim about the external world made from nothing.
			name:            "no swagger getter: refuses",
			mutate:          func(u *unstructured.Unstructured) {},
			wantErrContains: "swagger info getter",
		},
		{
			// Observe's get path treats notfound+Pending as EXISTS, which is right for drift detection and
			// wrong here: during recovery the open question IS whether the create landed, so `true` would
			// stamp external-create-succeeded on a resource that may not exist.
			//
			// Needs the stub getter, or Exists refuses on the nil getter first and this asserts nothing
			// about Pending at all.
			name:   "Pending condition: refuses rather than inheriting Observe's assumption",
			getter: stubGetter{info: minimalInfo()},
			mutate: func(u *unstructured.Unstructured) {
				_ = unstructuredtools.SetConditions(u, customcondition.Pending())
			},
			wantErrContains: "Pending",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := crWithKind("Thing")
			tc.mutate(u)

			h := existsHandler(tc.getter)
			exists, err := h.Exists(context.Background(), u)

			if err == nil {
				t.Fatalf("expected an error, got exists=%v -- a boolean here is a claim this code cannot support", exists)
			}
			if exists {
				t.Error("exists must be false alongside an error: an undetermined answer is never affirmative")
			}
			if !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tc.wantErrContains)
			}
		})
	}
}

// TestExistsNeverMutatesTheObject is the assertion the whole fix exists for.
//
// The wedge this replaces was caused by Observe writing the status subresource during recovery, bumping the
// object's resourceVersion so the recovery block's own bare Update conflicted on every pass. If Exists ever
// acquires a status write -- a SetConditions, an UpdateStatus, a tools.Update -- the wedge returns and
// nothing else in the suite would notice.
//
// Compares the whole object before and after rather than checking named fields, so a write to any part of it
// fails this test, including one added later by someone who did not read this comment.
func TestExistsNeverMutatesTheObject(t *testing.T) {
	u := crWithKind("Thing")
	u.SetResourceVersion("424242")
	_ = unstructuredtools.SetConditions(u, customcondition.Pending())

	before := u.DeepCopy()

	h := existsHandler(nil)
	_, _ = h.Exists(context.Background(), u)

	if diff := cmpUnstructured(before, u); diff != "" {
		t.Errorf("Exists mutated the object, which is what caused the wedge it replaces: %s", diff)
	}
}

// cmpUnstructured reports the first structural difference, or "" when the two are equal.
func cmpUnstructured(a, b *unstructured.Unstructured) string {
	if a.GetResourceVersion() != b.GetResourceVersion() {
		return "resourceVersion changed: " + a.GetResourceVersion() + " -> " + b.GetResourceVersion()
	}
	ac, _, _ := unstructured.NestedSlice(a.Object, "status", "conditions")
	bc, _, _ := unstructured.NestedSlice(b.Object, "status", "conditions")
	if len(ac) != len(bc) {
		return "status.conditions count changed"
	}
	if len(a.Object) != len(b.Object) {
		return "top-level key count changed"
	}
	return ""
}
