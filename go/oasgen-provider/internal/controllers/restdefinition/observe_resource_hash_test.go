package restdefinition

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/krateo-platformops/oasgen-provider/apis"
	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/deploy"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/objects"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/plurals"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// recordingLogger captures Debug messages so a test can assert WHICH branch of Observe was taken.
// Asserting only on the returned ExternalObservation cannot do that: several branches return the same
// {exists, upToDate} pair for different reasons, and #224 was precisely a branch that was never reached.
type recordingLogger struct {
	msgs  *[]string
	lines *[]string // message plus its key-values, for diagnosing WHERE Observe stopped
}

func (l recordingLogger) record(msg string, kv ...any) {
	*l.msgs = append(*l.msgs, msg)
	if l.lines != nil {
		*l.lines = append(*l.lines, fmt.Sprint(append([]any{msg, " | "}, kv...)...))
	}
}

func (l recordingLogger) Info(msg string, kv ...any)           { l.record(msg, kv...) }
func (l recordingLogger) Debug(msg string, kv ...any)          { l.record(msg, kv...) }
func (l recordingLogger) Warn(msg string, kv ...any)           { l.record(msg, kv...) }
func (l recordingLogger) Error(_ error, msg string, kv ...any) { l.record(msg, kv...) }
func (l recordingLogger) WithValues(_ ...any) logging.Logger   { return l }
func (l recordingLogger) WithName(_ string) logging.Logger     { return l }

// minimal deployment template, so the test does not depend on the asset shipped in the image.
const rdcDeploymentTemplate = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: placeholder
  namespace: placeholder
spec:
  replicas: 1
  selector:
    matchLabels:
      app: placeholder
  template:
    metadata:
      labels:
        app: placeholder
    spec:
      containers:
        - name: rdc
          image: placeholder
