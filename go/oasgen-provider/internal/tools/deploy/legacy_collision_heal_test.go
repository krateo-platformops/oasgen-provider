//go:build unit

package deploy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// A LIVE STARTING STATE THAT #475's PREMISE RULED OUT, reported from a real cluster by the
// github-provider-kog session and reproduced here.
//
// #475's write-up argued the ClusterRole narrowing was self-healing because "existing installs already
// have these Roles -- they were provisioned all along, merely redundant". That is FALSE for a pair of
// RestDefinitions that collided on the pre-#440 group-less name:
//
//	RD acme-feedback-form    Form, acmefeedback.example.io    status namespaces [krateo-system]
//	RD event-feedback-form   Form, eventfeedback.example.io   status namespaces [krateo-system]
//
//	Role forms-v1alpha1-authsecrets   resourceNames [acme-feedback-form-credentials]
//	RoleBinding, same name            subject: acme's ServiceAccount ONLY
//
// Both RDs claim the namespace is provisioned. Only one Secret is named, and only one SA is bound. Event's
// credential was reachable solely through the blanket cluster-wide `secrets` grant that #475 removes, so
// for THIS resource the narrowing removes the last working path rather than a redundant one.
//
// WHY THIS IS NOT COVERED BY TestObserveReportsDriftWhenTheAuthSecretRoleIsMissing: that arm starts from
// no Role at all. This one starts from a Role that EXISTS, is bound, and names a Secret -- just not this
// resource's. The two are the same to MissingAuthSecretRBAC only because it looks up the group-qualified
// name and nothing else, and that is precisely the property worth pinning: a future change that made the
// check "tolerant" of the legacy name would silently restore the outage for event-feedback-form.
func TestLegacyCollisionHealsForTheRestDefinitionThatLostTheSharedRole(t *testing.T) {
	const ns = "krateo-system"
	acme := schema.GroupVersionResource{Group: "acmefeedback.example.io", Version: "v1alpha1", Resource: "forms"}
	event := schema.GroupVersionResource{Group: "eventfeedback.example.io", Version: "v1alpha1", Resource: "forms"}

	// The two group-qualified names 0.53.0 looks up. They differ ONLY by group, which is the whole of #440.
	acmeName, eventName := AuthSecretRoleName(acme), AuthSecretRoleName(event)
	require.NotEqual(t, acmeName, eventName,
		"if these collide the fixture cannot represent the bug and this test is vacuous")
	legacyName := legacyAuthSecretRoleName(acme)
	require.Equal(t, legacyName, legacyAuthSecretRoleName(event),
		"the control: the two RDs must share ONE legacy name -- that shared name IS the collision")

	// The live state: one legacy Role, naming acme's Secret only, bound to acme's SA only.
	legacyRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: legacyName, Namespace: ns},
		Rules: []rbacv1.PolicyRule{{
			APIGroups:     []string{""},
			Resources:     []string{"secrets"},
			ResourceNames: []string{"acme-feedback-form-credentials"},
			Verbs:         authSecretVerbs,
		}},
	}
	legacyBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: legacyName, Namespace: ns},
		Subjects: []rbacv1.Subject{{
			Kind: "ServiceAccount", Name: "forms-acmefeedback.example.io-v1alpha1", Namespace: ns,
		}},
	}
	kube := fake.NewClientBuilder().WithScheme(rbacScheme(t)).WithObjects(legacyRole, legacyBinding).Build()
	ctx := context.Background()

	// 1. The check must report event's namespace missing, DESPITE a bound Role existing under the legacy
	//    name in that same namespace. This is the link that makes the heal reachable at all.
	missing, err := MissingAuthSecretRBAC(ctx, kube, eventName, []string{ns})
	require.NoError(t, err)
	require.Equal(t, []string{ns}, missing,
		"a legacy Role naming ANOTHER RestDefinition's Secret must not count as this one's RBAC")

	// 2. acme is in the same position: its group-qualified Role does not exist either, so both RDs drift
	//    once rather than one silently keeping a Role that happens to carry its Secret.
	missingAcme, err := MissingAuthSecretRBAC(ctx, kube, acmeName, []string{ns})
	require.NoError(t, err)
	require.Equal(t, []string{ns}, missingAcme,
		"acme must also report missing -- the legacy name is not the name 0.53.0 provisions or reads")

	// 3. The repair: Update's sync provisions event's own Role naming event's own Secret.
	sa := types.NamespacedName{Namespace: ns, Name: "forms-eventfeedback.example.io-v1alpha1"}
	provisioned, err := SyncAuthSecretRBAC(ctx, kube, eventName,
		map[string][]string{ns: {"event-feedback-form-credentials"}}, []string{ns}, sa)
	require.NoError(t, err)
	require.Equal(t, []string{ns}, provisioned)

	var healed rbacv1.Role
	require.NoError(t, kube.Get(ctx, types.NamespacedName{Name: eventName, Namespace: ns}, &healed))
	require.Len(t, healed.Rules, 1)
	require.Equal(t, []string{"event-feedback-form-credentials"}, healed.Rules[0].ResourceNames,
		"the healed Role must name EVENT's Secret, not the one the shared legacy Role happened to carry")

	var healedBinding rbacv1.RoleBinding
	require.NoError(t, kube.Get(ctx, types.NamespacedName{Name: eventName, Namespace: ns}, &healedBinding))
	require.Len(t, healedBinding.Subjects, 1)
	require.Equal(t, "forms-eventfeedback.example.io-v1alpha1", healedBinding.Subjects[0].Name,
		"and bind EVENT's ServiceAccount")

	// 4. After the repair the check is quiet for event. Without this the assertions above would pass
	//    against an implementation that reports missing forever, which is a reconcile loop, not a heal.
	after, err := MissingAuthSecretRBAC(ctx, kube, eventName, []string{ns})
	require.NoError(t, err)
	require.Empty(t, after, "the heal must settle, not re-report on every pass")

	// 5. Syncing event must NOT disturb acme's legacy objects. Removing them is DeleteLegacyAuthSecretRBAC's
	//    job, called separately and non-fatally; a sync that deleted them here would revoke acme's access
	//    before acme's own reconcile had provisioned its replacement.
	var stillThere rbacv1.Role
	require.NoError(t, kube.Get(ctx, types.NamespacedName{Name: legacyName, Namespace: ns}, &stillThere),
		"event's sync must leave the legacy Role alone; acme still depends on it until acme reconciles")
	require.Equal(t, []string{"acme-feedback-form-credentials"}, stillThere.Rules[0].ResourceNames,
		"and must not rewrite its contents")
}
