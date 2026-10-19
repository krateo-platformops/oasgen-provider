package restdefinition

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/krateo-platformops/oasgen-provider/apis"
	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/render"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	discoveryfake "k8s.io/client-go/discovery/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// failingDiscovery stands in for an unreachable apiserver. Only the one method the guard calls is
// implemented; the embedded nil interface would panic on anything else, which is the point -- it keeps
// the stub honest about what the guard is allowed to touch.
type failingDiscovery struct{ discovery.DiscoveryInterface }

func (failingDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	return nil, nil, errors.New("connection refused")
}

func builtInDiscovery() discovery.DiscoveryInterface {
	return &discoveryfake.FakeDiscovery{Fake: &clienttesting.Fake{Resources: []*metav1.APIResourceList{
		{
			GroupVersion: "apps/v1",
			APIResources: []metav1.APIResource{{Name: "deployments", SingularName: "deployment"}},
		},
	}}}
}

func collisionScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(s))
	require.NoError(t, apis.AddToScheme(s))
	return s
}

func newGuardExternal(t *testing.T, disc discovery.DiscoveryInterface, existing ...client.Object) (*external, *record.FakeRecorder) {
	t.Helper()
	rec := record.NewFakeRecorder(20)
	return &external{
		kube: fake.NewClientBuilder().WithScheme(collisionScheme(t)).WithObjects(existing...).Build(),
		log:  logging.NewNopLogger(),
		rec:  rec,
		disc: disc,
	}, rec
}

func renderedCRD(name, group, kind, plural, singular string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: kind, Plural: plural, Singular: singular},
		},
	}
}

func restDefinitionForCollision() *definitionv1alpha1.RestDefinition {
	return &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "github-provider-kog-deployment", Namespace: "krateo-system"},
	}
}

// events drains the recorder.
func events(rec *record.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func hasEventWithReason(evts []string, reason string) bool {
	for _, e := range evts {
		if strings.Contains(e, reason) {
			return true
		}
	}
	return false
}

// Minting a NEW colliding CRD is refused -- this is the half of the problem that can still be prevented.
func TestGuardAgainstBuiltInCollision_RefusesANewCollidingCRD(t *testing.T) {
	e, rec := newGuardExternal(t, builtInDiscovery())
	rendered := &render.Result{
		CRD: renderedCRD("deployments.github.krateo.io", "github.krateo.io", "Deployment", "deployments", "deployment"),
	}

	err := e.guardAgainstBuiltInCollision(context.Background(), restDefinitionForCollision(), rendered)
	require.Error(t, err, "a CRD that would shadow apps/deployments must not be applied")
	assert.Contains(t, err.Error(), "deployments")
	assert.True(t, hasEventWithReason(events(rec), "BuiltInNameCollision"))
}

// Once the colliding CRD is already served, refusing cannot unbreak kubectl -- it would only block every
// future update, and the only remedy (renaming the kind) destroys that CRD's resources. So: warn, proceed.
func TestGuardAgainstBuiltInCollision_WarnsButProceedsWhenAlreadyServed(t *testing.T) {
	served := renderedCRD("deployments.github.krateo.io", "github.krateo.io", "Deployment", "deployments", "deployment")
	e, rec := newGuardExternal(t, builtInDiscovery(), served)
	rendered := &render.Result{
		CRD: renderedCRD("deployments.github.krateo.io", "github.krateo.io", "Deployment", "deployments", "deployment"),
	}

	err := e.guardAgainstBuiltInCollision(context.Background(), restDefinitionForCollision(), rendered)
	require.NoError(t, err, "an already-served collision must not start failing generation")

	evts := events(rec)
	assert.True(t, hasEventWithReason(evts, "BuiltInNameCollisionExisting"), "it must still be reported: %v", evts)
}

// Discovery being unreachable means the guard is ABSENT, not satisfied. Failing closed would turn an
// apiserver hiccup into an outage for every RestDefinition on the cluster.
func TestGuardAgainstBuiltInCollision_FailsOpenWhenDiscoveryIsUnavailable(t *testing.T) {
	e, rec := newGuardExternal(t, failingDiscovery{})
	rendered := &render.Result{
		CRD: renderedCRD("deployments.github.krateo.io", "github.krateo.io", "Deployment", "deployments", "deployment"),
	}

	err := e.guardAgainstBuiltInCollision(context.Background(), restDefinitionForCollision(), rendered)
	require.NoError(t, err, "an unreachable safety net must be stepped over, not treated as a refusal")

	evts := events(rec)
	assert.True(t, hasEventWithReason(evts, "BuiltInCollisionCheckSkipped"),
		"skipping the check silently would claim a clean bill of health it does not have: %v", evts)
}

func TestGuardAgainstBuiltInCollision_AllowsANonCollidingName(t *testing.T) {
	e, rec := newGuardExternal(t, builtInDiscovery())
	rendered := &render.Result{
		CRD: renderedCRD("repositories.github.krateo.io", "github.krateo.io", "Repository", "repositories", "repository"),
	}

	require.NoError(t, e.guardAgainstBuiltInCollision(context.Background(), restDefinitionForCollision(), rendered))
	assert.Empty(t, events(rec), "a clean name must be silent")
}

// The generated Configuration CRD goes through the same guard. Its plural is unlikely to collide, but
// "unlikely" is how `deployments.github.krateo.io` got made.
func TestGuardAgainstBuiltInCollision_ChecksTheConfigurationCRD(t *testing.T) {
	e, _ := newGuardExternal(t, builtInDiscovery())
	rendered := &render.Result{
		CRD:              renderedCRD("repositories.github.krateo.io", "github.krateo.io", "Repository", "repositories", "repository"),
		ConfigurationCRD: renderedCRD("deployments.github.krateo.io", "github.krateo.io", "Deployment", "deployments", "deployment"),
	}

	err := e.guardAgainstBuiltInCollision(context.Background(), restDefinitionForCollision(), rendered)
	require.Error(t, err, "the Configuration CRD must be checked too")
}
