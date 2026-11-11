package kube

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ApplyOptions struct {
	// When present, indicates that modifications should not be
	// persisted. An invalid or unrecognized dryRun directive will
	// result in an error response and no further processing of the
	// request. Valid values are:
	// - All: all dry run stages will be processed
	DryRun []string

	// FieldManager is the name of the user or component submitting
	// this request.  It must be set with server-side apply.
	FieldManager string

	// fieldValidation instructs the server on how to handle
	// objects in the request (POST/PUT/PATCH) containing unknown
	// or duplicate fields. Valid values are:
	// - Ignore: This will ignore any unknown fields that are silently
	// dropped from the object, and will ignore all but the last duplicate
	// field that the decoder encounters. This is the default behavior
	// prior to v1.23.
	// - Warn: This will send a warning via the standard warning response
	// header for each unknown field that is dropped from the object, and
	// for each duplicate field that is encountered. The request will
	// still succeed if there are no other errors, and will only persist
	// the last of any duplicate fields. This is the default in v1.23+
	// - Strict: This will fail the request with a BadRequest error if
	// any unknown fields would be dropped from the object, or if any
	// duplicate fields are present. The error returned from the server
	// will contain all unknown and duplicate fields encountered.
	FieldValidation string

	// FromCurrent, when set, is called on the UPDATE path after the live object has been read and
	// before it is replaced, with that live object and the desired object the caller passed to Apply.
	// It lets a caller keep a field that ANOTHER WRITER OWNS, copied from the very version being
	// replaced. Not called on create: there is nothing to copy from.
	//
	// WHY THIS EXISTS. Apply's update path is a full `Update` with the object the caller rendered, so
	// every field present on the live object and absent from that render is DESTROYED. The render comes
	// from a static asset, so anything a second writer put there is lost on the next reconcile — even
	// when the reconcile had nothing to change.
	//
	// Measured (issue 595): the generated controller's `kubectl.kubernetes.io/restartedAt` pod-template
	// annotation. deploy.Deploy applies the rendered Deployment and THEN restarts it when the
	// ConfigMap's data changed, so one data change produced two rollouts -- the restart itself, and a
	// spurious one on the next reconcile when the apply removed the annotation the restart had written.
	// The annotation also never survived, so `kubectl rollout history` could not show why a pod rolled.
	//
	// `spec.replicas` is the same shape and is the next one: a Deployment scaled by anything other than
	// this render -- an HPA, an operator, `kubectl scale`, or the scale-to-zero of issue 550 -- has its
	// replica count overwritten by whatever the asset says.
	//
	// THE NAME IS NOT OURS. It matches `ApplyOptions.FromCurrent` in
	// github.com/krateo-platformops/plumbing/kubeutil/objectclient (v1.41.0+), which solved this for
	// core-provider, down to the argument order and the "not called on create" rule. The signature
	// differs in one way only: plumbing's `desired` is unstructured because its Apply is dynamic-client
	// based, while ours is the typed `client.Object` the caller already holds, so a hook can assert its
	// own type and set a field directly instead of walking an unstructured map.
	//
	// AN ERROR FROM IT IS PERMANENT. It is the caller's verdict about the object, not a transient
	// apiserver failure, so Apply returns it without retrying.
	FromCurrent func(current *unstructured.Unstructured, desired client.Object) error
}

type UninstallOptions struct {
	// GracePeriodSeconds is the duration in seconds before the object should be
	// deleted. Value must be non-negative integer. The value zero indicates
	// delete immediately. If this value is nil, the default grace period for the
	// specified type will be used.
	GracePeriodSeconds *int64

	// Preconditions must be fulfilled before a deletion is carried out. If not
	// possible, a 409 Conflict status will be returned.
	Preconditions *metav1.Preconditions

	// PropagationPolicy determined whether and how garbage collection will be
	// performed. Either this field or OrphanDependents may be set, but not both.
	// The default policy is decided by the existing finalizer set in the
	// metadata.finalizers and the resource-specific default policy.
	// Acceptable values are: 'Orphan' - orphan the dependents; 'Background' -
	// allow the garbage collector to delete the dependents in the background;
	// 'Foreground' - a cascading policy that deletes all dependents in the
	// foreground.
	PropagationPolicy *metav1.DeletionPropagation

	// When present, indicates that modifications should not be
	// persisted. An invalid or unrecognized dryRun directive will
	// result in an error response and no further processing of the
	// request. Valid values are:
	// - All: all dry run stages will be processed
	DryRun []string
}
