package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/avast/retry-go"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ApplyGenericObject applies a generic object to the cluster
func Apply(ctx context.Context, kube client.Client, obj client.Object, opts ApplyOptions) error {
	return retry.Do(
		func() error {
			tmp := &unstructured.Unstructured{}
			tmp.SetKind(obj.GetObjectKind().GroupVersionKind().Kind)
			tmp.SetAPIVersion(obj.GetObjectKind().GroupVersionKind().GroupVersion().String())
			err := kube.Get(ctx, client.ObjectKeyFromObject(obj), tmp)
			if err != nil {
				if apierrors.IsNotFound(err) {
					createOpts := &client.CreateOptions{
						DryRun:          opts.DryRun,
						FieldManager:    opts.FieldManager,
						FieldValidation: opts.FieldValidation,
					}
					return kube.Create(ctx, obj, createOpts)
				}
				return err
			}

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

			updateOpts := &client.UpdateOptions{
				DryRun:          opts.DryRun,
				FieldManager:    opts.FieldManager,
				FieldValidation: opts.FieldValidation,
			}
			return kube.Update(ctx, obj, updateOpts)
		},
		retry.RetryIf(retryableAPIError),
		retry.LastErrorOnly(true),
	)
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

func Uninstall(ctx context.Context, kube client.Client, obj client.Object, opts UninstallOptions) error {
	return retry.Do(
		func() error {
			tmp := &unstructured.Unstructured{}
			tmp.SetKind(obj.GetObjectKind().GroupVersionKind().Kind)
			tmp.SetAPIVersion(obj.GetObjectKind().GroupVersionKind().GroupVersion().String())
			err := kube.Get(ctx, client.ObjectKeyFromObject(obj), tmp)
			if err != nil {
				if apierrors.IsNotFound(err) {
					return nil
				}

				return err
			}

			obj.SetResourceVersion(tmp.GetResourceVersion())

			err = kube.Delete(ctx, obj, &client.DeleteOptions{
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
