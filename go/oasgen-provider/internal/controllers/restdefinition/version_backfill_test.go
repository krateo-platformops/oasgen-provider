package restdefinition

import (
	"context"
	"testing"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/crd/generation"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var backfillGVK = schema.GroupVersionKind{Group: "github.krateo.io", Version: "v1-0-27", Kind: "PullRequest"}

func pr(name string, labels map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(backfillGVK)
	u.SetName(name)
	u.SetNamespace("demo")
	if labels != nil {
		u.SetLabels(labels)
	}
	return u
}

func backfillExternal(t *testing.T, objs ...client.Object) *external {
	t.Helper()
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(backfillGVK, &unstructured.Unstructured{})
	lgvk := backfillGVK
	lgvk.Kind += "List"
	s.AddKnownTypeWithName(lgvk, &unstructured.UnstructuredList{})

	return &external{
		kube: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build(),
		log:  logging.NewNopLogger(),
		rec:  record.NewFakeRecorder(100),
	}
}

func labelOf(t *testing.T, e *external, name string) string {
	t.Helper()
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(backfillGVK)
	require.NoError(t, e.kube.Get(context.Background(), client.ObjectKey{Namespace: "demo", Name: name}, got))
	return got.GetLabels()[generation.VersionLabel]
}

// The migration this exists for: instances written before the oas-version policy existed carry no label,
// and under an exact watch selector they would match nothing and be reconciled by nobody.
func TestBackfillStampsInstancesThatPredateThePolicy(t *testing.T) {
	e := backfillExternal(t, pr("old-one", nil), pr("old-two", map[string]string{"unrelated": "x"}))

	require.NoError(t, e.backfillVersionLabel(context.Background(), backfillGVK, "v1-0-27"))

	assert.Equal(t, "v1-0-27", labelOf(t, e, "old-one"))
	assert.Equal(t, "v1-0-27", labelOf(t, e, "old-two"))
	assert.Equal(t, "x", func() string {
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(backfillGVK)
		_ = e.kube.Get(context.Background(), client.ObjectKey{Namespace: "demo", Name: "old-two"}, got)
		return got.GetLabels()["unrelated"]
	}(), "a merge patch on one label must not drop the instance's other labels")
}

// An instance that already names a version must be left alone. Rewriting it would be MIGRATING the
// instance onto another version, which is a deliberate act -- core-provider gates the equivalent behind
// upgradePolicy -- and must never happen as a side effect of a backfill.
func TestBackfillNeverMovesAnExistingVersion(t *testing.T) {
	e := backfillExternal(t, pr("already", map[string]string{generation.VersionLabel: "v1-0-26"}))

	require.NoError(t, e.backfillVersionLabel(context.Background(), backfillGVK, "v1-0-27"))

	assert.Equal(t, "v1-0-26", labelOf(t, e, "already"),
		"the backfill fills an absence; it is not a migration")
}

// Self-limiting: once everything is labelled there is nothing to patch, so this is safe to run on every
// reconcile rather than needing a one-shot migration flag somebody has to remember.
func TestBackfillIsANoOpOnceEverythingIsLabelled(t *testing.T) {
	e := backfillExternal(t, pr("a", map[string]string{generation.VersionLabel: "v1-0-27"}))

	require.NoError(t, e.backfillVersionLabel(context.Background(), backfillGVK, "v1-0-27"))
	assert.Equal(t, "v1-0-27", labelOf(t, e, "a"))
}

// Guard rails: an empty version would stamp a label that selects nothing, and the vacuum version is never
// served, so neither may ever be written onto an instance.
func TestBackfillRefusesEmptyAndVacuumVersions(t *testing.T) {
	for _, v := range []string{"", generation.VacuumVersionName} {
		e := backfillExternal(t, pr("untouched", nil))
		require.NoError(t, e.backfillVersionLabel(context.Background(), backfillGVK, v))
		assert.Empty(t, labelOf(t, e, "untouched"), "must not stamp version %q", v)
	}
}
