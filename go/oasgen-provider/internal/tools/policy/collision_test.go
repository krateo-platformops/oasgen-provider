package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// The slug collides: [^a-z0-9]+ -> "-" collapses "." and "-" to the same separator, so these three
// distinct, individually valid API groups all produce one policy name.
func TestDistinctGroupsCollideOnOneName(t *testing.T) {
	a := PolicyName("github.krateo.io")
	for _, other := range []string{"github-krateo.io", "github.krateo-io", "GitHub.Krateo.io"} {
		assert.Equal(t, a, PolicyName(other),
			"this test documents the collision EnsureVersionPolicy must detect, not a behaviour to preserve")
	}
}

func policyScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	// The objects are unstructured, so the fake client only needs to know the GVKs exist.
	for _, kind := range []string{"MutatingAdmissionPolicy", "MutatingAdmissionPolicyBinding"} {
		u := &unstructured.Unstructured{}
		u.SetAPIVersion(policyAPIVersion)
		u.SetKind(kind)
		s.AddKnownTypeWithName(u.GroupVersionKind(), u)

		l := &unstructured.UnstructuredList{}
		l.SetAPIVersion(policyAPIVersion)
		l.SetKind(kind + "List")
		s.AddKnownTypeWithName(l.GroupVersionKind(), l)
	}
	return s
}

func newFake(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(policyScheme(t)).WithObjects(objs...).Build()
}

// A second RestDefinition in the SAME group must converge on the existing policy, not report a conflict.
// This is the behaviour the unexamined AlreadyExists was there to provide, and it must survive the fix.
func TestTwoDefinitionsInOneGroupConverge(t *testing.T) {
	kube := newFake(t)

	first, err := EnsureVersionPolicy(context.Background(), kube, "github.krateo.io")
	require.NoError(t, err)
	assert.Equal(t, OutcomeCreated, first)

	second, err := EnsureVersionPolicy(context.Background(), kube, "github.krateo.io")
	require.NoError(t, err, "a second definition in the same group must converge, not conflict")
	assert.Equal(t, OutcomeCurrent, second)
}

// The collision: the name is taken by a policy matching a DIFFERENT group. Accepting that silently is
// what left the second group's instances unstamped.
func TestCollidingGroupIsReportedNotSilentlyAccepted(t *testing.T) {
	kube := newFake(t)

	_, err := EnsureVersionPolicy(context.Background(), kube, "github.krateo.io")
	require.NoError(t, err)

	outcome, err := EnsureVersionPolicy(context.Background(), kube, "github-krateo.io")
	require.Error(t, err, "a different group colliding on the same name must not be reported as success")
	assert.Equal(t, OutcomeUnknown, outcome)

	var mismatch *ErrPolicyGroupMismatch
	require.True(t, errors.As(err, &mismatch), "the caller must be able to tell a collision from any other failure")
	assert.Equal(t, "github-krateo.io", mismatch.Want)
	assert.Equal(t, []string{"github.krateo.io"}, mismatch.Got)
	assert.Contains(t, mismatch.Error(), "rename one group", "the message must say what the operator can do")
}

// A policy created before SpecHashAnnotation existed -- every policy written by 0.27.0 -- has no
// annotation, and must read as stale rather than current. Those are exactly the policies that cannot be
// corrected except by deleting them, so reporting them as fine would hide the problem the annotation
// exists to surface.
func TestPolicyWithoutTheAnnotationReadsAsStale(t *testing.T) {
	p, b := objects("github.krateo.io")
	p.SetAnnotations(nil)
	b.SetAnnotations(nil)
	kube := newFake(t, p, b)

	outcome, err := EnsureVersionPolicy(context.Background(), kube, "github.krateo.io")
	require.NoError(t, err, "a stale policy still stamps, so it must not be an error")
	assert.Equal(t, OutcomeStale, outcome)
}

// A policy whose spec differs from what this build writes must read as stale.
func TestPolicyFromADifferentSpecReadsAsStale(t *testing.T) {
	p, b := objects("github.krateo.io")
	p.SetAnnotations(map[string]string{SpecHashAnnotation: "0000000000000000000000000000000000000000000000000000000000000000"})
	kube := newFake(t, p, b)

	outcome, err := EnsureVersionPolicy(context.Background(), kube, "github.krateo.io")
	require.NoError(t, err)
	assert.Equal(t, OutcomeStale, outcome)
}

// The hash must actually track the spec: a change to the mutation expression has to change it, or drift
// detection reports "current" forever and the annotation is decoration.
func TestSpecHashTracksTheSpec(t *testing.T) {
	p, _ := objects("github.krateo.io")
	before := specHash(p)

	spec := p.Object["spec"].(map[string]any)
	spec["failurePolicy"] = "Ignore"

	assert.NotEqual(t, before, specHash(p), "changing the spec must change the hash")
}

// Two groups that do NOT collide must each get their own policy.
func TestDistinctNonCollidingGroupsEachGetAPolicy(t *testing.T) {
	kube := newFake(t)

	for _, g := range []string{"github.krateo.io", "petstore.example.io"} {
		outcome, err := EnsureVersionPolicy(context.Background(), kube, g)
		require.NoError(t, err)
		assert.Equal(t, OutcomeCreated, outcome, "group %q", g)
	}

	list := &unstructured.UnstructuredList{}
	list.SetAPIVersion(policyAPIVersion)
	list.SetKind("MutatingAdmissionPolicyList")
	require.NoError(t, kube.List(context.Background(), list))
	assert.Len(t, list.Items, 2, "one policy per distinct group")
}

// The binding is what activates the policy. Deleting it alone must be repaired, or the policy is present
// and inert -- which looks healthy and stamps nothing.
func TestADeletedBindingIsRecreated(t *testing.T) {
	ctx := context.Background()
	kube := newFake(t)

	_, err := EnsureVersionPolicy(ctx, kube, "github.krateo.io")
	require.NoError(t, err)

	b := &unstructured.Unstructured{}
	b.SetAPIVersion(policyAPIVersion)
	b.SetKind("MutatingAdmissionPolicyBinding")
	b.SetName(PolicyName("github.krateo.io"))
	require.NoError(t, kube.Delete(ctx, b))

	_, err = EnsureVersionPolicy(ctx, kube, "github.krateo.io")
	require.NoError(t, err)

	got := &unstructured.Unstructured{}
	got.SetAPIVersion(policyAPIVersion)
	got.SetKind("MutatingAdmissionPolicyBinding")
	require.NoError(t, kube.Get(ctx, client.ObjectKey{Name: PolicyName("github.krateo.io")}, got),
		"a policy without its binding is inert, so the binding must be restored too")
}
