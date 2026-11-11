//go:build unit || integration

package restdefinition

import (
	"context"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// THE SECOND WRITER IS THE SUBJECT, not us.
//
// provider-runtime reads the managed object ONCE at the top of a reconcile (reconciler.go:773), hands that
// same pointer to our Update as `mg`, and after we return writes its OWN status onto it —
// `r.client.Status().Update(ctx, managed)` — through a helper that calls its function once and does NOT
// retry on conflict (reconciler.go:745).
//
// So when persistStatusWithRetry writes through a FRESHLY READ object, the server's resourceVersion moves
// and the runtime is left holding the old one. Its write then fails with
// `cannot update managed resource status: ... the object has been modified`, the reconcile returns an
// error, and its condition write for that pass is lost — on a pass that succeeded.
//
// Measured by azuredevops-provider-kog on the 0.57.0 upgrade: 25 kinds, 25 conflicts. By
// openstack-ironic-operator-kog on the same upgrade: 4 kinds, 4 conflicts. **An exact one-per-definition
// ratio is the signature of something deterministic**, not of a race, and it is deterministic because our
// write always precedes theirs.
//
// WHY THIS TEST HAD TO BE WRITTEN THIS WAY. persistStatusWithRetry already returns nil today, and
// TestPersistStatusWithRetryMirrorsOntoTheCallersCopy already passes — it asserts the status FIELDS are
// mirrored onto the caller's copy, and they are. The mirror is simply incomplete in the one field the next
// writer depends on. So any assertion about our own call succeeding passes before and after the fix and
// proves nothing. The only assertion that can fail is one about a write performed AFTER us, using the
// object we were handed.
func TestPersistStatusWithRetryLeavesTheCallersCopyWritable(t *testing.T) {
	seed := restDefinitionForStatusTest()
	e := externalForStatusTest(t, seed, interceptor.Funcs{})
	ctx := context.Background()
	key := client.ObjectKeyFromObject(seed)

	// Step 1: the runtime's single read at the top of the reconcile. `cr` is the pointer it keeps.
	cr := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, e.kube.Get(ctx, key, cr))
	rvHeldByRuntime := cr.GetResourceVersion()
	require.NotEmpty(t, rvHeldByRuntime, "the fake client must assign a resourceVersion, or this test cannot detect a conflict")

	// Step 2: our Update persists status through a freshly read object.
	require.NoError(t, e.persistStatusWithRetry(ctx, cr, func(target *definitionv1alpha1.RestDefinition) {
		target.Status.Digest = "sha256:ours"
	}))

	// CONTROL: the write really did move the server's resourceVersion. Without this the test could pass
	// because nothing was written at all, which is the same green for the opposite reason.
	stored := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, e.kube.Get(ctx, key, stored))
	require.NotEqual(t, rvHeldByRuntime, stored.GetResourceVersion(),
		"the status write did not move the resourceVersion, so there is no conflict for this test to be about")
	require.Equal(t, "sha256:ours", stored.Status.Digest, "and it must have written what we asked")

	// Step 3: THE SUBJECT. The runtime writes its own status using the object it has held all along.
	cr.Status.OASHash = "sha256:theirs"
	err := e.kube.Status().Update(ctx, cr)

	assert.False(t, apierrors.IsConflict(err),
		"the second writer got a conflict, so every reconcile that persists status reports an error on a "+
			"pass that succeeded: provider-runtime holds this same pointer and writes status after we "+
			"return, and it does not retry. persistStatusWithRetry must put the written resourceVersion "+
			"back onto the caller's copy, not just the status fields. Got: %v", err)
	require.NoError(t, err)

	// And their write must actually land, not merely avoid erroring.
	after := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, e.kube.Get(ctx, key, after))
	assert.Equal(t, "sha256:theirs", after.Status.OASHash,
		"the second writer's value is absent, so its write was lost even though it did not conflict")
	assert.Equal(t, "sha256:ours", after.Status.Digest,
		"and ours must survive theirs: a full-status write by the runtime must not drop what we persisted")
}

// The mirror must be COMPLETE, asserted as its own statement rather than inferred from the test above.
//
// The existing mirror test covers the status fields; this covers the metadata the next writer needs. Both
// halves are the same contract — "the caller's copy reflects what was persisted" — and splitting them is
// deliberate: a future change that mirrors fields but forgets the resourceVersion again would leave the
// other test green.
func TestPersistStatusWithRetryMirrorsTheResourceVersionToo(t *testing.T) {
	seed := restDefinitionForStatusTest()
	e := externalForStatusTest(t, seed, interceptor.Funcs{})
	ctx := context.Background()
	key := client.ObjectKeyFromObject(seed)

	cr := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, e.kube.Get(ctx, key, cr))

	require.NoError(t, e.persistStatusWithRetry(ctx, cr, func(target *definitionv1alpha1.RestDefinition) {
		target.Status.Digest = "sha256:x"
	}))

	stored := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, e.kube.Get(ctx, key, stored))

	assert.Equal(t, stored.GetResourceVersion(), cr.GetResourceVersion(),
		"the caller's copy holds a stale resourceVersion after the write, so any subsequent write through "+
			"it conflicts. The server is at %s and the caller's copy at %s",
		stored.GetResourceVersion(), cr.GetResourceVersion())
}

// A FAILED write must NOT leave a resourceVersion behind.
//
// The fix assigns the written resourceVersion inside the retry closure, so it has to be assigned only
// after a successful Status().Update — otherwise an attempt that read the object and then failed to write
// it would stamp the caller's copy with a version that was never persisted, which is worse than the stale
// one it replaces: stale conflicts loudly, wrong succeeds against the wrong generation.
func TestAFailedStatusWriteDoesNotStampTheCallersCopy(t *testing.T) {
	seed := restDefinitionForStatusTest()
	e := externalForStatusTest(t, seed, alwaysFailStatusUpdate())
	ctx := context.Background()
	key := client.ObjectKeyFromObject(seed)

	cr := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, e.kube.Get(ctx, key, cr))
	before := cr.GetResourceVersion()

	err := e.persistStatusWithRetry(ctx, cr, func(target *definitionv1alpha1.RestDefinition) {
		target.Status.Digest = "sha256:never"
	})
	require.Error(t, err, "the interceptor must make every status write fail, or this test asserts nothing")

	assert.Equal(t, before, cr.GetResourceVersion(),
		"a write that never succeeded changed the caller's resourceVersion")
}

func alwaysFailStatusUpdate() interceptor.Funcs {
	return interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			// Deliberately NOT a conflict: RetryOnConflict would retry that one and the write could still
			// succeed. An internal error is terminal, so the function returns having never written.
			return apierrors.NewInternalError(assert.AnError)
		},
	}
}
