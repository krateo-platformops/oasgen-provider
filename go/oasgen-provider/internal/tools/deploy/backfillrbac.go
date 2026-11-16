package deploy

import (
	"context"
	"fmt"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubecli "github.com/krateo-platformops/oasgen-provider/internal/tools/kube"
)

// BackfillAggregationLabel is the label a ClusterRole must carry to be aggregated into the provider's
// own backfill ClusterRole.
//
// THE CHART OWNS THE BINDING AND THIS PACKAGE OWNS ONLY RULES, which is the whole point of using
// aggregation here rather than provisioning a ClusterRoleBinding (#717).
//
// The provider needs `patch` on kinds it generates -- their API group comes from each RestDefinition's
// spec.resourceGroup and therefore cannot be enumerated in a chart. The chart's own wildcard rule is
// `apiGroups: ["*"], resources: ["*"], verbs: [get, list, watch]`: read-only by design, so the
// oas-version backfill (which selects on label ABSENCE and then PATCHES) is refused on every generated
// kind. A Configuration written once at install, before its group's MutatingAdmissionPolicy existed, is
// therefore permanently unlabelled, permanently selected and permanently refused.
//
// Three things make aggregation the right mechanism rather than a clever one:
//
//  1. The provider never names a SUBJECT. It publishes a ClusterRole carrying rules and a label; the
//     controller-manager merges those rules into the chart's aggregated ClusterRole, which the chart
//     binds to the provider's own ServiceAccount. So no bug here can grant anything to anyone else --
//     the worst case is granting the provider a patch it did not need.
//  2. It needs no self-identity. A ClusterRoleBinding would require the provider to know its own
//     ServiceAccount name and namespace, which is not available from the pod without either a chart-set
//     environment variable (one more value that can be wrong, absent, or materialised by installer's
//     schema over the chart's) or a SelfSubjectReview round trip.
//  3. The binding stays auditable in the release. An operator reading the chart sees exactly which
//     ServiceAccount can grow permissions this way, even though the rules arrive later.
//
// Kubernetes guarantees the merge direction we rely on, in its own words
// (k8s.io/api/rbac/v1, ClusterRole.AggregationRule):
//
//	"If AggregationRule is set, then the Rules are controller managed and direct changes to Rules
//	 will be stomped by the controller."
//
// So the aggregated ClusterRole in the chart deliberately ships with NO rules of its own: anything
// written there would be overwritten, and anyone adding a rule to it later needs to know that.
const BackfillAggregationLabel = "ogen.krateo.io/aggregate-to-backfill"

// backfillVerbs is exactly what the backfill does and nothing more.
//
// NOT `update`, and the difference is not cosmetic: the backfill issues a MERGE PATCH carrying only the
// one label, specifically so it cannot conflict with whatever else writes the object. `update` would
// authorise a whole-object replacement this code never performs, and would make the grant larger than
// the operation it exists for.
//
// NOT `get` or `list` either, even though the backfill lists: those are already covered cluster-wide by
// the chart's read rule, and restating them here would make this ClusterRole look like the authority for
// a read it does not actually gate. A grant that duplicates a wider one reads as a control while
// restricting nothing -- the mistake 0.54.0's blanket `secrets` rule made.
var backfillVerbs = []string{"patch"}

// BackfillClusterRoleName is the aggregated-into ClusterRole provisioned for one RestDefinition's
// generated kinds.
//
// NAMED WITHOUT A VERSION, like the RDC RBAC set and for the same reason: the RULES grant on
// group+resource, never on a version, so a name carrying the derived version would orphan the previous
// version's ClusterRole on every CRD version bump -- and a teardown running after the OAS had gone
// (where the version falls back) would then silently fail to delete the real one.
func BackfillClusterRoleName(gvr schema.GroupVersionResource) string {
	return fmt.Sprintf("oasgen-backfill-%s-%s", gvr.Resource, gvr.Group)
}

// BuildBackfillClusterRole returns the labelled ClusterRole granting `patch` on resource and, when it is
// non-empty, configurationResource -- both in group.
//
// configurationResource is empty for a RestDefinition that generates no Configuration CRD (no
// configuration fields and no security schemes). It is NOT an error: there is simply nothing of that kind
// to label, which is the same condition getConfigurationGVR already encodes for the backfill itself.
func BuildBackfillClusterRole(group, resource, configurationResource string) *rbacv1.ClusterRole {
	resources := []string{resource}
	if configurationResource != "" {
		resources = append(resources, configurationResource)
	}
	return &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ClusterRole",
			APIVersion: rbacv1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: BackfillClusterRoleName(schema.GroupVersionResource{Group: group, Resource: resource}),
			Labels: map[string]string{
				BackfillAggregationLabel: "true",
			},
		},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{group},
			Resources: resources,
			Verbs:     backfillVerbs,
		}},
	}
}

// SyncBackfillRBAC applies the labelled ClusterRole for one RestDefinition's generated kinds.
//
// Idempotent and cheap: one Apply of a three-line object, on the same reconcile that runs the backfill.
// Provisioning it next to its only consumer rather than at deploy time is deliberate -- the grant and the
// operation it authorises then cannot drift apart, which is the failure mode that produced #717 in the
// first place (a backfill whose scope was widened by #180 while the grant was not).
//
// A FRESH GRANT DOES NOT TAKE EFFECT INSTANTLY, and callers must tolerate that. The apiserver's
// authorizer caches RBAC, so the backfill on THIS pass may still be refused after a successful Apply
// here. That is not a failure state: the next pass succeeds, and a caller treating the first 403 after
// provisioning as terminal would latch a condition that is about to become false.
func SyncBackfillRBAC(ctx context.Context, kube client.Client, group, resource, configurationResource string) error {
	role := BuildBackfillClusterRole(group, resource, configurationResource)
	if _, err := kubecli.Apply(ctx, kube, role, kubecli.ApplyOptions{}); err != nil {
		return fmt.Errorf("applying backfill ClusterRole %q: %w", role.Name, err)
	}
	return nil
}

// DeleteBackfillRBAC removes the ClusterRole provisioned by SyncBackfillRBAC.
//
// IsNotFound-tolerant via kubecli.Uninstall, so it is safe for a RestDefinition whose grant was never
// provisioned or has already been removed.
func DeleteBackfillRBAC(ctx context.Context, kube client.Client, group, resource string) error {
	role := &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ClusterRole",
			APIVersion: rbacv1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: BackfillClusterRoleName(schema.GroupVersionResource{Group: group, Resource: resource}),
		},
	}
	if _, err := kubecli.Uninstall(ctx, kube, role, kubecli.UninstallOptions{}); err != nil {
		return fmt.Errorf("removing backfill ClusterRole %q: %w", role.Name, err)
	}
	return nil
}
