//go:build unit || integration

package deploy

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// ISSUE 599: the restart gate shipped in #590 never fired, for any kind, in the whole of 0.59.0.
//
// Deploy built ONE NamespacedName -- "<rd>-controller" -- and used it both for the Deployment and for
// the live ConfigMap read. But the two assets render their names differently:
//
//	assets/rdc/configmap.yaml   ->  name: {{ .name }}-configmap
//	assets/rdc/deployment.yaml  ->  name: {{ .name }}
//
// So the read asked for a ConfigMap at the DEPLOYMENT's name, got NotFound every time, and set
// cmExisted=false. restartNeeded then returned false unconditionally.
//
// It was silent because the NotFound is deliberately tolerated, with a true-sounding reason: "an absent
// ConfigMap means this is the first deploy". That is correct for the case it was written for and
// indistinguishable from "I looked up the wrong name" -- so a 100% failure reported success to every
// layer above it.
//
// Measured on GKE by azuredevops-provider-kog, 0.59.0, 25 controllers, two rdc.env flips, 400s settle:
// 50 of 50 rows "rollouts: 0", one ReplicaSet per Deployment created at install, all pods restartCount 0
// with install-time startedAt, and ZERO occurrences of "Restarting the generated controller" in a fully
// covered log window. envFrom is read at container start, so all 25 kept their install-time environment.

const renderedCMName = "petstore-controller-configmap"

// the name the PRE-FIX code read at: cmNSName.Name, which is the Deployment's name
const deploymentName = "petstore-controller"

func cmScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	return s
}

func cmAt(name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "krateo-system"},
		Data:       data,
	}
}

// renderedCM stands in for what templates.CreateK8sObject produces from the asset: the name carries the
// `-configmap` suffix the asset appends.
func renderedCM(data map[string]string) *corev1.ConfigMap { return cmAt(renderedCMName, data) }

func TestTheLiveConfigMapIsReadAtTheNameTheApplyWillWrite(t *testing.T) {
	ctx := context.Background()
	oldData := map[string]string{"HOME": "/tmp"}

	t.Run("it finds the object the apply is about to replace", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(cmScheme(t)).
			WithObjects(cmAt(renderedCMName, oldData)).Build()

		data, existed, err := liveConfigMapData(ctx, kube, renderedCM(map[string]string{"HOME": "/tmp", "REST_CONTROLLER_DEBUG": "true"}))

		require.NoError(t, err)
		assert.True(t, existed,
			"the live ConfigMap was not found at the rendered object's own name, so every reconcile reads "+
				"this as a first deploy and the generated controller is never restarted (#599)")
		assert.Equal(t, oldData, data,
			"the PREVIOUS data must come back: it is the only thing that can say whether the pod's "+
				"environment changed, and the apply is about to overwrite it")
	})

	// THE CONTROL, and it is what makes the arm above mean something.
	//
	// Pre-#599 the read was keyed from cmNSName.Name -- the Deployment's name. Here ONLY that name is
	// seeded, so a lookup at it would succeed and a lookup at the rendered name must not. Without this
	// arm the test above would pass equally against a read keyed from the wrong name, as long as the
	// fixture happened to be seeded at that name too.
	t.Run("CONTROL: the Deployment's name is a different object and must not satisfy the read", func(t *testing.T) {
		require.NotEqual(t, deploymentName, renderedCMName,
			"premise: the two names must differ, or this control cannot distinguish them")

		kube := fake.NewClientBuilder().WithScheme(cmScheme(t)).
			WithObjects(cmAt(deploymentName, oldData)).Build()

		_, existed, err := liveConfigMapData(ctx, kube, renderedCM(nil))

		require.NoError(t, err)
		assert.False(t, existed,
			"a ConfigMap at the DEPLOYMENT's name satisfied the read, so the read is still keyed from the "+
				"wrong name and #599 is back")
	})

	t.Run("an absent ConfigMap is a first deploy, not an error", func(t *testing.T) {
		// A pod that has not started cannot be holding stale environment, and the Deployment created
		// alongside starts with the new ConfigMap already in place.
		kube := fake.NewClientBuilder().WithScheme(cmScheme(t)).Build()

		data, existed, err := liveConfigMapData(ctx, kube, renderedCM(map[string]string{"A": "1"}))

		require.NoError(t, err, "a genuinely absent ConfigMap must not fail the deploy")
		assert.False(t, existed)
		assert.Nil(t, data)
	})

	// THE #599 FAILURE MODE, as its own assertion: a lookup that could not be performed must never be
	// reported as an absence. This is the difference between "there is no previous ConfigMap" and "I
	// could not find out", and conflating them is what cost a release.
	t.Run("a non-NotFound failure is surfaced, never read as an absence", func(t *testing.T) {
		kube := fake.NewClientBuilder().WithScheme(cmScheme(t)).
			WithObjects(cmAt(renderedCMName, oldData)).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					return apierrors.NewInternalError(assert.AnError)
				},
			}).Build()

		_, existed, err := liveConfigMapData(ctx, kube, renderedCM(nil))

		require.Error(t, err,
			"a read that FAILED was reported as a successful absence. The caller then treats it as a first "+
				"deploy and skips the restart, which is #599's exact shape: a true-sounding reason for an "+
				"absence hiding a lookup that never happened")
		assert.False(t, existed)
	})

	t.Run("a rendered ConfigMap with no name is refused", func(t *testing.T) {
		// Without this, an asset that lost its metadata.name would make every read look up the empty
		// name, miss, and silently disable the restart again -- the same bug through a different door.
		kube := fake.NewClientBuilder().WithScheme(cmScheme(t)).Build()

		_, existed, err := liveConfigMapData(ctx, kube, cmAt("", nil))

		require.Error(t, err, "an unnamed rendered object was looked up instead of being refused")
		assert.False(t, existed)
	})
}

