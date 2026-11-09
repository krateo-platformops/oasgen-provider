//go:build unit

package restdefinition

import (
	"context"
	"testing"

	"github.com/krateo-platformops/oasgen-provider/apis"
	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/deploy"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/plurals"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// WHY THIS TEST EXISTS, and it is not symmetry.
//
// SyncAuthSecretRBAC revokes the Role in every namespace that is in the CALLER'S RECORD and no longer in
// its collected set. #510 added a second caller to it. If the two callers share either the Role name or the
// status record, each revokes the other's namespaces on every reconcile -- and the symptom, a credential
// readable one moment and Forbidden the next, passes every SINGLE-caller test, because a single-caller test
// cannot observe the other caller.
//
// A reviewer ran 9 mutants against the first version of #510 and FOUR survived, all of them caller-crossing:
//
//	removing both syncSecretRefRBAC calls from Create and Update
//	syncSecretRefRBAC using AuthSecretRoleName
//	syncSecretRefRBAC reading/writing AuthSecretRBACNamespaces
//	syncSecretRefRBAC storing into AuthSecretDigest
//
// Every one of those is invisible unless a test runs BOTH callers on one resource with an OVERLAPPING
// namespace. That overlap is the whole point of the fixture: in separate namespaces, a cross-wired record
// still produces a plausible-looking Role.
const sharedNS = "shared"

func twoCallerExternal(t *testing.T, cr *definitionv1alpha1.RestDefinition, objs ...client.Object) *external {
	t.Helper()

	prev := RDCrbacConfigFolder
	RDCrbacConfigFolder = "testdata/setup/rdc/rbac/"
	t.Cleanup(func() { RDCrbacConfigFolder = prev })

	s := runtime.NewScheme()
	require.NoError(t, apis.AddToScheme(s))
	require.NoError(t, rbacv1.AddToScheme(s))

	// The Configuration Kind and the generated Kind, both as unstructured, plus their List kinds so the
	// collectors can List them.
	for _, gvk := range []schema.GroupVersionKind{getConfigurationGVK(cr), secretRefGeneratedGVK(cr, twoCallerGVR(cr))} {
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		lk := gvk
		lk.Kind += "List"
		s.AddKnownTypeWithName(lk, &unstructured.UnstructuredList{})
	}

	return &external{
		kube: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build(),
		log:  logging.NewNopLogger(),
	}
}

func twoCallerGVR(cr *definitionv1alpha1.RestDefinition) schema.GroupVersionResource {
	return plurals.ToGroupVersionResource(schema.GroupVersionKind{
		Group:   cr.Spec.ResourceGroup,
		Version: resourceVersion,
		Kind:    cr.Spec.Resource.Kind,
	})
}

// A RestDefinition declaring BOTH an auth path (ConfigurationFields) and a secretRef resolver.
func restDefinitionWithBothGrants(t *testing.T) *definitionv1alpha1.RestDefinition {
	t.Helper()
	cr := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "pullrequest", Namespace: "demo-system"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			OASPath:       "configmap://demo-system/pr/openapi.yaml",
			ResourceGroup: "github.krateo.io",
			Resource: definitionv1alpha1.Resource{
				Kind:        "PullRequest",
				Identifiers: []string{"number"},
				ConfigurationFields: []definitionv1alpha1.ConfigurationField{{
					FromOpenAPI:        definitionv1alpha1.FromOpenAPI{Name: "api-version", In: "query"},
					FromRestDefinition: definitionv1alpha1.FromRestDefinition{Actions: []string{"*"}},
				}},
				VerbsDescription: []definitionv1alpha1.VerbsDescription{{
					Action: "create",
					FieldMapping: []definitionv1alpha1.FieldMappingItem{{
						InBody: "token",
						Resolver: &definitionv1alpha1.FieldResolver{
							Type: "secretRef",
							SecretRef: &definitionv1alpha1.SecretRefResolver{
								NameFromCustomResource: "spec.tokenRef.name",
								KeyFromCustomResource:  "spec.tokenRef.key",
							},
						},
					}},
				}},
			},
		},
	}
	return cr
}

