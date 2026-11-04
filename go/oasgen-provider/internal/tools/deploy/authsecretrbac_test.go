package deploy

import (
	"context"
	"fmt"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"strings"
	"testing"
)

var widgetConfigurationGVK = schema.GroupVersionKind{Group: "widgets.example.io", Version: "v1alpha1", Kind: "WidgetConfiguration"}

func newConfigScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := rbacv1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	s.AddKnownTypeWithName(widgetConfigurationGVK, &unstructured.Unstructured{})
	listGVK := widgetConfigurationGVK
	listGVK.Kind = listGVK.Kind + "List"
	s.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})
	return s
}

func newConfigInstance(ns, name string, basicUser, basicPass, bearerToken *map[string]interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(widgetConfigurationGVK)
	u.SetNamespace(ns)
	u.SetName(name)
	auth := map[string]interface{}{}
	if basicUser != nil || basicPass != nil {
		basic := map[string]interface{}{}
		if basicUser != nil {
			basic["usernameRef"] = *basicUser
		}
		if basicPass != nil {
			basic["passwordRef"] = *basicPass
		}
		auth["basic"] = basic
	}
	if bearerToken != nil {
		auth["bearer"] = map[string]interface{}{"tokenRef": *bearerToken}
	}
	_ = unstructured.SetNestedMap(u.Object, auth, "spec", "authentication")
	return u
}

func secretRef(ns, name, key string) map[string]interface{} {
	return map[string]interface{}{"namespace": ns, "name": name, "key": key}
}

func TestCollectAuthSecretRefs_AcrossInstancesAndNamespaces(t *testing.T) {
	s := newConfigScheme(t)
	tokenRef := secretRef("ns2", "gh-token", "token")
	userRef := secretRef("ns1", "gh-creds", "username")
	passRef := secretRef("ns1", "gh-creds", "password")
	inst1 := newConfigInstance("ns1", "cfg1", &userRef, &passRef, nil)
	inst2 := newConfigInstance("ns2", "cfg2", nil, nil, &tokenRef)

	kube := fake.NewClientBuilder().WithScheme(s).WithObjects(inst1, inst2).Build()

	got, err := CollectAuthSecretRefs(context.Background(), kube, widgetConfigurationGVK)
	if err != nil {
		t.Fatalf("CollectAuthSecretRefs: %v", err)
	}
	if len(got["ns1"]) != 1 || got["ns1"][0] != "gh-creds" {
		t.Fatalf("expected ns1: [gh-creds], got %v", got["ns1"])
	}
	if len(got["ns2"]) != 1 || got["ns2"][0] != "gh-token" {
		t.Fatalf("expected ns2: [gh-token], got %v", got["ns2"])
	}
}

func TestCollectAuthSecretRefs_NoConfigurationKind(t *testing.T) {
	s := newConfigScheme(t)
	kube := fake.NewClientBuilder().WithScheme(s).Build()

	got, err := CollectAuthSecretRefs(context.Background(), kube, schema.GroupVersionKind{})
	if err != nil {
		t.Fatalf("expected no error for an empty GVK, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil result for an empty GVK, got %v", got)
	}
}

func TestCollectAuthSecretRefs_NoInstances(t *testing.T) {
	s := newConfigScheme(t)
	kube := fake.NewClientBuilder().WithScheme(s).Build()

	got, err := CollectAuthSecretRefs(context.Background(), kube, widgetConfigurationGVK)
	if err != nil {
		t.Fatalf("CollectAuthSecretRefs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no secret refs, got %v", got)
	}
}

func rbacScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := rbacv1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return s
}

