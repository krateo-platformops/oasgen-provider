package collision

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	discoveryfake "k8s.io/client-go/discovery/fake"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(s))
	return s
}

// clusterResources is a cluster's discovery, cut down to what the guard reads. `deployments` sits in
// `apps`, NOT in core -- the whole point of #200 is that a guard scoped to the core group would miss it.
func clusterResources() []*metav1.APIResourceList {
	return []*metav1.APIResourceList{
		{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{Name: "services", SingularName: "service"},
				{Name: "secrets", SingularName: "secret"},
				{Name: "serviceaccounts", SingularName: "serviceaccount"},
			},
		},
		{
			GroupVersion: "apps/v1",
			APIResources: []metav1.APIResource{
				{Name: "deployments", SingularName: "deployment"},
				{Name: "deployments/status", SingularName: ""},
				{Name: "statefulsets", SingularName: "statefulset"},
			},
		},
		{
			GroupVersion: "batch/v1",
			APIResources: []metav1.APIResource{
				{Name: "jobs", SingularName: "job"},
			},
		},
		// An aggregated, non-CRD API. It is as built-in as the rest for this purpose.
		{
			GroupVersion: "metrics.k8s.io/v1beta1",
			APIResources: []metav1.APIResource{
				{Name: "nodes", SingularName: "node"},
			},
		},
	}
}

func discoverWith(t *testing.T, lists []*metav1.APIResourceList, crds ...client.Object) *BuiltInNames {
	t.Helper()
	disc := &discoveryfake.FakeDiscovery{Fake: &clienttesting.Fake{Resources: lists}}
	kube := ctrlfake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(crds...).Build()
	b, err := Discover(context.Background(), disc, kube)
	require.NoError(t, err)
	return b
}

func generatedCRD(name, group, kind, plural, singular string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: kind, Plural: plural, Singular: singular},
		},
	}
}

func existingCRD(name, group, plural string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: plural},
		},
	}
}

// The reported case: GitHub's document contains "deployment", and `deployments` is in apps/v1.
func TestCheck_RefusesTheReportedCollision(t *testing.T) {
	b := discoverWith(t, clusterResources())

	err := b.Check(generatedCRD("deployments.github.krateo.io", "github.krateo.io", "Deployment", "deployments", "deployment"))
	require.Error(t, err)

	var ce *Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "deployments", ce.Name)
	assert.Equal(t, []string{"apps"}, ce.Groups, "the collision must be attributed to apps, not core")
	assert.Contains(t, err.Error(), "GithubDeployment", "the message must suggest a concrete non-colliding kind")
}

// The guard written too narrowly is the predicted failure, so it gets its own assertion: a check that
// only knew the core group would pass this and ship the bug.
func TestCheck_CoversNonCoreGroups(t *testing.T) {
	b := discoverWith(t, clusterResources())

	for _, tc := range []struct{ plural, group string }{
		{"deployments", "apps"},
		{"jobs", "batch"},
		{"statefulsets", "apps"},
		{"nodes", "metrics.k8s.io"},
	} {
		t.Run(tc.plural, func(t *testing.T) {
			err := b.Check(generatedCRD(tc.plural+".vendor.krateo.io", "vendor.krateo.io", "X", tc.plural, "x"))
			require.Error(t, err, "%s is served by %s and must be refused", tc.plural, tc.group)
		})
	}
}

// Singulars are checked too. The authn sibling case shadows a singular and returns a WRONG ANSWER
// rather than an error, which is the worse failure of the two.
func TestCheck_CoversSingulars(t *testing.T) {
	b := discoverWith(t, clusterResources())

	// A kind whose plural is free but whose singular is `service`.
	err := b.Check(generatedCRD("servicelist.vendor.krateo.io", "vendor.krateo.io", "Servicelist", "servicelist", "service"))
	require.Error(t, err)

	var ce *Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "service", ce.Name)
}

