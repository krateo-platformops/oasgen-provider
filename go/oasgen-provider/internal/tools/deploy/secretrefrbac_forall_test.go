//go:build unit || integration

package deploy

import (
	"context"
	"reflect"
	"testing"

	"errors"
	"github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var userGVK = schema.GroupVersionKind{Group: "widgets.kog.krateo.io", Version: "v1alpha1", Kind: "User"}

func userScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(userGVK, &unstructured.Unstructured{})
	listGVK := userGVK
	listGVK.Kind += "List"
	s.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})
	return s
}

// user builds a KeycloakUser-shaped instance: credentials is a LIST, addressed by a predicate, which is
// the shape the consumer who filed #510 actually uses.
func user(ns, name string, creds []any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"credentials": creds},
	}}
	u.SetGroupVersionKind(userGVK)
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func otpCred(secretName string) any {
	c := map[string]any{"type": "otp"}
	if secretName != "" {
		c["secretDataSecretRef"] = map[string]any{"name": secretName, "key": "value"}
	}
	return c
}

func otpVerbs() []v1alpha1.VerbsDescription {
	return secretRefVerbs("spec.credentials[?type=otp].secretDataSecretRef.name")
}

// The point of the lister: a Role must cover EVERY instance, so names from different CRs in the same
// namespace must union rather than one winning.
func TestNamesFromDifferentInstancesUnionWithinANamespace(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(userScheme(t)).WithObjects(
		user("tenant-a", "alice", []any{otpCred("alice-otp")}),
		user("tenant-a", "bob", []any{otpCred("bob-otp")}),
	).Build()

	got, err := CollectSecretRefNamesForAll(context.Background(), kube, userGVK, otpVerbs(), logging.NewNopLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string][]string{"tenant-a": {"alice-otp", "bob-otp"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v -- a per-instance collector would have returned only one of these", got, want)
	}
}

// Two tenants must not end up in each other's Role.
func TestInstancesInDifferentNamespacesStaySeparate(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(userScheme(t)).WithObjects(
		user("tenant-a", "alice", []any{otpCred("alice-otp")}),
		user("tenant-b", "carol", []any{otpCred("carol-otp")}),
	).Build()

	got, err := CollectSecretRefNamesForAll(context.Background(), kube, userGVK, otpVerbs(), logging.NewNopLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string][]string{"tenant-a": {"alice-otp"}, "tenant-b": {"carol-otp"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// LENIENT: a password-only user alongside an OTP user must not cost the OTP user its name. This is the
// consumer's case 1 sitting next to their case 2, which is the realistic multi-tenant state.
func TestAPasswordOnlyInstanceDoesNotSuppressAnotherInstancesName(t *testing.T) {
	passwordOnly := user("tenant-a", "dave", []any{map[string]any{"type": "password", "valueSecretRef": map[string]any{"name": "dave-pw", "key": "v"}}})
	kube := fake.NewClientBuilder().WithScheme(userScheme(t)).WithObjects(
		passwordOnly,
		user("tenant-a", "alice", []any{otpCred("alice-otp")}),
	).Build()

	got, err := CollectSecretRefNamesForAll(context.Background(), kube, userGVK, otpVerbs(), logging.NewNopLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string][]string{"tenant-a": {"alice-otp"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v -- dave has no OTP credential, which must not affect alice", got, want)
	}
}

// An entry present with no ref (case 3a) contributes nothing and is not an error.
func TestAnInstanceWithAnOtpEntryAndNoRefContributesNothing(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(userScheme(t)).WithObjects(
		user("tenant-a", "erin", []any{otpCred("")}),
	).Build()

	got, err := CollectSecretRefNamesForAll(context.Background(), kube, userGVK, otpVerbs(), logging.NewNopLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

// A CRD that is not established yet is ABSENT, not an error -- the first reconcile after generation.
//
// The error has to be INJECTED. An earlier version of this test built a fake client with an empty scheme
// and asserted no error, which passed -- but a probe showed that client returns err=nil with zero items
// for an unregistered type, so the test never produced the error it claims to tolerate. A mutation
// disabling the tolerance survived, which is how the vacuity was caught: the test could not fail.
func TestAnUnestablishedKindYieldsNoNamesRatherThanAnError(t *testing.T) {
	for _, msg := range []string{
		// What a real discovery client says before the CRD is established, both spellings.
		`no matches for kind "User" in version "widgets.kog.krateo.io/v1alpha1"`,
		"the server could not find the requested resource",
	} {
		t.Run(msg[:18], func(t *testing.T) {
			kube := fake.NewClientBuilder().WithScheme(userScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
				List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
					return errors.New(msg)
				},
			}).Build()

			got, err := CollectSecretRefNamesForAll(context.Background(), kube, userGVK, otpVerbs(), logging.NewNopLogger())
			if err != nil {
				t.Fatalf("an unestablished CRD must not fail the reconcile, got: %v", err)
			}
			if got != nil {
				t.Fatalf("got %v, want nil", got)
			}
		})
	}
}

// The POSITIVE control for the tolerance above: any OTHER list failure must stay fatal. Without this, the
// tolerance could be widened to swallow every error and the test above would still pass -- which is the
// shape that turns "I could not tell" into "no".
func TestAListFailureThatIsNotAMissingCrdStaysFatal(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(userScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
			return errors.New("etcdserver: request timed out")
		},
	}).Build()

	if _, err := CollectSecretRefNamesForAll(context.Background(), kube, userGVK, otpVerbs(), logging.NewNopLogger()); err == nil {
		t.Fatal("a transient list failure must NOT be reported as an absence of secretRef names: that would " +
			"shrink the Role to nothing and revoke every CR's credential until the next successful list")
	}
}

// A definition defect affects every CR of the kind, so it stays FATAL even in the lenient lister.
func TestAnUnparseablePathStaysFatalAcrossInstances(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(userScheme(t)).WithObjects(
		user("tenant-a", "alice", []any{otpCred("alice-otp")}),
	).Build()

	_, err := CollectSecretRefNamesForAll(context.Background(), kube, userGVK,
		secretRefVerbs("spec.credentials[?type=otp"), logging.NewNopLogger())
	if err == nil {
		t.Fatal("an unparseable path affects every CR of the kind and must fail the reconcile")
	}
}

// THE DISTINCTNESS ASSERTION. SyncAuthSecretRBAC deletes the Role in every namespace that is in the
// caller's recorded set and not in its collected set, so two callers sharing one Role name delete each
// other's namespaces on every reconcile. Nothing else in either package asserts that these two names
// differ: a mutation returning AuthSecretRoleName(gvr) from SecretRefRoleName would otherwise pass the
// whole suite, because no single-caller test can observe the collision.
func TestTheSecretRefRoleNameIsNotTheAuthSecretRoleName(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "widgets.kog.krateo.io", Version: "v1alpha1", Resource: "users"}

	secretRef := SecretRefRoleName(gvr)
	authSecret := AuthSecretRoleName(gvr)

	if secretRef == authSecret {
		t.Fatalf("both callers would provision %q: each deletes the namespaces the other owns on every "+
			"reconcile, and the symptom is a credential that intermittently 403s", secretRef)
	}
	if secretRef == "" {
		t.Fatal("empty role name")
	}
}

// Two kinds sharing a resource plural across API groups must not collide -- the #440 failure, which cost
// a consumer its credential access once already.
func TestTwoGroupsSharingAPluralGetDifferentSecretRefRoleNames(t *testing.T) {
	a := SecretRefRoleName(schema.GroupVersionResource{Group: "widgets.kog.krateo.io", Version: "v1alpha1", Resource: "users"})
	b := SecretRefRoleName(schema.GroupVersionResource{Group: "gadgets.kog.krateo.io", Version: "v1alpha1", Resource: "users"})
	if a == b {
		t.Fatalf("both groups' users would share %q and revoke each other's access (#440)", a)
	}
}
