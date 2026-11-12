package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/avast/retry-go"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ApplyOp says what Apply did. It is what an audit record's "did this write change anything" predicate
// is read from, so that each caller does not re-derive it (#543).
type ApplyOp string

const (
	// ApplyCreated: the object did not exist and was created.
	ApplyCreated ApplyOp = "created"

	// ApplyUpdated: an Update was sent AND the apiserver persisted a new revision of the object.
	ApplyUpdated ApplyOp = "updated"

	// ApplyUnchanged: an Update was sent and the apiserver persisted NOTHING, because what we sent was
	// already what was stored.
	//
	// This is READ FROM THE APISERVER rather than decided here: a write that changes nothing is not
	// persisted, so the resourceVersion the apiserver returns is the one we sent. That makes the
	// apiserver the authority on whether our write changed the object, instead of a comparison in this
	// package re-deriving per-kind semantics (RBAC rules as sets, a Secret's stringData folded into
	// data) that it does not know and should not learn.
	//
	// MEASURED, and this is why the arm covering it is an INTEGRATION test rather than a unit test:
	// controller-runtime's fake client bumps the resourceVersion on a byte-identical Update (999 ->
	// 1000, with a genuinely changed Update going 1000 -> 1001 as a control). So under the fake this
	// outcome is unreachable, and a unit test for it would FAIL rather than merely be absent.
	ApplyUnchanged ApplyOp = "unchanged"

	// ApplyDryRun: nothing was written, because opts.DryRun asked the apiserver to validate only.
	//
	// Its own op rather than one of the three above, because each of those would be a lie a caller
	// could act on. A dry-run Update returns the stored resourceVersion untouched -- which is exactly
	// what a genuine no-op returns -- so it would otherwise report ApplyUnchanged; and a dry-run Create
	// would report ApplyCreated and let an audit caller record a write that never happened.
	//
	// THE LITERAL IS plumbing's, not ours. plumbing's objectclient carries the same op under the same
	// string (snowplow's AUDIT-4 ruling, plumbing#142), and the audit records of the two writers are
	// read together -- a kebab-case spelling here would split one outcome into two in every query.
	ApplyDryRun ApplyOp = "dryRun"
)

// ApplyResult reports what Apply did, from the attempt that finally succeeded.
//
// Before and After are the two sides a content comparison needs, and neither is recoverable by the
// caller afterwards:
//
//   - Before is the live object as it was immediately before the write. It is nil for ApplyCreated,
//     because there was nothing there.
//   - After is the object the APISERVER RETURNED, not the one we sent. Defaulting, admission and other
//     writers all act in between, so comparing against what we sent would compare against our
//     intention rather than against what is now stored.
//
// Both carry the GroupVersionKind explicitly, because a content comparison dispatches on it (a Secret's
// stringData is folded into data) and the typed client clears TypeMeta when it decodes a response.
type ApplyResult struct {
	Op     ApplyOp
	Before *unstructured.Unstructured
	After  *unstructured.Unstructured
}

