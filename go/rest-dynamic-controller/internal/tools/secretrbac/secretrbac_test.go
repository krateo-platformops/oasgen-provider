package secretrbac

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
)

var widgetGVR = schema.GroupVersionResource{Group: "widgets.example.io", Version: "v1alpha1", Resource: "widgets"}

func newFakeClient(t *testing.T) *fake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{
		roleGVR:    "RoleList",
		bindingGVR: "RoleBindingList",
	}
	return fake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)
}

// seedPair writes the Role and RoleBinding a release before #540 self-provisioned, which is the only
// way this package's remaining function can be exercised: nothing creates them any more.
func seedPair(t *testing.T, dyn *fake.FakeDynamicClient, ns, name string) {
	t.Helper()
	for gvr, kind := range map[schema.GroupVersionResource]string{roleGVR: "Role", bindingGVR: "RoleBinding"} {
		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion("rbac.authorization.k8s.io/v1")
		obj.SetKind(kind)
		obj.SetNamespace(ns)
		obj.SetName(name)
		if _, err := dyn.Resource(gvr).Namespace(ns).Create(context.Background(), obj, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding %s: %v", kind, err)
		}
	}
}

func count(t *testing.T, dyn *fake.FakeDynamicClient, gvr schema.GroupVersionResource, ns string) int {
	t.Helper()
	list, err := dyn.Resource(gvr).Namespace(ns).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("List %s: %v", gvr.Resource, err)
	}
	return len(list.Items)
}

// TestRoleNameDeterministicAndDistinct still matters after the provisioning is gone, and for a new
// reason: the teardown has to reproduce the name an OLDER release chose, so a change here silently
// orphans every Role already on a cluster instead of merely renaming new ones.
func TestRoleNameDeterministicAndDistinct(t *testing.T) {
	a := types.NamespacedName{Namespace: "ns1", Name: "my-widget"}
	b := types.NamespacedName{Namespace: "ns1", Name: "other-widget"}

	if RoleName(widgetGVR, a) != RoleName(widgetGVR, a) {
		t.Fatal("expected RoleName to be deterministic for the same input")
	}
	if RoleName(widgetGVR, a) == RoleName(widgetGVR, b) {
		t.Fatal("expected distinct CR instances to get distinct role names")
	}
	if !strings.HasSuffix(RoleName(widgetGVR, a), "-secrets") {
		t.Fatalf("expected role name to end in -secrets, got %q", RoleName(widgetGVR, a))
	}
	if !strings.HasPrefix(RoleName(widgetGVR, a), "widgets-v1alpha1-") {
		t.Fatalf("expected role name to start with <plural>-<version>-, got %q", RoleName(widgetGVR, a))
	}
}

// TestRoleNameIsKeyedOnTheCRNotTheNamespace pins the fact #540 was briefly wrong about: two resources
// in ONE namespace got two DIFFERENT Roles, so a per-CR Role holding a subset of that namespace's
// referenced Secrets was correct rather than one resource overwriting another's grant. Asserted on the
// namespace axis too, since a name that ignored either half would still pass the test above.
func TestRoleNameIsKeyedOnTheCRNotTheNamespace(t *testing.T) {
	sameNS := []types.NamespacedName{
		{Namespace: "ns1", Name: "widget-a"},
		{Namespace: "ns1", Name: "widget-b"},
	}
	if RoleName(widgetGVR, sameNS[0]) == RoleName(widgetGVR, sameNS[1]) {
		t.Fatal("two CRs in one namespace must not share a Role name")
	}

	sameName := []types.NamespacedName{
		{Namespace: "ns1", Name: "widget-a"},
		{Namespace: "ns2", Name: "widget-a"},
	}
	if RoleName(widgetGVR, sameName[0]) == RoleName(widgetGVR, sameName[1]) {
		t.Fatal("the same CR name in two namespaces must not share a Role name")
	}
}

func TestDeleteSecretRoleRemovesBoth(t *testing.T) {
	dyn := newFakeClient(t)
	roleName := RoleName(widgetGVR, types.NamespacedName{Namespace: "ns1", Name: "my-widget"})
	seedPair(t, dyn, "ns1", roleName)

	// CONTROL: both are present before the call, so a zero afterwards is the teardown's doing.
	if n := count(t, dyn, roleGVR, "ns1"); n != 1 {
		t.Fatalf("fixture: expected 1 Role, found %d", n)
	}
	if n := count(t, dyn, bindingGVR, "ns1"); n != 1 {
		t.Fatalf("fixture: expected 1 RoleBinding, found %d", n)
	}

	if err := DeleteSecretRole(context.Background(), dyn, "ns1", roleName); err != nil {
		t.Fatalf("DeleteSecretRole: %v", err)
	}

	if n := count(t, dyn, roleGVR, "ns1"); n != 0 {
		t.Errorf("expected the Role to be gone, %d remain", n)
	}
	if n := count(t, dyn, bindingGVR, "ns1"); n != 0 {
		t.Errorf("expected the RoleBinding to be gone, %d remain", n)
	}
}

// TestDeleteSecretRoleLeavesAnotherInstancesPairAlone: the teardown names ONE pair, and a per-CR scheme
// means a namespace can hold several. A teardown that deleted by namespace, or by a prefix, would pass
// every test above and revoke a sibling resource's access.
func TestDeleteSecretRoleLeavesAnotherInstancesPairAlone(t *testing.T) {
	dyn := newFakeClient(t)
	mine := RoleName(widgetGVR, types.NamespacedName{Namespace: "ns1", Name: "widget-a"})
	theirs := RoleName(widgetGVR, types.NamespacedName{Namespace: "ns1", Name: "widget-b"})
	seedPair(t, dyn, "ns1", mine)
	seedPair(t, dyn, "ns1", theirs)

	if err := DeleteSecretRole(context.Background(), dyn, "ns1", mine); err != nil {
		t.Fatalf("DeleteSecretRole: %v", err)
	}

	if n := count(t, dyn, roleGVR, "ns1"); n != 1 {
		t.Fatalf("expected exactly the sibling's Role to survive, %d remain", n)
	}
	list, err := dyn.Resource(roleGVR).Namespace("ns1").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := list.Items[0].GetName(); got != theirs {
		t.Fatalf("the surviving Role should be %q, got %q", theirs, got)
	}
}

func TestDeleteSecretRoleIdempotentWhenNeverProvisioned(t *testing.T) {
	dyn := newFakeClient(t)
	if err := DeleteSecretRole(context.Background(), dyn, "ns1", "never-provisioned"); err != nil {
		t.Fatalf("expected a no-op teardown to succeed, got %v", err)
	}
}
