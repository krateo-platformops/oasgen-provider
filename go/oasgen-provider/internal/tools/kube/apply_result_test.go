//go:build unit || integration

package kube

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// WHY Apply AND Uninstall REPORT WHAT THEY DID
//
// The audit contract emits one record per object whose rules, subjects or data CHANGED. With a helper
// that returns only `error`, every caller would have to re-derive that, and several callers deriving the
// same predicate separately is how they come to disagree. So the layer that performs the write reports
// what it did, and the content comparison runs on the Before/After it hands back (#543).
//
// WHERE `unchanged` IS TESTED, AND WHY NOT HERE.
//
// ApplyUnchanged is read from the apiserver: a write that changes nothing is not persisted, so the
// resourceVersion returned is the one we sent. The FAKE client does not behave that way. Measured, with
// a control:
//
//	rv after seed+get:                      "999"
//	rv returned by a BYTE-IDENTICAL update: "1000"   (sent "999")   -> no-op detectable? NO
//	rv returned by a CHANGED update:        "1001"   (sent "1000")  -> change detectable? yes
//
// It bumps unconditionally, so under the fake a no-op and a real write are indistinguishable by
// resourceVersion and ApplyUnchanged is UNREACHABLE. A unit test for that arm would not merely be
// absent, it would FAIL. So it lives in apply_result_integration_test.go against a real apiserver, and
// it measures this premise rather than assuming it.
//
// Teaching the fake to preserve the resourceVersion with an interceptor was rejected deliberately: it
// would assert against a reimplementation of the apiserver rather than against the apiserver.

func arScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	return s
}

func arCM(name string, data map[string]string) *corev1.ConfigMap {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "krateo-system"}, Data: data}
	cm.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("ConfigMap"))
	return cm
}

func arKey(name string) client.ObjectKey {
	return client.ObjectKey{Namespace: "krateo-system", Name: name}
}

// THE FOUR OPS MUST BE DISTINCT VALUES.
//
// Nothing else in this file asserts distinctness: every arm compares a result against one constant, and
// if two constants held the same string every one of those comparisons would still pass. A mutation that
// swaps one op's literal for another that ALREADY EXISTS is invisible to tests that only ever check
// "equals the constant I expect".
func TestTheApplyOpsAreDistinct(t *testing.T) {
	ops := map[ApplyOp]string{
		ApplyCreated:   "ApplyCreated",
		ApplyUpdated:   "ApplyUpdated",
		ApplyUnchanged: "ApplyUnchanged",
		ApplyDryRun:    "ApplyDryRun",
	}
	assert.Len(t, ops, 4,
		"two ApplyOp constants share a value, so a caller cannot tell those outcomes apart and every "+
			"other test in this file still passes")

	// And none may be the zero value, which is what a result from a FAILED Apply carries.
	for op, name := range ops {
		assert.NotEmpty(t, string(op), "%s is the empty string, which is indistinguishable from a failed Apply", name)
	}

	un := map[UninstallOp]string{
		UninstallDeleted: "UninstallDeleted",
		UninstallAbsent:  "UninstallAbsent",
		UninstallDryRun:  "UninstallDryRun",
	}
	assert.Len(t, un, 3, "two UninstallOp constants share a value")
	for op, name := range un {
		assert.NotEmpty(t, string(op), "%s is the empty string", name)
	}
}

