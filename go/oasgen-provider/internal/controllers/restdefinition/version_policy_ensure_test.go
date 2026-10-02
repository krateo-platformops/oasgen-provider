package restdefinition

import (
	"context"
	"testing"

	"github.com/krateo-platformops/oasgen-provider/apis"
	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// policyKinds records every Kind the reconciler tried to Create, so a test can assert on what was
// attempted without needing a cluster that actually serves MutatingAdmissionPolicy. A real-cluster
// assertion would be silently vacuous on a kind node below 1.36, where EnsureVersionPolicy correctly
// returns nil because the API is absent.
func interceptCreates(seen *[]string) interceptor.Funcs {
	return interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			*seen = append(*seen, obj.GetObjectKind().GroupVersionKind().Kind)
			// Report success without writing: the fake client's tracker has no schema for these, and the
			// test cares only that the attempt was made.
			return nil
		},
	}
}

func newRestDefinitionForPolicyTest() *definitionv1alpha1.RestDefinition {
	cr := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "pullrequest", Namespace: "demo-system"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			OASPath:       "configmap://demo-system/pr/openapi.yaml",
			ResourceGroup: "github.krateo.io",
			Resource: definitionv1alpha1.Resource{
				Kind:        "PullRequest",
				Identifiers: []string{"number"},
			},
		},
	}
	// Close BOTH gates that gate generateAndApplyCRDs, which is where the policy used to be ensured from:
	// the OAS hash and the resource hash both match what is already in status. On a stable install this is
	// the steady state, and it is exactly the state in which the policy was previously never ensured.
	cr.Status.OASHash = "unchanged"
	cr.Status.ResourceHash = resourceHash(cr)
	return cr
}

func externalForPolicyTest(t *testing.T, cr *definitionv1alpha1.RestDefinition, seen *[]string) *external {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, apis.AddToScheme(s))

	kube := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(cr.DeepCopy()).
		WithInterceptorFuncs(interceptCreates(seen)).
		Build()

	return &external{
		kube: kube,
		log:  logging.NewNopLogger(),
		rec:  record.NewFakeRecorder(100),
	}
}

// TestObserveEnsuresTheVersionPolicyWithBothGatesClosed is the regression test for #173.
//
// EnsureVersionPolicy used to be reachable only from generateAndApplyCRDs, whose callers run it when the
// CRD is absent (Create) or when the OAS/resource hash changed (Update). On a stable install neither
// fires, so a policy deleted out of band was never restored and nothing reported its absence. Observe now
// ensures it on every reconcile, ahead of either gate.
//
// Deleting the call from Observe must make this fail.
func TestObserveEnsuresTheVersionPolicyWithBothGatesClosed(t *testing.T) {
	cr := newRestDefinitionForPolicyTest()
	var seen []string
	e := externalForPolicyTest(t, cr, &seen)

	// Observe is expected to fail further down -- there is no OAS document behind the fake client. The
	// ensure happens before that, which is the point: it does not depend on the reconcile succeeding.
	_, _ = e.Observe(context.Background(), cr)

	assert.Contains(t, seen, "MutatingAdmissionPolicy",
		"Observe must ensure the oas-version policy even when both generation gates are closed")
	assert.Contains(t, seen, "MutatingAdmissionPolicyBinding",
		"the binding is what activates the policy; ensuring one without the other stamps nothing")
}

// A RestDefinition being deleted must not recreate the policy Delete may be tearing down.
func TestObserveDoesNotEnsureTheVersionPolicyWhileDeleting(t *testing.T) {
	cr := newRestDefinitionForPolicyTest()
	now := metav1.Now()
	cr.SetDeletionTimestamp(&now)
	cr.SetFinalizers([]string{"composition.krateo.io/finalizer"})

	var seen []string
	e := externalForPolicyTest(t, cr, &seen)

	_, _ = e.Observe(context.Background(), cr)

	assert.NotContains(t, seen, "MutatingAdmissionPolicy",
		"a RestDefinition on its way out must not recreate the version policy")
}
