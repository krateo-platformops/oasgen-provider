//go:build unit

package restdefinition

import (
	"context"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/deploy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// THE POINT OF TESTING THIS AT THE OBSERVE LEVEL, in the words #473 already used in this package: a test on
// the comparison alone reproduces the bug it is meant to catch.
//
// That is not a hypothetical here. #510's first implementation had the collector, the lister, the sync and
// the teardown, each unit-tested and green, and provisioned NOTHING on a real cluster -- 8 CRs across 7
// namespaces, zero Roles, zero log lines. syncSecretRefRBAC runs only from Create and Update; the generated
// CRD is created INSIDE Create, so Create's own collection always sees zero instances; and Observe had no
// check, so nothing ever reached Update. Every test passed because they drove Create and Update directly.
//
// So these tests assert on Observe's OUTPUT, which is the only thing the runtime acts on.
// Built from restDefinitionForAuthRBACTest rather than from scratch, deliberately: my first version
// differed in name, group and kind as well as in the resolver, and Observe returned early with "CRD not
// found" before reaching either new check -- so all four tests were measuring the wrong thing. Reusing the
// known-good fixture isolates the ONE variable under test, which is the secretRef resolver.
func restDefinitionWithASecretRefResolver(t *testing.T) *definitionv1alpha1.RestDefinition {
	t.Helper()
	cr := restDefinitionForAuthRBACTest(t)

	// The auth fixture sets these so its own checks pass; clear the recorded namespace so the secretRef
	// existence check is the only one that can speak, and keep the auth digest so the auth checks stay quiet.
	cr.Status.AuthSecretRBACNamespaces = nil

	cr.Spec.Resource.VerbsDescription = []definitionv1alpha1.VerbsDescription{{
		Action: "create",
		// POINTED AT THE HARNESS OAS'S ONLY OPERATION, deliberately (#627).
		//
		// This verb used to carry no path and no method, because no test here had ever needed the
		// generator to RUN -- the CRD is hand-seeded so Observe can get past the CRD gate. That was only
		// survivable while Create stamped status.oasHash without rendering: the false stamp made Update's
		// regeneration gate go quiet, so the generator was never asked to do anything. Three tests in this
		// harness were therefore passing BECAUSE of that defect, and removing it made them ask the
		// generator to render a verb with no path.
		//
		// A fixture that cannot generate is a fixture that cannot exercise the reconcile loop it is named
		// for, so the path and method belong here rather than being patched in by the one helper that
		// happened to need them.
		Path:   "/prs",
		Method: "post",
		FieldMapping: []definitionv1alpha1.FieldMappingItem{{
			InBody: "credentials.value",
			Resolver: &definitionv1alpha1.FieldResolver{
				Type: "secretRef",
				SecretRef: &definitionv1alpha1.SecretRefResolver{
					NameFromCustomResource: "spec.credentials[?type=otp].secretDataSecretRef.name",
					KeyFromCustomResource:  "spec.credentials[?type=otp].secretDataSecretRef.key",
				},
			},
		}},
	}}
	cr.Status.ResourceHash = resourceHash(cr)
	return cr
}

func secretRefRoleFor(cr *definitionv1alpha1.RestDefinition, ns string) *rbacv1.Role {
	return &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{
		Name: deploy.SecretRefRoleName(targetGVRForTest(cr)), Namespace: ns}}
}

func secretRefBindingFor(cr *definitionv1alpha1.RestDefinition, ns string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{
		Name: deploy.SecretRefRoleName(targetGVRForTest(cr)), Namespace: ns}}
}

// THE REGRESSION TEST FOR THE WHOLE DEFECT. A stale digest -- which is what a CR appearing, changing or
// being deleted produces -- must reach Observe's output as drift. Without this the sync is unreachable.
func TestObserveReportsDriftWhenTheSecretRefDigestIsStale(t *testing.T) {
	var msgs, lines []string
	cr := restDefinitionWithASecretRefResolver(t)
	cr.Status.SecretRefDigest = "stale-from-a-previous-set-of-instances"
	e := externalForHashTest(t, cr, &msgs, &lines)

	obs, err := e.Observe(context.Background(), cr)
	require.NoError(t, err)

	assert.False(t, obs.ResourceUpToDate,
		"a changed set of secretRef Secrets must report drift, or syncSecretRefRBAC is never reached and "+
			"the Role is never provisioned -- the measured 0-Roles defect")
	assert.True(t, containsSubstring(msgs, "secretRef secret names changed"),
		"the drift must come from the secretRef digest check specifically; got %v", msgs)
}

