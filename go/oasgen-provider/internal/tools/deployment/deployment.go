package deployment

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RestartAnnotation is the pod-template annotation whose VALUE CHANGING is what actually rolls a
// Deployment. `kubectl rollout restart` writes this same key, so a restart by hand and a restart by this
// controller are indistinguishable to the Deployment -- and both are destroyed by an apply that renders
// the pod template from a static asset.
const RestartAnnotation = "kubectl.kubernetes.io/restartedAt"

func LookupDeployment(ctx context.Context, kube client.Client, obj *appsv1.Deployment) (bool, bool, error) {
	err := kube.Get(ctx, client.ObjectKeyFromObject(obj), obj)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, false, nil
		}

		return false, false, err
	}

	ready := obj.Spec.Replicas != nil && *obj.Spec.Replicas == obj.Status.ReadyReplicas

	return true, ready, nil
}

func RestartDeployment(ctx context.Context, kube client.Client, obj *appsv1.Deployment) error {
	patch := client.MergeFrom(obj.DeepCopy())

	// Set the annotation to trigger a rollout
	if obj.Spec.Template.Annotations == nil {
		obj.Spec.Template.Annotations = map[string]string{}
	}
	obj.Spec.Template.Annotations[RestartAnnotation] = time.Now().Format(time.RFC3339)

	// patch the deployment
	return kube.Patch(ctx, obj, patch)
}

func CleanFromRestartAnnotation(obj *appsv1.Deployment) {
	if obj.Spec.Template.Annotations != nil {
		delete(obj.Spec.Template.Annotations, RestartAnnotation)
	}
}

// PreserveRestartAnnotation is a kube.ApplyOptions.FromCurrent hook that carries the restart annotation
// from the Deployment being replaced onto the Deployment about to replace it.
//
// WITHOUT IT (issue 595, and the state 0.59.0 shipped): the rendered pod template comes from
// helm/oasgen-provider/assets/rdc/deployment.yaml, which declares no annotations at all, so Apply's full
// Update removes whatever restart annotation is live. That costs a rollout -- removing the annotation
// changes the pod template exactly as much as adding it does -- on a reconcile that had nothing to
// change. deploy.Deploy restarts AFTER it applies, so a single ConfigMap change produced two rollouts:
// the intended one, then a spurious one on the next poll when the apply undid its annotation. The
// annotation also never survived long enough to explain, in `kubectl rollout history` or on the
// ReplicaSet, why a pod had rolled.
//
// It is deliberately ONE key rather than "every live pod-template annotation". A blanket carry-over would
// also preserve annotations that a removed sidecar injector or a since-deleted mutating webhook had put
// there, and those SHOULD disappear when they stop being applied. Widening this is a change at the call
// site, not here.
//
// An absent annotation is not an error: a generated controller that has never been restarted has none.
func PreserveRestartAnnotation(current *unstructured.Unstructured, desired client.Object) error {
	dep, ok := desired.(*appsv1.Deployment)
	if !ok {
		// Not a silent no-op. This hook is wired per call site, so the wrong type here is a wiring
		// mistake, and returning nil would hide it behind behaviour that merely looks correct.
		return fmt.Errorf("PreserveRestartAnnotation wired onto a %T, which is not a *appsv1.Deployment", desired)
	}

	live, found, err := unstructured.NestedString(current.Object,
		"spec", "template", "metadata", "annotations", RestartAnnotation)
	if err != nil {
		return fmt.Errorf("reading %s from the live Deployment: %w", RestartAnnotation, err)
	}
	if !found {
		// Never restarted, so there is nothing to carry over and nothing to lose.
		return nil
	}

	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = map[string]string{}
	}
	dep.Spec.Template.Annotations[RestartAnnotation] = live
	return nil
}
