//go:build unit

package restdefinition

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/krateo-platformops/oasgen-provider/apis"
	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/deploy"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/oas2jsonschema"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/objects"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// WHY THIS FILE EXISTS (#510). Every other secretRef test calls the collector, the sync or one Observe check
// directly. None of them can fail if the sync is unreachable from the reconcile loop, and that was the
// defect: syncSecretRefRBAC worked and never ran. Removing BOTH of its call sites from Create and Update
// passed the whole suite.
//
// So these tests touch the controller only through the entry points provider-runtime calls (Observe, then
// Create or Update according to the observation, and Delete), with real instances of the GENERATED kind in
// the fake cluster. They assert on the Roles in the cluster and on the PERSISTED status, never on a return
// value of an internal helper.

const secretRefLoopOAS = `openapi: 3.0.0
info:
  title: pullrequests
  version: v1alpha1
paths:
  /prs:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                credentials:
                  type: object
                  properties:
                    value:
                      type: string
      responses:
        "201":
          description: created
          content:
            application/json:
              schema:
                type: object
                properties:
                  number:
                    type: integer
`

var (
	loopGeneratedGVK = schema.GroupVersionKind{Group: "github.krateo.io", Version: "v1alpha1", Kind: "PullRequest"}
	loopConfigGVK    = schema.GroupVersionKind{Group: "github.krateo.io", Version: "v1alpha1", Kind: "PullRequestConfiguration"}
)

type secretRefLoop struct {
	t    *testing.T
	e    *external
	cr   *definitionv1alpha1.RestDefinition
	msgs *[]string
}

// newSecretRefLoop builds a fake cluster in which the generated CRD is served but the RDC Deployment does
// not exist yet, and the RestDefinition's status is EMPTY. That is the state right after Create's first
// phase applied the CRD -- the point where #510's instances cannot exist yet -- so the first reconcile is a
// real Create (Observe answers "does not exist" for a missing Deployment).
func newSecretRefLoop(t *testing.T, extra ...client.Object) *secretRefLoop {
	t.Helper()
	var msgs, lines []string
	cr := restDefinitionWithASecretRefResolver(t)
	cr.Status = definitionv1alpha1.RestDefinitionStatus{}

	// The CRD comes from the existing harness, so this file cannot drift from what the other Observe tests
	// consider a served CRD. The harness also points RDCtemplateDeploymentPath at its minimal template.
	base := externalForHashTest(t, cr, &msgs, &lines)
	var crds apiextensionsv1.CustomResourceDefinitionList
	require.NoError(t, base.kube.List(context.Background(), &crds))
	require.Len(t, crds.Items, 1)

	s := runtime.NewScheme()
	require.NoError(t, apis.AddToScheme(s))
	require.NoError(t, apiextensionsv1.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))
	require.NoError(t, rbacv1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	for _, gvk := range []schema.GroupVersionKind{loopGeneratedGVK, loopConfigGVK} {
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		list := gvk
		list.Kind += "List"
		s.AddKnownTypeWithName(list, &unstructured.UnstructuredList{})
	}

	oas := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "pr", Namespace: "demo-system"},
		Data:       map[string]string{"openapi.yaml": secretRefLoopOAS},
	}
	objs := append([]client.Object{cr.DeepCopy(), &crds.Items[0], oas}, extra...)
	base.kube = fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		// Status is a subresource on a real apiserver: a Deploy that re-applies the Deployment must not
		// reset it to not-ready, and a spec write must not carry the RestDefinition's status.
		WithStatusSubresource(&definitionv1alpha1.RestDefinition{}, &appsv1.Deployment{}).
		Build()
	base.parser = oas2jsonschema.NewLibOASParser()

	testdata, err := filepath.Abs("../../tools/deploy/testdata")
	require.NoError(t, err)
	prevRBAC, prevCM := RDCrbacConfigFolder, RDCtemplateConfigmapPath
	RDCrbacConfigFolder = testdata
	RDCtemplateConfigmapPath = filepath.Join(testdata, "cm.yaml")
	t.Cleanup(func() { RDCrbacConfigFolder, RDCtemplateConfigmapPath = prevRBAC, prevCM })

	return &secretRefLoop{t: t, e: base, cr: cr, msgs: &msgs}
}

