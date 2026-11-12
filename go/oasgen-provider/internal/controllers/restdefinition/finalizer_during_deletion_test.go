//go:build unit || integration

package restdefinition

import (
	"context"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/provider-runtime/pkg/meta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// holdFinalizer stands in for finalizer.managedresource.krateo.io: something OTHER than ours that keeps
// the object alive after a Delete, which is the state that was measured -- a RestDefinition holding only
// the managed-resource finalizer, with a deletionTimestamp, and ours absent.
const holdFinalizer = "test.krateo.io/hold"

// countingList returns a List interceptor that reports n instances, and an Update counter.
//
// The Update count is the subject of the first test, not the error return. `manageFinalizers` already
// returns an error today in the #612 case, so "it returns nil now" is a true statement about the fix and
// a weak assertion about it: a version that swallowed the error and still attempted the write would
// satisfy it. What must not happen is the WRITE.
func countingList(n int, updates *int) interceptor.Funcs {
	return interceptor.Funcs{
		List: func(_ context.Context, _ client.WithWatch, l client.ObjectList, _ ...client.ListOption) error {
			ul, ok := l.(*unstructured.UnstructuredList)
			if !ok {
				return nil
			}
			ul.Items = nil
			for i := 0; i < n; i++ {
				ul.Items = append(ul.Items, unstructured.Unstructured{
					Object: map[string]any{"metadata": map[string]any{"name": "p" + string(rune('0'+i))}},
				})
			}
			return nil
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, isRD := obj.(*definitionv1alpha1.RestDefinition); isRD {
				*updates++
			}
			return c.Update(ctx, obj, opts...)
		},
	}
}

// fzDeleting builds a RestDefinition that is genuinely mid-deletion, and proves it before returning.
//
// A deletionTimestamp cannot be fabricated on a fake client -- it refuses an object created with one and
// no finalizers -- so the object is given a foreign finalizer and then actually deleted. If the fake
// client did not implement finalizer semantics the Get below would fail, which is why the premise is a
// require and not a comment.
func fzDeleting(t *testing.T, withOurFinalizer bool, funcs interceptor.Funcs) (client.Client, *definitionv1alpha1.RestDefinition) {
	t.Helper()
	ctx := context.Background()

	seed := fzDefinition(withOurFinalizer)
	meta.AddFinalizer(seed, holdFinalizer)
	kube, live := fzLive(t, seed, funcs)

	require.NoError(t, kube.Delete(ctx, live))
	require.NoError(t, kube.Get(ctx, client.ObjectKeyFromObject(live), live),
		"premise: the object must SURVIVE the Delete because of its foreign finalizer; if it is gone, "+
			"this fake client does not implement finalizer semantics and the test below is void")
	require.NotNil(t, live.GetDeletionTimestamp(),
		"premise: the object must be mid-deletion, which is the whole subject of this test")
	require.True(t, meta.FinalizerExists(live, holdFinalizer), "premise: the foreign finalizer must remain")
	return kube, live
}

func storedRD(t *testing.T, kube client.Client, cr *definitionv1alpha1.RestDefinition) *definitionv1alpha1.RestDefinition {
	t.Helper()
	out := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, kube.Get(context.Background(), client.ObjectKeyFromObject(cr), out))
	return out
}

