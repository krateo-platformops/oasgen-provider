//go:build unit || integration

package kube

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// ApplyOptions.FromCurrent exists because Apply's update path is a FULL Update with the object the
// caller rendered: every field present on the live object and absent from that render is destroyed.
// Issue 595 is the first measured victim (the generated controller's restart annotation); spec.replicas
// under issue 550's scale-to-zero is the next.
//
// These tests are about the MECHANISM -- when the hook runs, what it is handed, and what its error does.
// What the restart hook itself copies is tested in internal/tools/deployment, against the apiserver's
// copy of the object.

func fcScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	return s
}

func fcCM(name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "krateo-system"},
		Data:       data,
	}
}

func TestFromCurrentRunsOnUpdateAndIsHandedTheObjectAboutToBeReplaced(t *testing.T) {
	ctx := context.Background()
	live := fcCM("cm", map[string]string{"KEEP": "from-the-other-writer"})
	kube := fake.NewClientBuilder().WithScheme(fcScheme(t)).WithObjects(live).Build()

	calls := 0
	desired := fcCM("cm", map[string]string{"RENDERED": "yes"})

	err := Apply(ctx, kube, desired, ApplyOptions{
		FromCurrent: func(current *unstructured.Unstructured, d client.Object) error {
			calls++
			// The hook must see the LIVE object, not the render it is about to replace.
			v, found, err := unstructured.NestedString(current.Object, "data", "KEEP")
			require.NoError(t, err)
			require.True(t, found, "the hook was handed an object without the live object's data")
			assert.Equal(t, "from-the-other-writer", v)

			// And `d` must be the very object the caller passed, so a typed caller can mutate it.
			cm, ok := d.(*corev1.ConfigMap)
			require.True(t, ok, "desired was not the caller's own typed object, but a %T", d)
			cm.Data["KEEP"] = v
			return nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls, "the hook ran %d times for one uncontended apply", calls)

	stored := &corev1.ConfigMap{}
	require.NoError(t, kube.Get(ctx, client.ObjectKeyFromObject(live), stored))
	assert.Equal(t, map[string]string{"RENDERED": "yes", "KEEP": "from-the-other-writer"}, stored.Data,
		"what the hook wrote did not reach the apiserver, so the hook runs after the object is already sent")
}

func TestFromCurrentIsNotCalledOnCreate(t *testing.T) {
	// There is nothing to carry over from an object that does not exist, and calling the hook with an
	// empty `current` would invite a hook to copy a zero value onto a fresh object.
	ctx := context.Background()
	kube := fake.NewClientBuilder().WithScheme(fcScheme(t)).Build()

	calls := 0
	err := Apply(ctx, kube, fcCM("fresh", map[string]string{"A": "1"}), ApplyOptions{
		FromCurrent: func(_ *unstructured.Unstructured, _ client.Object) error {
			calls++
			return nil
		},
	})
	require.NoError(t, err)
	assert.Zero(t, calls, "the hook ran on the create path, where there is no previous version to copy from")

	// CONTROL: the create really happened, so the zero above is not "Apply did nothing".
	stored := &corev1.ConfigMap{}
	require.NoError(t, kube.Get(ctx, client.ObjectKey{Name: "fresh", Namespace: "krateo-system"}, stored))
}

// A HOOK ERROR MUST NOT RETRY, and "1 attempt" only means that if a retryable error DOES retry in the
// same test. Without the control arm this passes equally against a retry that is wired up wrong.
func TestAFromCurrentErrorIsPermanentAndTheUpdateIsNeverSent(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("the caller refuses this object")

	t.Run("the hook's error ends Apply after one attempt", func(t *testing.T) {
		updates := 0
		kube := fake.NewClientBuilder().WithScheme(fcScheme(t)).WithObjects(fcCM("a", nil)).
			WithInterceptorFuncs(interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					updates++
					return c.Update(ctx, obj, opts...)
				},
			}).Build()

		calls := 0
		err := Apply(ctx, kube, fcCM("a", nil), ApplyOptions{
			FromCurrent: func(_ *unstructured.Unstructured, _ client.Object) error {
				calls++
				return boom
			},
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, boom, "the caller's own error must reach the caller, not be replaced by a wrapper's text")
		assert.Equal(t, 1, calls,
			"the hook ran %d times. Its error is the caller's verdict about the object it was shown, so "+
				"showing it the same object nine more times cannot change the answer -- and the default "+
				"retry budget is ~50s of backoff", calls)
		assert.Zero(t, updates,
			"the Update was sent %d time(s) despite the hook refusing the object; a refused apply must not "+
				"reach the apiserver", updates)
	})

	t.Run("CONTROL: a transient error still retries", func(t *testing.T) {
		// If this arm also reported one attempt, the arm above would prove nothing about permanence: a
		// retry that never retries produces the same "1" for both.
		//
		// It succeeds on the second attempt rather than failing forever on purpose. Letting the default
		// budget run out costs ~50s of real backoff per run, which is how long the ORIGINAL unwired
		// retry took (measured 51.6s, see retryable_test.go) -- a control must not be the slowest thing
		// in the package.
		updates := 0
		kube := fake.NewClientBuilder().WithScheme(fcScheme(t)).WithObjects(fcCM("b", nil)).
			WithInterceptorFuncs(interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					updates++
					if updates == 1 {
						return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "b", errors.New("modified"))
					}
					return c.Update(ctx, obj, opts...)
				},
			}).Build()

		require.NoError(t, Apply(ctx, kube, fcCM("b", nil), ApplyOptions{}),
			"a conflict must be retried to success, not returned")
		assert.Equal(t, 2, updates,
			"a conflict was attempted %d time(s); want 2 (one conflict, one success). If it is 1, this "+
				"package's retry is not retrying and the permanence assertion above is vacuous", updates)
	})

	t.Run("the hook is re-run on each retry, so what it copies is never stale", func(t *testing.T) {
		// The hook sits INSIDE the retry: a conflict re-reads the live object and asks the hook again.
		// If it sat outside, a hook could copy a value from a version that is no longer the one being
		// replaced -- which is the whole reason this is a hook and not a pre-Apply mutation by the caller.
		attempts := 0
		kube := fake.NewClientBuilder().WithScheme(fcScheme(t)).WithObjects(fcCM("c", nil)).
			WithInterceptorFuncs(interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					attempts++
					if attempts < 3 {
						return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "c", errors.New("modified"))
					}
					return c.Update(ctx, obj, opts...)
				},
			}).Build()

		calls := 0
		require.NoError(t, Apply(ctx, kube, fcCM("c", nil), ApplyOptions{
			FromCurrent: func(_ *unstructured.Unstructured, _ client.Object) error {
				calls++
				return nil
			},
		}))
		assert.Equal(t, attempts, calls,
			"the hook ran %d times across %d update attempts; it must run once per attempt, from the "+
				"version that attempt is about to replace", calls, attempts)
		require.Greater(t, attempts, 1, "premise: the interceptor must have forced at least one retry")
	})
}

// A nil hook must leave Apply exactly as it was: this is the path every other caller in the tree uses.
func TestANilFromCurrentChangesNothing(t *testing.T) {
	ctx := context.Background()
	kube := fake.NewClientBuilder().WithScheme(fcScheme(t)).WithObjects(fcCM("d", map[string]string{"OLD": "1"})).Build()

	require.NoError(t, Apply(ctx, kube, fcCM("d", map[string]string{"NEW": "2"}), ApplyOptions{}))

	stored := &corev1.ConfigMap{}
	require.NoError(t, kube.Get(ctx, client.ObjectKey{Name: "d", Namespace: "krateo-system"}, stored))
	assert.Equal(t, map[string]string{"NEW": "2"}, stored.Data,
		"with no hook the apply must still fully replace the object, which is what every other call site relies on")
}
