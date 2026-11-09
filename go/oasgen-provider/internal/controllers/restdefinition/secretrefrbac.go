package restdefinition

import (
	"context"
	"fmt"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/deploy"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/text"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// syncSecretRefRBAC collects every GENERATED-Kind instance's secretRef resolver targets, provisions/refreshes
// RDC's per-namespace RBAC to match, and updates cr.Status.SecretRefRBACNamespaces in place.
//
// WHY THIS EXISTS (#510). 0.54.0 removed a blanket secrets grant on the grounds that the narrow per-Secret
// Role covered it. It did not: the narrow Role is built from Configuration-level auth refs only, so every
// Secret named by a secretRef fieldMapping resolver lost its grant, and 25 of a consumer's 36 kinds stopped
// reconciling. This is the second collector that the narrow Role always needed.
//
// SEPARATE ROLE NAME AND SEPARATE STATUS FIELD, both load-bearing. SyncAuthSecretRBAC deletes the Role in
// every namespace that is in the caller's recorded set and no longer in its collected set -- so two callers
// sharing either the name or the record delete each other's namespaces on every reconcile. See
// deploy.SecretRefRoleName and Status.SecretRefRBACNamespaces.
//
// Called from Create and Update beside syncAuthSecretRBAC, after deploy.Deploy has provisioned RDC's own
// ServiceAccount, so SelfServiceAccountName can resolve it.
func (e *external) syncSecretRefRBAC(ctx context.Context, cr *definitionv1alpha1.RestDefinition, gvr schema.GroupVersionResource) error {
	// The GENERATED Kind, not the Configuration Kind. secretRef paths address a CR instance of the kind this
	// RestDefinition generates, which is why a per-RestDefinition collector cannot size this grant: the names
	// live in the CRs.
	generatedGVK := secretRefGeneratedGVK(cr, gvr)

	refs, err := deploy.CollectSecretRefNamesForAll(ctx, e.kube, generatedGVK, cr.Spec.Resource.VerbsDescription, e.log)
	if err != nil {
		return fmt.Errorf("collecting secretRef secret names: %w", err)
	}

	selfSA, err := deploy.SelfServiceAccountName(gvr, types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name}, RDCrbacConfigFolder)
	if err != nil {
		return fmt.Errorf("resolving RDC's own ServiceAccount identity: %w", err)
	}

	roleName := deploy.SecretRefRoleName(gvr)
	provisioned, err := deploy.SyncAuthSecretRBAC(ctx, e.kube, roleName, refs, cr.Status.SecretRefRBACNamespaces, selfSA)
	if err != nil {
		return fmt.Errorf("syncing secretRef RBAC: %w", err)
	}

	cr.Status.SecretRefRBACNamespaces = provisioned

	digest, err := deploy.AuthSecretRefsDigest(refs)
	if err != nil {
		return fmt.Errorf("hashing secretRef secret names: %w", err)
	}
	cr.Status.SecretRefDigest = digest
	return nil
}

// teardownSecretRefRBAC removes the secretRef RBAC from every namespace recorded on the RestDefinition's
// status. Called from Delete, beside teardownAuthSecretRBAC.
//
// NOT OPTIONAL, and not symmetry for its own sake. Delete is the LAST pass over this resource: nothing
// reconciles it again, so a Role left behind here is permanent. It would grant read access on real Secrets
// to a ServiceAccount that no longer exists -- the same hazard #440 describes for the auth path, which is
// why that path takes its legacy name here too. There is no legacy name for this one: it is new, so the
// group-qualified name is the only one it has ever had.
func (e *external) teardownSecretRefRBAC(ctx context.Context, cr *definitionv1alpha1.RestDefinition, gvr schema.GroupVersionResource) error {
	if len(cr.Status.SecretRefRBACNamespaces) == 0 {
		return nil
	}
	roleName := deploy.SecretRefRoleName(gvr)
	return deploy.DeleteAllAuthSecretRBAC(ctx, e.kube, roleName, cr.Status.SecretRefRBACNamespaces)
}

// secretRefGeneratedGVK is the GVK of the Kind this RestDefinition generates. Shared by syncSecretRefRBAC
// and Observe's drift check so the two cannot disagree about which kind to collect from -- if they did, the
// digest would be computed over one set and the Role provisioned from another, and the resource would report
// drift on every reconcile for ever.
// CapitaliseFirstLetter, like every other generated-GVK construction in this package (see the Kind passed
// to plurals.ToGroupVersionResource at restdefinition.go:354). spec.resource.kind is author-written and is
// NOT validated for case, so a definition saying `kind: user` yields a CRD serving `User`. Using the raw
// value here made the lister ask for a Kind the apiserver does not serve: zero instances, zero names, no
// Role -- #510's symptom reproduced silently by the fix for #510. A reviewer found this by checking my new
// call against the existing convention rather than against the symptom.
func secretRefGeneratedGVK(cr *definitionv1alpha1.RestDefinition, gvr schema.GroupVersionResource) schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   gvr.Group,
		Version: gvr.Version,
		Kind:    text.CapitaliseFirstLetter(cr.Spec.Resource.Kind),
	}
}
