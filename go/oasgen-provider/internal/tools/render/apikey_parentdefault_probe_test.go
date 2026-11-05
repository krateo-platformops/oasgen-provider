//go:build unit

package render_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/oas2jsonschema"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/render"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Does an apiKey scheme get `default: {}` stamped on the Configuration CRD's authentication subtree?
//
// configuration_builder.go:258 is the ONLY place oasgen authors a schema default of its own -- every other
// Default assignment copies the vendor's. It fires only for apiKey, and NO document in this repo declares
// one (12 declare bearer), so the path has never been rendered by a test.
func TestApiKeyAuthoredDefaultAndParentStamping(t *testing.T) {
	oasText, err := os.ReadFile("testdata/parentdefault/apikey.yaml")
	require.NoError(t, err)

	cr := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "probe", Namespace: "default"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			ResourceGroup: "probe.ogen.krateo.io",
			Resource: definitionv1alpha1.Resource{
				Kind: "Widget",
				VerbsDescription: []definitionv1alpha1.VerbsDescription{
					{Action: "get", Method: "GET", Path: "/widgets/{id}"},
					{Action: "create", Method: "POST", Path: "/widgets/{id}"},
				},
			},
		},
	}

	doc, err := oas2jsonschema.NewLibOASParser().Parse(oasText)
	require.NoError(t, err)
	res, err := render.CRDs(context.Background(), cr, render.TargetGVK(cr, doc), oas2jsonschema.NewDocumentSet(doc), render.HasSecuritySchemes(doc))
	require.NoError(t, err)
	require.NotNil(t, res.ConfigurationCRD, "an apiKey scheme must produce a Configuration CRD")

	blob, err := json.MarshalIndent(res.ConfigurationCRD, "", "  ")
	require.NoError(t, err)

	t.Logf("authored default present (the X-Probe-Token header): %v", strings.Contains(string(blob), "X-Probe-Token"))
	t.Logf("`\"default\": {}` stamped anywhere:                  %v", strings.Contains(string(blob), `"default": {}`))
	for _, w := range res.GenerationWarnings {
		t.Logf("  WARNING: %v", w)
	}
	t.Logf("total generation warnings: %d", len(res.GenerationWarnings))

	// Print the authentication subtree so the shape is on the record either way.
	var m map[string]any
	require.NoError(t, json.Unmarshal(blob, &m))
	for _, line := range strings.Split(string(blob), "\n") {
		if strings.Contains(line, `"default"`) || strings.Contains(line, "X-Probe-Token") {
			t.Logf("    %s", strings.TrimRight(line, " "))
		}
	}
}

// POSITIVE CONTROL. Without this, the `false` above is an unproven zero: an instrument that cannot
// report a stamped parent reports none whether or not one exists.
//
// `opts` is an optional object whose child `verbose` carries `default: false`, and it declares no required
// properties -- the exact predicate crdgen's materializeDefaultedParents matches. This MUST stamp, and the
// census warning MUST name the path. If it does not, the probe above proves nothing.
func TestPositiveControl_AnOptionalObjectWithADefaultedChildMustStamp(t *testing.T) {
	oasText, err := os.ReadFile("testdata/parentdefault/stamped.yaml")
	require.NoError(t, err)

	cr := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "probe2", Namespace: "default"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			ResourceGroup: "probe2.ogen.krateo.io",
			Resource: definitionv1alpha1.Resource{
				Kind: "Widget",
				VerbsDescription: []definitionv1alpha1.VerbsDescription{
					{Action: "get", Method: "GET", Path: "/widgets/{id}"},
					{Action: "create", Method: "POST", Path: "/widgets/{id}"},
				},
			},
		},
	}

	doc, err := oas2jsonschema.NewLibOASParser().Parse(oasText)
	require.NoError(t, err)
	res, err := render.CRDs(context.Background(), cr, render.TargetGVK(cr, doc), oas2jsonschema.NewDocumentSet(doc), render.HasSecuritySchemes(doc))
	require.NoError(t, err)

	blob, err := json.MarshalIndent(res.CRD, "", "  ")
	require.NoError(t, err)
	stamped := strings.Contains(string(blob), `"default": {}`)
	t.Logf("`\"default\": {}` stamped: %v", stamped)
	for _, w := range res.GenerationWarnings {
		t.Logf("  WARNING: %v", w)
	}

	require.True(t, stamped,
		"the control did not stamp -- so the apiKey probe's `false` is an unproven zero and must not be reported")
	var named bool
	for _, w := range res.GenerationWarnings {
		if strings.Contains(w.Error(), "default {}") || strings.Contains(w.Error(), "given default") {
			named = true
		}
	}
	require.True(t, named,
		"crdgen stamped a parent but the wired warning channel did not report it -- the census would be blind")
}
