// Package secretrbac tears down the per-CR-instance secretRef RBAC that this controller used to
// self-provision (issue #31).
//
// # It no longer provisions anything (#540)
//
// It used to create a namespace-scoped Role naming exactly the Secrets a reconciled resource's secretRef
// fields referenced, via resourceNames, bound to this controller's own ServiceAccount. oasgen-provider
// now writes that grant instead: one Role per generated Kind per namespace, unioned across every live
// instance (#510/#527) and refreshed from a watch on the generated Kind (#539). Two independent grants
// for one read meant adoption could never converge and a first create raced RBAC escalation -- see
// restResources.resolveRequestFields for the measurements.
//
// # Why the teardown survives the provisioning
//
// The names do not overlap:
//
//	this package, per CR instance   <plural>-<version>-<fnv32a(ns/name)>-secrets
//	oasgen-provider, per Kind       <plural>-<group>-<version>-secretrefs
//
// So the provider's revocation path cannot see a Role this package created, and every per-CR Role on a
// cluster that ran 0.52.0 through 0.56.0 would otherwise stay there, granting Secret reads, forever.
// Keeping DeleteSecretRole on every delete path cleans them up as resources churn and makes the change a
// strict reduction in privilege. A Role belonging to a resource that is never deleted still needs one
// sweep per cluster.
//
// RoleName is kept for the same reason: the teardown has to be able to name what an older release wrote,
// so this is the one place that still has to know the old scheme exactly.
package secretrbac

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/krateo-platformops/plumbing/kubeutil/objectclient"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

var (
	roleGVR    = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}
	bindingGVR = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}
)

// RoleName reproduces the name an older release gave a CR instance's secretRef Role:
// <plural>-<version>-<fnv32a(namespace/name)>-secrets. The hash suffix (not the raw CR name) kept the
// result within Kubernetes' 253-char name limit regardless of CR name length, and is recomputed
// identically every reconcile -- there was never a generated name to store or look up.
//
// IT IS KEYED ON THE CR, namespace AND name, so two resources in one namespace had two different Roles.
// Worth stating because the opposite was asserted on #540 for a while: a per-CR Role holding a subset of
// a namespace's referenced Secrets is that Role being correct, not one resource overwriting another's
// grant. Nothing here ever shared a Role between instances.
func RoleName(gvr schema.GroupVersionResource, cr types.NamespacedName) string {
	h := fnv.New32a()
	h.Write([]byte(cr.Namespace + "/" + cr.Name))
	return fmt.Sprintf("%s-%s-%x-secrets", gvr.Resource, gvr.Version, h.Sum32())
}

// DeleteSecretRole tears down the Role+RoleBinding pair named roleName in namespace ns. It is
// unconditional and IsNotFound-tolerant: teardown never depends on whether the pair was ever
// provisioned, which from #540 onwards is the normal case.
func DeleteSecretRole(ctx context.Context, dyn dynamic.Interface, ns, roleName string) error {
	if _, err := objectclient.Uninstall(ctx, dyn, bindingGVR, ns, roleName, objectclient.UninstallOptions{}); err != nil {
		return fmt.Errorf("deleting secretRef RBAC RoleBinding %s/%s: %w", ns, roleName, err)
	}
	if _, err := objectclient.Uninstall(ctx, dyn, roleGVR, ns, roleName, objectclient.UninstallOptions{}); err != nil {
		return fmt.Errorf("deleting secretRef RBAC Role %s/%s: %w", ns, roleName, err)
	}
	return nil
}
