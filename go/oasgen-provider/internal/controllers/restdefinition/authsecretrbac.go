package restdefinition

import (
	"context"
	"errors"
	"fmt"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/deploy"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// syncAuthSecretRBAC collects every Configuration instance's usernameRef/passwordRef/tokenRef, provisions/
// refreshes RDC's per-namespace auth-secret RBAC to match, and updates cr.Status.AuthSecretRBACNamespaces/
// AuthSecretDigest in place. Called from Create and Update, after deploy.Deploy has provisioned RDC's own
// ServiceAccount (so SelfServiceAccountName can resolve it).
func (e *external) syncAuthSecretRBAC(ctx context.Context, cr *definitionv1alpha1.RestDefinition, gvr schema.GroupVersionResource) error {
	configurationGVK := getConfigurationGVK(cr)

	refs, err := deploy.CollectAuthSecretRefs(ctx, e.kube, configurationGVK)
	if err != nil {
		return fmt.Errorf("collecting auth secret references: %w", err)
	}

	selfSA, err := deploy.SelfServiceAccountName(gvr, types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name}, RDCrbacConfigFolder)
	if err != nil {
		return fmt.Errorf("resolving RDC's own ServiceAccount identity: %w", err)
	}

	roleName := deploy.AuthSecretRoleName(gvr)
	provisioned, err := deploy.SyncAuthSecretRBAC(ctx, e.kube, roleName, refs, cr.Status.AuthSecretRBACNamespaces, selfSA)
	if err != nil {
		return fmt.Errorf("syncing auth secret RBAC: %w", err)
	}

	// MIGRATION (#440), and deliberately AFTER the sync above. The auth-secret RBAC name gained the API
	// group, because without it two RestDefinitions sharing a resource plural across groups collided on
	// one name and revoked each other's credential access. Nothing would ever delete the objects left
	// under the old group-less name, and an orphan there grants read access on real secrets to a
	// ServiceAccount that no longer exists.
	//
	// AFTER rather than before, so a failure cannot leave the resource with neither name provisioned: the
	// group-qualified objects exist by this point, so the worst case is a leftover orphan rather than an
	// RDC with no access at all.
	//
	// NON-FATAL, at Warn, and the asymmetry with the fatal sync above is deliberate. A legacy orphan is a
	// latent grant to a deleted identity; failing the reconcile over it would block the sync that is
	// actively fixing the collision, which is the worse outcome. Union of the namespaces, because a
	// namespace that is no longer provisioned may still carry a legacy object.
	legacyNamespaces := unionNamespaces(cr.Status.AuthSecretRBACNamespaces, provisioned)
	if lerr := deploy.DeleteLegacyAuthSecretRBAC(ctx, e.kube, gvr, legacyNamespaces); lerr != nil {
		e.log.Warn("Removing pre-#440 group-less auth-secret RBAC failed (non-fatal); an orphaned Role may "+
			"still grant secret access to a deleted ServiceAccount",
			"error", lerr, "namespaces", legacyNamespaces)
	}

	cr.Status.AuthSecretRBACNamespaces = provisioned

	digest, err := deploy.AuthSecretRefsDigest(refs)
	if err != nil {
		return fmt.Errorf("hashing auth secret references: %w", err)
	}
	cr.Status.AuthSecretDigest = digest
	return nil
}

// teardownAuthSecretRBAC removes RDC's auth-secret RBAC from every namespace recorded on the
// RestDefinition's status. Called from Delete; unconditional and IsNotFound-tolerant (via
// deploy.DeleteAuthSecretRBAC), so it is safe even if some or all of it was never provisioned.
func (e *external) teardownAuthSecretRBAC(ctx context.Context, cr *definitionv1alpha1.RestDefinition, gvr schema.GroupVersionResource) error {
	if len(cr.Status.AuthSecretRBACNamespaces) == 0 {
		return nil
	}
	roleName := deploy.AuthSecretRoleName(gvr)
	err := deploy.DeleteAllAuthSecretRBAC(ctx, e.kube, roleName, cr.Status.AuthSecretRBACNamespaces)
	// Also take the pre-#440 group-less name (#440). A RestDefinition deleted before it ever re-synced
	// under the new name still has legacy objects recorded against these namespaces, and Delete is the
	// last chance to remove them -- nothing reconciles the resource again afterwards.
	if lerr := deploy.DeleteLegacyAuthSecretRBAC(ctx, e.kube, gvr, cr.Status.AuthSecretRBACNamespaces); lerr != nil {
		err = errors.Join(err, fmt.Errorf("removing pre-#440 group-less auth-secret RBAC: %w", lerr))
	}
	return err
}

// unionNamespaces returns the distinct namespaces across both slices, order-stable on the first.
//
// Both are needed for the legacy sweep: `provisioned` is where RBAC exists now, and the status list is
// where it existed before -- a namespace that has dropped out of the refs still carries a legacy object
// that nothing else will ever revisit.
func unionNamespaces(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, group := range [][]string{a, b} {
		for _, ns := range group {
			if !seen[ns] {
				seen[ns] = true
				out = append(out, ns)
			}
		}
	}
	return out
}