`

func restDefinitionForHashTest() *definitionv1alpha1.RestDefinition {
	cr := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "pullrequest", Namespace: "demo-system"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			OASPath:       "configmap://demo-system/pr/openapi.yaml",
			ResourceGroup: "github.krateo.io",
			Resource: definitionv1alpha1.Resource{
				Kind:                   "PullRequest",
				Identifiers:            []string{"number"},
				AdditionalStatusFields: []string{"metadata.id"},
			},
		},
	}
	cr.Status.OASHash = "unchanged"
	cr.Status.ResourceHash = resourceHash(cr)
	return cr
}

// externalForHashTest builds an external whose fake cluster gets Observe as far as the drift checks:
// the CRD exists and the generated Deployment exists and is ready, which are the two gates that would
// otherwise return early.
func externalForHashTest(t *testing.T, cr *definitionv1alpha1.RestDefinition, msgs, lines *[]string) *external {
	t.Helper()

	tmpl := filepath.Join(t.TempDir(), "deployment.yaml")
	require.NoError(t, os.WriteFile(tmpl, []byte(rdcDeploymentTemplate), 0o600))
	prev := RDCtemplateDeploymentPath
	RDCtemplateDeploymentPath = tmpl
	t.Cleanup(func() { RDCtemplateDeploymentPath = prev })

	s := runtime.NewScheme()
	require.NoError(t, apis.AddToScheme(s))
	require.NoError(t, apiextensionsv1.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))

	// crd.Lookup requires the CRD to DECLARE the requested version in spec.versions, not merely to exist
	// under that name -- a CRD without it reads as absent, which is correct (nothing serves that version)
	// and is what a bare ObjectMeta-only fixture gets wrong.
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "pullrequests.github.krateo.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "github.krateo.io",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: "pullrequests", Singular: "pullrequest", Kind: "PullRequest",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1alpha1", Served: true, Storage: true},
			},
		},
	}
	// Build the Deployment fixture with the SAME constructor Observe uses, rather than guessing its name.
	// Observe looks the deployment up by the object CreateK8sObject produces, so a hand-named fixture can
	// silently fail to match and the test then exercises the not-deployed-yet branch instead of the drift
	// checks -- which is how this test first failed.
	gvr := plurals.ToGroupVersionResource(schema.GroupVersionKind{
		Group:   cr.Spec.ResourceGroup,
		Version: observedVersion(cr),
		Kind:    cr.Spec.Resource.Kind,
	})
	dep := &appsv1.Deployment{}
	require.NoError(t, objects.CreateK8sObject(dep, gvr,
		types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name + deploy.ControllerResourceSuffix}, tmpl))
	dep.Spec.Replicas = ptr(int32(1))
	dep.Status = appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}

	kube := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(cr.DeepCopy(), crd, dep).
		Build()

	return &external{
		kube: kube,
		log:  recordingLogger{msgs: msgs, lines: lines},
		rec:  record.NewFakeRecorder(100),
	}
}

func ptr[T any](v T) *T { return &v }

// TestObserveReportsDriftWhenSpecResourceChanged is the regression test for #224.
//
// status.resourceHash existed, Create stored it, and Update compared it and regenerated the CRD -- but
// Observe never consulted it. Since the runtime calls Update only when Observe reports drift, appending
// to additionalStatusFields was accepted, reported "up to date", and did nothing: the CRD kept its old
// status schema while its own RestDefinition described a different one, with no event and no warning.
//
// This asserts Observe REACHES the comparison, which is the part that was missing. A test on the
// comparison alone would reproduce the bug it is meant to catch.
func TestObserveReportsDriftWhenSpecResourceChanged(t *testing.T) {
	cr := restDefinitionForHashTest()
	// The append the append-only CEL rule explicitly invites, leaving status.resourceHash at its old value
	// exactly as a real `kubectl apply` would.
	cr.Spec.Resource.AdditionalStatusFields = append(cr.Spec.Resource.AdditionalStatusFields, "status.failureReason")

	var msgs, lines []string
	e := externalForHashTest(t, cr, &msgs, &lines)

	obs, err := e.Observe(context.Background(), cr)
	require.NoError(t, err)
	t.Logf("observe trace:\n  %s", strings.Join(lines, "\n  "))
	assert.False(t, obs.ResourceUpToDate,
		"appending to additionalStatusFields must report drift, so the runtime calls Update and the CRD is regenerated")
	assert.True(t, obs.ResourceExists, "the CRD is present; this is drift, not absence")
	assert.Contains(t, msgs, "spec.resource changed",
		"the drift must come from the spec.resource comparison specifically, not from an unrelated branch")
}

// TestObserveDoesNotReportSpecDriftWhenUnchanged is the other half: the steady state must not report
// drift through this branch, or every reconcile of a stable install would regenerate the CRD and
// redeploy the controller.
func TestObserveDoesNotReportSpecDriftWhenUnchanged(t *testing.T) {
	cr := restDefinitionForHashTest() // status.resourceHash already matches the spec

	var msgs, lines []string
	e := externalForHashTest(t, cr, &msgs, &lines)

	_, _ = e.Observe(context.Background(), cr)

	assert.NotContains(t, msgs, "spec.resource changed",
		"an unchanged spec.resource must not be reported as drift")
}

// TestObserveReportsDriftWhenResourceHashNeverStored covers the upgrade path. status.resourceHash was
// introduced in 0.27.0; a RestDefinition created before it has the field empty, so the first reconcile
// after this fix reports drift once and Update re-stores it. That is a bounded, self-healing
// regeneration rather than a loop, and it is correct: such a CRD may genuinely be stale.
func TestObserveReportsDriftWhenResourceHashNeverStored(t *testing.T) {
	cr := restDefinitionForHashTest()
	cr.Status.ResourceHash = ""

	var msgs, lines []string
	e := externalForHashTest(t, cr, &msgs, &lines)

	obs, err := e.Observe(context.Background(), cr)
	require.NoError(t, err)
	assert.False(t, obs.ResourceUpToDate, "an unstored hash must regenerate once rather than stay silently stale")
	assert.Contains(t, msgs, "spec.resource changed")
}
