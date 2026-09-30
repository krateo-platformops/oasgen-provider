package crd

import (
	"context"
	"fmt"
	"strings"

	restdefinitionsv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/crd/generation"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Get returns the CRD for the given group-resource, or (nil, nil) when it does not exist.
func Get(ctx context.Context, kubecli client.Client, gr schema.GroupResource) (*apiextensionsv1.CustomResourceDefinition, error) {
	if err := registerEventually(); err != nil {
		return nil, err
	}
	res := &apiextensionsv1.CustomResourceDefinition{}
	if err := kubecli.Get(ctx, client.ObjectKey{Name: gr.String()}, res); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return res, nil
}

// OwnerAnnotation records which RestDefinition (namespace/name) owns a generated CRD. It enforces that a CRD
// for a given group+kind is managed by exactly one LIVE RestDefinition: a second RestDefinition targeting the
// same kind is rejected, so two RDCs never end up reconciling the same resource (every served version's
// endpoint serves all objects, so two controllers would double-reconcile).
//
// The word LIVE is the whole content of the invariant, and it used not to be checked. The annotation is a
// string; a string cannot double-reconcile anything. A RestDefinition that has been deleted leaves its name
// behind, and comparing names alone made that leftover indistinguishable from a real second owner -- so an
// ordinary OAS change could never be applied again, with no path back that did not involve a human editing
// annotations by hand (#137).
const OwnerAnnotation = "krateo.io/owned-by-restdefinition"

// ErrOwnershipConflict is returned when the live CRD is owned by a different RestDefinition that STILL EXISTS.
// It is not a Kubernetes conflict, so it is surfaced immediately (not retried) -- correct here, because a
// second live owner is a modelling mistake that no amount of retrying will resolve.
type ErrOwnershipConflict struct {
	CRD, Owner, Requester string
}

func (e *ErrOwnershipConflict) Error() string {
	return fmt.Sprintf("CRD %q is owned by RestDefinition %q; RestDefinition %q may not manage the same group+kind", e.CRD, e.Owner, e.Requester)
}

// ErrOwnershipUndetermined is returned when we could not find out whether the annotated owner still exists.
//
// This is deliberately NOT ErrOwnershipConflict and NOT an adoption. An RBAC denial or a transport failure is
// not evidence that the owner is gone, and adopting on it would hand a CRD to a second controller on the
// strength of a failed lookup. It is also not evidence the owner is present. Unlike a conflict, it is a
// transient condition, so it is returned as an ordinary retryable error and the next reconcile asks again.
//
// This is the same discipline the pagination package enforces for #119: "I could not determine" must not be
// spelled the same way as either of the answers it is not.
type ErrOwnershipUndetermined struct {
	CRD, Owner string
	Err        error
}

func (e *ErrOwnershipUndetermined) Error() string {
	return fmt.Sprintf("CRD %q is annotated as owned by RestDefinition %q, but whether that RestDefinition still exists could not be determined: %v; refusing to either adopt or reject on an unverified owner", e.CRD, e.Owner, e.Err)
}

func (e *ErrOwnershipUndetermined) Unwrap() error { return e.Err }

// ApplyOutcome reports what the apply did about ownership, for callers that surface it to operators.
type ApplyOutcome struct {
	// GVR is the target group-version-resource.
	GVR schema.GroupVersionResource

	// AdoptedFrom names the DEAD RestDefinition whose stale claim was displaced, or "" when nothing was
	// displaced. It is returned rather than logged here so the caller can raise a Kubernetes Event against
	// the RestDefinition: adoption is a change of ownership, and an operator tracing which object manages a
	// CRD should be able to see it happen instead of inferring it from an annotation that quietly changed.
	AdoptedFrom string
}

// ownerIsLive reports whether owner ("namespace/name") resolves to an existing RestDefinition.
//
// The three-way return is the point: (false, nil) means confirmed absent, (true, nil) means confirmed present,
// and a non-nil error means neither was established. Callers must not collapse the error into either answer.
//
// The lookup is unstructured on purpose. The client this runs on is built with client.New(cfg,
// client.Options{}), whose scheme does not know RestDefinition -- a typed Get would fail with "no kind is
// registered", which would surface as an undetermined owner forever rather than as the missing registration
// it actually is.
func ownerIsLive(ctx context.Context, kubecli client.Client, owner string) (bool, error) {
	ns, name, found := strings.Cut(owner, "/")
	if !found || ns == "" || name == "" {
		// A malformed annotation names nobody. It cannot be a live owner, and treating it as undetermined
		// would wedge on a value that no future reconcile will improve.
		return false, nil
	}

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(restdefinitionsv1alpha1.RestDefinitionGroupVersionKind)
	if err := kubecli.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, u); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ApplyOrUpdateCRD reconciles a freshly generated single-version CRD (newcrd) into the live, possibly
// multi-version, CRD WITHOUT clobbering other versions:
//
//   - live absent             → create newcrd as-is (stamped with owner).
//   - live already has this version → replace ONLY that version's schema (spec+status) in place, preserving
//     every other version, the vacuum, and the live served/storage topology. Breaking same-version changes
//     are allowed (this is oasgen's deliberate divergence from core-provider, which does status-only here).
//   - live lacks this version → append the version alongside the non-served "vacuum" storage version.
//
// owner (the managing RestDefinition's namespace/name) is enforced: a CRD owned by a different RestDefinition
// that STILL EXISTS is rejected with *ErrOwnershipConflict. An unowned pre-existing CRD is adopted, and so is
// one whose annotated owner has been deleted -- those are the same situation, and only the first of them used
// to be recognised (#137). If liveness cannot be established, neither happens: *ErrOwnershipUndetermined is
// returned and the next reconcile asks again. Conversion is set to None
// (the vacuum storage version provides lossless cross-version storage; no webhook). The merge path uses
// optimistic concurrency (read → merge → Update with the read's resourceVersion, retry on conflict), so a
// concurrent sibling-version change is re-merged rather than clobbered. Returns the target GVR.
func ApplyOrUpdateCRD(ctx context.Context, kubecli client.Client, newcrd *apiextensionsv1.CustomResourceDefinition, owner string) (ApplyOutcome, error) {
	if len(newcrd.Spec.Versions) == 0 {
		return ApplyOutcome{}, fmt.Errorf("generated CRD %s has no versions", newcrd.Name)
	}
	gvr := schema.GroupVersionResource{
		Group:    newcrd.Spec.Group,
		Version:  newcrd.Spec.Versions[0].Name,
		Resource: newcrd.Spec.Names.Plural,
	}
	PrepareForApply(newcrd, owner)

	out := ApplyOutcome{GVR: gvr}

	live, err := Get(ctx, kubecli, gvr.GroupResource())
	if err != nil {
		return out, fmt.Errorf("getting CRD %s: %w", gvr.GroupResource().String(), err)
	}

	// Create when absent. If a concurrent create beat us (AlreadyExists), fall through to the ownership-checked
	// merge path rather than overwriting — so we never clobber another RestDefinition's CRD.
	if live == nil {
		if cerr := kubecli.Create(ctx, newcrd); cerr == nil {
			return out, nil
		} else if !apierrors.IsAlreadyExists(cerr) {
			return out, fmt.Errorf("creating CRD %s: %w", newcrd.Name, cerr)
		}
	}

	// Merge into the live CRD with optimistic concurrency: re-read inside the retry, verify ownership against
	// the FRESH state, decide in-place vs append, then Update with that read's resourceVersion.
	gvk := schema.GroupVersionKind{Group: newcrd.Spec.Group, Kind: newcrd.Spec.Names.Kind, Version: gvr.Version}
	err = applyMergedWithRetry(ctx, kubecli, gvr.GroupResource(), func(cur *apiextensionsv1.CustomResourceDefinition) error {
		// A different name in the annotation is not yet a conflict -- it is a question. Ask whether that
		// RestDefinition still exists before concluding anything, because the invariant being protected is
		// "one LIVE owner per group+kind", and a deleted owner cannot double-reconcile anything (#137).
		if o := ownerOf(cur); o != "" && o != owner {
			alive, lerr := ownerIsLive(ctx, kubecli, o)
			switch {
			case lerr != nil:
				// Neither answer was established. Refuse without deciding: adopting here would hand the CRD
				// to a second controller on the strength of a failed lookup.
				out.AdoptedFrom = ""
				return &ErrOwnershipUndetermined{CRD: cur.Name, Owner: o, Err: lerr}
			case alive:
				out.AdoptedFrom = ""
				return &ErrOwnershipConflict{CRD: cur.Name, Owner: o, Requester: owner}
			default:
				// Confirmed gone. Adopt, and remember who was displaced so the caller can say so out loud --
				// this ran on every retry attempt, so it is set (not appended) to the last verdict.
				out.AdoptedFrom = o
			}
		}
		setOwner(cur, owner) // adopt if previously unowned, or if the previous owner no longer exists
		ensureCRDTypeMeta(cur)
		if generation.GVKExists(cur, gvk) {
			// In-place: swap ONLY this version's schema (breaking allowed); other versions + vacuum untouched.
			replaceVersionSchema(cur, gvr.Version, newcrd.Spec.Versions[0])
		} else {
			// Append: add this version alongside the non-served vacuum storage version.
			merged, aerr := generation.AppendVersion(*cur, *newcrd)
			if aerr != nil {
				return aerr
			}
			cur.Spec = merged.Spec
			generation.SetServedStorage(cur, gvr.Version, true, false)
		}
		setNoneConversion(cur)
		generation.AddVersionColumn(cur)
		return nil
	})
	if err != nil {
		switch err.(type) {
		case *ErrOwnershipConflict, *ErrOwnershipUndetermined:
			return out, err // surface both ownership verdicts verbatim; neither is a merge failure
		}
		return out, fmt.Errorf("merging version %s into CRD %s: %w", gvr.Version, gvr.GroupResource().String(), err)
	}
	return out, nil
}

// PrepareForApply stamps a freshly generated CRD with what ApplyOrUpdateCRD adds before writing it: the CRD
// TypeMeta, the VERSION printer column and the owner annotation. On a cluster where the CRD does not exist yet,
// the prepared object is exactly what gets created, which is what lets oasgen-render show it without applying.
func PrepareForApply(newcrd *apiextensionsv1.CustomResourceDefinition, owner string) {
	ensureCRDTypeMeta(newcrd)
	generation.AddVersionColumn(newcrd)
	setOwner(newcrd, owner)
}

// setOwner stamps the OwnerAnnotation (no-op for an empty owner, e.g. tests that don't exercise ownership).
func setOwner(crd *apiextensionsv1.CustomResourceDefinition, owner string) {
	if owner == "" {
		return
	}
	if crd.Annotations == nil {
		crd.Annotations = map[string]string{}
	}
	crd.Annotations[OwnerAnnotation] = owner
}

// ownerOf returns the CRD's owning RestDefinition (namespace/name), or "" if unowned.
func ownerOf(crd *apiextensionsv1.CustomResourceDefinition) string {
	return crd.Annotations[OwnerAnnotation]
}

// applyMergedWithRetry re-reads the CRD by group-resource, applies mergeFn, and Updates it with optimistic
// concurrency (the read's resourceVersion), retrying on conflict. Because it re-reads and re-merges on every
// attempt, a concurrent change (e.g. another served version added) is merged on top rather than clobbered by
// a stale full PUT.
func applyMergedWithRetry(ctx context.Context, kubecli client.Client, gr schema.GroupResource, mergeFn func(*apiextensionsv1.CustomResourceDefinition) error) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur := &apiextensionsv1.CustomResourceDefinition{}
		if err := kubecli.Get(ctx, client.ObjectKey{Name: gr.String()}, cur); err != nil {
			return err
		}
		if err := mergeFn(cur); err != nil {
			return err
		}
		return kubecli.Update(ctx, cur)
	})
}

// replaceVersionSchema swaps ONLY the OpenAPIV3Schema of the named version with the generated one, leaving
// its served/storage flags, name, and printer columns intact — the live version topology (e.g. a vacuum
// holding storage) must be preserved.
func replaceVersionSchema(live *apiextensionsv1.CustomResourceDefinition, version string, gen apiextensionsv1.CustomResourceDefinitionVersion) {
	for i := range live.Spec.Versions {
		if live.Spec.Versions[i].Name == version {
			live.Spec.Versions[i].Schema = gen.Schema
			return
		}
	}
}

// setNoneConversion sets conversion strategy None (no webhook; the vacuum storage version provides lossless
// cross-version storage).
func setNoneConversion(crd *apiextensionsv1.CustomResourceDefinition) {
	crd.Spec.Conversion = &apiextensionsv1.CustomResourceConversion{Strategy: apiextensionsv1.NoneConverter}
}

// ensureCRDTypeMeta stamps the CRD GVK onto TypeMeta. Objects read via a typed client Get do not carry it,
// but oasgen's kube.Apply reads GVK off the object to build its GET, so it must be present.
func ensureCRDTypeMeta(crd *apiextensionsv1.CustomResourceDefinition) {
	crd.SetGroupVersionKind(apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"))
}
