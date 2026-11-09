package restResources

import (
	"context"
	"fmt"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/fieldmapping"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/secretrbac"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// resolveRequestFields runs this CR instance's dependent lookups and resolves callInfo's FieldMapping
// resolver entries against mg, returning the map BuildCallConfig needs to finish assembling the request.
//
// # This function used to self-provision RBAC, and no longer does (#540)
//
// It was `ensureSecretRefRBACAndResolve`: before resolving, it created a per-CR-instance Role and
// RoleBinding naming exactly the Secrets this resource's secretRef fields referenced, bound to this
// controller's own ServiceAccount (#31). That was the only grant letting the read succeed, so it had to
// exist before the read. **oasgen-provider now provisions the grant** — one Role per generated Kind per
// namespace, unioned across every live instance, revoked when a namespace's last instance goes away
// (#510, shipped in 0.56.0; refreshed from a watch on the generated Kind in #539).
//
// Three symptoms went with the self-provisioning, two of them measured on consumers' clusters:
//
//	ADOPTION NEVER CONVERGED for any secretRef kind, at any version since at least 0.52.0. The Update
//	path demanded a Role that only Create creates, and an adopted resource never ran Create -- so
//	findby adopted correctly, Observe reported drift correctly, and Update then hard-failed forever
//	with "secretRef RBAC Role ... is missing but was expected to already exist". Pinning back did not
//	escape it: `expectExisting` was false/true/false at Create/Update/Delete identically at 0.52.0,
//	0.54.1, 0.55.0 and 0.56.0.
//
//	AN ESCALATION ORDERING RACE on the first create. RBAC escalation prevention only lets this
//	controller apply a Role for permissions it already holds, which is true only once the provider's
//	Role has granted that Secret -- so a create could fail once, forbidden, and succeed later (#526).
//
//	A grant computed by a DIFFERENT WALKER from the provider's. Both sides parse a path with the same
//	code since #513, but this module walked the flattened form while the provider walks typed
//	segments. Two per-CR grants, each internally consistent, hid the disagreement. That is why the
//	walker move came FIRST, in its own change, with a differential test over both.
//
// # What is deliberately NOT removed: the teardown
//
// `deleteSecretRefRBAC` stays and still runs on every delete path. The per-CR Roles already on
// consumers' clusters are not named like the provider's -- `<plural>-<version>-<fnv(ns/name)>-secrets`
// against the provider's `<plural>-<group>-<version>-secretrefs` -- so the provider will never revoke
// them. Keeping the teardown means they are cleaned up as resources churn, and the change is a strict
// reduction in privilege rather than a swap. What it does NOT clean up is a Role belonging to a resource
// that is never deleted; that needs one sweep per cluster, which the release notes must say.
//
// Dependent lookups are executed HERE, not at the call sites: this is the one choke point every
// request-building path already passes through. A lookup run at the call sites instead would be three
// copies to keep in step, and a path that forgot one would send its request with the field missing --
// which builder.applyFieldMapping treats as "not applicable here" and skips silently. Every defect this
// surface has produced came from helpers that were individually correct and never driven together
// (#218, #220, #224, #229); one entry point is the structural answer to that.
func (h *handler) resolveRequestFields(ctx context.Context, clients *clientSet, clientInfo *getter.Info, mapping []getter.FieldMappingItem, mg *unstructured.Unstructured) (map[string]interface{}, error) {
	lookupValues, err := h.runDependentLookups(ctx, clients, clientInfo, mapping, mg)
	if err != nil {
		return nil, fmt.Errorf("running dependent lookups: %w", err)
	}

	resolved, err := fieldmapping.ResolveRequestResolvers(ctx, h.dynamicClient, mapping, mg, lookupValues)
	if err != nil {
		return nil, fmt.Errorf("resolving field mapping resolvers: %w", err)
	}
	return resolved, nil
}

// deleteSecretRefRBAC tears down a per-CR secretRef Role and RoleBinding left by a release that still
// self-provisioned them (see resolveRequestFields). Unconditional and IsNotFound-tolerant (see
// secretrbac.DeleteSecretRole), so it is correct whether or not this resource ever had one -- which from
// #540 onwards is the normal case, since nothing creates them any more.
//
// It is called from every delete path, including the "RestDefinition not found" early return: teardown
// never depends on the RestDefinition or the external resource still existing.
func (h *handler) deleteSecretRefRBAC(ctx context.Context, mg *unstructured.Unstructured) error {
	gvr, err := h.pluralizer.GVKtoGVR(mg.GroupVersionKind())
	if err != nil {
		return fmt.Errorf("resolving GVR for secretRef RBAC teardown: %w", err)
	}
	roleName := secretrbac.RoleName(gvr, types.NamespacedName{Namespace: mg.GetNamespace(), Name: mg.GetName()})
	return secretrbac.DeleteSecretRole(ctx, h.dynamicClient, mg.GetNamespace(), roleName)
}
