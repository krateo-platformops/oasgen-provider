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

// TestVersionPolicyIsRestoredAfterDeletion is the live half of #173.
//
// The unit test proves Observe calls EnsureVersionPolicy with both generation gates closed. This proves
// the call actually RESTORES a policy against a real apiserver, which is the user-facing claim: before
// this fix, deleting the policy left it deleted until somebody edited the OAS or the resource spec.
//
// Skips loudly rather than passing on a cluster that does not serve MutatingAdmissionPolicy (< 1.36).
// EnsureVersionPolicy correctly returns nil there, so an assertion would report a failure that is really
// an environment gap -- and a silent pass would be worse still.
func TestVersionPolicyIsRestoredAfterDeletion(t *testing.T) {
	name := policy.PolicyName(restoreTestGroup)

	f := features.New("version policy is restored after deletion").
		Assess("ensure creates it", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			kube, err := client.New(cfg.Client().RESTConfig(), client.Options{})
			if err != nil {
				t.Fatal(err)
			}

			if err := policy.EnsureVersionPolicy(ctx, kube, restoreTestGroup); err != nil {
				t.Fatalf("ensuring the policy: %v", err)
			}

			got := policyObject()
			if err := kube.Get(ctx, types.NamespacedName{Name: name}, got); err != nil {
				if policy.IsUnsupported(err) || errors.IsNotFound(err) {
					t.Skipf("cluster does not serve MutatingAdmissionPolicy (needs Kubernetes 1.36+): %v", err)
				}
				t.Fatalf("policy %q was not created: %v", name, err)
			}
			return ctx
		}).
		Assess("deleting it and ensuring again brings it back", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			kube, err := client.New(cfg.Client().RESTConfig(), client.Options{})
			if err != nil {
				t.Fatal(err)
			}

			doomed := policyObject()
			doomed.SetName(name)
			if err := kube.Delete(ctx, doomed); err != nil {
				if policy.IsUnsupported(err) {
					t.Skipf("cluster does not serve MutatingAdmissionPolicy: %v", err)
				}
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
				t.Fatalf("policy %q was not restored: %v", name, err)
			}
			return ctx
		}).Feature()

	testenv.Test(t, f)
}
