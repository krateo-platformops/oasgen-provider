package kube

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// Apply, Uninstall and UninstallFromReference used retry.Do with NO options, so every error got the
// library defaults: 10 attempts with exponential backoff from 100ms — roughly 50 seconds of waiting.
//
// MEASURED, and the timing is what exposed it: one injected Forbidden took a unit test from milliseconds
// to 51.6s, and 0.38s after this predicate landed. A permission error cannot clear inside a backoff, so
// that minute established what the first attempt already knew.
//
// It matters beyond latency because Uninstall runs during Delete, holding the finalizer. A permission
// error stalls teardown ~50s per object, and nothing retries afterwards because the caller is already in
// its final pass.
//
// ASSERTED ON THE PREDICATE RATHER THAN ON ELAPSED TIME. A timing assertion would pass or fail with CI
// load, and would not say which errors were classified wrongly.
func TestRetryableAPIError(t *testing.T) {
	gr := schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "roles"}

	// PERMANENT: the apiserver has judged the request itself, not its timing. Retrying cannot change it.
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"Forbidden — RBAC does not change inside a backoff", apierrors.NewForbidden(gr, "r", errors.New("nope"))},
		{"Unauthorized — credentials do not appear inside a backoff", apierrors.NewUnauthorized("nope")},
		{"Invalid — a malformed object does not become valid", apierrors.NewInvalid(schema.GroupKind{Group: gr.Group, Kind: "Role"}, "r", nil)},
		{"BadRequest", apierrors.NewBadRequest("nope")},
		{"MethodNotSupported", apierrors.NewMethodNotSupported(gr, "delete")},
		{"RequestEntityTooLarge", apierrors.NewRequestEntityTooLargeError("nope")},
		// NotAcceptable is also classified permanent by the predicate and is NOT exercised here:
		// apimachinery ships IsNotAcceptable with no corresponding constructor, so there is no way to
		// build one without hand-assembling a StatusError and asserting against apimachinery's internals.
		// Classified rather than dropped, because misclassifying it to keep the table complete would be
		// the wrong trade -- but the row is untested and should not be counted as covered.
	} {
		t.Run("permanent/"+tc.name, func(t *testing.T) {
			if retryableAPIError(tc.err) {
				t.Errorf("%T must NOT be retried: 10 attempts with exponential backoff is ~50s of waiting "+
					"for an answer the first attempt already gave, and Uninstall pays it while holding a "+
					"finalizer during Delete", tc.err)
			}
		})
	}

	// TRANSIENT: these are what the retry was added for, and they must still retry. Without this half the
	// predicate could return false for everything and the permanent cases above would all still pass.
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"Conflict — the resourceVersion moved; retrying is the whole point", apierrors.NewConflict(gr, "r", errors.New("moved"))},
		{"Internal — the apiserver is unwell", apierrors.NewInternalError(errors.New("boom"))},
		{"Timeout", apierrors.NewTimeoutError("slow", 1)},
		{"TooManyRequests", apierrors.NewTooManyRequestsError("throttled")},
		{"ServiceUnavailable", apierrors.NewServiceUnavailable("restarting")},
		{"a plain non-API error — a connection failure reaches here untyped", errors.New("dial tcp: connection refused")},
	} {
		t.Run("transient/"+tc.name, func(t *testing.T) {
			if !retryableAPIError(tc.err) {
				t.Errorf("%T must still be retried: narrowing the retry is only safe if it keeps retrying "+
					"the transient failures it was added for", tc.err)
			}
		})
	}

	// NotFound never reaches the predicate — both call sites treat it as success before returning — but
	// classify it as retryable anyway rather than relying on that, so a future caller that does surface it
	// behaves no worse than before this change.
	if !retryableAPIError(apierrors.NewNotFound(gr, "r")) {
		t.Error("NotFound must not be newly treated as permanent by this predicate: the call sites handle " +
			"it themselves, and changing its classification here would be an unrelated behaviour change")
	}
}