func TestSyncAuthSecretRBAC_ProvisionsPerNamespace(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(rbacScheme(t)).Build()
	sa := types.NamespacedName{Namespace: "rdc-system", Name: "rdc"}

	bySecretNamespace := map[string][]string{
		"ns1": {"gh-creds"},
		"ns2": {"gh-token"},
	}

	provisioned, err := SyncAuthSecretRBAC(context.Background(), kube, "widgets-v1alpha1-authsecrets", bySecretNamespace, nil, sa)
	if err != nil {
		t.Fatalf("SyncAuthSecretRBAC: %v", err)
	}
	if len(provisioned) != 2 || provisioned[0] != "ns1" || provisioned[1] != "ns2" {
		t.Fatalf("expected [ns1 ns2], got %v", provisioned)
	}

	for _, ns := range []string{"ns1", "ns2"} {
		var role rbacv1.Role
		if err := kube.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "widgets-v1alpha1-authsecrets"}, &role); err != nil {
			t.Fatalf("expected Role in %s: %v", ns, err)
		}
		var binding rbacv1.RoleBinding
		if err := kube.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "widgets-v1alpha1-authsecrets"}, &binding); err != nil {
			t.Fatalf("expected RoleBinding in %s: %v", ns, err)
		}
		if len(binding.Subjects) != 1 || binding.Subjects[0].Name != sa.Name || binding.Subjects[0].Namespace != sa.Namespace {
			t.Fatalf("unexpected RoleBinding subjects in %s: %+v", ns, binding.Subjects)
		}
	}
}

func TestSyncAuthSecretRBAC_RemovesStaleNamespaces(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(rbacScheme(t)).Build()
	sa := types.NamespacedName{Namespace: "rdc-system", Name: "rdc"}
	roleName := "widgets-v1alpha1-authsecrets"

	// First sync provisions ns1 and ns2.
	provisioned, err := SyncAuthSecretRBAC(context.Background(), kube, roleName,
		map[string][]string{"ns1": {"a"}, "ns2": {"b"}}, nil, sa)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// Second sync only needs ns1 now: ns2's RBAC must be torn down.
	provisioned, err = SyncAuthSecretRBAC(context.Background(), kube, roleName,
		map[string][]string{"ns1": {"a"}}, provisioned, sa)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if len(provisioned) != 1 || provisioned[0] != "ns1" {
		t.Fatalf("expected [ns1], got %v", provisioned)
	}

	var role rbacv1.Role
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "ns2", Name: roleName}, &role); !errors.IsNotFound(err) {
		t.Fatalf("expected ns2's Role to be removed, got err %v", err)
	}
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "ns1", Name: roleName}, &role); err != nil {
		t.Fatalf("expected ns1's Role to remain: %v", err)
	}
}

func TestDeleteAllAuthSecretRBAC(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(rbacScheme(t)).Build()
	sa := types.NamespacedName{Namespace: "rdc-system", Name: "rdc"}
	roleName := "widgets-v1alpha1-authsecrets"

	if _, err := SyncAuthSecretRBAC(context.Background(), kube, roleName,
		map[string][]string{"ns1": {"a"}, "ns2": {"b"}}, nil, sa); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if err := DeleteAllAuthSecretRBAC(context.Background(), kube, roleName, []string{"ns1", "ns2"}); err != nil {
		t.Fatalf("DeleteAllAuthSecretRBAC: %v", err)
	}

	var role rbacv1.Role
	for _, ns := range []string{"ns1", "ns2"} {
		if err := kube.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: roleName}, &role); !errors.IsNotFound(err) {
			t.Fatalf("expected %s's Role to be removed, got err %v", ns, err)
		}
	}
}

func TestDeleteAuthSecretRBAC_IdempotentWhenNeverProvisioned(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(rbacScheme(t)).Build()
	if err := DeleteAuthSecretRBAC(context.Background(), kube, "never-provisioned", "ns1"); err != nil {
		t.Fatalf("expected teardown of never-provisioned RBAC to succeed, got %v", err)
	}
}

func TestAuthSecretRoleName_DeterministicAndDistinct(t *testing.T) {
	a := schema.GroupVersionResource{Group: "widgets.example.io", Version: "v2", Resource: "widgets"}
	b := schema.GroupVersionResource{Group: "widgets.example.io", Version: "v2", Resource: "gadgets"}
	if AuthSecretRoleName(a) != AuthSecretRoleName(a) {
		t.Fatal("expected deterministic naming")
	}
	if AuthSecretRoleName(a) == AuthSecretRoleName(b) {
		t.Fatal("expected distinct resources to get distinct role names")
	}
}