// A Configuration instance whose auth ref names Secret "auth-s" IN THE SHARED NAMESPACE.
func configurationNaming(cr *definitionv1alpha1.RestDefinition, secretName string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"authentication": map[string]any{
				// "basic", NOT "basicAuth". My first fixture used the latter and the auth collector found nothing,
				// so the auth Role was never created -- which made one of these tests PASS VACUOUSLY: it asserts
				// the auth Role is absent after a deletion, and an absent Role that was never created satisfies
				// that. The paths come from oas2jsonschema (#500); I printed them rather than guessing twice.
				"basic": map[string]any{
					"usernameRef": map[string]any{"name": secretName, "namespace": sharedNS, "key": "u"},
					"passwordRef": map[string]any{"name": secretName, "namespace": sharedNS, "key": "p"},
				},
			},
		},
	}}
	u.SetGroupVersionKind(getConfigurationGVK(cr))
	u.SetNamespace(sharedNS)
	u.SetName("cfg")
	return u
}

// A generated-Kind instance whose secretRef names Secret "otp-s" IN THE SAME NAMESPACE.
func generatedInstanceNaming(cr *definitionv1alpha1.RestDefinition, secretName string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"tokenRef": map[string]any{"name": secretName, "key": "t"}},
	}}
	u.SetGroupVersionKind(secretRefGeneratedGVK(cr, twoCallerGVR(cr)))
	u.SetNamespace(sharedNS)
	u.SetName("pr-1")
	return u
}

// A second generated-Kind instance in a DIFFERENT namespace. Its whole job is to make the two status
// records DIFFER.
//
// The overlapping namespace catches a crossed Role NAME and a crossed DIGEST, because both produce a
// visibly wrong Role or an equal pair of digests. It is blind to a crossed RECORD: with one namespace, both
// records hold ["shared"], so reading or writing the other caller's record changes nothing observable. A
// mutant doing exactly that survived until this instance existed.
func secondInstanceInAnotherNamespace(cr *definitionv1alpha1.RestDefinition, ns, secretName string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"tokenRef": map[string]any{"name": secretName, "key": "t"}},
	}}
	u.SetGroupVersionKind(secretRefGeneratedGVK(cr, twoCallerGVR(cr)))
	u.SetNamespace(ns)
	u.SetName("pr-2")
	return u
}

const otherNS = "other"

func roleNames(t *testing.T, kube client.Client, name, ns string) []string {
	t.Helper()
	var r rbacv1.Role
	err := kube.Get(context.Background(), types.NamespacedName{Name: name, Namespace: ns}, &r)
	if apierrors.IsNotFound(err) {
		return nil
	}
	require.NoError(t, err)
	if len(r.Rules) == 0 {
		return nil
	}
	return r.Rules[0].ResourceNames
}

func TestBothCallersOnOneResourceDoNotRevokeEachOther(t *testing.T) {
	cr := restDefinitionWithBothGrants(t)
	gvr := twoCallerGVR(cr)
	e := twoCallerExternal(t, cr,
		configurationNaming(cr, "auth-s"),
		generatedInstanceNaming(cr, "otp-s"),
		secondInstanceInAnotherNamespace(cr, otherNS, "otp-2"))
	ctx := context.Background()

	require.NoError(t, e.syncAuthSecretRBAC(ctx, cr, gvr))
	require.NoError(t, e.syncSecretRefRBAC(ctx, cr, gvr))

	authRole, secretRefRole := deploy.AuthSecretRoleName(gvr), deploy.SecretRefRoleName(gvr)

	// THE CORE ASSERTION: each Role names only its OWN Secret, in the SAME namespace.
	assert.Equal(t, []string{"auth-s"}, roleNames(t, e.kube, authRole, sharedNS),
		"the auth Role must name only the Configuration's Secret")
	assert.Equal(t, []string{"otp-s"}, roleNames(t, e.kube, secretRefRole, sharedNS),
		"the secretRef Role must name only the instance's Secret -- if this is empty, the second sync "+
			"either did not run or wrote to the auth Role's name")

	// The secretRef grant also reaches the OTHER namespace, which the auth grant never touches.
	assert.Equal(t, []string{"otp-2"}, roleNames(t, e.kube, secretRefRole, otherNS),
		"the second instance's namespace must get its own Role")
	assert.Nil(t, roleNames(t, e.kube, authRole, otherNS),
		"and the auth Role must NOT appear there -- it has no Configuration in that namespace")

	// TWO RECORDS THAT DIFFER. This is what makes a crossed record visible: with one namespace each they
	// would be equal, and reading the other caller's record would change nothing observable.
	assert.Equal(t, []string{sharedNS}, cr.Status.AuthSecretRBACNamespaces)
	assert.Equal(t, []string{otherNS, sharedNS}, cr.Status.SecretRefRBACNamespaces,
		"the secretRef record spans both namespaces; the auth record spans one")
	assert.NotEqual(t, cr.Status.AuthSecretRBACNamespaces, cr.Status.SecretRefRBACNamespaces,
		"if these are ever equal this test has lost its power to see a crossed record")

	// Two separate digests. Equal digests would mean one call was handed the other's refs.
	assert.NotEmpty(t, cr.Status.AuthSecretDigest)
	assert.NotEmpty(t, cr.Status.SecretRefDigest)
	assert.NotEqual(t, cr.Status.AuthSecretDigest, cr.Status.SecretRefDigest,
		"the two digests are computed by the SAME function over different refs; equal values mean a "+
			"cross-wiring that no single-caller test can see")
}

