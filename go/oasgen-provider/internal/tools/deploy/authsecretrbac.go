package deploy

import (
	"context"
	"errors"
	"fmt"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/oas2jsonschema"
	"sort"
	"strings"

	hasher "github.com/krateo-platformops/oasgen-provider/internal/tools/hash"
	kubecli "github.com/krateo-platformops/oasgen-provider/internal/tools/kube"
	"github.com/krateo-platformops/plumbing/kubeutil/rbacgen"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// authSecretVerbs are the only verbs granted: reading a Secret's value never requires create/update/delete.
var authSecretVerbs = []string{"get", "list", "watch"}

// authSecretRefPaths are the spec paths, on a Configuration CR instance, of every Secret reference the
// oas2jsonschema security-scheme generator can produce.
//
// IT IS NO LONGER A LIST HERE, and that is the fix for #500. This was three hand-written paths covering
// basic's two refs and bearer's one, with a comment that accurately said "(basic and bearer auth)" -- while
// the generator produces a THIRD shape, apiKey in a header, whose tokenRef was never added. The comment
// described the implementation correctly and neither described nor matched the generator.
//
// Latent until 0.54.0: the shipped RDC ClusterRole granted `resources: ["secrets"]` cluster-wide, so an
// incomplete narrow Role cost nothing. #473/#475 removed that grant because the narrow Role "already
// covered the same ground" -- which was true for basic and bearer and never true for apiKey. The release
// could not reconcile any apiKey-authenticated resource.
//
// Deriving the paths from oas2jsonschema means the two can no longer disagree: see AuthSecretRefPaths for
// why a derivation rather than another fixture.
var authSecretRefPaths = oas2jsonschema.AuthSecretRefPaths()

// AuthSecretRoleName is deterministic per target resource and INCLUDES THE API GROUP, matching the naming
// convention the shipped RDC RBAC templates use -- `{{ .resource }}-{{ .apiGroup }}-{{ .apiVersion }}` in
// all five of helm/oasgen-provider/assets/rdc/rbac/.
//
// THE GROUP IS WHAT MAKES IT COLLISION-FREE, and it used to be accepted as a parameter and discarded
// (#440). The old key was `<resource>-<rbacVersion>-authsecrets`, and its comment claimed that being
// deterministic meant it "can never collide across RestDefinitions" -- which is inverted. Determinism over
// a PARTIAL key is what makes a collision possible. The same comment also claimed to match the convention
// it was dropping the group from.
//
// Two RestDefinitions sharing a resource plural across groups are admissible: ownership is enforced per
// GROUP+KIND (crd/apply.go's OwnerAnnotation -- "a CRD for a given group+kind is managed by exactly one
// LIVE RestDefinition"), and nothing at admission constrains the group/plural pair. Namespace was no
// protection either, because these objects live in the SECRET's namespace, cross-namespace by design.
//
// With the old key the two collided on one name, and each reconcile applied a Role whose resourceNames
// listed only its own secrets with a RoleBinding subject of only its own ServiceAccount -- so the grant
// flapped, and deleting either RestDefinition revoked the other's credential access by name.
//
// rbacVersion stays the fixed version rather than the target CRD version, for the reason deploy.go gives:
// it decouples these names from a derived version so a teardown cannot miss them.
func AuthSecretRoleName(gvr schema.GroupVersionResource) string {
	return fmt.Sprintf("%s-%s-%s-authsecrets", gvr.Resource, gvr.Group, rbacVersion)
}

// legacyAuthSecretRoleName returns the pre-#440 name, which omitted the API group.
//
// MIGRATION ONLY, AND DELIBERATELY TEMPORARY. Existing clusters carry Role/RoleBinding pairs under this
// name; after the rename nothing would ever delete them, and they would persist with stale resourceNames
// granting a since-deleted ServiceAccount read access to real secrets. DeleteLegacyAuthSecretRBAC removes
// them. Both can be deleted once no supported upgrade path starts from a release older than the one
// carrying #440.
func legacyAuthSecretRoleName(gvr schema.GroupVersionResource) string {
	return fmt.Sprintf("%s-%s-authsecrets", gvr.Resource, rbacVersion)
}

// DeleteLegacyAuthSecretRBAC removes any pre-#440 auth-secret RBAC left under the group-less name.
//
// SEPARATE FROM SyncAuthSecretRBAC on purpose. This is a migration, not part of steady-state
// reconciliation, and keeping it out of Sync means Sync's contract does not acquire a parameter that stops
// meaning anything once the migration is retired -- the whole function can be deleted in one commit.
//
// Best-effort and error-collecting, like DeleteAllAuthSecretRBAC: a namespace whose legacy object cannot
// be removed must not stop the others, and the caller logs rather than failing the reconcile, because an
// orphaned legacy Role grants access to a ServiceAccount that no longer exists.
//
// ONE HAZARD WORTH NAMING, and the earlier version of this comment described it wrongly: where two
// RestDefinitions previously collided on the legacy name, the first to reconcile deletes the shared
// object. The first then creates its own group-qualified name on the SAME pass -- but the second does not.
// They are separate RestDefinitions with separate reconciles, so the second's Role arrives on ITS next
// Create or Update, and until then its RDC has no read access to its credential.
//
// That window used to be unbounded: Observe's auth-secret check compared a digest of the secret
// REFERENCES, which had not changed, so Observe reported up-to-date and Update -- the only caller of the
// sync -- never ran. #473 closes it by giving Observe an existence check on the Role itself
// (MissingAuthSecretRBAC above), so the second RestDefinition's next reconcile sees the absence and
// reprovisions. The window is now one reconcile, which is what this comment originally claimed.
//
// There is still no ordering that avoids the window entirely without leaving the collision in place.
func DeleteLegacyAuthSecretRBAC(ctx context.Context, kube client.Client, gvr schema.GroupVersionResource, namespaces []string) error {
	legacy := legacyAuthSecretRoleName(gvr)
	if legacy == AuthSecretRoleName(gvr) {
		// Defensive: if the two ever coincide, deleting the legacy name would delete the live one.
		return nil
	}
	var errs []error
	for _, ns := range namespaces {
		if err := DeleteAuthSecretRBAC(ctx, kube, legacy, ns); err != nil {
			errs = append(errs, fmt.Errorf("namespace %q: %w", ns, err))
		}
	}
	return errors.Join(errs...)
}

// SelfServiceAccountName returns RDC's own ServiceAccount identity for the given RestDefinition/target GVR
// — the RoleBinding subject the auth-secret Role/RoleBinding pair must bind. It renders createRBACResources'
// own serviceaccount.yaml template (the same one Deploy/Undeploy/Lookup use) rather than re-deriving the
// naming convention independently, so this can never drift from the actual granted ServiceAccount's name.
func SelfServiceAccountName(gvr schema.GroupVersionResource, namespacedName types.NamespacedName, rbacFolderPath string) (types.NamespacedName, error) {
	sa, _, _, _, _, err := createRBACResources(gvr, namespacedName, schema.GroupVersionResource{}, rbacFolderPath)
	if err != nil {
		return types.NamespacedName{}, err
	}
	return types.NamespacedName{Namespace: sa.Namespace, Name: sa.Name}, nil
}

// CollectAuthSecretRefs lists every instance of the Configuration Kind identified by configurationGVK and
// extracts the distinct {namespace: [secret names]} it references via usernameRef/passwordRef/tokenRef.
// Each SecretKeySelector carries its OWN namespace (provider-runtime's SecretKeySelector documents this as
// "a reference to a secret key in an arbitrary namespace" — cross-namespace is the intended design, not an
// edge case), so the result may span more namespaces than the RestDefinition's own.
//
// Returns (nil, nil) when configurationGVK's Kind is empty (no Configuration CRD exists for this
// RestDefinition — no security schemes, no configuration fields) or when the Configuration CRD is not yet
// installed (mirrors manageFinalizers' own tolerance for this exact error class).
func CollectAuthSecretRefs(ctx context.Context, kube client.Client, configurationGVK schema.GroupVersionKind) (map[string][]string, error) {
	if configurationGVK.Kind == "" {
		return nil, nil
	}

	list := unstructured.UnstructuredList{}
	list.SetGroupVersionKind(configurationGVK)
	if err := kube.List(ctx, &list); err != nil {
		if strings.Contains(err.Error(), "no matches for") || strings.Contains(err.Error(), "the server could not find the requested resource") {
			return nil, nil
		}
		return nil, fmt.Errorf("listing %s instances: %w", configurationGVK.Kind, err)
	}

	byNamespace := map[string]map[string]struct{}{}
	for i := range list.Items {
		for _, path := range authSecretRefPaths {
			ref, found, err := unstructured.NestedMap(list.Items[i].Object, path...)
			if err != nil || !found {
				continue
			}
			ns, _, _ := unstructured.NestedString(ref, "namespace")
			name, _, _ := unstructured.NestedString(ref, "name")
			if name == "" {
				continue
			}
			if byNamespace[ns] == nil {
				byNamespace[ns] = map[string]struct{}{}
			}
			byNamespace[ns][name] = struct{}{}
		}
	}

	out := make(map[string][]string, len(byNamespace))
	for ns, names := range byNamespace {
		sorted := make([]string, 0, len(names))
		for n := range names {
			sorted = append(sorted, n)
		}
		sort.Strings(sorted)
		out[ns] = sorted
	}
	return out, nil
}

// AuthSecretRefsDigest returns a deterministic hash of refs (namespace -> secret names), for Observe-time
// drift detection against RestDefinitionStatus.AuthSecretDigest. Deterministic across calls regardless of
// Go's randomized map iteration order: namespace keys are hashed in sorted order.
func AuthSecretRefsDigest(refs map[string][]string) (string, error) {
	h := hasher.NewFNVObjectHash()
	namespaces := make([]string, 0, len(refs))
	for ns := range refs {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)
	for _, ns := range namespaces {
		if err := h.SumHash(ns, refs[ns]); err != nil {
			return "", err
		}
	}
	return h.GetHash(), nil
}

// SyncAuthSecretRBAC provisions a Role+RoleBinding pair named roleName (resourceNames-scoped to exactly
// bySecretNamespace's secret names, verbs get/list/watch) in every namespace present in bySecretNamespace,
// binding sa. It then removes the pair from every namespace in previouslyProvisioned that is no longer
// present in bySecretNamespace, and returns the new complete set of provisioned namespaces — callers store
// this back on the RestDefinition's status so the next call knows what to clean up.
// NAMING NOTE. These errors name the ROLE rather than saying "auth-secret", because this function is
// shared: #510 added a second caller that provisions the secretRef Role through it. Hardcoding the auth
// noun sent an operator debugging a secretRef grant to the auth path -- a consumer found that by reading
// the code before running it. The role name is also what a test can assert against.
func SyncAuthSecretRBAC(ctx context.Context, kube client.Client, roleName string, bySecretNamespace map[string][]string, previouslyProvisioned []string, sa types.NamespacedName) ([]string, error) {
	nowProvisioned := make([]string, 0, len(bySecretNamespace))
	for ns, secretNames := range bySecretNamespace {
		role := rbacgen.BuildRoleForSecret(ns, roleName, secretNames, authSecretVerbs)
		if err := kubecli.Apply(ctx, kube, role, kubecli.ApplyOptions{}); err != nil {
			return nil, fmt.Errorf("applying Role %q in namespace %s: %w", roleName, ns, err)
		}
		binding := rbacgen.BuildRoleBindingForRole(ns, roleName, roleName, sa)
		if err := kubecli.Apply(ctx, kube, binding, kubecli.ApplyOptions{}); err != nil {
			return nil, fmt.Errorf("applying RoleBinding %q in namespace %s: %w", roleName, ns, err)
		}
		nowProvisioned = append(nowProvisioned, ns)
	}
	sort.Strings(nowProvisioned)

	current := make(map[string]struct{}, len(nowProvisioned))
	for _, ns := range nowProvisioned {
		current[ns] = struct{}{}
	}
	for _, ns := range previouslyProvisioned {
		if _, ok := current[ns]; ok {
			continue
		}
		if err := DeleteAuthSecretRBAC(ctx, kube, roleName, ns); err != nil {
			return nil, fmt.Errorf("removing stale RBAC %q in namespace %s: %w", roleName, ns, err)
		}
	}
	return nowProvisioned, nil
}

// DeleteAuthSecretRBAC tears down the Role+RoleBinding pair named roleName in namespace ns.
// IsNotFound-tolerant (via kubecli.Uninstall): safe to call for a namespace whose RBAC was never
// provisioned or already removed.
func DeleteAuthSecretRBAC(ctx context.Context, kube client.Client, roleName, ns string) error {
	role := &rbacv1.Role{
		TypeMeta:   metav1.TypeMeta{Kind: "Role", APIVersion: rbacv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: roleName, Namespace: ns},
	}
	if err := kubecli.Uninstall(ctx, kube, role, kubecli.UninstallOptions{}); err != nil {
		return fmt.Errorf("deleting Role %q: %w", roleName, err)
	}
	binding := &rbacv1.RoleBinding{
		TypeMeta:   metav1.TypeMeta{Kind: "RoleBinding", APIVersion: rbacv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: roleName, Namespace: ns},
	}
	if err := kubecli.Uninstall(ctx, kube, binding, kubecli.UninstallOptions{}); err != nil {
		return fmt.Errorf("deleting RoleBinding %q: %w", roleName, err)
	}
	return nil
}

// DeleteAllAuthSecretRBAC tears down the Role+RoleBinding pair named roleName in every namespace in
// namespaces (the full set on the RestDefinition's status), used on RestDefinition delete.
// DeleteAllAuthSecretRBAC removes the per-namespace auth-secret grants, and COLLECTS failures rather than
// returning on the first.
//
// It used to return on the first error, so one namespace failing left every LATER namespace entirely
// untouched -- and because the caller is in Delete, nothing retries and nothing records what was skipped.
// Fail-fast is right when the caller can try again; here there is no second attempt, so the only useful
// behaviour is to clean everything cleanable and report the whole set that was not.
//
// errors.Join keeps every namespace's error in the message, which matters because the caller's Warn is the
// only durable channel: a message naming one namespace out of five would understate the residue in exactly
// the way the old Debug level did.
func DeleteAllAuthSecretRBAC(ctx context.Context, kube client.Client, roleName string, namespaces []string) error {
	var errs []error
	for _, ns := range namespaces {
		if err := DeleteAuthSecretRBAC(ctx, kube, roleName, ns); err != nil {
			errs = append(errs, fmt.Errorf("namespace %q: %w", ns, err))
		}
	}
	return errors.Join(errs...)
}

// MissingAuthSecretRBAC returns the namespaces, among those given, where the auth-secret Role or its
// RoleBinding is absent. Both are checked because SyncAuthSecretRBAC applies both and either one missing
// is enough to break the grant.
//
// WHY THIS EXISTS (#473). Observe's auth-secret drift check compares a digest of the collected secret
// REFERENCES. That channel answers "did the references change" and cannot express "does the Role exist", so
// a Role deleted or never provisioned left Observe reporting ResourceUpToDate and Update never running --
// and syncAuthSecretRBAC is reachable only from Create and Update. The repair was unreachable except
// through an unrelated change to the spec, the OAS or the references.
//
// That was unobservable rather than harmful only because the shipped RDC ClusterRole granted `secrets`
// cluster-wide, so the narrow Role restricted nothing. #473 removes that grant, which is exactly what makes
// this check load-bearing: with the blanket grant gone, a missing Role IS a credential outage.
//
// NOT IsNotFound-TOLERANT IN THE WAY THE DELETERS ARE. A NotFound here is the signal, not a condition to
// swallow; any OTHER error is returned, because reporting "missing" on a transient API failure would flap a
// healthy resource into Creating -- the same reason Observe's other checks treat a failed evaluation as
// "not drift" rather than as drift.
func MissingAuthSecretRBAC(ctx context.Context, kube client.Client, roleName string, namespaces []string) ([]string, error) {
	var missing []string
	for _, ns := range namespaces {
		key := types.NamespacedName{Name: roleName, Namespace: ns}

		var role rbacv1.Role
		switch err := kube.Get(ctx, key, &role); {
		case apierrors.IsNotFound(err):
			missing = append(missing, ns)
			continue
		case err != nil:
			return nil, fmt.Errorf("getting Role %q in namespace %s: %w", roleName, ns, err)
		}

		var binding rbacv1.RoleBinding
		switch err := kube.Get(ctx, key, &binding); {
		case apierrors.IsNotFound(err):
			missing = append(missing, ns)
		case err != nil:
			return nil, fmt.Errorf("getting RoleBinding %q in namespace %s: %w", roleName, ns, err)
		}
	}
	sort.Strings(missing)
	return missing, nil
}