func TestApplyReportsWhatItDid(t *testing.T) {
	ctx := context.Background()

	t.Run("an absent object is created; Before is nil and After is what was stored", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(arScheme(t)).Build()

		res, err := Apply(ctx, kube, arCM("a", map[string]string{"K": "1"}), ApplyOptions{})
		require.NoError(t, err)

		assert.Equal(t, ApplyCreated, res.Op)
		assert.Nil(t, res.Before,
			"there was nothing there, so a non-nil Before would be a fabricated previous version")
		require.NotNil(t, res.After, "After must carry the object the apiserver returned")
		data := arData(t, res.After)
		assert.Equal(t, map[string]string{"K": "1"}, data)
	})

	t.Run("an existing object is updated; Before holds the OLD value and After the NEW", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(arScheme(t)).
			WithObjects(arCM("b", map[string]string{"K": "old"})).Build()

		res, err := Apply(ctx, kube, arCM("b", map[string]string{"K": "new"}), ApplyOptions{})
		require.NoError(t, err)

		assert.Equal(t, ApplyUpdated, res.Op)
		require.NotNil(t, res.Before)
		require.NotNil(t, res.After)

		beforeData := arData(t, res.Before)
		afterData := arData(t, res.After)
		assert.Equal(t, map[string]string{"K": "old"}, beforeData,
			"Before must be the version the write REPLACED; a content comparison against the new value "+
				"on both sides can never report a change")
		assert.Equal(t, map[string]string{"K": "new"}, afterData)
		assert.NotEqual(t, beforeData, afterData,
			"Before and After are the same object, so the two sides of the comparison came from one read")
	})

	t.Run("BOTH sides carry the GroupVersionKind", func(t *testing.T) {
		// A content comparison dispatches on kind -- a Secret's stringData is folded into data -- and
		// controller-runtime's typed client clears TypeMeta when it decodes a response. So an After built
		// straight from the returned object can arrive with an empty kind and silently take the wrong
		// branch. Apply sets it from the GVK it read off the object up front.
		kube := fake.NewClientBuilder().WithScheme(arScheme(t)).
			WithObjects(arCM("gvk", map[string]string{"K": "old"})).Build()

		res, err := Apply(ctx, kube, arCM("gvk", map[string]string{"K": "new"}), ApplyOptions{})
		require.NoError(t, err)

		want := corev1.SchemeGroupVersion.WithKind("ConfigMap")
		assert.Equal(t, want, res.After.GroupVersionKind(), "After lost its kind")
		assert.Equal(t, want, res.Before.GroupVersionKind(), "Before lost its kind")
	})

	t.Run("a byte-identical write still reports updated UNDER THE FAKE, and that is the fake", func(t *testing.T) {
		// This arm exists to keep the integration test honest about why it is needed. It pins the
		// measurement in the header comment: the fake bumps the resourceVersion even when nothing
		// differs, so this is `updated` here and `unchanged` against a real apiserver.
		//
		// It also fails if someone reintroduces a content comparison inside Apply without saying so --
		// which would make this `unchanged` and move per-kind semantics back into this package.
		kube := fake.NewClientBuilder().WithScheme(arScheme(t)).
			WithObjects(arCM("same", map[string]string{"K": "identical"})).Build()

		res, err := Apply(ctx, kube, arCM("same", map[string]string{"K": "identical"}), ApplyOptions{})
		require.NoError(t, err)

		assert.Equal(t, ApplyUpdated, res.Op,
			"got %q. If this is now `unchanged`, either the fake client's resourceVersion behaviour "+
				"changed (re-measure, and the integration arm may be redundant) or Apply has grown a "+
				"content comparison, which belongs with the caller that knows the kind's semantics", res.Op)
	})

	t.Run("Before and After are COPIES, not views onto live state", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(arScheme(t)).
			WithObjects(arCM("copy", map[string]string{"K": "old"})).Build()

		sent := arCM("copy", map[string]string{"K": "new"})
		res, err := Apply(ctx, kube, sent, ApplyOptions{})
		require.NoError(t, err)

		// Mutating the caller's object must not reach into the reported result: a retry writes to `obj`
		// again, so an aliased After would let a later attempt rewrite a result already reported.
		sent.Data["K"] = "mutated-by-caller"
		afterData := arData(t, res.After)
		assert.Equal(t, map[string]string{"K": "new"}, afterData,
			"After aliases the caller's object, so whoever holds the result sees later edits to it")

		// And mutating the result must not reach the cluster.
		res.Before.Object["data"] = map[string]interface{}{"K": "mutated-by-holder"}
		stored := &corev1.ConfigMap{}
		require.NoError(t, kube.Get(ctx, arKey("copy"), stored))
		assert.Equal(t, "new", stored.Data["K"], "mutating the result changed the stored object")
	})

	t.Run("a failed Apply reports NO op, not a stale one", func(t *testing.T) {
		// The zero ApplyResult is the only honest answer when the write failed: an op from an attempt
		// that did not succeed would let a caller record a write that never landed.
		kube := fake.NewClientBuilder().WithScheme(arScheme(t)).
			WithObjects(arCM("fail", map[string]string{"K": "old"})).
			WithInterceptorFuncs(interceptor.Funcs{
				Update: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.UpdateOption) error {
					return apierrors.NewForbidden(
						corev1.Resource("configmaps"), "fail", assert.AnError)
				},
			}).Build()

		res, err := Apply(ctx, kube, arCM("fail", map[string]string{"K": "new"}), ApplyOptions{})
		require.Error(t, err, "the interceptor must make the update fail, or this arm asserts nothing")

		assert.Empty(t, string(res.Op), "a failed Apply reported op %q", res.Op)
		assert.Nil(t, res.Before, "a failed Apply handed back a Before")
		assert.Nil(t, res.After, "a failed Apply handed back an After")
	})

	t.Run("the op comes from the attempt that SUCCEEDED, not the first one", func(t *testing.T) {
		// A conflict re-reads the live object and tries again. The result must describe the attempt that
		// landed -- and `res` is reset at the top of every attempt so a failed one cannot leave an
		// outcome behind for a later success to report.
		var updates int
		kube := fake.NewClientBuilder().WithScheme(arScheme(t)).
			WithObjects(arCM("retry", map[string]string{"K": "old"})).
			WithInterceptorFuncs(interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					updates++
					if updates == 1 {
						return apierrors.NewConflict(
							corev1.Resource("configmaps"), "retry", assert.AnError)
					}
					return c.Update(ctx, obj, opts...)
				},
			}).Build()

		res, err := Apply(ctx, kube, arCM("retry", map[string]string{"K": "new"}), ApplyOptions{})
		require.NoError(t, err)
		require.Equal(t, 2, updates, "the first update must have conflicted, or there was no retry to test")

		assert.Equal(t, ApplyUpdated, res.Op)
		require.NotNil(t, res.Before, "the successful attempt re-read the object, so Before must be set")
		beforeData := arData(t, res.Before)
		assert.Equal(t, map[string]string{"K": "old"}, beforeData)
	})
}

