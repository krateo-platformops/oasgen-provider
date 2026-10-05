package restdefinition

import (
	"context"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/crd/generation"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var pruneGVK = schema.GroupVersionKind{Group: "github.krateo.io", Version: "v1-0-29", Kind: "PullRequest"}
var pruneGVR = schema.GroupVersionResource{Group: pruneGVK.Group, Version: pruneGVK.Version, Resource: "pullrequests"}

func crdWithVersions(versions ...string) *apiextensionsv1.CustomResourceDefinition {
	c := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "pullrequests.github.krateo.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: pruneGVK.Group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind: "PullRequest", ListKind: "PullRequestList", Plural: "pullrequests", Singular: "pullrequest",
			},
			Scope: apiextensionsv1.NamespaceScoped,
		},
	}
	for _, v := range versions {
		c.Spec.Versions = append(c.Spec.Versions, apiextensionsv1.CustomResourceDefinitionVersion{
			Name:    v,
			Served:  v != generation.VacuumVersionName,
			Storage: v == generation.VacuumVersionName,
		})
		c.Status.StoredVersions = append(c.Status.StoredVersions, v)
	}
	return c
}

func pruneCR() *definitionv1alpha1.RestDefinition {
	cr := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "pullrequest", Namespace: "demo"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			ResourceGroup: pruneGVK.Group,
			Resource:      definitionv1alpha1.Resource{Kind: "PullRequest"},
		},
	}
	cr.Status.Resource.APIVersion = pruneGVK.Version
	return cr
}

func instanceAt(version, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(pruneGVK)
	u.SetName(name)
	u.SetNamespace("demo")
	u.SetLabels(map[string]string{generation.VersionLabel: version})
	return u
}

func pruneExternal(t *testing.T, objs ...client.Object) *external {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(s))
	require.NoError(t, definitionv1alpha1.SchemeBuilder.AddToScheme(s))
	s.AddKnownTypeWithName(pruneGVK, &unstructured.Unstructured{})
	lg := pruneGVK
	lg.Kind += "List"
	s.AddKnownTypeWithName(lg, &unstructured.UnstructuredList{})

	return &external{
		kube: fake.NewClientBuilder().WithScheme(s).
			WithObjects(objs...).
			WithStatusSubresource(&apiextensionsv1.CustomResourceDefinition{}).Build(),
		log: logging.NewNopLogger(),
		rec: record.NewFakeRecorder(100),
	}
}

// THE assertion this feature lives or dies on. Pruning a version that still has instances deletes a
// served endpoint out from under live objects. Everything else here is tidiness; this one is data.
func TestAVersionWithInstancesIsNeverPrunable(t *testing.T) {
	cr := pruneCR()
	e := pruneExternal(t,
		crdWithVersions(generation.VacuumVersionName, "v1-0-27", "v1-0-28", pruneGVK.Version),
		cr,
		instanceAt("v1-0-27", "old-but-alive"),
	)

	prunable, kept, err := e.prunableServedVersions(context.Background(), cr, pruneGVK, pruneGVR)
	require.NoError(t, err)

	assert.NotContains(t, prunable, "v1-0-27",
		"a version with a live instance must never be retired; its endpoint is what serves that object")
	assert.Contains(t, kept, "v1-0-27:instances=1", "and the reason must be recoverable from the log")
	assert.Contains(t, prunable, "v1-0-28", "the version with no instances is the one that goes")
}

// The current version and vacuum are structural: one is what this definition serves right now, the other
// is where every stored object actually lives.
func TestCurrentAndVacuumAreNeverPrunable(t *testing.T) {
	cr := pruneCR()
	e := pruneExternal(t, crdWithVersions(generation.VacuumVersionName, "v1-0-28", pruneGVK.Version), cr)

	prunable, _, err := e.prunableServedVersions(context.Background(), cr, pruneGVK, pruneGVR)
	require.NoError(t, err)

	assert.NotContains(t, prunable, pruneGVK.Version, "the version being served right now")
	assert.NotContains(t, prunable, generation.VacuumVersionName, "the storage version holding every object")
	assert.Equal(t, []string{"v1-0-28"}, prunable)
}

// Two RestDefinitions can generate the same Kind. Retiring a version another definition still serves
// would delete its endpoint, so being unused by THIS definition is not sufficient.
func TestAVersionAnotherDefinitionServesIsNeverPrunable(t *testing.T) {
	cr := pruneCR()
	other := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "pullrequest-legacy", Namespace: "demo"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			ResourceGroup: pruneGVK.Group,
			Resource:      definitionv1alpha1.Resource{Kind: "PullRequest"},
		},
	}
	other.Status.Resource.APIVersion = "v1-0-28"

	e := pruneExternal(t, crdWithVersions(generation.VacuumVersionName, "v1-0-28", pruneGVK.Version), cr, other)

	prunable, kept, err := e.prunableServedVersions(context.Background(), cr, pruneGVK, pruneGVR)
	require.NoError(t, err)

	assert.NotContains(t, prunable, "v1-0-28", "another definition is still serving it")
	assert.Contains(t, kept, "v1-0-28:referenced")
}

// storedVersions must be trimmed before the spec update, or the apiserver rejects the whole thing.
func TestPruneTrimsStoredVersionsAndRemovesTheVersion(t *testing.T) {
	cr := pruneCR()
	e := pruneExternal(t, crdWithVersions(generation.VacuumVersionName, "v1-0-28", pruneGVK.Version), cr)

	require.NoError(t, e.pruneStaleServedVersions(context.Background(), cr, pruneGVK, pruneGVR))

	got := &apiextensionsv1.CustomResourceDefinition{}
	require.NoError(t, e.kube.Get(context.Background(),
		client.ObjectKey{Name: "pullrequests.github.krateo.io"}, got))

	var names []string
	for _, v := range got.Spec.Versions {
		names = append(names, v.Name)
	}
	assert.NotContains(t, names, "v1-0-28", "the retired version is gone from spec.versions")
	assert.Contains(t, names, pruneGVK.Version)
	assert.Contains(t, names, generation.VacuumVersionName)
	assert.NotContains(t, got.Status.StoredVersions, "v1-0-28",
		"and out of storedVersions, or the apiserver would have refused the spec update")
}

// Nothing to do must be a genuine no-op, since Observe calls this predicate on every reconcile.
func TestNothingPrunableIsANoOp(t *testing.T) {
	cr := pruneCR()
	e := pruneExternal(t, crdWithVersions(generation.VacuumVersionName, pruneGVK.Version), cr)

	prunable, _, err := e.prunableServedVersions(context.Background(), cr, pruneGVK, pruneGVR)
	require.NoError(t, err)
	assert.Empty(t, prunable)
	require.NoError(t, e.pruneStaleServedVersions(context.Background(), cr, pruneGVK, pruneGVR))
}
