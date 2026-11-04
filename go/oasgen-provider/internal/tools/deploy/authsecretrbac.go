package deploy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	hasher "github.com/krateo-platformops/oasgen-provider/internal/tools/hash"
	kubecli "github.com/krateo-platformops/oasgen-provider/internal/tools/kube"
	"github.com/krateo-platformops/plumbing/kubeutil/rbacgen"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// authSecretVerbs are the only verbs granted: reading a Secret's value never requires create/update/delete.
var authSecretVerbs = []string{"get", "list", "watch"}

// authSecretRefPaths are the spec paths, on a Configuration CR instance, of every usernameRef/passwordRef/
// tokenRef the oas2jsonschema security-scheme generator can produce (basic and bearer auth).
var authSecretRefPaths = [][]string{
	{"spec", "authentication", "basic", "usernameRef"},
	{"spec", "authentication", "basic", "passwordRef"},
	{"spec", "authentication", "bearer", "tokenRef"},
}

// AuthSecretRoleName is deterministic per target resource, matching the naming convention the other RDC
// RBAC objects already use (<resource>-<version>), so it can never collide across RestDefinitions.
func AuthSecretRoleName(gvr schema.GroupVersionResource) string {
	return fmt.Sprintf("%s-%s-authsecrets", gvr.Resource, rbacVersion)
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
func SyncAuthSecretRBAC(ctx context.Context, kube client.Client, roleName string, bySecretNamespace map[string][]string, previouslyProvisioned []string, sa types.NamespacedName) ([]string, error) {
	nowProvisioned := make([]string, 0, len(bySecretNamespace))
	for ns, secretNames := range bySecretNamespace {
		role := rbacgen.BuildRoleForSecret(ns, roleName, secretNames, authSecretVerbs)
		if err := kubecli.Apply(ctx, kube, role, kubecli.ApplyOptions{}); err != nil {
			return nil, fmt.Errorf("applying auth-secret Role in namespace %s: %w", ns, err)
		}
		binding := rbacgen.BuildRoleBindingForRole(ns, roleName, roleName, sa)
		if err := kubecli.Apply(ctx, kube, binding, kubecli.ApplyOptions{}); err != nil {
			return nil, fmt.Errorf("applying auth-secret RoleBinding in namespace %s: %w", ns, err)
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
			return nil, fmt.Errorf("removing stale auth-secret RBAC in namespace %s: %w", ns, err)
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
		return fmt.Errorf("deleting auth-secret Role: %w", err)
	}
	binding := &rbacv1.RoleBinding{
		TypeMeta:   metav1.TypeMeta{Kind: "RoleBinding", APIVersion: rbacv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: roleName, Namespace: ns},
	}
	if err := kubecli.Uninstall(ctx, kube, binding, kubecli.UninstallOptions{}); err != nil {
		return fmt.Errorf("deleting auth-secret RoleBinding: %w", err)
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
