//go:build unit

package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const probeRole = "widgets-example.io-v1alpha1-authsecrets"

func role(ns string) *rbacv1.Role {
	return &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: probeRole, Namespace: ns}}
}

func binding(ns string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: probeRole, Namespace: ns}}
}

// Both objects present in every namespace: nothing is missing.
func TestMissingAuthSecretRBAC_ReportsNothingWhenBothObjectsExist(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(rbacScheme(t)).
		WithObjects(role("ns1"), binding("ns1"), role("ns2"), binding("ns2")).Build()

	missing, err := MissingAuthSecretRBAC(context.Background(), kube, probeRole, []string{"ns1", "ns2"})
	require.NoError(t, err)
	require.Empty(t, missing)
}

// The Role itself gone -- the case the pre-#440 legacy sweep of a colliding RestDefinition produces.
func TestMissingAuthSecretRBAC_ReportsANamespaceWhoseRoleWasDeleted(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(rbacScheme(t)).
		WithObjects(role("ns1"), binding("ns1"), binding("ns2")).Build()

	missing, err := MissingAuthSecretRBAC(context.Background(), kube, probeRole, []string{"ns1", "ns2"})
	require.NoError(t, err)
	require.Equal(t, []string{"ns2"}, missing)
}

// THE ROLE ALONE IS NOT ENOUGH. A Role with no RoleBinding grants nothing, so checking only the Role
// would report "present" for a namespace whose access is in fact broken.
func TestMissingAuthSecretRBAC_ReportsANamespaceWhoseRoleBindingWasDeleted(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(rbacScheme(t)).
		WithObjects(role("ns1"), binding("ns1"), role("ns2")).Build()

	missing, err := MissingAuthSecretRBAC(context.Background(), kube, probeRole, []string{"ns1", "ns2"})
	require.NoError(t, err)
	require.Equal(t, []string{"ns2"}, missing,
		"a Role without its RoleBinding grants nothing and must count as missing")
}

// A TRANSIENT API FAILURE IS NOT AN ABSENCE. Reporting "missing" here would flap a healthy resource into
// Creating on every blip -- the same reason Observe's other checks treat a failed evaluation as not-drift.
func TestMissingAuthSecretRBAC_ReturnsTheErrorRatherThanCallingItMissing(t *testing.T) {
	boom := errors.New("etcdserver: request timed out")
	kube := fake.NewClientBuilder().WithScheme(rbacScheme(t)).
		WithObjects(role("ns1"), binding("ns1")).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if key.Namespace == "ns2" {
					return boom
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).Build()

	missing, err := MissingAuthSecretRBAC(context.Background(), kube, probeRole, []string{"ns1", "ns2"})
	require.Error(t, err)
	require.ErrorIs(t, err, boom)
	require.Nil(t, missing, "on an error the caller must get no namespace list to act on")
	require.False(t, apierrors.IsNotFound(err), "the control: this path must not be reached via NotFound")
}

// Deterministic output, because it reaches a drift message a user reads.
func TestMissingAuthSecretRBAC_SortsItsResult(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(rbacScheme(t)).Build()
	missing, err := MissingAuthSecretRBAC(context.Background(), kube, probeRole, []string{"zeta", "alpha", "mu"})
	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "mu", "zeta"}, missing)
}

// The empty namespace list is the common case for a RestDefinition with no Configuration instances, and
// must not be mistaken for "everything is missing".
func TestMissingAuthSecretRBAC_NoNamespacesIsNotAnAbsence(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(rbacScheme(t)).Build()
	missing, err := MissingAuthSecretRBAC(context.Background(), kube, probeRole, nil)
	require.NoError(t, err)
	require.Empty(t, missing)
}

var _ = schema.GroupVersionResource{}