func TestDeletingTheInstanceRevokesOnlyTheSecretRefGrant(t *testing.T) {
	cr := restDefinitionWithBothGrants(t)
	gvr := twoCallerGVR(cr)
	inst := generatedInstanceNaming(cr, "otp-s")
	e := twoCallerExternal(t, cr, configurationNaming(cr, "auth-s"), inst)
	ctx := context.Background()

	require.NoError(t, e.syncAuthSecretRBAC(ctx, cr, gvr))
	require.NoError(t, e.syncSecretRefRBAC(ctx, cr, gvr))
	authDigestBefore := cr.Status.AuthSecretDigest

	require.NoError(t, e.kube.Delete(ctx, inst))
	require.NoError(t, e.syncSecretRefRBAC(ctx, cr, gvr))

	assert.Nil(t, roleNames(t, e.kube, deploy.SecretRefRoleName(gvr), sharedNS),
		"the last instance is gone, so the secretRef Role must be revoked")
	assert.Empty(t, cr.Status.SecretRefRBACNamespaces, "and its record must drop the namespace")

	// THE HALF A SINGLE-CALLER TEST CANNOT SEE.
	assert.Equal(t, []string{"auth-s"}, roleNames(t, e.kube, deploy.AuthSecretRoleName(gvr), sharedNS),
		"revoking the secretRef grant must NOT touch the auth Role in the same namespace")
	assert.Equal(t, []string{sharedNS}, cr.Status.AuthSecretRBACNamespaces, "nor its record")
	assert.Equal(t, authDigestBefore, cr.Status.AuthSecretDigest, "nor its digest")
}

// THE TEST THAT SEES A CROSSED *READ* OF THE STATUS RECORD, which every other assertion here is blind to.
//
// SyncAuthSecretRBAC revokes the Role in every namespace that is in previouslyProvisioned and NOT in the
// newly collected set. A mutant passing the AUTH record as previouslyProvisioned survived all of the above,
// because every namespace in play was in both records -- so there was nothing for the wrong record to fail
// to revoke.
//
// The shape that exposes it: a namespace the secretRef sync provisioned and the AUTH sync never did, which
// then needs revoking. With the correct record it is revoked. With the auth record it is not even
// considered, so a Role granting Secret reads survives its last instance -- silently, and for ever.
func TestANamespaceOnlyTheSecretRefSyncProvisionedIsStillRevoked(t *testing.T) {
	cr := restDefinitionWithBothGrants(t)
	gvr := twoCallerGVR(cr)
	second := secondInstanceInAnotherNamespace(cr, otherNS, "otp-2")
	e := twoCallerExternal(t, cr,
		configurationNaming(cr, "auth-s"), // only in sharedNS, so otherNS is secretRef-only
		generatedInstanceNaming(cr, "otp-s"),
		second)
	ctx := context.Background()

	require.NoError(t, e.syncAuthSecretRBAC(ctx, cr, gvr))
	require.NoError(t, e.syncSecretRefRBAC(ctx, cr, gvr))
	require.Equal(t, []string{"otp-2"}, roleNames(t, e.kube, deploy.SecretRefRoleName(gvr), otherNS),
		"precondition: otherNS has a secretRef Role")
	require.Equal(t, []string{sharedNS}, cr.Status.AuthSecretRBACNamespaces,
		"precondition: the AUTH record does NOT contain otherNS -- that is what makes this test able to fail")

	// otherNS's only instance goes away. sharedNS still has one, so this is a revoke of otherNS alone.
	require.NoError(t, e.kube.Delete(ctx, second))
	require.NoError(t, e.syncSecretRefRBAC(ctx, cr, gvr))

	assert.Nil(t, roleNames(t, e.kube, deploy.SecretRefRoleName(gvr), otherNS),
		"otherNS has no instances left, so its secretRef Role must be revoked; if it survives, the sync "+
			"consulted a record that never listed otherNS and so never knew to revoke it")
	assert.Equal(t, []string{sharedNS}, cr.Status.SecretRefRBACNamespaces, "and the record must drop it")

	// sharedNS is untouched in both directions.
	assert.Equal(t, []string{"otp-s"}, roleNames(t, e.kube, deploy.SecretRefRoleName(gvr), sharedNS))
	assert.Equal(t, []string{"auth-s"}, roleNames(t, e.kube, deploy.AuthSecretRoleName(gvr), sharedNS))
}