// reconcile does what provider-runtime's managed reconciler does with an observation: Create when the
// resource does not exist, Update when it exists but is not up to date, nothing otherwise. It reports
// whether it acted, and the messages Observe logged, so a caller can see WHY.
func (l *secretRefLoop) reconcile() (drift bool, observed []string) {
	l.t.Helper()
	ctx := context.Background()
	// Re-read, as the runtime does at the top of every reconcile, so what is compared is what persisted.
	fresh := &definitionv1alpha1.RestDefinition{}
	require.NoError(l.t, l.e.kube.Get(ctx, client.ObjectKeyFromObject(l.cr), fresh))
	l.cr = fresh

	*l.msgs = nil
	obs, err := l.e.Observe(ctx, l.cr)
	require.NoError(l.t, err)
	observed = append([]string(nil), *l.msgs...)
	switch {
	case !obs.ResourceExists:
		require.NoError(l.t, l.e.Create(ctx, l.cr))
		l.rollOut()
		return true, observed
	case !obs.ResourceUpToDate:
		require.NoError(l.t, l.e.Update(ctx, l.cr))
		return true, observed
	}
	// Up to date must mean Observe RAN the secretRef checks and passed them, not that it stopped at a gate
	// above them ("CRD not found", "not ready yet") which also answers up to date. Finalizer management is
	// logged only after every drift check has passed.
	require.True(l.t, containsSubstring(observed, "Managing finalizers for RestDefinition"),
		"Observe answered up to date without reaching the end of its drift checks: %v", observed)
	return false, observed
}

// rollOut does the kubelet's part: the Deployment Create applied becomes ready. Without it every later
// Observe stops at "Dynamic Controller not ready yet", which answers up to date and would make every
// assertion below vacuous.
func (l *secretRefLoop) rollOut() {
	l.t.Helper()
	ctx := context.Background()
	// Located with the SAME constructor Observe uses, so this readies exactly the object Observe reads.
	dep := &appsv1.Deployment{}
	require.NoError(l.t, objects.CreateK8sObject(dep, targetGVRForTest(l.cr), types.NamespacedName{
		Namespace: l.cr.Namespace, Name: l.cr.Name + deploy.ControllerResourceSuffix}, RDCtemplateDeploymentPath))
	require.NoError(l.t, l.e.kube.Get(ctx, client.ObjectKeyFromObject(dep), dep),
		"Create must have deployed the controller")
	dep.Status = appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, UpdatedReplicas: 1}
	require.NoError(l.t, l.e.kube.Status().Update(ctx, dep))
}

// settle reconciles until Observe stops asking for work, failing if it never does: a loop that does not
// converge is the perpetual-redeploy failure this mechanism must never have.
func (l *secretRefLoop) settle() {
	l.t.Helper()
	for i := 0; i < 5; i++ {
		if drift, _ := l.reconcile(); !drift {
			return
		}
	}
	l.t.Fatalf("the reconcile loop did not converge within 5 passes; last Observe said: %v", *l.msgs)
}

func (l *secretRefLoop) persisted() definitionv1alpha1.RestDefinitionStatus {
	l.t.Helper()
	got := &definitionv1alpha1.RestDefinition{}
	require.NoError(l.t, l.e.kube.Get(context.Background(), client.ObjectKeyFromObject(l.cr), got))
	return got.Status
}

func (l *secretRefLoop) add(obj client.Object) {
	l.t.Helper()
	require.NoError(l.t, l.e.kube.Create(context.Background(), obj))
}

func (l *secretRefLoop) remove(obj client.Object) {
	l.t.Helper()
	require.NoError(l.t, l.e.kube.Delete(context.Background(), obj))
}

// roleNames returns the resourceNames of roleName in ns, or nil when the Role does not exist. It also
// requires the RoleBinding to exist exactly when the Role does: half a pair grants nothing, or grants
// something nobody tracks.
func (l *secretRefLoop) roleNames(roleName, ns string) []string {
	l.t.Helper()
	ctx := context.Background()
	key := types.NamespacedName{Namespace: ns, Name: roleName}
	var role rbacv1.Role
	roleErr := l.e.kube.Get(ctx, key, &role)
	var binding rbacv1.RoleBinding
	bindingErr := l.e.kube.Get(ctx, key, &binding)
	if apierrors.IsNotFound(roleErr) {
		require.True(l.t, apierrors.IsNotFound(bindingErr), "Role %s/%s is gone but its RoleBinding is not", ns, roleName)
		return nil
	}
	require.NoError(l.t, roleErr)
	require.NoError(l.t, bindingErr, "Role %s/%s exists without its RoleBinding", ns, roleName)
	var names []string
	for _, r := range role.Rules {
		names = append(names, r.ResourceNames...)
	}
	return names
}