// A DRY RUN IS ITS OWN OUTCOME, and the three write ops would each be a lie a caller could act on.
//
// A dry-run Update returns the stored resourceVersion untouched -- indistinguishable from a genuine
// no-op -- so it would otherwise report `unchanged`; and a dry-run Create would report `created` and let
// an audit caller record a write that never happened. deploy.go has a live DryRun caller.
func TestADryRunReportsDryRunAndNotAWrite(t *testing.T) {
	ctx := context.Background()

	t.Run("dry-run create", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(arScheme(t)).Build()

		res, err := Apply(ctx, kube, arCM("dr-new", map[string]string{"K": "1"}),
			ApplyOptions{DryRun: []string{"All"}})
		require.NoError(t, err)

		assert.Equal(t, ApplyDryRun, res.Op,
			"a dry-run create reported %q, which says a write happened", res.Op)
	})

	t.Run("dry-run update", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(arScheme(t)).
			WithObjects(arCM("dr-old", map[string]string{"K": "old"})).Build()

		res, err := Apply(ctx, kube, arCM("dr-old", map[string]string{"K": "new"}),
			ApplyOptions{DryRun: []string{"All"}})
		require.NoError(t, err)

		assert.Equal(t, ApplyDryRun, res.Op,
			"a dry-run update reported %q. `unchanged` is the dangerous one: it is what a genuine no-op "+
				"reports, so a caller cannot tell a validated-only write from one that changed nothing", res.Op)
	})

	// CONTROL: without DryRun the same two calls must report real ops, or this test would pass against
	// an Apply that reported ApplyDryRun unconditionally.
	t.Run("CONTROL the same calls without DryRun report real writes", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(arScheme(t)).
			WithObjects(arCM("ctl-old", map[string]string{"K": "old"})).Build()

		created, err := Apply(ctx, kube, arCM("ctl-new", map[string]string{"K": "1"}), ApplyOptions{})
		require.NoError(t, err)
		assert.Equal(t, ApplyCreated, created.Op)

		updated, err := Apply(ctx, kube, arCM("ctl-old", map[string]string{"K": "new"}), ApplyOptions{})
		require.NoError(t, err)
		assert.Equal(t, ApplyUpdated, updated.Op)
	})
}