// Apply applies a generic object to the cluster, reporting what it did.
//
// The result comes from the attempt that SUCCEEDED, not the first one: a conflict re-reads the live
// object and may turn an intended update into a different outcome, and the caller needs the outcome that
// actually happened.
func Apply(ctx context.Context, kube client.Client, obj client.Object, opts ApplyOptions) (ApplyResult, error) {
	gvk := obj.GetObjectKind().GroupVersionKind()
	// A dry run is decided by the caller once, outside the retry: it cannot change between attempts, and
	// reading it here keeps the two write paths from each re-deriving it.
	dryRun := len(opts.DryRun) > 0

	var res ApplyResult
	err := retry.Do(
		func() error {
			// Reset per attempt, so a failed attempt cannot leave a stale outcome behind for the next one
			// to inherit -- or, worse, for a later success to report.
			res = ApplyResult{}

			tmp := &unstructured.Unstructured{}
			tmp.SetKind(gvk.Kind)
			tmp.SetAPIVersion(gvk.GroupVersion().String())
			err := kube.Get(ctx, client.ObjectKeyFromObject(obj), tmp)
			if err != nil {
				if apierrors.IsNotFound(err) {
					createOpts := &client.CreateOptions{
						DryRun:          opts.DryRun,
						FieldManager:    opts.FieldManager,
						FieldValidation: opts.FieldValidation,
					}
					if cerr := kube.Create(ctx, obj, createOpts); cerr != nil {
						return cerr
					}
					after, aerr := asUnstructured(obj, gvk)
					if aerr != nil {
						return &permanentError{err: fmt.Errorf("reading back the created %s/%s: %w", obj.GetNamespace(), obj.GetName(), aerr)}
					}
					op := ApplyCreated
					if dryRun {
						op = ApplyDryRun
					}
					res = ApplyResult{Op: op, After: after}
					return nil
				}
				return err
			}

			// Captured before anything is written, and deep-copied because `tmp` is reused by the next
			// retry attempt: handing the caller a pointer into our own scratch object would let a later
			// attempt rewrite the "before" it was given.
			before := tmp.DeepCopy()

			obj.SetResourceVersion(tmp.GetResourceVersion())

			// Hand the caller the version we are about to replace, so it can carry over a field that
			// another writer owns. This sits INSIDE the retry on purpose: a conflict re-reads the live
			// object and calls the hook again, so what the hook copies is never older than the object
			// the Update actually replaces.
			if opts.FromCurrent != nil {
				if err := opts.FromCurrent(tmp, obj); err != nil {
					return &permanentError{err: fmt.Errorf("FromCurrent on %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)}
				}
			}

			// Read immediately before the write, and from obj rather than from tmp: FromCurrent may have
			// changed the object we are about to send, and what decides the outcome is the version WE
			// SENT, not the one we happened to read.
			sentRV := obj.GetResourceVersion()

			updateOpts := &client.UpdateOptions{
				DryRun:          opts.DryRun,
				FieldManager:    opts.FieldManager,
				FieldValidation: opts.FieldValidation,
			}
			if uerr := kube.Update(ctx, obj, updateOpts); uerr != nil {
				return uerr
			}
			after, aerr := asUnstructured(obj, gvk)
			if aerr != nil {
				return &permanentError{err: fmt.Errorf("reading back the updated %s/%s: %w", obj.GetNamespace(), obj.GetName(), aerr)}
			}

			op := ApplyUpdated
			switch {
			case dryRun:
				op = ApplyDryRun
			case after.GetResourceVersion() == sentRV:
				// The apiserver stored nothing new, so our write changed nothing.
				op = ApplyUnchanged
			}
			res = ApplyResult{Op: op, Before: before, After: after}
			return nil
		},
		retry.RetryIf(retryableAPIError),
		retry.LastErrorOnly(true),
	)
	if err != nil {
		return ApplyResult{}, err
	}
	return res, nil
}

// asUnstructured is obj as the apiserver last left it, as an unstructured COPY.
//
// Copied rather than aliased: obj belongs to the caller and a retry may write to it again, so handing
// back a view onto it would let a later attempt rewrite a result already reported.
//
// The GroupVersionKind is set from the one Apply read off the object up front, not from whatever the
// response carried: controller-runtime's typed client clears TypeMeta when it decodes, and a content
// comparison that dispatches on kind would then silently take the wrong branch.
func asUnstructured(obj client.Object, gvk schema.GroupVersionKind) (*unstructured.Unstructured, error) {
	var u *unstructured.Unstructured
	if src, ok := obj.(*unstructured.Unstructured); ok {
		u = src.DeepCopy()
	} else {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj.DeepCopyObject())
		if err != nil {
			return nil, err
		}
		u = &unstructured.Unstructured{Object: m}
	}
	u.SetGroupVersionKind(gvk)
	return u, nil
}

// retryableAPIError reports whether err is worth retrying. These three operations used retry.Do with NO
// options, so every error got the library defaults -- 10 attempts with exponential backoff from 100ms,
// which is ~50 SECONDS of waiting before giving up.
//
// MEASURED: a single Forbidden injected into one Delete took a unit test from milliseconds to 51.6s. And
// it cannot succeed: RBAC does not change inside the backoff, a malformed object does not become valid,
// and a method the apiserver does not support does not start supporting it. The retry spends a minute
// establishing what the first attempt already knew.
//
// The cost is not just latency. Uninstall runs during Delete, holding the finalizer -- so a permission
// error stalls teardown for ~50s per object, and nothing retries afterwards because the caller is already
// in its final pass. That is also what made collecting errors across namespaces look expensive rather
// than correct: the price was the pointless retry, not the extra namespaces.
//
// Everything NOT listed here still retries exactly as before: conflicts, timeouts, server errors, "too
// many requests" and connection failures are all transient and are what the retry was added for.
// WHY retry.LastErrorOnly(true) IS SET ON ALL THREE retry.Do SITES.
//
// retry-go v3.0.0's `Error` is a `[]error` with a WrappedErrors() accessor and NO Unwrap(), so
// `errors.Is` and `errors.As` cannot see through it. Without this option every error these three
// functions return is therefore unmatchable by any caller, and reads in a log as
// "All attempts fail:\n#1: <the actual error>" -- the real error indented under a header that says
// nothing. No caller in the tree depends on that text.
//
// It became load-bearing with FromCurrent: a hook's error is the caller's own verdict, and a verdict the
// caller cannot then match with errors.Is is only half returned.
//
// permanentError marks an error that retrying cannot change because it did not come from the apiserver
// at all. Today that is a FromCurrent verdict: the caller judged the object it was shown, and showing it
// the same object nine more times cannot change the answer.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func retryableAPIError(err error) bool {
	// A caller's own verdict is never transient. Checked before the apierrors switch because a
	// FromCurrent error may WRAP an apierrors value -- a hook that reads the live object can legitimately
	// return a NotFound -- and the wrapper's judgement must win over what it wraps.
	var perm *permanentError
	if errors.As(err, &perm) {
		return false
	}

	switch {
	case apierrors.IsForbidden(err),
		apierrors.IsUnauthorized(err),
		apierrors.IsInvalid(err),
		apierrors.IsBadRequest(err),
		apierrors.IsMethodNotSupported(err),
		apierrors.IsNotAcceptable(err),
		apierrors.IsRequestEntityTooLargeError(err):
		// Permanent by construction: the apiserver has judged the request itself, not its timing.
		return false
	}
	return true
}

