package restdefinition

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/crd/generation"
)

// backfillVersionLabel stamps the version label onto instances of gvk that carry none.
//
// Required by version-scoped watching, and only by it. A controller watching with exact equality on
// krateo.io/oas-version never sees an unlabelled instance, so it never reconciles one and never stamps
// one either -- the instance is created, looks healthy, and belongs to nobody. core-provider's own prune
// e2e records the same state for compositions:
//
//	S3 VERDICT: unlabeled instance is ORPHANED (its version pruned) but NOT data-lost
//	            -- recoverable by re-stamping the label
//
// Recoverable, but only by someone who knows to re-stamp. Every instance written before the oas-version
// policy existed is in exactly that state, which on an established cluster is most of them: the policy
// stamps on CREATE and UPDATE, so an object nobody has touched since the upgrade still carries no label.
// Switching to exact selectors without this would orphan all of them in one roll.
//
// Stamping the CURRENT served version is the only available answer and is the right one. The vacuum
// storage version erases the apiVersion an object was written as, so an unlabelled instance carries no
// record of where it came from -- and before coexistence there was only one served version anyway, so
// there is nothing else it could have belonged to. This is the same value rest-dynamic-controller's own
// fallback stamped, which this replaces.
//
// Selecting on label ABSENCE (!krateo.io/oas-version) makes this self-limiting: once the backfill has run,
// the list comes back empty and the call costs one request. It is therefore safe to run every reconcile
// rather than needing a one-shot migration flag that could be missed or re-run.
func (e *external) backfillVersionLabel(ctx context.Context, gvk schema.GroupVersionKind, version string) error {
	if version == "" || version == generation.VacuumVersionName {
		return nil
	}

	// DoesNotExist, not Equals-to-empty: an absent label and a label set to "" are different states to
	// the apiserver, and only the first is what an un-backfilled instance has.
	req, err := labels.NewRequirement(generation.VersionLabel, selection.DoesNotExist, nil)
	if err != nil {
		return fmt.Errorf("building the unlabelled-instance selector: %w", err)
	}
	sel := labels.NewSelector().Add(*req)

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    gvk.Kind + "List",
	})
	if err := e.kube.List(ctx, list, &client.ListOptions{LabelSelector: sel}); err != nil {
		// A CRD that is not yet established has no endpoint to list. That is not a backfill failure --
		// there are no instances to orphan yet -- so it is reported rather than raised.
		return fmt.Errorf("listing unlabelled instances of %s: %w", gvk.Kind, err)
	}

	patch := []byte(fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, generation.VersionLabel, version))
	for i := range list.Items {
		item := &list.Items[i]
		// A merge patch on just this label carries no resourceVersion precondition, so it cannot conflict
		// with whatever else is writing the object -- the same reason the admission policy uses a JSON
		// Patch rather than a structured merge.
		if err := e.kube.Patch(ctx, item, client.RawPatch(types.MergePatchType, patch)); err != nil {
			if apierrors.IsNotFound(err) {
				continue // went away mid-backfill; nothing to label
			}
			return fmt.Errorf("stamping %s on %s/%s: %w", generation.VersionLabel, item.GetNamespace(), item.GetName(), err)
		}
		e.log.Debug("Backfilled the oas-version label", "kind", gvk.Kind, "name", item.GetName(), "version", version)
	}

	if n := len(list.Items); n > 0 {
		e.log.Info("Backfilled the oas-version label onto instances that predate the version policy",
			"kind", gvk.Kind, "version", version, "count", n)
	}
	return nil
}