// THE AXIS THE TEST ABOVE CANNOT SEE, which is why #440 shipped for months.
//
// That test holds Group fixed at "widgets.example.io" in BOTH arms and varies only Resource. Its inputs
// cannot distinguish the axis the function's comment made a claim about ("can never collide across
// RestDefinitions"), so it asserted distinctness along the one component that was already in the key and
// said nothing about the one that was missing.
//
// Two RestDefinitions sharing a resource plural across groups are admissible -- ownership is enforced per
// GROUP+KIND, and nothing at admission constrains the pair -- and the objects live in the SECRET's
// namespace, so they need not even share a namespace.
func TestAuthSecretRoleNameDistinguishesTheAPIGroup(t *testing.T) {
	a := schema.GroupVersionResource{Group: "a.example.io", Version: "v1", Resource: "widgets"}
	b := schema.GroupVersionResource{Group: "b.example.io", Version: "v1", Resource: "widgets"}

	if AuthSecretRoleName(a) == AuthSecretRoleName(b) {
		t.Fatalf("two RestDefinitions differing ONLY by API group share the auth-secret RBAC name %q. "+
			"Each reconcile then applies a Role whose resourceNames list only its own secrets, with a "+
			"RoleBinding subject of only its own ServiceAccount -- so the grant flaps -- and deleting "+
			"either revokes the other's credential access by name (#440)", AuthSecretRoleName(a))
	}

	// And it must match the shipped convention it claims to: all five templates in
	// helm/oasgen-provider/assets/rdc/rbac/ name objects {{ .resource }}-{{ .apiGroup }}-{{ .apiVersion }}.
	// Asserted on the ORDER too, because a name that merely contains the group would satisfy the arm above
	// while diverging from the convention the comment cites.
	if got, want := AuthSecretRoleName(a), "widgets-a.example.io-"+rbacVersion+"-authsecrets"; got != want {
		t.Fatalf("name = %q, want %q: the shipped RBAC templates order these resource-group-version", got, want)
	}
}

// The legacy name must stay EXACTLY the pre-#440 format, because its only job is to find objects already
// on disk. A "tidying" edit here silently strands every one of them.
func TestLegacyAuthSecretRoleNameIsThePreMigrationFormat(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "a.example.io", Version: "v1", Resource: "widgets"}

	if got, want := legacyAuthSecretRoleName(gvr), "widgets-"+rbacVersion+"-authsecrets"; got != want {
		t.Fatalf("legacy name = %q, want %q -- this must match what pre-#440 releases actually wrote, "+
			"not what looks tidy now", got, want)
	}
	if legacyAuthSecretRoleName(gvr) == AuthSecretRoleName(gvr) {
		t.Fatal("the legacy and current names coincide, so the migration would delete the LIVE objects")
	}
}

// The migration must remove the pre-#440 group-less objects and MUST NOT touch the current ones. The
// second half is the safety property: a migration that deleted the live Role would revoke RDC's access to
// its credential on every reconcile, which is worse than the orphan it is cleaning up.
func TestDeleteLegacyAuthSecretRBACRemovesTheOldNameAndSparesTheNew(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "a.example.io", Version: "v1", Resource: "widgets"}
	current := AuthSecretRoleName(gvr)
	legacy := legacyAuthSecretRoleName(gvr)
	sa := types.NamespacedName{Namespace: "rdc-system", Name: "rdc"}

	kube := fake.NewClientBuilder().WithScheme(rbacScheme(t)).Build()

	// Provision BOTH: the legacy pair as a pre-upgrade cluster would carry it, and the current pair as
	// this release writes it.
	for _, name := range []string{legacy, current} {
		if _, err := SyncAuthSecretRBAC(context.Background(), kube, name,
			map[string][]string{"ns1": {"creds"}}, nil, sa); err != nil {
			t.Fatalf("seeding %s: %v", name, err)
		}
	}

	// PREMISE: both exist before the migration, or the assertions below prove nothing.
	for _, name := range []string{legacy, current} {
		var r rbacv1.Role
		if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "ns1", Name: name}, &r); err != nil {
			t.Fatalf("premise: %s must exist before the migration runs: %v", name, err)
		}
	}

	if err := DeleteLegacyAuthSecretRBAC(context.Background(), kube, gvr, []string{"ns1"}); err != nil {
		t.Fatalf("migration returned an error: %v", err)
	}

	var gone rbacv1.Role
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "ns1", Name: legacy}, &gone); !errors.IsNotFound(err) {
		t.Errorf("the pre-#440 group-less Role %q survived the migration; an orphan there grants secret "+
			"read access to a ServiceAccount that no longer exists. Get returned: %v", legacy, err)
	}

	var kept rbacv1.Role
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "ns1", Name: current}, &kept); err != nil {
		t.Errorf("the migration deleted the CURRENT Role %q, which revokes RDC's access to its own "+
			"credential: %v", current, err)
	}
}

