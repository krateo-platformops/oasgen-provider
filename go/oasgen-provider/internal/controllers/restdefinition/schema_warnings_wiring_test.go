//go:build unit

package restdefinition

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/krateo-platformops/oasgen-provider/apis"
	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/oas2jsonschema"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/render"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// WHY THIS EXISTS SEPARATELY FROM THE renderWarnings TESTS, and it is the whole point of the file:
//
// renderWarnings' own arms and the snapshot arm all pass with the assignment at the render site DELETED.
// Measured: with the package suite green as a baseline, removing `cr.Status.SchemaWarnings = ...` left the
// ENTIRE suite green. A test on the formatter alone reproduces the bug it is meant to catch -- the same
// shape as #224, where status.resourceHash existed and Observe never consulted it.
//
// So this arm exercises generateAndApplyCRDs itself, which is the only place the field is written.
func restDefinitionWithABadExclusion() *definitionv1alpha1.RestDefinition {
	return &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "widget", Namespace: "demo"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			ResourceGroup: "warn.ogen.krateo.io",
			Resource: definitionv1alpha1.Resource{
				Kind:        "Widget",
				Identifiers: []string{"name"},
				// The declaration names a field the document does not have. Non-fatal by design, and the
				// exact string from #462's searchable symptoms.
				ExcludedSpecFields: []string{"this_field_does_not_exist"},
				VerbsDescription: []definitionv1alpha1.VerbsDescription{
					{Action: "get", Method: "GET", Path: "/widgets/{name}"},
					{Action: "create", Method: "POST", Path: "/widgets/{name}"},
				},
			},
		},
	}
}

func externalForWarningTest(t *testing.T) *external {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, apis.AddToScheme(s))
	require.NoError(t, apiextensionsv1.AddToScheme(s))
	return &external{
		kube: fake.NewClientBuilder().WithScheme(s).Build(),
		log:  logging.NewNopLogger(),
		rec:  record.NewFakeRecorder(100),
	}
}

func docSetFor(t *testing.T) (*oas2jsonschema.DocumentSet, *definitionv1alpha1.RestDefinition) {
	t.Helper()
	oasText, err := os.ReadFile("testdata/schemawarnings/oas.yaml")
	require.NoError(t, err)
	doc, err := oas2jsonschema.NewLibOASParser().Parse(oasText)
	require.NoError(t, err)
	return oas2jsonschema.NewDocumentSet(doc), restDefinitionWithABadExclusion()
}

// The regression test for #462: a render that produces warnings must leave them on status.
func TestGenerateAndApplyCRDsRecordsSchemaWarningsOnStatus(t *testing.T) {
	e := externalForWarningTest(t)
	docs, cr := docSetFor(t)
	require.Empty(t, cr.Status.SchemaWarnings, "the fixture must start clean or this proves nothing")

	err := e.generateAndApplyCRDs(context.Background(), cr, render.TargetGVK(cr, docs.Default()), docs, false)
	require.NoError(t, err, "a bad excludedSpecFields entry is non-fatal; generation must still succeed")

	require.NotEmpty(t, cr.Status.SchemaWarnings,
		"the render produced a warning and status must carry it -- an Event alone expires in an hour "+
			"while the declaration stays wrong until someone edits it")
	joined := strings.Join(cr.Status.SchemaWarnings, "\n")
	require.Contains(t, joined, "this_field_does_not_exist",
		"the message must name the offending field, which is what makes it actionable")
	require.Contains(t, joined, "excludedSpecFields",
		"and name the declaration it came from")
}

// THE CLEARING CASE, exercised through the real render rather than through the formatter.
//
// A definition whose declarations are fixed must go quiet. Asserted here with a CR that starts with a
// stale warning on its status and a render that produces none: if the assignment were conditional on
// `len(warnings) > 0`, the stale entry would survive and the surface would never be trustworthy.
func TestGenerateAndApplyCRDsClearsStaleSchemaWarnings(t *testing.T) {
	e := externalForWarningTest(t)
	docs, cr := docSetFor(t)
	cr.Spec.Resource.ExcludedSpecFields = nil // the declaration is now correct
	cr.Status.SchemaWarnings = []string{"generation error: field 'this_field_does_not_exist' set in excludedSpecFields not found in schema"}

	err := e.generateAndApplyCRDs(context.Background(), cr, render.TargetGVK(cr, docs.Default()), docs, false)
	require.NoError(t, err)

	require.Empty(t, cr.Status.SchemaWarnings,
		"a clean render must clear the field; a surface that cannot go quiet is one nobody checks")
}

// And it must not touch readiness, because these warnings are deliberately non-fatal.
func TestGenerateAndApplyCRDsLeavesConditionsAloneWhenWarning(t *testing.T) {
	e := externalForWarningTest(t)
	docs, cr := docSetFor(t)
	before := cr.Status.ConditionedStatus.DeepCopy()

	require.NoError(t, e.generateAndApplyCRDs(context.Background(), cr, render.TargetGVK(cr, docs.Default()), docs, false))

	require.NotEmpty(t, cr.Status.SchemaWarnings, "the control: this arm is only meaningful if a warning occurred")
	require.Equal(t, before, cr.Status.ConditionedStatus.DeepCopy(),
		"recording warnings must not alter any condition -- failing a working install over a declaration "+
			"naming a missing field is worse than the warning")
}