// UninstallOp says what Uninstall did.
type UninstallOp string

const (
	// UninstallDeleted: the object existed and a Delete was accepted.
	UninstallDeleted UninstallOp = "deleted"
	// UninstallDryRun: nothing was deleted, because opts.DryRun asked the apiserver to validate only.
	//
	// The same trap as ApplyDryRun and found with it: a dry-run Delete is ACCEPTED by the apiserver and
	// deletes nothing, so without this it reports UninstallDeleted and an audit caller records a deletion
	// that never happened. UninstallOptions.DryRun is passed straight to client.DeleteOptions, so this is
	// reachable by any caller that sets it.
	UninstallDryRun UninstallOp = "dryRun"
	// UninstallAbsent: there was nothing to delete -- either the Get said NotFound, or the Delete did
	// because something removed it in between. Both are the same outcome for a caller: no write of ours
	// happened, so there is nothing to record.
	UninstallAbsent UninstallOp = "absent"
)

// UninstallResult reports what Uninstall did. Before is the live object as it was, set only for
// UninstallDeleted -- so a nil Before is exactly the "nothing of ours happened" case.
type UninstallResult struct {
	Op     UninstallOp
	Before *unstructured.Unstructured
}

func Uninstall(ctx context.Context, kube client.Client, obj client.Object, opts UninstallOptions) (UninstallResult, error) {
	var res UninstallResult
	err := retry.Do(
		func() error {
			res = UninstallResult{}

			tmp := &unstructured.Unstructured{}
			tmp.SetKind(obj.GetObjectKind().GroupVersionKind().Kind)
			tmp.SetAPIVersion(obj.GetObjectKind().GroupVersionKind().GroupVersion().String())
			err := kube.Get(ctx, client.ObjectKeyFromObject(obj), tmp)
			if err != nil {
				if apierrors.IsNotFound(err) {
					res = UninstallResult{Op: UninstallAbsent}
					return nil
				}

				return err
			}

			before := tmp.DeepCopy()
			obj.SetResourceVersion(tmp.GetResourceVersion())

			err = kube.Delete(ctx, obj, &client.DeleteOptions{
				DryRun:             opts.DryRun,
				Preconditions:      opts.Preconditions,
				PropagationPolicy:  opts.PropagationPolicy,
				GracePeriodSeconds: opts.GracePeriodSeconds,
			})
			if err != nil {
				if apierrors.IsNotFound(err) {
					// Removed between our Get and our Delete. Not our write, so it is absent rather than
					// deleted, and Before stays nil to say so.
					res = UninstallResult{Op: UninstallAbsent}
					return nil
				}

				return err
			}
			op := UninstallDeleted
			if len(opts.DryRun) > 0 {
				op = UninstallDryRun
			}
			res = UninstallResult{Op: op, Before: before}
			return nil
		},
		retry.RetryIf(retryableAPIError),
		retry.LastErrorOnly(true),
	)
	if err != nil {
		return UninstallResult{}, err
	}
	return res, nil
}

