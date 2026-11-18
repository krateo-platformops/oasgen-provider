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

// keypairRD is the shape #305 describes: an API whose document declares a version header as `required`
// with a `default`, which is what an author reaches for to pin a constant.
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

const versionHeader = "X-OpenStack-Nova-API-Version"

// TestARequiredParameterWhoseOnlyValueSourceIsADefaultIsReported covers the three arms that matter, and
// the third one is the one that found a bug in the implementation.
//
// WHY THIS IS WORTH A DIAGNOSTIC AT ALL, measured on this fixture before the check existed:
//
//	generation error   <nil>                  -- the RestDefinition goes Ready
//	schema warnings    3, all about response schemas
//	naming the header  NONE
//
// So an author pinning a constant the way OpenAPI invites got a kind that installed cleanly, reported
// Ready, failed every verb with "missing header: X-OpenStack-Nova-API-Version", and emitted three
// warnings pointing at something else entirely.
func TestARequiredParameterWhoseOnlyValueSourceIsADefaultIsReported(t *testing.T) {
	pinned := []definitionv1alpha1.HeaderItem{{Name: versionHeader, Value: "2.2"}}

	t.Run("nothing supplies it: both verbs are reported", func(t *testing.T) {
		w := renderKeypair(t, keypairRD(nil, nil))
		// SETUP CONTROL: the render produced diagnostics at all. Without this, a fixture that failed to
		// parse would give an empty list and an assertion on content could read as a pass.
		require.NotEmpty(t, w, "the render emitted nothing -- the fixture is broken, not the subject")

		hits := findingsNaming(w, versionHeader)
		require.Len(t, hits, 2, "both verbs declare the parameter, so both must be reported: %v", w)
		joined := strings.Join(hits, "\n")
		require.Contains(t, joined, `on action "create"`)
		require.Contains(t, joined, `on action "get"`)
		// The message must carry the runtime failure and the working alternative, or it is a wall.
		require.Contains(t, joined, "missing header: "+versionHeader,
			"the message must name the error the author will actually see")
		require.Contains(t, joined, "verbsDescription[].headers",
			"the message must name the path that DOES work, which is the whole discoverability defect")
	})

	t.Run("both verbs wire it up: nothing is reported", func(t *testing.T) {
		w := renderKeypair(t, keypairRD(pinned, pinned))
		require.NotEmpty(t, w, "the baseline warnings must still be here, or this arm measures nothing")
		require.Empty(t, findingsNaming(w, versionHeader),
			"the parameter IS supplied on both verbs, and a diagnostic that fires anyway is noise: %v", w)
	})

	// THE NEGATIVE CONTROL THE FIRST VERSION LACKED, found by a surviving mutant. Dropping the
	// `Default != nil` guard passed every arm above, because they all count only findings that NAME THE
	// VERSION HEADER -- so a flood of findings about `name`, a required PATH parameter with no default
	// that the request path fills from the CR, was invisible to them.
	//
	// A required parameter with no default is not this defect: there is nothing unreachable about it, and
	// reporting it would fire on almost every document in existence.
	t.Run("a required parameter with NO default is not reported", func(t *testing.T) {
		w := renderKeypair(t, keypairRD(pinned, pinned))
		// `name` is declared required on /os-keypairs/{name} with no default. It must not appear.
		require.Empty(t, findingsNaming(w, `parameter "name"`),
			"a required parameter without a default is reported, which would fire on nearly every "+
				"document and is not this defect at all: %v", w)
	})

	// THE ARM THAT FOUND THE BUG. StaticHeaders is a PER-VERB field, so declaring the header on `create`
	// must leave `get` reported. The first implementation built one supplied-set across every verb and
	// this arm measured ZERO findings where one was predicted -- a suppression that hid a real, every-call
	// failure. Predicting the count before running is what made a wrong answer visible.
	t.Run("only create wires it up: get is still reported", func(t *testing.T) {
		w := renderKeypair(t, keypairRD(pinned, nil))
		hits := findingsNaming(w, versionHeader)
		require.Len(t, hits, 1,
			"exactly the unwired verb must be reported -- 0 means the suppression is resource-wide when "+
				"the declaration is per-verb, 2 means it is not suppressing at all: %v", w)
		require.Contains(t, hits[0], `on action "get"`,
			"the WRONG verb is reported, which would send the author to edit the one that is correct")
	})
}