// retryableAPIError being correct is only half of the fix, and the half that does nothing on its own.
// There are THREE hand-built retry.Do call sites -- Apply, Uninstall and UninstallFromReference -- and each
// needs its own retry.RetryIf. Nothing above would notice if one were missing: delete all three and
// TestRetryableAPIError still passes, because it calls the predicate directly.
//
// The teardown test in internal/tools/deploy is not the missing assertion either. It traverses the retry
// (which is how the 51.6s was observed) but asserts on which namespaces got cleaned, so an unwired site
// makes it SLOW, not red -- and a 51s PASS is a result nobody reads.
//
// So assert the ATTEMPT COUNT, which is the channel that carries the distinction: wired, a Forbidden means
// exactly one call into the client; unwired, ten. A count is load-independent in a way elapsed time is not.
func TestRetryIfIsWiredIntoEveryRetryDoSite(t *testing.T) {
	gr := schema.GroupResource{Resource: "configmaps"}
	forbidden := func(name string) error {
		return apierrors.NewForbidden(gr, name, errors.New("no permission"))
	}
	newCM := func(name string) *corev1.ConfigMap {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name}}
		cm.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"})
		return cm
	}
	newScheme := func() *runtime.Scheme {
		s := runtime.NewScheme()
		if err := corev1.AddToScheme(s); err != nil {
			t.Fatalf("scheme: %v", err)
		}
		return s
	}

	t.Run("Apply", func(t *testing.T) {
		calls := 0
		kube := fake.NewClientBuilder().WithScheme(newScheme()).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					calls++
					return forbidden(obj.GetName())
				},
			}).Build()

		if _, err := Apply(context.Background(), kube, newCM("a"), ApplyOptions{}); err == nil {
			t.Fatal("premise: the injected Forbidden must surface as an error")
		}
		if calls != 1 {
			t.Errorf("Apply made %d attempts against a Forbidden; want 1. retry.RetryIf(retryableAPIError) "+
				"is missing from this site, so it still burns the retry-go default of 10 attempts with "+
				"exponential backoff (~50s) on an error that cannot clear", calls)
		}
	})

	t.Run("Uninstall", func(t *testing.T) {
		calls := 0
		kube := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(newCM("b")).
			WithInterceptorFuncs(interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					calls++
					return forbidden(obj.GetName())
				},
			}).Build()

		if _, err := Uninstall(context.Background(), kube, newCM("b"), UninstallOptions{}); err == nil {
			t.Fatal("premise: the injected Forbidden must surface as an error")
		}
		if calls != 1 {
			t.Errorf("Uninstall made %d attempts against a Forbidden; want 1. This is the site that runs "+
				"during Delete while a finalizer is held, so an unwired retry stalls teardown ~50s per "+
				"object with nothing afterwards to retry", calls)
		}
	})

	t.Run("UninstallFromReference", func(t *testing.T) {
		calls := 0
		kube := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(newCM("c")).
			WithInterceptorFuncs(interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					calls++
					return forbidden(obj.GetName())
				},
			}).Build()

		ref := corev1.ObjectReference{APIVersion: "v1", Kind: "ConfigMap", Namespace: "ns", Name: "c"}
		if err := UninstallFromReference(context.Background(), kube, ref, UninstallOptions{}); err == nil {
			t.Fatal("premise: the injected Forbidden must surface as an error")
		}
		if calls != 1 {
			t.Errorf("UninstallFromReference made %d attempts against a Forbidden; want 1. Each retry.Do "+
				"site needs its own RetryIf -- a shared predicate does not reach a site nobody wired it "+
				"into", calls)
		}
	})

	// THE CONTROL, and it must be able to fail for a different reason than the three arms above. Each of
	// those passes if the retry stops early -- including if it were removed outright, or capped at one
	// attempt. This arm fails in exactly that case: a Conflict must still be retried, so the second call
	// must happen and must succeed.
	t.Run("control: a transient error is still retried", func(t *testing.T) {
		calls := 0
		kube := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(newCM("d")).
			WithInterceptorFuncs(interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					calls++
					if calls == 1 {
						return apierrors.NewConflict(gr, obj.GetName(), errors.New("the resourceVersion moved"))
					}
					return c.Delete(ctx, obj, opts...)
				},
			}).Build()

		if _, err := Uninstall(context.Background(), kube, newCM("d"), UninstallOptions{}); err != nil {
			t.Fatalf("a Conflict must be retried and the second attempt must succeed, got: %v", err)
		}
		if calls != 2 {
			t.Errorf("a Conflict produced %d attempts; want 2. Narrowing the retry is only safe while it "+
				"keeps retrying what it was added for, and without this arm every assertion above would "+
				"also pass with the retry removed entirely", calls)
		}
	})
}