// TestNoFinalizerAddOnAnObjectBeingDeleted is #612.
//
// The apiserver forbids adding a finalizer to an object with a deletionTimestamp:
//
//	metadata.finalizers: Forbidden: no new finalizers can be added if the object is being deleted
//
// manageFinalizers attempted it whenever it counted instances, with no deletionTimestamp check, and
// returned the rejection as an error. `observe` then failed on every pass, so the delete path -- and
// therefore the live-instance guard that refuses the uninstall -- was never reached. The RestDefinition
// sat in Terminating forever while the operator was shown a message about finalizers, which says nothing
// about the instances that are the real reason it cannot go away.
//
// Measured by github-provider-kog on 0.60.0: a CR created 10 seconds after the RestDefinition's previous
// reconcile and the RestDefinition deleted 32 seconds later.
func TestNoFinalizerAddOnAnObjectBeingDeleted(t *testing.T) {
	ctx := context.Background()

	t.Run("deleting, instances exist, our finalizer absent: no write is attempted", func(t *testing.T) {
		var updates int
		kube, cr := fzDeleting(t, false, countingList(1, &updates))
		updates = 0 // the setup's own writes are not the subject

		err := manageFinalizers(ctx, kube, cr, func(string, ...any) {})

		require.NoError(t, err,
			"an object that is already being deleted cannot gain a finalizer, and failing here stops the "+
				"delete path from ever running -- including the live-instance guard that is supposed to "+
				"produce the refusal the operator sees (#612)")
		assert.Zero(t, updates,
			"an Update was attempted on a RestDefinition that is being deleted. The apiserver rejects it "+
				"with `no new finalizers can be added if the object is being deleted`, and %d attempt(s) "+
				"means the rejection is back", updates)
		assert.False(t, meta.FinalizerExists(storedRD(t, kube, cr), restresourcesStillExistFinalizer),
			"the finalizer reached the stored object, which the apiserver would not have permitted")
	})

	t.Run("CONTROL -- NOT deleting: the finalizer is still added", func(t *testing.T) {
		// Without this arm the test above passes against a version that never adds the finalizer at all,
		// which would remove the #125 protection entirely: every RestDefinition would become deletable
		// while its instances were live.
		var updates int
		kube, cr := fzLive(t, fzDefinition(false), countingList(1, &updates))
		updates = 0

		require.NoError(t, manageFinalizers(ctx, kube, cr, func(string, ...any) {}))

		assert.NotZero(t, updates, "the finalizer must be persisted, not just set in memory")
		assert.True(t, meta.FinalizerExists(storedRD(t, kube, cr), restresourcesStillExistFinalizer),
			"an instance exists and the object is NOT being deleted, so deletion must be blocked")
	})

	t.Run("deleting, no instances, our finalizer present: it is still REMOVED", func(t *testing.T) {
		// The legitimate deletion path. Removing a finalizer from an object being deleted is permitted and
		// is how deletion completes, so the fix must not have made the remove branch conditional too --
		// that would make every RestDefinition with instances permanently undeletable even after its
		// instances were gone.
		var updates int
		kube, cr := fzDeleting(t, true, countingList(0, &updates))
		updates = 0

		require.NoError(t, manageFinalizers(ctx, kube, cr, func(string, ...any) {}))

		assert.NotZero(t, updates, "the removal must be persisted")
		assert.False(t, meta.FinalizerExists(storedRD(t, kube, cr), restresourcesStillExistFinalizer),
			"the instances are gone, so the finalizer must be released even though the object is "+
				"mid-deletion -- otherwise deletion can never complete")
	})

	t.Run("already held, instances exist: no write on every reconcile", func(t *testing.T) {
		var updates int
		kube, cr := fzLive(t, fzDefinition(true), countingList(1, &updates))
		updates = 0

		require.NoError(t, manageFinalizers(ctx, kube, cr, func(string, ...any) {}))

		assert.Zero(t, updates,
			"the finalizer was already present, so re-persisting it writes to the apiserver on every "+
				"reconcile for no change; %d write(s)", updates)
	})
}

// TestTheDeletingCaseIsDISTINCTFromTheHeldCase asserts the two silent arms are not the same arm.
//
// Both "already held" and "being deleted" end without a write, so a single implementation that skipped
// the add whenever the finalizer was absent would satisfy every count above. The distinction is what the
// object ends up holding: in one case our finalizer is present, in the other it must NOT have appeared.
func TestTheDeletingCaseIsDISTINCTFromTheHeldCase(t *testing.T) {
	ctx := context.Background()

	var u1, u2 int
	kubeDeleting, crDeleting := fzDeleting(t, false, countingList(1, &u1))
	kubeHeld, crHeld := fzLive(t, fzDefinition(true), countingList(1, &u2))

	require.NoError(t, manageFinalizers(ctx, kubeDeleting, crDeleting, func(string, ...any) {}))
	require.NoError(t, manageFinalizers(ctx, kubeHeld, crHeld, func(string, ...any) {}))

	deletingHas := meta.FinalizerExists(storedRD(t, kubeDeleting, crDeleting), restresourcesStillExistFinalizer)
	heldHas := meta.FinalizerExists(storedRD(t, kubeHeld, crHeld), restresourcesStillExistFinalizer)

	assert.False(t, deletingHas, "the deleting object must not have acquired the finalizer")
	assert.True(t, heldHas, "the held object must still have it")
	assert.NotEqual(t, deletingHas, heldHas,
		"the two no-write arms are now indistinguishable in their effect, so this suite would pass "+
			"against an implementation that simply never adds the finalizer")
}
