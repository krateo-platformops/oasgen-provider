//go:build unit || integration

package deployment_test

import (
	"context"
	"testing"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/deployment"
	kubecli "github.com/krateo-platformops/oasgen-provider/internal/tools/kube"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// ISSUE 595. deploy.Deploy renders the generated controller's Deployment from
// helm/oasgen-provider/assets/rdc/deployment.yaml -- which declares no annotations at all -- and applies
// it with kube.Apply, whose update path is a full Update. So the apply REMOVED the
// kubectl.kubernetes.io/restartedAt annotation that the restart immediately below it had written.
//
// Removing that annotation changes the pod template exactly as much as adding it does, so one ConfigMap
// change cost two rollouts: the intended restart, then a spurious one on the next poll when the apply
// undid it. 0.59.0 shipped in that state, and azuredevops-provider-kog measured the annotation absent
// across every flip -- which is why `kubectl rollout history` could never say why a pod had rolled.
//
// These tests assert on what the APISERVER HOLDS after the apply, not on what the hook returns, because
// the question is whether the annotation survived being replaced.

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))
	return s
}

// liveDeployment is a generated controller that HAS been restarted: the annotation is present.
func liveDeployment(restartedAt string) *appsv1.Deployment {
	d := renderedDeployment()
	d.Spec.Template.Annotations = map[string]string{deployment.RestartAnnotation: restartedAt}
	return d
}

// renderedDeployment is what templates.CreateK8sObject produces from the asset: NO annotations.
func renderedDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: "petstore-controller", Namespace: "krateo-system"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "petstore"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "petstore"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "rdc", Image: "rest-dynamic-controller:0.59.0"}},
				},
			},
		},
	}
}

// THE TWO ARMS MUST DIFFER, and the difference is the whole fix.
//
// The no-hook arm is not redundant: it is the only thing that proves this test can detect the bug at
// all. A test that only ran the hooked arm would also pass against an Apply that had stopped replacing
// the pod template for some unrelated reason, and would pass against a hook that did nothing if
// something else happened to preserve the annotation.
func TestAnApplyMustNotUndoARestart(t *testing.T) {
	ctx := context.Background()
	key := client.ObjectKeyFromObject(renderedDeployment())

	applied := func(t *testing.T, opts kubecli.ApplyOptions) *appsv1.Deployment {
		t.Helper()
		kube := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(liveDeployment("2026-11-11T09:00:00Z")).Build()

		rendered := renderedDeployment()
		require.Empty(t, rendered.Spec.Template.Annotations,
			"premise: the rendered pod template must carry no annotations, or this test is not about the asset deploy.Deploy uses")

		_, applyErr1 := kubecli.Apply(ctx, kube, rendered, opts)
		require.NoError(t, applyErr1)

		after := &appsv1.Deployment{}
		require.NoError(t, kube.Get(ctx, key, after))
		return after
	}

	t.Run("CONTROL: without the hook the apply destroys it", func(t *testing.T) {
		after := applied(t, kubecli.ApplyOptions{})
		assert.NotContains(t, after.Spec.Template.Annotations, deployment.RestartAnnotation,
			"the apply preserved the restart annotation with no FromCurrent hook set. If kube.Apply was "+
				"changed to stop replacing the pod template, this test no longer measures issue 595 and the "+
				"hook below may be dead code -- decide deliberately rather than deleting this arm.")
	})

	t.Run("with the hook it survives", func(t *testing.T) {
		after := applied(t, kubecli.ApplyOptions{FromCurrent: deployment.PreserveRestartAnnotation})
		assert.Equal(t, "2026-11-11T09:00:00Z", after.Spec.Template.Annotations[deployment.RestartAnnotation],
			"the restart annotation did not survive the apply, so the next reconcile rolls the generated "+
				"controller's pods for nothing (issue 595) and `kubectl rollout history` cannot say why the "+
				"previous roll happened")
	})
}

func TestPreserveRestartAnnotation(t *testing.T) {
	t.Run("an absent annotation is not resurrected", func(t *testing.T) {
		// A generated controller that has never been restarted has none. Inventing a value here would
		// itself roll the pods -- the failure this fix exists to stop, in the opposite direction.
		current := &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{}}},
		}}
		desired := renderedDeployment()

		require.NoError(t, deployment.PreserveRestartAnnotation(current, desired))
		assert.NotContains(t, desired.Spec.Template.Annotations, deployment.RestartAnnotation,
			"a Deployment that was never restarted came out of the hook carrying a restart annotation, "+
				"which rolls its pods on the next apply")
	})

	t.Run("a nil annotation map on the desired object is created", func(t *testing.T) {
		current := &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{
				"annotations": map[string]any{deployment.RestartAnnotation: "T1"},
			}}},
		}}
		desired := renderedDeployment()
		require.Nil(t, desired.Spec.Template.Annotations, "premise: the rendered object's map is nil, not empty")

		require.NoError(t, deployment.PreserveRestartAnnotation(current, desired))
		assert.Equal(t, "T1", desired.Spec.Template.Annotations[deployment.RestartAnnotation])
	})

	t.Run("the live value wins over one already on the desired object", func(t *testing.T) {
		// DELIBERATE, and asserted so a change to it is deliberate too. The hook's contract is "carry over
		// what another writer owns", and the restart annotation is owned by the restart path -- which runs
		// AFTER the apply, via a merge Patch, so the render never legitimately carries one. If a caller
		// ever needs the rendered value to win, that is a new hook, not a quiet edit to this one.
		current := &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{
				"annotations": map[string]any{deployment.RestartAnnotation: "live"},
			}}},
		}}
		desired := renderedDeployment()
		desired.Spec.Template.Annotations = map[string]string{deployment.RestartAnnotation: "rendered"}

		require.NoError(t, deployment.PreserveRestartAnnotation(current, desired))
		assert.Equal(t, "live", desired.Spec.Template.Annotations[deployment.RestartAnnotation])
	})

	t.Run("other pod-template annotations are left alone", func(t *testing.T) {
		// Scope statement: this hook carries ONE key. An annotation a since-removed sidecar injector put
		// on the live object SHOULD disappear when it stops being applied, so a blanket carry-over would
		// be wrong rather than merely broader.
		current := &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{
				"annotations": map[string]any{
					deployment.RestartAnnotation: "T1",
					"sidecar.istio.io/inject":    "true",
				},
			}}},
		}}
		desired := renderedDeployment()

		require.NoError(t, deployment.PreserveRestartAnnotation(current, desired))
		assert.Equal(t, map[string]string{deployment.RestartAnnotation: "T1"}, desired.Spec.Template.Annotations,
			"the hook carried over an annotation other than the restart one")
	})

	t.Run("the wrong desired type is an error, not a silent no-op", func(t *testing.T) {
		// A hook wired onto the shared ApplyOptions would be handed a ClusterRole, a ServiceAccount and a
		// ConfigMap. Returning nil there would look like success while preserving nothing.
		err := deployment.PreserveRestartAnnotation(&unstructured.Unstructured{Object: map[string]any{}},
			&corev1.ConfigMap{})
		require.Error(t, err, "a non-Deployment passed silently, so a mis-wired hook would preserve nothing and say nothing")
		assert.Contains(t, err.Error(), "ConfigMap")
	})

	t.Run("a non-string annotation value is an error", func(t *testing.T) {
		current := &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{
				"annotations": map[string]any{deployment.RestartAnnotation: 42},
			}}},
		}}
		err := deployment.PreserveRestartAnnotation(current, renderedDeployment())
		require.Error(t, err, "a non-string value must surface rather than be dropped")
	})
}