func generatedInstance(ns, name, otpSecret string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"credentials": []any{map[string]any{
			"type":                "otp",
			"secretDataSecretRef": map[string]any{"name": otpSecret, "key": "value"},
		}},
	}}}
	u.SetGroupVersionKind(loopGeneratedGVK)
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func configurationInstance(ns, name, passwordSecretNS, passwordSecret string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
		"authentication": map[string]any{"basic": map[string]any{
			"username": "bot",
			"passwordRef": map[string]any{
				"name": passwordSecret, "namespace": passwordSecretNS, "key": "password",
			},
		}},
	}}}
	u.SetGroupVersionKind(loopConfigGVK)
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func emptySecretRefDigest(t *testing.T) string {
	t.Helper()
	d, err := deploy.AuthSecretRefsDigest(nil)
	require.NoError(t, err)
	return d
}

// THE REGRESSION TEST FOR #510's measured defect, through the reconcile entry points only.
//
// Create runs while no instance can exist yet, so the grant has to come from a LATER pass: an instance
// appears, Observe notices, Update provisions, and the loop goes quiet. Then the reverse for revocation.
// Remove the sync from Create and Update and this fails before any Role is asserted: Create never stores
// the digest, so Observe compares "" with the empty set's digest on every pass and the loop never settles.
func TestSecretRefRBACIsProvisionedAndRevokedThroughTheReconcileLoop(t *testing.T) {
	l := newSecretRefLoop(t)
	roleName := deploy.SecretRefRoleName(targetGVRForTest(l.cr))
	empty := emptySecretRefDigest(t)

	// First pass: Create, with no instance of the kind in existence.
	drift, _ := l.reconcile()
	require.True(t, drift, "an empty status must make the first pass act")
	l.settle()
	require.Equal(t, empty, l.persisted().SecretRefDigest,
		"with no instances, the persisted digest must be the digest of the empty set, or Observe drifts for ever")
	require.Empty(t, l.persisted().SecretRefRBACNamespaces)

	// An instance appears. Observe must notice, and the action it leads to must provision the Role.
	l.add(generatedInstance("team-a", "u1", "otp-1"))
	drift, observed := l.reconcile()
	require.True(t, drift, "a new secretRef-bearing instance must be drift; Observe said %v", observed)
	require.True(t, containsSubstring(observed, "secretRef secret names changed"),
		"the drift must come from the secretRef digest check; Observe said %v", observed)
	require.Equal(t, []string{"otp-1"}, l.roleNames(roleName, "team-a"),
		"the reconcile that followed the drift must provision a Role naming exactly the referenced Secret")
	st := l.persisted()
	require.Equal(t, []string{"team-a"}, st.SecretRefRBACNamespaces, "the provisioned namespace must be PERSISTED")
	require.NotEqual(t, empty, st.SecretRefDigest)

	// And then quiet: the stored digest must match what Observe recomputes.
	drift, observed = l.reconcile()
	require.False(t, drift, "the loop must be quiet once the grant matches the instances; Observe said %v", observed)

	// The instance goes. Observe must notice, and the action must revoke.
	l.remove(generatedInstance("team-a", "u1", "otp-1"))
	drift, observed = l.reconcile()
	require.True(t, drift, "a removed instance must be drift; Observe said %v", observed)
	require.Nil(t, l.roleNames(roleName, "team-a"), "the Role must be revoked once no instance names the Secret")
	st = l.persisted()
	require.Empty(t, st.SecretRefRBACNamespaces)
	require.Equal(t, empty, st.SecretRefDigest, "with every instance gone the digest must return to the empty set's")

	drift, observed = l.reconcile()
	require.False(t, drift, "the loop must be quiet after revocation; Observe said %v", observed)
}

