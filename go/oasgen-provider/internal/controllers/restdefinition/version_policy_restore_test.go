//go:build integration
// +build integration

package restdefinition

import (
	"context"
	"testing"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/policy"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

const restoreTestGroup = "restore-check.krateo.io"

func policyObject() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("admissionregistration.k8s.io/v1")
	u.SetKind("MutatingAdmissionPolicy")
	return u
}

// policyAPIServed asks the CLUSTER whether it serves MutatingAdmissionPolicy, rather than inferring it
// from a failed Get on the object under test.
//
// That distinction is the whole point. "The policy is not there" has two causes — the API does not exist
// (Kubernetes < 1.36, a legitimate environment gap) and EnsureVersionPolicy failed to create it (the bug
// this test exists to catch). Treating a NotFound on the object as the former would skip on exactly the
// failure it is meant to report, and CI runs without -v, so the skip would be invisible and the package
// would still print "ok".
func policyAPIServed(ctx context.Context, kube client.Client) bool {
	list := &unstructured.UnstructuredList{}
	list.SetAPIVersion("admissionregistration.k8s.io/v1")
	list.SetKind("MutatingAdmissionPolicyList")
	err := kube.List(ctx, list)
	return err == nil || !policy.IsUnsupported(err)
}

// TestVersionPolicyIsRestoredAfterDeletion is the live half of #173.
//
// The unit test proves Observe calls EnsureVersionPolicy with both generation gates closed. This proves
// the call actually RESTORES a policy against a real apiserver, which is the user-facing claim: before
// this fix, deleting the policy left it deleted until somebody edited the OAS or the resource spec.
func TestVersionPolicyIsRestoredAfterDeletion(t *testing.T) {
	name := policy.PolicyName(restoreTestGroup)

	f := features.New("version policy is restored after deletion").
		Assess("ensure creates it", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			kube, err := client.New(cfg.Client().RESTConfig(), client.Options{})
			if err != nil {
				t.Fatal(err)
			}

			if !policyAPIServed(ctx, kube) {
				t.Skip("cluster does not serve MutatingAdmissionPolicy (needs Kubernetes 1.36+)")
			}

			if err := policy.EnsureVersionPolicy(ctx, kube, restoreTestGroup); err != nil {
				t.Fatalf("ensuring the policy: %v", err)
			}

			// The API is served, so a missing object here is a real failure, never an environment gap.
			got := policyObject()
			if err := kube.Get(ctx, types.NamespacedName{Name: name}, got); err != nil {
				t.Fatalf("policy %q was not created on a cluster that serves the API: %v", name, err)
			}
			return ctx
		}).
		Assess("deleting it and ensuring again brings it back", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			kube, err := client.New(cfg.Client().RESTConfig(), client.Options{})
			if err != nil {
				t.Fatal(err)
			}

			if !policyAPIServed(ctx, kube) {
				t.Skip("cluster does not serve MutatingAdmissionPolicy (needs Kubernetes 1.36+)")
			}

			doomed := policyObject()
			doomed.SetName(name)
			if err := kube.Delete(ctx, doomed); err != nil {
				t.Fatalf("deleting the policy: %v", err)
			}

			// Confirm the delete actually took, so the restore assertion below cannot pass simply because
			// the object was never gone.
			gone := policyObject()
			if err := kube.Get(ctx, types.NamespacedName{Name: name}, gone); !errors.IsNotFound(err) {
				t.Fatalf("expected the policy to be deleted, Get returned: %v", err)
			}

			if err := policy.EnsureVersionPolicy(ctx, kube, restoreTestGroup); err != nil {
				t.Fatalf("re-ensuring the policy: %v", err)
			}

			back := policyObject()
			if err := kube.Get(ctx, types.NamespacedName{Name: name}, back); err != nil {
				t.Fatalf("policy %q was not restored after deletion: %v", name, err)
			}
			return ctx
		}).Feature()

	testenv.Test(t, f)
}
