//go:build unit

package restdefinition

import (
	"context"
	"strings"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/deploy"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/plurals"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// THE POINT OF TESTING THIS AT THE OBSERVE LEVEL (#473), in the words this package already uses for #224:
// a test on the comparison alone would reproduce the bug it is meant to catch. MissingAuthSecretRBAC has
// its own unit tests; they say nothing about whether Observe ever calls it, and "Observe never consulted
// it" is exactly the defect -- the runtime calls Update only when Observe reports drift, so an unreached
// check is indistinguishable from no check.
func restDefinitionForAuthRBACTest(t *testing.T) *definitionv1alpha1.RestDefinition {
	t.Helper()
	cr := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "pullrequest", Namespace: "demo-system"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			OASPath:       "configmap://demo-system/pr/openapi.yaml",
			ResourceGroup: "github.krateo.io",
			Resource: definitionv1alpha1.Resource{
				Kind:        "PullRequest",
				Identifiers: []string{"number"},
				// Non-empty so getConfigurationGVR is non-empty and the auth-secret block is reached at all.
				ConfigurationFields: []definitionv1alpha1.ConfigurationField{{
					FromOpenAPI:        definitionv1alpha1.FromOpenAPI{Name: "api-version", In: "query"},
					FromRestDefinition: definitionv1alpha1.FromRestDefinition{Actions: []string{"*"}},
				}},
			},
		},
	}
	cr.Status.OASHash = "unchanged"
	cr.Status.ResourceHash = resourceHash(cr)

	// The digest of what CollectAuthSecretRefs returns when no Configuration instance exists. Setting it
	// makes the DIGEST check pass, which is what forces Observe past it and into the existence check --
	// the whole point, since a digest mismatch would report drift for the wrong reason.
	d, err := deploy.AuthSecretRefsDigest(map[string][]string{})
	require.NoError(t, err)
	cr.Status.AuthSecretDigest = d
	cr.Status.AuthSecretRBACNamespaces = []string{"demo-system"}
	return cr
}

func authSecretRoleFor(cr *definitionv1alpha1.RestDefinition, ns string) *rbacv1.Role {
	return &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{
		Name: deploy.AuthSecretRoleName(targetGVRForTest(cr)), Namespace: ns}}
}

func authSecretBindingFor(cr *definitionv1alpha1.RestDefinition, ns string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{
		Name: deploy.AuthSecretRoleName(targetGVRForTest(cr)), Namespace: ns}}
}

func targetGVRForTest(cr *definitionv1alpha1.RestDefinition) schema.GroupVersionResource {
	return plurals.ToGroupVersionResource(schema.GroupVersionKind{
		Group:   cr.Spec.ResourceGroup,
		Version: resourceVersion,
		Kind:    cr.Spec.Resource.Kind,
	})
}

// The regression test for #473: a missing auth-secret Role must be drift.
//
// Before this, the status recorded the namespace as provisioned, the references had not changed so the
// digest matched, and Observe reported ResourceUpToDate -- so Update never ran and syncAuthSecretRBAC,
// reachable only from Create and Update, never reprovisioned the Role. The controller sat without read
// access to its credential until something unrelated changed the spec, the OAS or the references.
func TestObserveReportsDriftWhenTheAuthSecretRoleIsMissing(t *testing.T) {
	var msgs, lines []string
	cr := restDefinitionForAuthRBACTest(t)
	e := externalForHashTest(t, cr, &msgs, &lines)

	obs, err := e.Observe(context.Background(), cr)
	require.NoError(t, err)

	assert.False(t, obs.ResourceUpToDate,
		"a provisioned namespace whose auth-secret Role is gone must report drift, so the runtime calls "+
			"Update and the Role is reprovisioned")
	assert.True(t, obs.ResourceExists, "the CRD is present; this is drift, not absence")
	assert.True(t, containsSubstring(msgs, "Auth secret RBAC is missing"),
		"the drift must come from the RBAC existence check specifically, not from an unrelated branch; got %v", msgs)
}

// THE CONTROL THAT MAKES THE ABOVE MEAN SOMETHING. With the Role and RoleBinding present, this same CR
// must report up-to-date. Without it, a test asserting "drift" would pass against an Observe that reports
// drift unconditionally -- which is the 0.28.0 readiness loop, not a fix.
func TestObserveIsQuietWhenTheAuthSecretRoleIsPresent(t *testing.T) {
	var msgs, lines []string
	cr := restDefinitionForAuthRBACTest(t)
	e := externalForHashTest(t, cr, &msgs, &lines,
		authSecretRoleFor(cr, "demo-system"), authSecretBindingFor(cr, "demo-system"))

	obs, err := e.Observe(context.Background(), cr)
	require.NoError(t, err)

	assert.False(t, containsSubstring(msgs, "Auth secret RBAC is missing"),
		"with both objects present the existence check must stay silent; got %v", msgs)
	assert.True(t, obs.ResourceUpToDate,
		"nothing else about this resource has drifted, so the RBAC check must be the only thing that could "+
			"have reported it -- if this fails, the drift above may not have come from the RBAC check at all")
}

func containsSubstring(hay []string, needle string) bool {
	for _, h := range hay {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}