// A namespace with no legacy object must be a no-op, not an error: the sweep runs on every reconcile and
// most of them have nothing to clean.
func TestDeleteLegacyAuthSecretRBACIsANoOpWhenThereIsNothingToRemove(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "a.example.io", Version: "v1", Resource: "widgets"}
	kube := fake.NewClientBuilder().WithScheme(rbacScheme(t)).Build()

	if err := DeleteLegacyAuthSecretRBAC(context.Background(), kube, gvr, []string{"ns1", "ns2"}); err != nil {
		t.Fatalf("a clean cluster must not error: this runs every reconcile, so a false failure here would "+
			"log a warning forever. Got: %v", err)
	}
}

// ONE NAMESPACE FAILING USED TO LEAVE EVERY LATER NAMESPACE UNTOUCHED.
//
// The loop returned on the first error. Fail-fast is right when the caller can try again — here the caller
// is in Delete, so there is no second attempt, nothing retries, and nothing records what was skipped.
// status.AuthSecretRBACNamespaces, the only list of what needed cleaning, disappears with the object.
//
// THE DISCRIMINATING ASSERTION IS ns3, NOT THE ERROR. An error-only test passes against the old
// fail-fast code, because that also returned an error — it just stopped. ns3 being cleaned is the only
// observation that distinguishes "collected and continued" from "returned at the first failure".
//
// And the error must NAME the failing namespace, because the caller's Warn is the only durable channel
// once the object is gone: a message naming one namespace out of three would understate the residue in
// exactly the way the old Debug level did.
func TestDeleteAllAuthSecretRBAC_CollectsAndContinuesPastAFailure(t *testing.T) {
	roleName := "widgets-v1alpha1-authsecrets"
	sa := types.NamespacedName{Namespace: "rdc-system", Name: "rdc"}

	kube := fake.NewClientBuilder().WithScheme(rbacScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if obj.GetNamespace() == "ns2" {
					return errors.NewForbidden(
						schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "roles"},
						obj.GetName(), fmt.Errorf("no delete permission in ns2"))
				}
				return c.Delete(ctx, obj, opts...)
			},
		}).Build()

	if _, err := SyncAuthSecretRBAC(context.Background(), kube, roleName,
		map[string][]string{"ns1": {"a"}, "ns2": {"b"}, "ns3": {"c"}}, nil, sa); err != nil {
		t.Fatalf("sync: %v", err)
	}

	err := DeleteAllAuthSecretRBAC(context.Background(), kube, roleName, []string{"ns1", "ns2", "ns3"})

	if err == nil {
		t.Fatal("a namespace that could not be cleaned must be reported: the caller is in Delete and this " +
			"is the only channel left once the object is gone")
	}
	if !strings.Contains(err.Error(), "ns2") {
		t.Errorf("the error must name the namespace that failed, got: %v", err)
	}

	// THE POINT OF THE TEST. ns3 comes after the failure, so pre-fix it was never attempted.
	var role rbacv1.Role
	if gerr := kube.Get(context.Background(), client.ObjectKey{Namespace: "ns3", Name: roleName}, &role); !errors.IsNotFound(gerr) {
		t.Errorf("ns3 is AFTER the failing namespace and must still have been cleaned; a fail-fast loop "+
			"leaves it behind with nothing to retry and no record that it was skipped. Get returned: %v", gerr)
	}
	// And ns1, before the failure, must also be gone — so the test cannot pass by the loop doing nothing.
	if gerr := kube.Get(context.Background(), client.ObjectKey{Namespace: "ns1", Name: roleName}, &role); !errors.IsNotFound(gerr) {
		t.Errorf("ns1 precedes the failure and must have been cleaned: %v", gerr)
	}
	// The failing namespace's Role must REMAIN — otherwise the interceptor did not actually block anything
	// and both assertions above are vacuous.
	if gerr := kube.Get(context.Background(), client.ObjectKey{Namespace: "ns2", Name: roleName}, &role); gerr != nil {
		t.Errorf("premise: ns2's Role must survive, or the injected failure did nothing and this test "+
			"proves nothing: %v", gerr)
	}
}