// WHY KEYING FROM THE RENDERED OBJECT IS THE FIX, rather than appending "-configmap" in Go.
//
// Asserted on the assets themselves, following the source-level precedent in this package
// (TestTheRestartDecisionTakesNoRBACInput). The two name expressions genuinely differ, so any code that
// reuses one NamespacedName for both objects is wrong -- and will be wrong again the next time an
// asset's name expression changes. Re-deriving the suffix in Go would put that knowledge in two places.
func TestTheConfigMapAndDeploymentAssetsDoNotShareAName(t *testing.T) {
	const assets = "../../../../../helm/oasgen-provider/assets/rdc/"

	cmSrc, err := os.ReadFile(assets + "configmap.yaml")
	require.NoError(t, err, "the ConfigMap asset moved; this test and the comment in deploy.go name its path")
	depSrc, err := os.ReadFile(assets + "deployment.yaml")
	require.NoError(t, err)

	nameLine := func(src string) string {
		for _, l := range strings.Split(src, "\n") {
			if t := strings.TrimSpace(l); strings.HasPrefix(t, "name:") {
				return t
			}
		}
		return ""
	}

	cmName, depName := nameLine(string(cmSrc)), nameLine(string(depSrc))
	require.NotEmpty(t, cmName, "no metadata.name line found in the ConfigMap asset")
	require.NotEmpty(t, depName, "no metadata.name line found in the Deployment asset")

	assert.NotEqual(t, depName, cmName,
		"the two assets now render the same name expression (%q). If that is deliberate, the live-read "+
			"comment in deploy.go about #599 needs updating; if it is accidental, one of the two objects "+
			"is about to collide with the other.", cmName)
	assert.Contains(t, cmName, "-configmap",
		"the ConfigMap asset's name no longer carries the -configmap suffix (%q). Nothing in Go depends on "+
			"that suffix any more -- the live read is keyed from the rendered object -- but this is the "+
			"difference that made #599 possible, so a change to it should be noticed here.", cmName)
}

// A guard so the GVR/NamespacedName plumbing cannot drift back: the value handed to the renderer must
// still be the "<rd>-controller" base, because the asset appends the suffix itself.
func TestTheControllerResourceSuffixIsNotTheConfigMapSuffix(t *testing.T) {
	assert.Equal(t, "-controller", ControllerResourceSuffix)
	assert.NotContains(t, ControllerResourceSuffix, "configmap",
		"ControllerResourceSuffix now contains the ConfigMap suffix, which means the renderer is being "+
			"handed a name that already carries it and the rendered object will be double-suffixed")
	_ = schema.GroupVersionResource{}
}
