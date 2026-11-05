//go:build unit

package restResources

import (
	"context"
	"errors"
	"net/http"
	"testing"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"
)

// #468. Both stop helpers exist to PERSIST a condition for a failure that will never succeed, so the
// controller stops retrying it. They returned bool, where true meant "classified and persisted, stop",
// and they returned FALSE on any status-write failure -- including a benign 409.
//
// That cost the classification. The caller read false as "not my case" and returned the ORIGINAL error,
// so a permanent 403 or host-policy refusal reached the runtime as a generic failure, was recorded as
// ReconcileError, counted toward maxRetries and was retried. A transient conflict downgraded a precise
// permanent-failure condition into retried noise -- the shape unstructured-runtime#30 set out to remove,
// arriving by another door.
//
// WHY `false` WAS NEVERTHELESS THE RIGHT HALF of a two-way choice: returning true with the condition
// unwritten makes the caller stop retrying a resource carrying NO condition at all, which is silently
// inert. bool could not express the third case -- "the classification is correct and the write deferred".
//
// DORMANCY, and this is the paragraph that should stop a future reader deleting these arms. On
// unstructured-runtime v1.7.6, which rest-dynamic-controller pins today, tools.UpdateStatus wraps the
// write in retry.RetryOnConflict and usually SUCCEEDS on attempt 2 -- by re-submitting the caller's stale
// stanza onto the fresh resourceVersion. So the helper mostly sees nil and returns true after a write
// that may have clobbered a newer one, and a conflict only reaches this code if all five attempts
// conflict. These arms are correct-but-rare on v1.7.6 and become the common path once core-provider#359
// lands and UpdateStatus stops re-submitting. **"No conflicts observed on v1.7.6" is not evidence that
// this is unneeded.**

// alwaysConflicting returns a handler whose every status write fails with a 409 -- which is what
// RetryOnConflict exhausting its budget looks like from the caller's side.
func alwaysConflicting(t *testing.T) *handler {
	t.Helper()
	h, dyn := observeAuthzHandler(t, driftMG(1, nil))
	dyn.PrependReactor("update", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(
			schema.GroupResource{Group: widgetGVR.Group, Resource: widgetGVR.Resource}, "widget-1",
			errors.New("the object has been modified; please apply your changes to the latest version"))
	})
	// PREMISE. If the reactor does not actually make the write fail, every arm below passes for the wrong
	// reason -- the helper would simply succeed and return (true, nil).
	_, cerr := h.stopOnAuthorizationDenied(context.Background(), driftMG(1, nil), "get",
		&restclient.StatusError{StatusCode: http.StatusForbidden}, h.logger)
	require.Error(t, cerr, "the reactor must make status writes conflict, or these arms assert nothing")
	require.True(t, apierrors.IsConflict(cerr), "and it must conflict specifically")
	return h
}

func deniedErr() error { return &restclient.StatusError{StatusCode: http.StatusForbidden} }

// A conflicted persist must report the conflict rather than a bare false. Both helpers, because they are
// separate functions carrying separate copies of the block.
func TestAConflictedPersistReportsTheConflictRatherThanLosingTheClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*testing.T, *handler) (bool, error)
	}{
		{"stopOnAuthorizationDenied", func(t *testing.T, h *handler) (bool, error) {
			return h.stopOnAuthorizationDenied(context.Background(), driftMG(1, nil), "create", deniedErr(), h.logger)
		}},
		{"stopOnEndpointRefusal", func(t *testing.T, h *handler) (bool, error) {
			return h.stopOnEndpointRefusal(context.Background(), driftMG(1, nil), "create", realRefusal(t), h.logger)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handled, cerr := tc.call(t, alwaysConflicting(t))

			require.Error(t, cerr,
				"a conflicted write must surface, or the caller falls back to the original permanent error "+
					"and retries something that can never succeed")
			assert.True(t, apierrors.IsConflict(cerr),
				"it must still satisfy apierrors.IsConflict, which is what the runtime keys on -- and what "+
					"makes this correct both on v1.7.6 and after core-provider#359")
			assert.False(t, handled,
				"handled must be FALSE: the condition did not persist, and true would make the caller stop "+
					"retrying a resource with no condition written")
		})
	}
}

// THE INVARIANT. (true, non-nil) is representable here and meaningless, so it is asserted rather than
// made impossible. A three-valued type would make it unrepresentable and would also stop the conflict
// composing straight through as an error -- which is the property the runtime's handling depends on, so
// bool-plus-error wins the trade and this arm pays for it.
func TestStopHelpersNeverReportHandledWithAnError(t *testing.T) {
	for name, call := range map[string]func(*testing.T, *handler) (bool, error){
		"authz under conflict": func(t *testing.T, h *handler) (bool, error) {
			return h.stopOnAuthorizationDenied(context.Background(), driftMG(1, nil), "create", deniedErr(), h.logger)
		},
		"refusal under conflict": func(t *testing.T, h *handler) (bool, error) {
			return h.stopOnEndpointRefusal(context.Background(), driftMG(1, nil), "create", realRefusal(t), h.logger)
		},
		"authz with a working write": func(t *testing.T, h *handler) (bool, error) {
			w, _ := observeAuthzHandler(t, driftMG(1, nil))
			return w.stopOnAuthorizationDenied(context.Background(), driftMG(1, nil), "create", deniedErr(), w.logger)
		},
	} {
		t.Run(name, func(t *testing.T) {
			handled, err := call(t, alwaysConflicting(t))
			if handled && err != nil {
				t.Fatalf("(handled=true, err=%v): the caller returns err on this, so it would both stop "+
					"retrying AND report a failure", err)
			}
		})
	}
}

// An error that was never this helper's business is still a clean (false, nil). Without this, a helper
// could report a deferral for anything at all and the arms above would not notice.
func TestAnUnrelatedErrorIsStillACleanNotMine(t *testing.T) {
	h := alwaysConflicting(t)
	unrelated := errors.New("connection reset by peer")

	handled, cerr := h.stopOnAuthorizationDenied(context.Background(), driftMG(1, nil), "create", unrelated, h.logger)
	assert.False(t, handled)
	assert.NoError(t, cerr, "the write is never attempted for an unrelated error, so there is no deferral")

	handled, cerr = h.stopOnEndpointRefusal(context.Background(), driftMG(1, nil), "create", unrelated, h.logger)
	assert.False(t, handled)
	assert.NoError(t, cerr)
}