// A recorded namespace whose Role has gone must be drift too. #473's lesson: the digest cannot express
// "does the Role exist", so a hand-deleted Role would otherwise leave Update unreachable for ever.
func TestObserveReportsDriftWhenTheSecretRefRoleIsMissing(t *testing.T) {
	var msgs, lines []string
	cr := restDefinitionWithASecretRefResolver(t)
	d, err := deploy.AuthSecretRefsDigest(map[string][]string{})
	require.NoError(t, err)
	cr.Status.SecretRefDigest = d // digest matches, so only the existence check can fire
	cr.Status.SecretRefRBACNamespaces = []string{"demo-system"}
	e := externalForHashTest(t, cr, &msgs, &lines)

	obs, err := e.Observe(context.Background(), cr)
	require.NoError(t, err)

	assert.False(t, obs.ResourceUpToDate, "a provisioned namespace whose secretRef Role is gone must be drift")
	assert.True(t, containsSubstring(msgs, "secretRef RBAC is missing"),
		"the drift must come from the existence check, not the digest; got %v", msgs)
}

// THE CONTROL. With the digest matching and both objects present, this same CR must be quiet -- otherwise
// the two tests above would pass against an Observe that reports drift unconditionally.
func TestObserveIsQuietWhenTheSecretRefRoleIsPresent(t *testing.T) {
	var msgs, lines []string
	cr := restDefinitionWithASecretRefResolver(t)
	d, err := deploy.AuthSecretRefsDigest(map[string][]string{})
	require.NoError(t, err)
	cr.Status.SecretRefDigest = d
	cr.Status.SecretRefRBACNamespaces = []string{"demo-system"}
	e := externalForHashTest(t, cr, &msgs, &lines,
		secretRefRoleFor(cr, "demo-system"), secretRefBindingFor(cr, "demo-system"))

	obs, err := e.Observe(context.Background(), cr)
	require.NoError(t, err)

	assert.False(t, containsSubstring(msgs, "secretRef RBAC is missing"),
		"with both objects present the existence check must stay silent; got %v", msgs)
	assert.False(t, containsSubstring(msgs, "secretRef secret names changed"),
		"with a matching digest the digest check must stay silent; got %v", msgs)
	assert.True(t, obs.ResourceUpToDate, "nothing has drifted, so Observe must be quiet")
}

// THE REGRESSION TEST FOR THE BUG I INTRODUCED WITH THE FIX. AuthSecretRefsDigest of an empty map is NOT
// "" -- it is the FNV offset basis -- so comparing it to a status field that is "" on every resource
// predating the field reported drift on every reconcile, for ever. An existing auth test caught it.
//
// The gate is DeclaresSecretRef: a kind with no secretRef resolver skips both checks, so an empty status is
// not drift. This also removes a List per reconcile for the majority of kinds, which cannot have any.
func TestObserveIsQuietForAKindThatDeclaresNoSecretRefAtAll(t *testing.T) {
	var msgs, lines []string
	cr := restDefinitionWithASecretRefResolver(t)
	// Same resource, resolver removed: this is the shape of nearly every RestDefinition in existence.
	cr.Spec.Resource.VerbsDescription[0].FieldMapping = nil
	cr.Status.ResourceHash = resourceHash(cr)
	require.Empty(t, cr.Status.SecretRefDigest, "the point of this test is an UNSET digest")

	e := externalForHashTest(t, cr, &msgs, &lines)
	obs, err := e.Observe(context.Background(), cr)
	require.NoError(t, err)

	assert.True(t, obs.ResourceUpToDate,
		"a kind with no secretRef resolver and no digest must NOT be drift: the digest of an empty set is a "+
			"non-empty constant, so an ungated check redeploys this resource on every reconcile for ever")
	assert.False(t, containsSubstring(msgs, "secretRef secret names changed"),
		"the digest check must not even run for a kind that declares no secretRef; got %v", msgs)
}