func TestDeletingTheConfigurationRevokesOnlyTheAuthGrant(t *testing.T) {
	cr := restDefinitionWithBothGrants(t)
	gvr := twoCallerGVR(cr)
	cfg := configurationNaming(cr, "auth-s")
	e := twoCallerExternal(t, cr, cfg, generatedInstanceNaming(cr, "otp-s"))
	ctx := context.Background()

	require.NoError(t, e.syncAuthSecretRBAC(ctx, cr, gvr))
	require.NoError(t, e.syncSecretRefRBAC(ctx, cr, gvr))
	secretRefDigestBefore := cr.Status.SecretRefDigest

	require.NoError(t, e.kube.Delete(ctx, cfg))
	require.NoError(t, e.syncAuthSecretRBAC(ctx, cr, gvr))

	assert.Nil(t, roleNames(t, e.kube, deploy.AuthSecretRoleName(gvr), sharedNS))
	assert.Empty(t, cr.Status.AuthSecretRBACNamespaces)

	// The mirror image, so neither direction is asserted alone.
	assert.Equal(t, []string{"otp-s"}, roleNames(t, e.kube, deploy.SecretRefRoleName(gvr), sharedNS),
		"revoking the auth grant must NOT touch the secretRef Role in the same namespace")
	assert.Equal(t, []string{sharedNS}, cr.Status.SecretRefRBACNamespaces)
	assert.Equal(t, secretRefDigestBefore, cr.Status.SecretRefDigest)
}

func TestEachTeardownRemovesOnlyItsOwnPair(t *testing.T) {
	cr := restDefinitionWithBothGrants(t)
	gvr := twoCallerGVR(cr)
	e := twoCallerExternal(t, cr, configurationNaming(cr, "auth-s"), generatedInstanceNaming(cr, "otp-s"))
	ctx := context.Background()

	require.NoError(t, e.syncAuthSecretRBAC(ctx, cr, gvr))
	require.NoError(t, e.syncSecretRefRBAC(ctx, cr, gvr))

	require.NoError(t, e.teardownSecretRefRBAC(ctx, cr, gvr))
	assert.Nil(t, roleNames(t, e.kube, deploy.SecretRefRoleName(gvr), sharedNS), "its own pair goes")
	assert.Equal(t, []string{"auth-s"}, roleNames(t, e.kube, deploy.AuthSecretRoleName(gvr), sharedNS),
		"the auth pair must survive the secretRef teardown -- Delete is the LAST pass, so a pair removed "+
			"here by the wrong caller is gone for good")

	require.NoError(t, e.teardownAuthSecretRBAC(ctx, cr, gvr))
	assert.Nil(t, roleNames(t, e.kube, deploy.AuthSecretRoleName(gvr), sharedNS))
}

// S2's regression test: spec.resource.kind is author-written and uncased, so the GVK the collector asks for
// must be the one the apiserver serves. With the raw value, `kind: pullRequest` made the lister ask for a
// Kind nothing serves -- swallowed as "CRD not served yet", giving zero names, no Role and silence: #510's
// symptom reproduced by the fix for #510.
func TestALowercaseKindStillResolvesToTheServedGVK(t *testing.T) {
	cr := restDefinitionWithBothGrants(t)
	cr.Spec.Resource.Kind = "pullRequest"
	gvr := twoCallerGVR(cr)

	got := secretRefGeneratedGVK(cr, gvr)
	assert.Equal(t, "PullRequest", got.Kind,
		"the collector must ask for the Kind the apiserver serves, not the spelling the author used")
	assert.Equal(t, getConfigurationGVK(cr).Group, got.Group, "and the same group as every other GVK here")
}