func UninstallFromReference(ctx context.Context, kube client.Client, ref v1.ObjectReference, opts UninstallOptions) error {
	return retry.Do(
		func() error {
			tmp := &unstructured.Unstructured{}

			// tmp.SetAPIVersion(gvk.GroupVersion().String())
			// tmp.SetKind(gvk.Kind)

			tmp.SetGroupVersionKind(ref.GroupVersionKind())
			err := kube.Get(ctx, client.ObjectKey{
				Name:      ref.Name,
				Namespace: ref.Namespace,
			}, tmp)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil
				}
				return err
			}

			err = kube.Delete(ctx, tmp, &client.DeleteOptions{
				DryRun:             opts.DryRun,
				Preconditions:      opts.Preconditions,
				PropagationPolicy:  opts.PropagationPolicy,
				GracePeriodSeconds: opts.GracePeriodSeconds,
			})
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil
				}
				return err
			}
			return nil
		},
		retry.RetryIf(retryableAPIError),
		retry.LastErrorOnly(true),
	)
}

func GetFromReference(ctx context.Context, kube client.Client, ref v1.ObjectReference) error {
	tmp := &unstructured.Unstructured{}
	tmp.SetGroupVersionKind(ref.GroupVersionKind())
	err := kube.Get(ctx, client.ObjectKey{
		Name:      ref.Name,
		Namespace: ref.Namespace,
	}, tmp)
	if err != nil {
		return err
	}
	return nil
}

func Get(ctx context.Context, kube client.Client, obj client.Object) error {
	return kube.Get(ctx, client.ObjectKeyFromObject(obj), obj)
}

// Note: currently not used
func CountRestResourcesWithGroup(ctx context.Context, kube client.Client, discovery discovery.DiscoveryInterface, group string) (configuration bool, rest int, err error) {
	_, apiResourceList, err := discovery.ServerGroupsAndResources()
	if err != nil {
		return false, 0, fmt.Errorf("failed to discover API resources: %v", err)
	}

	if len(apiResourceList) == 0 {
		return false, 0, fmt.Errorf("no API resources found")
	}
	configuration = false
	rest = 0

	for _, apiResource := range apiResourceList {
		gv, err := schema.ParseGroupVersion(apiResource.GroupVersion)
		if err != nil {
			return configuration, 0, fmt.Errorf("failed to parse group version: %v", err)
		}

		if gv.Group == group {
			// list the resources of each gvk
			li := unstructured.UnstructuredList{}

			for _, resource := range apiResource.APIResources {
				if resource.Kind == "" {
					continue
				}
				li.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   gv.Group,
					Version: gv.Version,
					Kind:    resource.Kind,
				})

				if strings.HasSuffix(resource.Kind, "Configuration") {
					configuration = true
				} else {
					err = kube.List(ctx, &li)
					if err != nil {
						if !strings.Contains(err.Error(), "no matches for") {
							return configuration, 0, fmt.Errorf("failed to list resources: %v", err)
						}
					}

					rest += len(li.Items)
				}
			}
		}
	}

	return configuration, rest, nil
}

// Note: currently not used
func CountRestDefinitionsWithGroup(ctx context.Context, kube client.Client, discovery discovery.DiscoveryInterface, group string) (configuration bool, rest int, err error) {
	_, apiResourceList, err := discovery.ServerGroupsAndResources()
	if err != nil {
		return false, 0, fmt.Errorf("failed to discover API resources: %v", err)
	}

	if len(apiResourceList) == 0 {
		return false, 0, fmt.Errorf("no API resources found")
	}

	count := 0
	for _, apiResource := range apiResourceList {
		gv, err := schema.ParseGroupVersion(apiResource.GroupVersion)
		if err != nil {
			return false, 0, fmt.Errorf("failed to parse group version: %v", err)
		}
		if gv.Group == group {
			for _, resource := range apiResource.APIResources {
				if resource.Kind == "" {
					continue
				}
				if strings.HasSuffix(resource.Kind, "Configuration") {
					configuration = true
				} else if !strings.HasSuffix(resource.Name, "/status") {
					count += 1
				}
			}
		}
	}
	return configuration, count, nil
}
