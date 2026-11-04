package restResources

import (
	"context"
	"net/http"
	"testing"

	"github.com/krateo-platformops/plumbing/kubeutil/event"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/unstructured-runtime/pkg/controller"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	runtimecondition "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

// A permanent authorisation refusal on the OBSERVE path used to reach unstructured-runtime as a bare
// error, so the runtime wrote Ready=Unknown/ObserveFailed over it (worker.go:649) -- a reason a composition
// rollup buckets as CONVERGING. A credential that cannot read therefore presented as an install still in
// progress, indefinitely, and the installer's gate (strict on False/<anything>) never emitted a dependent
// (#436).
//
// THE ASSERTION IS ON THE PERSISTED CONDITION, NOT ON THE WRAP, and that is deliberate: there are TWO
// independent ways for this to fail silently and only this channel catches both.
//
//  1. a broken errors.As chain -- one fmt.Errorf("...: %v", err) instead of %w anywhere between the
//     classification and the return, and IsReadyAssessed returns false and the generic reason wins;
//  2. a condition set in memory and never written -- the object handleObserve writes Synced onto is a
//     FRESH re-fetch from the apiserver, so an unpersisted Ready is simply absent and the CR shows
//     whatever was in etcd.
//
// A test that checked only "was it wrapped" passes against (2). A test that checked only the in-memory
// object passes against nothing useful at all.
func observeAuthzHandler(t *testing.T, seed *unstructured.Unstructured) (*handler, *fake.FakeDynamicClient) {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{widgetGVR: "WidgetList"}, seed)
	return &handler{
		pluralizer:    fakePluralizer{},
		logger:        logging.NewNopLogger(),
		dynamicClient: dyn,
		eventRecorder: event.NewNopRecorder(),
	}, dyn
}

// persistedReadyReason reads the Ready reason back OUT OF THE CLIENT, so an in-memory-only write cannot
// satisfy it.
func persistedReadyReason(t *testing.T, dyn *fake.FakeDynamicClient, name string) string {
	t.Helper()
	got, err := dyn.Resource(widgetGVR).Namespace("default").Get(context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err, "the seeded object must be readable, or this assertion means nothing")
	conds, found, err := unstructured.NestedSlice(got.Object, "status", "conditions")
	require.NoError(t, err)
	if !found {
		return ""
	}
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if m["type"] == runtimecondition.TypeReady {
			if r, ok := m["reason"].(string); ok {
				return r
			}
		}
	}
	return ""
}

func TestObserveClassifiesAPermanentRefusalAndPersistsIt(t *testing.T) {
	mg := driftMG(1, nil)
	h, dyn := observeAuthzHandler(t, mg)

	denied := &restclient.StatusError{StatusCode: http.StatusForbidden}
	out := h.observeAuthorizationDenied(context.Background(), mg, "get", denied, h.logger)

	// It must STILL return an error. Returning nil from Observe means "the resource does not exist and is
	// not up to date", which dispatches a CREATE -- so a 403 on a read would provoke a write.
	require.Error(t, out,
		"observeAuthorizationDenied must not swallow the error: a nil from Observe means 'does not exist' "+
			"and would dispatch a create in response to a refused READ")

	assert.True(t, controller.IsReadyAssessed(out),
		"the returned error must carry the ReadyAssessed wrap, or unstructured-runtime overwrites the "+
			"specific reason with the generic ObserveFailed")

	assert.Equal(t, condition.ReasonAuthorizationDenied, persistedReadyReason(t, dyn, mg.GetName()),
		"AuthorizationDenied must be PERSISTED, not merely set in memory: handleObserve writes Synced onto "+
			"a fresh re-fetch, so an unpersisted Ready is absent and the CR keeps whatever was in etcd")
}

// THE CONTROL ARM. Without it, a suite where the wrap silently never worked would still show the arm above
// failing for a plausible-looking reason, and the debugging would go to the wrap rather than to the test.
func TestAnUnclassifiedObserveFailureIsLeftToTheRuntime(t *testing.T) {
	mg := driftMG(1, nil)
	h, dyn := observeAuthzHandler(t, mg)

	boom := &restclient.StatusError{StatusCode: http.StatusInternalServerError}
	out := h.observeAuthorizationDenied(context.Background(), mg, "get", boom, h.logger)

	require.Error(t, out)
	assert.False(t, controller.IsReadyAssessed(out),
		"a 500 is not a permanent refusal: it must go back UNWRAPPED so the runtime writes ObserveFailed, "+
			"which is the honest reason when we could not establish a specific one")
	assert.NotEqual(t, condition.ReasonAuthorizationDenied, persistedReadyReason(t, dyn, mg.GetName()),
		"nothing may claim an authorisation refusal for a 500")
}

// 401 IS NOT CLASSIFIED, and this arm is load-bearing rather than belt-and-braces: without it a classifier
// that swallowed 401 as well would pass both arms above.
//
// Credential rotation is delegated to a secret manager, and retrying a 401 is the mechanism by which a
// rotation in flight is survived. A classifier that treated it as permanent would convert a self-healing
// case into a terminal one wearing a confident reason.
func TestA401IsNotAPermanentRefusal(t *testing.T) {
	mg := driftMG(1, nil)
	h, dyn := observeAuthzHandler(t, mg)

	rotating := &restclient.StatusError{StatusCode: http.StatusUnauthorized}
	out := h.observeAuthorizationDenied(context.Background(), mg, "get", rotating, h.logger)

	require.Error(t, out)
	assert.False(t, controller.IsReadyAssessed(out),
		"a 401 must stay unclassified: retrying it is how a mid-reconcile credential rotation is survived")
	assert.NotEqual(t, condition.ReasonAuthorizationDenied, persistedReadyReason(t, dyn, mg.GetName()),
		"a 401 must not be recorded as a permanent authorisation refusal")
}

// A THROTTLE IS A 403 TOO. The status code alone cannot classify this -- GitHub answers a rate limit with
// 403 and distinguishes it from a real denial only in retry-after / x-ratelimit-remaining. So this arm
// proves the classifier consults the headers rather than the code, which the three arms above cannot
// distinguish between.
func TestAThrottledForbiddenIsNotAPermanentRefusal(t *testing.T) {
	mg := driftMG(1, nil)
	h, dyn := observeAuthzHandler(t, mg)

	throttled := &restclient.StatusError{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"Retry-After": []string{"30"}},
	}
	out := h.observeAuthorizationDenied(context.Background(), mg, "get", throttled, h.logger)

	require.Error(t, out)
	assert.False(t, controller.IsReadyAssessed(out),
		"a throttled 403 clears by itself: classifying it as permanent would stop a verb that would have "+
			"succeeded on the next reconcile")
	assert.NotEqual(t, condition.ReasonAuthorizationDenied, persistedReadyReason(t, dyn, mg.GetName()),
		"a throttle must not be recorded as an authorisation decision")
}