// A DRY-RUN UNINSTALL IS NOT A DELETION. Same trap as the apply side: the apiserver accepts a dry-run
// Delete and removes nothing, so reporting `deleted` would let an audit caller record a deletion that
// never happened.
func TestADryRunUninstallIsNotADeletion(t *testing.T) {
	ctx := context.Background()
	kube := fake.NewClientBuilder().WithScheme(arScheme(t)).
		WithObjects(arCM("dr-del", map[string]string{"K": "v"})).Build()

	res, err := Uninstall(ctx, kube, arCM("dr-del", nil), UninstallOptions{DryRun: []string{"All"}})
	require.NoError(t, err)
	assert.Equal(t, UninstallDryRun, res.Op,
		"a dry-run uninstall reported %q, which says an object was removed", res.Op)

	// CONTROL: without DryRun the same call must report a real deletion, or this test would pass
	// against an Uninstall that reported UninstallDryRun unconditionally.
	real, err := Uninstall(ctx, kube, arCM("dr-del", nil), UninstallOptions{})
	require.NoError(t, err)
	assert.Equal(t, UninstallDeleted, real.Op,
		"the control reported %q, so UninstallDryRun carries no information", real.Op)
}

func TestUninstallReportsWhatItDid(t *testing.T) {
	ctx := context.Background()

	t.Run("an existing object is deleted, and Before holds what was removed", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(arScheme(t)).
			WithObjects(arCM("h", map[string]string{"K": "v"})).Build()

		res, err := Uninstall(ctx, kube, arCM("h", nil), UninstallOptions{})
		require.NoError(t, err)

		assert.Equal(t, UninstallDeleted, res.Op)
		require.NotNil(t, res.Before, "a delete that removed something must say what it removed")
		data := arData(t, res.Before)
		assert.Equal(t, map[string]string{"K": "v"}, data)
	})

	t.Run("an absent object is absent, and Before is nil", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(arScheme(t)).Build()

		res, err := Uninstall(ctx, kube, arCM("i", nil), UninstallOptions{})
		require.NoError(t, err)

		assert.Equal(t, UninstallAbsent, res.Op)
		assert.Nil(t, res.Before,
			"nothing was there, so a non-nil Before would invent an object to record the deletion of")
	})

	t.Run("deleting twice is deleted then absent", func(t *testing.T) {
		// The second call is the idempotent one, and it must NOT report a deletion: an audit caller
		// recording on `deleted` would emit a second record for one removal.
		kube := fake.NewClientBuilder().WithScheme(arScheme(t)).
			WithObjects(arCM("j", map[string]string{"K": "v"})).Build()

		first, err := Uninstall(ctx, kube, arCM("j", nil), UninstallOptions{})
		require.NoError(t, err)
		second, err := Uninstall(ctx, kube, arCM("j", nil), UninstallOptions{})
		require.NoError(t, err)

		assert.Equal(t, UninstallDeleted, first.Op)
		assert.Equal(t, UninstallAbsent, second.Op)
		assert.NotEqual(t, first.Op, second.Op,
			"both calls report the same op, so a caller cannot tell the removal from the no-op after it")
	})
}

// arData is the object's `data` map, failing the test rather than returning a zero value that would
// read as "the field was empty".
func arData(t *testing.T, u *unstructured.Unstructured) map[string]string {
	t.Helper()
	m, found, err := unstructured.NestedStringMap(u.Object, "data")
	require.NoError(t, err)
	require.True(t, found, "the object has no data field, so comparing it to an expected map proves nothing")
	return m
}
