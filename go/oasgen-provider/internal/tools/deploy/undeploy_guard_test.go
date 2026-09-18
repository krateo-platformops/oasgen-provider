package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var repoGVR = schema.GroupVersionResource{Group: "github.krateo.io", Version: "v1alpha1", Resource: "repositories"}
var repoGVK = schema.GroupVersionKind{Group: "github.krateo.io", Version: "v1alpha1", Kind: "Repository"}

func newRepoScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(repoGVK, &unstructured.Unstructured{})
	lg := repoGVK
	lg.Kind += "List"
	s.AddKnownTypeWithName(lg, &unstructured.UnstructuredList{})
	return s
}

func newRepo(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(repoGVK)
	u.SetName(name)
	u.SetNamespace("default")
	return u
}

// TestUndeploy_RefusesToDropCRDWithLiveInstances covers #125, which is the only defect in this repo's
// backlog capable of destroying production assets irreversibly.
//
// Uninstalling a CRD cascade-deletes every CR of that kind, and those CRs carry finalizers that delete
// the EXTERNAL resource. So deleting a RestDefinition could delete real GitHub repositories — no per-CR
// opt-in, nothing to undo it, and triggered by a routine lifecycle operation.
//
// A guard already existed (the restresources-still-exist finalizer) but was consulted only for
// SkipDeploy, never for SkipCRD, and reflected a PREVIOUS reconcile's observation rather than the
// cluster's state at the moment of deletion.
func TestUndeploy_RefusesToDropCRDWithLiveInstances(t *testing.T) {
	logf := func(msg string, kv ...any) {}

	t.Run("live instances: refuses, and says why", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(newRepoScheme(t)).
			WithObjects(newRepo("publish-pet-repo"), newRepo("another")).Build()

		err := Undeploy(context.Background(), kube, UndeployOptions{
			KubeClient: kube, GVR: repoGVR, GVK: repoGVK, Log: logf, SkipCRD: false, SkipDeploy: true,
		})

		if err == nil {
			t.Fatal("Undeploy must REFUSE to uninstall a CRD with live instances: doing so cascade-deletes " +
				"the CRs and their finalizers delete the real external resources (#125)")
		}
		if !errors.Is(err, ErrLiveInstances) {
			t.Errorf("refusal must be identifiable as ErrLiveInstances, got: %v", err)
		}
		if !strings.Contains(err.Error(), "2 live instance") {
			t.Errorf("the error should name how many instances blocked it, got: %v", err)
		}
	})

	t.Run("no instances: proceeds", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(newRepoScheme(t)).Build()

		err := Undeploy(context.Background(), kube, UndeployOptions{
			KubeClient: kube, GVR: repoGVR, GVK: repoGVK, Log: logf, SkipCRD: false, SkipDeploy: true,
		})

		// It gets past the guard. crd.Uninstall against a fake client may still fail for unrelated
		// reasons, so the assertion is narrow: whatever happens, it must NOT be the live-instance refusal.
		if errors.Is(err, ErrLiveInstances) {
			t.Errorf("must not refuse when no instances exist, got: %v", err)
		}
	})

	t.Run("SkipCRD short-circuits the guard entirely", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(newRepoScheme(t)).
			WithObjects(newRepo("publish-pet-repo")).Build()

		err := Undeploy(context.Background(), kube, UndeployOptions{
			KubeClient: kube, GVR: repoGVR, GVK: repoGVK, Log: logf, SkipCRD: true, SkipDeploy: true,
		})

		if errors.Is(err, ErrLiveInstances) {
			t.Errorf("SkipCRD means the caller is not touching the CRD at all, so the guard is irrelevant: %v", err)
		}
	})
}

// TestLiveInstanceCount_NoKindRefuses pins that an unlistable request refuses rather than reporting
// zero. A zero here would authorise dropping the CRD, which is the destructive act this guard exists to
// prevent — so "I could not look" must never be spelled the same way as "there was nothing there".
func TestLiveInstanceCount_NoKindRefuses(t *testing.T) {
	kube := fake.NewClientBuilder().WithScheme(newRepoScheme(t)).WithObjects(newRepo("r")).Build()

	if _, err := liveInstanceCount(context.Background(), kube, schema.GroupVersionKind{}); err == nil {
		t.Error("a missing Kind must be an error, not a count of zero: zero would authorise the delete")
	}
}

// TestLiveInstanceCount_MissingCRDIsZeroNotError pins the one case that must NOT be treated as a
// failure: if the CRD is already gone the kind cannot have instances, so there is nothing to protect.
//
// Every OTHER listing failure must propagate. A count that could not be taken must never read as zero —
// that conflation is what produced the delete-path bugs in #77/#98/#101, and here its cost is
// unrecoverable rather than merely wrong.
func TestLiveInstanceCount_MissingCRDIsZeroNotError(t *testing.T) {
	// A scheme that does not know the kind reproduces "no matches for kind".
	kube := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()

	n, err := liveInstanceCount(context.Background(), kube, repoGVK)
	if err != nil {
		t.Fatalf("a missing CRD means the kind is gone, not an error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 instances for a missing CRD, got %d", n)
	}
}
