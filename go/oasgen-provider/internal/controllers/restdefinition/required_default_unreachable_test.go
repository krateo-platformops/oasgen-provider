//go:build unit

package restdefinition

import (
	"context"
	"os"
	"strings"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/oas2jsonschema"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/render"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// versionHeader carries a SCALAR default. It is sent automatically at request construction, so the kind
// works and this diagnostic must stay quiet about it -- the arm that proves these two changes agree.
const versionHeader = "X-OpenStack-Nova-API-Version"

// structuredHeader carries an OBJECT default, which cannot be placed in a header without inventing a
// serialisation the vendor never specified. That is the only case still worth reporting.
const structuredHeader = "X-Structured"

func keypairRD(createHeaders, getHeaders []definitionv1alpha1.HeaderItem) *definitionv1alpha1.RestDefinition {
	return &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "keypair", Namespace: "demo"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			ResourceGroup: "probe.ogen.krateo.io",
			Resource: definitionv1alpha1.Resource{
				Kind:        "Keypair",
				Identifiers: []string{"name"},
				VerbsDescription: []definitionv1alpha1.VerbsDescription{
					{Action: "create", Method: "POST", Path: "/os-keypairs", Headers: createHeaders},
					{Action: "get", Method: "GET", Path: "/os-keypairs/{name}", Headers: getHeaders},
				},
			},
		},
	}
}

func renderKeypair(t *testing.T, cr *definitionv1alpha1.RestDefinition) []string {
	t.Helper()
	oasText, err := os.ReadFile("testdata/requireddefault/oas.yaml")
	require.NoError(t, err)
	doc, err := oas2jsonschema.NewLibOASParser().Parse(oasText)
	require.NoError(t, err)
	e := externalForWarningTest(t)
	docs := oas2jsonschema.NewDocumentSet(doc)
	require.NoError(t, e.generateAndApplyCRDs(context.Background(), cr, render.TargetGVK(cr, docs.Default()), docs, false),
		"this diagnostic is NON-FATAL: a document that is legal OpenAPI must still install")
	return cr.Status.SchemaWarnings
}

func findingsNaming(warnings []string, name string) []string {
	var out []string
	for _, w := range warnings {
		if strings.Contains(w, name) {
			out = append(out, w)
		}
	}
	return out
}

// TestARequiredDefaultIsReportedOnlyWhenItCannotBeSent is the line between this diagnostic and the
// request-construction change that honours a vendor's default.
//
// A `default` on a request parameter is usually read as "what the server assumes when the value is
// absent". That reading is incoherent with `required: true`, because a required parameter is never
// absent -- so the pair means "this must be present, and here is the value", and a SCALAR default is now
// sent. What remains unsendable is a composite default, and reporting that is this check's whole job.
//
// WITHOUT THE FIRST ARM these two changes could ship contradicting each other: one warning that a kind
// cannot work while the other makes it work.
func TestARequiredDefaultIsReportedOnlyWhenItCannotBeSent(t *testing.T) {
	t.Run("a SCALAR required default is NOT reported, because it is sent", func(t *testing.T) {
		w := renderKeypair(t, keypairRD(nil, nil))
		// SETUP CONTROL: the render produced diagnostics at all, so an empty findings list below means
		// "nothing to report" rather than "the fixture never rendered".
		require.NotEmpty(t, w, "the render emitted nothing -- the fixture is broken, not the subject")
		require.Empty(t, findingsNaming(w, versionHeader),
			"a scalar default is honoured at request construction, so the kind works and a warning here "+
				"would contradict it: %v", w)
	})

	t.Run("a NON-SCALAR required default IS reported", func(t *testing.T) {
		w := renderKeypair(t, keypairRD(nil, nil))
		hits := findingsNaming(w, structuredHeader)
		require.Len(t, hits, 1,
			"the object default is declared on the create path only, and it cannot be sent: %v", w)
		require.Contains(t, hits[0], `on action "create"`)
		require.Contains(t, hits[0], "missing header: "+structuredHeader,
			"the message must name the error the author will actually see")
		require.Contains(t, hits[0], "verbsDescription[].headers",
			"the message must name the path that DOES work, which is the whole discoverability defect")
		require.Contains(t, hits[0], "not a scalar",
			"the message must say WHY this one cannot be sent when a scalar can, or a reader who knows "+
				"scalars are honoured will read this as a bug in that")
	})

	// THE NEGATIVE CONTROL, which a surviving mutant found: dropping the `Default != nil` guard passed
	// every other arm, because they all count findings that NAME ONE PARAMETER -- a flood of findings
	// about `name`, a required path parameter with no default, was invisible to them.
	t.Run("a required parameter with NO default is not reported", func(t *testing.T) {
		w := renderKeypair(t, keypairRD(nil, nil))
		require.Empty(t, findingsNaming(w, `parameter "name"`),
			"a required parameter without a default is reported, which would fire on nearly every "+
				"document and is not this defect at all: %v", w)
	})

	// PER-VERB SUPPRESSION, the arm that found a real bug: the supplied-set was first built across every
	// verb, so declaring a header on one verb silently suppressed the finding on another. StaticHeaders
	// is a per-verb field, so that suppression hid an every-call failure.
	t.Run("declaring the value on the verb suppresses the finding", func(t *testing.T) {
		w := renderKeypair(t, keypairRD(
			[]definitionv1alpha1.HeaderItem{{Name: structuredHeader, Value: "{}"}}, nil))
		require.NotEmpty(t, w, "the baseline warnings must still be here, or this arm measures nothing")
		require.Empty(t, findingsNaming(w, structuredHeader),
			"the author supplied the value explicitly, and a diagnostic that fires anyway is noise: %v", w)
	})
}
