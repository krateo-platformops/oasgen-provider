//go:build unit

package restdefinition

import (
	"context"
	"testing"

	"github.com/krateo-platformops/oasgen-provider/apis"
	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// THE WATCH EXISTS SO A DELETED RESOURCE'S SECRET STOPS BEING READABLE WITHOUT WAITING FOR THE POLL.
//
// These tests exercise the MAP FUNCTION, which is the half that decides which RestDefinition an instance
// event belongs to. Registration itself needs a live controller and is not reachable from here -- stated
// rather than implied, because "the watch is tested" would otherwise read as covering both halves.
//
// The cases that matter are the ones where it must NOT enqueue. A mapper that returned every
// RestDefinition for every event would satisfy "the right one is enqueued" and reconcile the whole fleet
// on any instance change.
func rdFor(name, group, kind string) *definitionv1alpha1.RestDefinition {
	cr := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "demo-system"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			ResourceGroup: group,
			Resource:      definitionv1alpha1.Resource{Kind: kind, Identifiers: []string{"id"}},
		},
	}
	return cr
}

func instanceOf(group, version, kind, ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: group, Version: version, Kind: kind})
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func mapperKube(t *testing.T, rds ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, apis.AddToScheme(s))
	return fake.NewClientBuilder().WithScheme(s).WithObjects(rds...).Build()
}

func TestAnInstanceEnqueuesTheRestDefinitionThatGeneratedItsKind(t *testing.T) {
	rd := rdFor("pullrequest", "github.krateo.io", "PullRequest")
	kube := mapperKube(t, rd)

	reqs := enqueueRestDefinitionForGeneratedKind(kube)(context.Background(),
		instanceOf("github.krateo.io", resourceVersion, "PullRequest", "team-a", "pr-1"))

	require.Len(t, reqs, 1, "exactly the owning RestDefinition")
	require.Equal(t, "pullrequest", reqs[0].Name)
	require.Equal(t, "demo-system", reqs[0].Namespace,
		"the RestDefinition's namespace, not the instance's -- the instance is in team-a")
}

// THE CONTROL THAT MAKES THE ABOVE MEAN SOMETHING. A mapper returning everything would pass that test.
func TestAnInstanceOfAnUnrelatedKindEnqueuesNothing(t *testing.T) {
	rd := rdFor("pullrequest", "github.krateo.io", "PullRequest")
	kube := mapperKube(t, rd)

	cases := []struct {
		name        string
		group, kind string
	}{
		{"different kind, same group", "github.krateo.io", "Issue"},
		{"same kind, different group", "gitlab.krateo.io", "PullRequest"},
		{"the Configuration Kind of the same RD", "github.krateo.io", "PullRequestConfiguration"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reqs := enqueueRestDefinitionForGeneratedKind(kube)(context.Background(),
				instanceOf(c.group, resourceVersion, c.kind, "team-a", "x"))
			require.Empty(t, reqs,
				"an unrelated event must enqueue nothing; reconciling the fleet on any instance change "+
					"would make the watch worse than the poll it replaces")
		})
	}
}

// With several RestDefinitions, only the owning one is enqueued -- the selectivity a shared registry and a
// cluster-wide List make easy to get wrong.
func TestOnlyTheOwningRestDefinitionIsEnqueued(t *testing.T) {
	kube := mapperKube(t,
		rdFor("pullrequest", "github.krateo.io", "PullRequest"),
		rdFor("issue", "github.krateo.io", "Issue"),
		rdFor("repo", "github.krateo.io", "Repository"),
	)

	reqs := enqueueRestDefinitionForGeneratedKind(kube)(context.Background(),
		instanceOf("github.krateo.io", resourceVersion, "Issue", "team-a", "i-1"))

	require.Len(t, reqs, 1)
	require.Equal(t, "issue", reqs[0].Name, "not pullrequest, not repo")
}

// A LOWERCASE spec.resource.kind STILL MATCHES. The CRD serves the capitalised Kind, so the event carries
// it; a raw comparison here would match nothing and the watch would silently never fire for such a
// definition -- the same defect a reviewer found in the collector, and the one rest-dynamic-controller's
// own lookup still carries (#530).
func TestALowercaseKindStillMatchesTheServedKind(t *testing.T) {
	rd := rdFor("pullrequest", "github.krateo.io", "pullRequest") // as the author wrote it
	kube := mapperKube(t, rd)

	reqs := enqueueRestDefinitionForGeneratedKind(kube)(context.Background(),
		instanceOf("github.krateo.io", resourceVersion, "PullRequest", "team-a", "pr-1")) // as served

	require.Len(t, reqs, 1,
		"the author wrote `kind: pullRequest` and the API server serves `PullRequest`. If this is empty "+
			"the watch never fires for that definition, silently, and the poll is the only trigger left")
}

// An event for a kind no RestDefinition generates must be harmless, not a panic or a fleet-wide enqueue.
func TestAnEventWithNoMatchingRestDefinitionIsHarmless(t *testing.T) {
	kube := mapperKube(t)
	reqs := enqueueRestDefinitionForGeneratedKind(kube)(context.Background(),
		instanceOf("unknown.krateo.io", "v1", "Mystery", "team-a", "m-1"))
	require.Empty(t, reqs)
}
