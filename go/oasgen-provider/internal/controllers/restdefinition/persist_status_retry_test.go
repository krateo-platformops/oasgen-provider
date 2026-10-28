package restdefinition

import (
	"context"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// persistStatusWithRetry is the mechanism BOTH of Create's phases now end in, so these cover it directly
// rather than through either call site. Phase 1's own tests (create_phase_status_test.go) cover its
// mutation; phase 2's writes seven fields, and what needed covering was the shared machinery and the
// contract it imposes on callers (#259).

// TestPersistStatusWithRetryMirrorsOntoTheCallersCopy is the defect a second hand-rolled copy of this
// would have introduced.
//
// The write goes to a FRESHLY READ object, so without the mirror the caller's `cr` still holds the
// pre-write state. Phase 2 is where that bites: Create continues after the write, and provider-runtime
// then inspects the same object to decide its own bookkeeping. A mirror that forgets a field is invisible
// at the point of the mistake and surfaces later as a stale read.
func TestPersistStatusWithRetryMirrorsOntoTheCallersCopy(t *testing.T) {
	cr := restDefinitionForStatusTest()
	e := externalForStatusTest(t, cr, interceptor.Funcs{})

	require.NoError(t, e.persistStatusWithRetry(context.Background(), cr, func(target *definitionv1alpha1.RestDefinition) {
		target.Status.Digest = "sha256:deadbeef"
		target.Status.OASHash = "sha256:cafe"
	}))

	assert.Equal(t, "sha256:deadbeef", cr.Status.Digest,
		"the caller's copy must carry what was persisted, not the pre-write state")
	assert.Equal(t, "sha256:cafe", cr.Status.OASHash)

	// And the same values must actually be on the server, not only mirrored locally.
	stored := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, e.kube.Get(context.Background(), client.ObjectKeyFromObject(cr), stored))
	assert.Equal(t, "sha256:deadbeef", stored.Status.Digest)
	assert.Equal(t, "sha256:cafe", stored.Status.OASHash)
}

// TestPersistStatusWithRetryRunsTheMutationOnEachAttempt pins the contract the doc comment states: mutate
// runs more than once, on more than one object. A caller deriving a value from the object it is handed
// would therefore compute it from whatever the latest read says rather than from the spec this reconcile
// acted on -- which is why phase 2 computes resourceHash once, before the retry, and captures it.
func TestPersistStatusWithRetryRunsTheMutationOnEachAttempt(t *testing.T) {
	cr := restDefinitionForStatusTest()
	attempts := 0
	e := externalForStatusTest(t, cr, conflictOnFirstStatusUpdate(&attempts))

	mutations := 0
	require.NoError(t, e.persistStatusWithRetry(context.Background(), cr, func(target *definitionv1alpha1.RestDefinition) {
		mutations++
		target.Status.Digest = "sha256:x"
	}))

	assert.Equal(t, 2, attempts, "one injected conflict must be retried exactly once")
	// Twice for the two write attempts, once more for the mirror.
	assert.Equal(t, 3, mutations,
		"mutate must run per attempt plus once for the mirror; a caller may not derive values from its argument")
}

// TestPersistStatusWithRetryMergesConcurrentStatusChanges is the reason each attempt re-reads instead of
// retrying the object it was handed.
//
// A stale retry would write the whole status as it stood before, silently reverting whatever another
// writer put there in between. Here a field is changed on the server between the failed attempt and the
// retry; it must survive.
func TestPersistStatusWithRetryMergesConcurrentStatusChanges(t *testing.T) {
	cr := restDefinitionForStatusTest()
	attempts := 0

	// The interceptor needs the very client it is installed on, to land the competing write through the
	// same store. `c` is that client, so no second handle is needed.
	e := externalForStatusTest(t, cr, interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, name string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			attempts++
			if attempts > 1 {
				return c.Status().Update(ctx, obj, opts...)
			}
			// Another writer lands a status change, and then our attempt conflicts.
			other := &definitionv1alpha1.RestDefinition{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(obj), other))
			other.Status.AuthSecretDigest = "written-by-someone-else"
			require.NoError(t, c.Status().Update(ctx, other))
			return apierrors.NewConflict(
				schema.GroupResource{Group: "ogen.krateo.io", Resource: "restdefinitions"},
				obj.GetName(), assert.AnError)
		},
	})

	require.NoError(t, e.persistStatusWithRetry(context.Background(), cr, func(target *definitionv1alpha1.RestDefinition) {
		target.Status.Digest = "sha256:ours"
	}))

	stored := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, e.kube.Get(context.Background(), client.ObjectKeyFromObject(cr), stored))
	assert.Equal(t, "sha256:ours", stored.Status.Digest, "our own write must land")
	assert.Equal(t, "written-by-someone-else", stored.Status.AuthSecretDigest,
		"a concurrent status change must be merged, not clobbered by a stale retry")
}

// TestPersistStatusWithRetrySurfacesAPersistentConflict -- retrying forever turns a wedge into a hang, and
// swallowing the error would report a success that wrote nothing.
func TestPersistStatusWithRetrySurfacesAPersistentConflict(t *testing.T) {
	cr := restDefinitionForStatusTest()
	attempts := 0
	e := externalForStatusTest(t, cr, interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, name string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			attempts++
			return apierrors.NewConflict(
				schema.GroupResource{Group: "ogen.krateo.io", Resource: "restdefinitions"},
				obj.GetName(), assert.AnError)
		},
	})

	err := e.persistStatusWithRetry(context.Background(), cr, func(target *definitionv1alpha1.RestDefinition) {
		target.Status.Digest = "sha256:never-lands"
	})

	require.Error(t, err, "a conflict that never clears must surface rather than be swallowed")
	assert.True(t, apierrors.IsConflict(err), "and it must still read as a conflict: %v", err)
	assert.Greater(t, attempts, 1, "it must have retried rather than failing on the first attempt")
	assert.Less(t, attempts, 20, "the retry must be bounded")

	assert.Empty(t, cr.Status.Digest,
		"nothing was persisted, so the caller's copy must not claim otherwise")
}