// Delete is the last pass over a RestDefinition, so whatever it leaves is permanent. It must remove the
// secretRef pair it provisioned, and ONLY that: a Role of the same namespace under another name -- here the
// one rest-dynamic-controller provisions per CR -- is someone else's.
//
// Driven the way it happens: the instances go, the RestDefinition is deleted, and the NEXT reconcile is the
// deletion pass (Observe's WasDeleted branch, which calls Delete). No ordinary reconcile runs in between, so
// the drift-driven revocation cannot be what removes the Role -- only Delete can.
func TestDeleteRemovesTheSecretRefPairAndNothingElse(t *testing.T) {
	foreign := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "pullrequests-v1alpha1-0badc0de-secrets", Namespace: "team-a"}}
	l := newSecretRefLoop(t, foreign)
	roleName := deploy.SecretRefRoleName(targetGVRForTest(l.cr))
	ctx := context.Background()

	l.settle()
	l.add(generatedInstance("team-a", "u1", "otp-1"))
	l.settle()
	require.Equal(t, []string{"otp-1"}, l.roleNames(roleName, "team-a"), "precondition: the grant is provisioned")

	l.remove(generatedInstance("team-a", "u1", "otp-1"))
	cr := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, l.e.kube.Get(ctx, client.ObjectKeyFromObject(l.cr), cr))
	require.NotEmpty(t, cr.Finalizers, "precondition: a finalizer holds the RestDefinition for its deletion pass")
	require.NoError(t, l.e.kube.Delete(ctx, cr))
	require.NoError(t, l.e.kube.Get(ctx, client.ObjectKeyFromObject(l.cr), cr))
	require.False(t, cr.DeletionTimestamp.IsZero(), "precondition: the RestDefinition is being deleted")
	_, _ = l.e.Observe(ctx, cr) // the deletion pass; Undeploy's own outcome is not under test here

	require.Nil(t, l.roleNames(roleName, "team-a"), "Delete must remove the secretRef Role and RoleBinding")
	var still rbacv1.Role
	require.NoError(t, l.e.kube.Get(context.Background(), client.ObjectKeyFromObject(foreign), &still),
		"Delete must not remove a Role it did not provision")
}

// TWO CALLERS, ONE RESOURCE, OVERLAPPING NAMESPACES. The auth grant (from the Configuration) and the
// secretRef grant (from the instances) share SyncAuthSecretRBAC, which revokes every namespace in the
// caller's RECORD that its collected set no longer names. Each caller must therefore pass its own record.
//
// The shape is chosen so a crossed record is observable: the auth record is [shared] only, while the
// secretRef record is [other, shared]. When `other`'s last instance goes, the secretRef sync must revoke
// `other` -- and it can only know to do so from its OWN record. Passing the auth record instead leaves
// `other` granted for ever, with nothing left that would ever remove it.
func TestTheSecretRefGrantRevokesFromItsOwnRecordAndLeavesTheAuthGrantAlone(t *testing.T) {
	l := newSecretRefLoop(t)
	gvr := targetGVRForTest(l.cr)
	secretRefRole, authRole := deploy.SecretRefRoleName(gvr), deploy.AuthSecretRoleName(gvr)

	l.settle()
	l.add(configurationInstance("shared", "cfg", "shared", "auth-s"))
	l.add(generatedInstance("shared", "s1", "otp-s"))
	l.add(generatedInstance("other", "o1", "otp-o"))
	l.settle()

	require.Equal(t, []string{"auth-s"}, l.roleNames(authRole, "shared"))
	require.Equal(t, []string{"otp-s"}, l.roleNames(secretRefRole, "shared"))
	require.Equal(t, []string{"otp-o"}, l.roleNames(secretRefRole, "other"))
	require.Nil(t, l.roleNames(authRole, "other"), "the auth grant must not follow the instances")
	before := l.persisted()
	require.Equal(t, []string{"shared"}, before.AuthSecretRBACNamespaces)
	require.ElementsMatch(t, []string{"shared", "other"}, before.SecretRefRBACNamespaces)
	require.NotEqual(t, before.AuthSecretDigest, before.SecretRefDigest)

	// `other` loses its only instance.
	l.remove(generatedInstance("other", "o1", "otp-o"))
	l.settle()

	require.Nil(t, l.roleNames(secretRefRole, "other"),
		"`other` must be revoked; it is in the secretRef record and nowhere in the auth record")
	require.Equal(t, []string{"otp-s"}, l.roleNames(secretRefRole, "shared"), "`shared` still has an instance")
	require.Equal(t, []string{"auth-s"}, l.roleNames(authRole, "shared"),
		"revoking a secretRef namespace must never touch the auth grant")
	after := l.persisted()
	require.Equal(t, []string{"shared"}, after.SecretRefRBACNamespaces)
	require.Equal(t, before.AuthSecretRBACNamespaces, after.AuthSecretRBACNamespaces)
	require.Equal(t, before.AuthSecretDigest, after.AuthSecretDigest)
}
