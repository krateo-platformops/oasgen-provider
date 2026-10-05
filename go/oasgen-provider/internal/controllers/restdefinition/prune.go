package restdefinition

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/crd"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/crd/generation"
)

// prunableServedVersions returns the served versions of this RestDefinition's CRD that are safe to
// retire, plus the per-version reasons the others were kept (logging only).
//
// A version is kept when ANY of these holds:
//
//   - it is the version currently being served by this RestDefinition
//   - it is the vacuum storage version, which is never served and carries every stored object
//   - another RestDefinition still generates that version of the same Kind
//   - any live instance still carries that version in krateo.io/oas-version
//
// SHARED by Observe and the prune itself, and that sharing is the point rather than tidiness. Observe
// reports not-up-to-date when this returns anything, so that Update runs and drives the prune to
// completion; the prune then removes exactly what Observe counted. If the two used different predicates
// -- even slightly -- Observe would keep asking for work Update does not do, and the pair would
// ping-pong forever while reporting Creating. oasgen shipped exactly that bug in 0.28.0 from two copies
// of a struct literal; core-provider documents the same hazard on its equivalent function. One
// predicate, two callers.
func (e *external) prunableServedVersions(
	ctx context.Context,
	cr *definitionv1alpha1.RestDefinition,
	gvk schema.GroupVersionKind,
	gvr schema.GroupVersionResource,
) (prunable, kept []string, err error) {
	live, err := crd.Get(ctx, e.kube, gvr.GroupResource())
	if err != nil {
		return nil, nil, fmt.Errorf("fetching CRD for prune evaluation: %w", err)
	}
	if live == nil {
		return nil, nil, nil
	}

	for _, v := range live.Spec.Versions {
		if v.Name == generation.VacuumVersionName || v.Name == gvk.Version {
			continue
		}

		referenced, refErr := e.versionReferencedByAnotherDefinition(ctx, cr, gvk.Kind, v.Name)
		if refErr != nil {
			return nil, nil, fmt.Errorf("checking references for version %s: %w", v.Name, refErr)
		}
		if referenced {
			kept = append(kept, v.Name+":referenced")
			continue
		}

		n, cntErr := e.countInstancesOfVersion(ctx, gvk, gvr, v.Name)
		if cntErr != nil {
			return nil, nil, fmt.Errorf("listing instances for version %s: %w", v.Name, cntErr)
		}
		if n > 0 {
			kept = append(kept, fmt.Sprintf("%s:instances=%d", v.Name, n))
			continue
		}

		prunable = append(prunable, v.Name)
	}
	return prunable, kept, nil
}

// countInstancesOfVersion counts live instances labelled as belonging to version.
//
// Counts by LABEL, not by listing the version's endpoint. Every served endpoint returns every object --
// the vacuum storage version means they are all stored once and served through each version -- so
// listing v1-0-27's endpoint returns v1-0-28's objects too and would never find a version prunable. The
// label is the only thing that distinguishes them, which is the whole reason it exists.
func (e *external) countInstancesOfVersion(
	ctx context.Context,
	gvk schema.GroupVersionKind,
	gvr schema.GroupVersionResource,
	version string,
) (int, error) {
	sel, err := labels.Parse(generation.VersionSelector(version))
	if err != nil {
		return 0, fmt.Errorf("parsing selector for version %s: %w", version, err)
	}

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version, // read through the CURRENT endpoint; the label does the filtering
		Kind:    gvk.Kind + "List",
	})
	if err := e.kube.List(ctx, list, &client.ListOptions{LabelSelector: sel}); err != nil {
		return 0, err
	}
	return len(list.Items), nil
}