func TestCheck_AllowsANameNothingBuiltInServes(t *testing.T) {
	b := discoverWith(t, clusterResources())
	assert.NoError(t, b.Check(generatedCRD("repositories.github.krateo.io", "github.krateo.io", "Repository", "repositories", "repository")))
}

// Without subtracting CRD-backed resources, every generated resource would collide with ITSELF the
// moment its own CRD appeared in discovery -- so a re-reconcile would start refusing what it just made.
func TestDiscover_ExcludesResourcesServedByCRDs(t *testing.T) {
	lists := append(clusterResources(), &metav1.APIResourceList{
		GroupVersion: "github.krateo.io/v2022-11-28",
		APIResources: []metav1.APIResource{{Name: "repositories", SingularName: "repository"}},
	})
	b := discoverWith(t, lists, existingCRD("repositories.github.krateo.io", "github.krateo.io", "repositories"))

	assert.NoError(t, b.Check(generatedCRD("repositories.github.krateo.io", "github.krateo.io", "Repository", "repositories", "repository")),
		"a resource this provider already generated must not collide with itself")
}

// A collision between two CRDs is not this guard's business: generation cannot refuse its way out of a
// third-party CRD that is already installed.
func TestDiscover_IgnoresCRDOnCRDCollisions(t *testing.T) {
	lists := append(clusterResources(), &metav1.APIResourceList{
		GroupVersion: "serviceusage.cnrm.cloud.google.com/v1beta1",
		APIResources: []metav1.APIResource{{Name: "services", SingularName: "service"}},
	})
	b := discoverWith(t, lists, existingCRD("services.serviceusage.cnrm.cloud.google.com", "serviceusage.cnrm.cloud.google.com", "services"))

	// `services` still collides -- but with CORE, which is the built-in, not with the Config Connector CRD.
	err := b.Check(generatedCRD("services.vendor.krateo.io", "vendor.krateo.io", "Service", "services", "service"))
	require.Error(t, err)
	var ce *Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, []string{coreGroupDisplay}, ce.Groups, "the third-party CRD must not be reported as a built-in")
}

func TestDiscover_SkipsSubresources(t *testing.T) {
	b := discoverWith(t, clusterResources())
	_, present := b.byName["deployments/status"]
	assert.False(t, present, "a subresource is not addressable as `kubectl get <name>` and cannot be collided with")
}

// An empty or nil set checks nothing: when discovery could not be reached the guard is ABSENT, not
// satisfied, and must not silently report a clean bill of health by refusing nothing on purpose.
func TestCheck_EmptySetChecksNothing(t *testing.T) {
	var nilSet *BuiltInNames
	assert.NoError(t, nilSet.Check(generatedCRD("deployments.github.krateo.io", "github.krateo.io", "Deployment", "deployments", "deployment")))
	assert.Equal(t, 0, nilSet.Len())

	empty := &BuiltInNames{byName: map[string][]string{}}
	assert.NoError(t, empty.Check(generatedCRD("deployments.github.krateo.io", "github.krateo.io", "Deployment", "deployments", "deployment")))
}

func TestSuggestPrefix(t *testing.T) {
	assert.Equal(t, "GithubDeployment", suggestPrefix("Deployment", "deployments.github.krateo.io"))
	// No group to borrow a prefix from: suggest the kind unchanged rather than invent one.
	assert.Equal(t, "Deployment", suggestPrefix("Deployment", "deployments"))
}

// The guard runs in the reconcile hot path, so a missing client must be an error, never a panic.
func TestDiscover_NilClientsAreReportedNotDereferenced(t *testing.T) {
	kube := ctrlfake.NewClientBuilder().WithScheme(scheme(t)).Build()
	disc := &discoveryfake.FakeDiscovery{Fake: &clienttesting.Fake{Resources: clusterResources()}}

	_, err := Discover(context.Background(), nil, kube)
	require.Error(t, err)

	_, err = Discover(context.Background(), disc, nil)
	require.Error(t, err)
}