// versionReferencedByAnotherDefinition reports whether a RestDefinition OTHER than cr currently serves
// this Kind at this version.
//
// Two RestDefinitions can generate the same Kind in the same group -- the CRD is shared and each appends
// its own version. Pruning a version another definition is still serving would delete its endpoint out
// from under it, so a version in use elsewhere is never prunable regardless of whether THIS definition
// has instances on it.
func (e *external) versionReferencedByAnotherDefinition(
	ctx context.Context,
	cr *definitionv1alpha1.RestDefinition,
	kind string,
	version string,
) (bool, error) {
	var defs definitionv1alpha1.RestDefinitionList
	if err := e.kube.List(ctx, &defs); err != nil {
		return false, err
	}

	for i := range defs.Items {
		other := &defs.Items[i]
		if other.Name == cr.Name && other.Namespace == cr.Namespace {
			continue
		}
		if other.Spec.ResourceGroup != cr.Spec.ResourceGroup {
			continue
		}
		if !strings.EqualFold(other.Spec.Resource.Kind, kind) {
			continue
		}
		// The version a definition currently serves is the one recorded in its status; a definition that
		// has never reconciled serves nothing yet and cannot be holding a version.
		if other.Status.Resource.APIVersion == version {
			return true, nil
		}
	}
	return false, nil
}

// pruneStaleServedVersions retires the versions prunableServedVersions found, so a CRD does not
// accumulate every version hop forever.
//
// Two reasons it matters beyond tidiness. A retired version is a served endpoint a client can still
// pick, and writing through it silently drops any field added after it — the object comes back missing
// data nobody said anything about. And once version-scoped watching is in place, every served version
// is a rest-dynamic-controller Deployment; versions that nothing uses are controllers that reconcile
// nothing and cost memory for the lifetime of the install.
//
// ORDER IS LOAD-BEARING. The apiserver refuses to remove a version from spec.versions while it is still
// listed in status.storedVersions, and rejects the WHOLE spec update when it does — so nothing prunes
// and the only symptom is a reconcile that never converges. storedVersions is trimmed first, through
// the status subresource, and only then are the versions removed from the spec. This is safe because
// the vacuum storage version holds every stored object: no data remains at the retired versions.
func (e *external) pruneStaleServedVersions(
	ctx context.Context,
	cr *definitionv1alpha1.RestDefinition,
	gvk schema.GroupVersionKind,
	gvr schema.GroupVersionResource,
) error {
	prunable, kept, err := e.prunableServedVersions(ctx, cr, gvk, gvr)
	if err != nil {
		return err
	}

	// INFO, not Debug. A prune deletes a served endpoint; which versions went and which were kept, and
	// why, is the kind of decision someone needs to be able to reconstruct from a default-level log
	// after the fact. The same reasoning that moved schema warnings off DEBUG+3.
	e.log.Info("Served-version prune evaluation",
		"gvr", gvr.String(), "current", gvk.Version, "prunable", prunable, "kept", kept)

	if len(prunable) == 0 {
		return nil
	}

	live, err := crd.Get(ctx, e.kube, gvr.GroupResource())
	if err != nil {
		return fmt.Errorf("fetching CRD for prune: %w", err)
	}
	if live == nil {
		return nil
	}

	pruneSet := make(map[string]bool, len(prunable))
	for _, v := range prunable {
		pruneSet[v] = true
	}

	var keptStored []string
	for _, sv := range live.Status.StoredVersions {
		if !pruneSet[sv] {
			keptStored = append(keptStored, sv)
		}
	}
	if len(keptStored) != len(live.Status.StoredVersions) {
		live.Status.StoredVersions = keptStored
		if err := e.kube.Status().Update(ctx, live); err != nil {
			return fmt.Errorf("trimming storedVersions before prune: %w", err)
		}
	}

	if !generation.RemoveStaleVersions(live, pruneSet) {
		return nil
	}
	if err := e.kube.Update(ctx, live); err != nil {
		return fmt.Errorf("applying pruned CRD: %w", err)
	}

	e.log.Info("Pruned stale served versions from CRD", "gvr", gvr.String(), "pruned", prunable)
	e.rec.Eventf(cr, corev1.EventTypeNormal, "PrunedServedVersions",
		"retired %d served version(s) no longer referenced by any instance or definition: %v",
		len(prunable), prunable)
	return nil
}
