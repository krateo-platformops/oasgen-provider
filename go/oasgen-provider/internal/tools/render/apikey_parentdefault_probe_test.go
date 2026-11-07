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

// THIS CONTROL WAS INVERTED WHEN oasgen SET crdgen's NoParentDefaults, and the rename is the point.
//
// It used to assert that `render.CRDs` STAMPS this shape, as a positive control proving the apiKey probe's
// `false` was not an unproven zero. oasgen now sets Options.NoParentDefaults, because a vendor's `default`
// DESCRIBES what the server assumes rather than instructing us to send it -- so through our render path a
// stamp must now never appear, and the old assertion asserted behaviour we deliberately turned off.
//
// ITS OLD NAME SAID "MUST STAMP", WHICH READS AS INTENT AND IS NOT. A consumer session read exactly this
// test, took "MUST stamp" as evidence that the stamping was wanted product behaviour, and was about to
// report it as such before opening the file -- and the probe it was reading was its own. A control's name
// states what the INSTRUMENT must be able to see, never what the product should do; this one now says so.
//
// The instrument proof moved rather than vanished: TestControlTheSameShapeIsStampedWhenTheFlagIsUnset in
// no_parent_defaults_test.go calls crdgen directly with the flag UNSET and requires both the stamp and the
// plumbing#84 warning. That is where "the detector can see a stamp" belongs, because it must not depend on
// a flag oasgen sets.
func TestTheRenderPathNeverStampsAnOptionalParent(t *testing.T) {
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

	require.False(t, stamped,
		"render.CRDs must NOT stamp an optional parent: a vendor default describes what the server assumes, "+
			"RDC sends top-level spec values whole, and a materialised value cannot afterwards be told apart "+
			"from an authored one. Set crdgen.Options.NoParentDefaults at BOTH call sites in render.go.")

	// AND NO STAMP WARNING EITHER, for the RESOURCE CRD that this arm renders.
	//
	// THIS ARM DOES NOT COVER THE SECOND CALL SITE, and the first version of this comment claimed it did.
	// Measured: removing NoParentDefaults from the Configuration literal alone left this test PASSING.
	// render.go builds two separate crdgen.Options literals and this fixture declares no configurationFields,
	// so its Configuration CRD is never rendered here at all. The claim was plausible and false, which is
	// the shape worth naming: a comment asserting a safety the test does not provide stops the next person
	// looking. TestTheConfigurationCRDAlsoNeverStampsAnOptionalParent is what actually covers that site, and
	// it was written only because the mutation survived.
	for _, w := range res.GenerationWarnings {
		require.NotContains(t, w.Error(), "given default",
			"a stamp warning survived with NoParentDefaults set, so at least one crdgen.Options literal in "+
				"render.go still omits the flag")
	}
}

// THE REQUIRED+DEFAULT INTERACTION, which is what makes oasgen's one authored default do real work.
//
// `header` is REQUIRED on the apiKey shape and also carries the authored default. Those are not in
// tension: Kubernetes applies structural defaults BEFORE validation, so a Configuration that omits
// `header` is defaulted to the scheme's declared name and then passes the required check. The authored
// default turns a required field into an effectively optional one.
//
// Pinned because no test asserted it, the behaviour is load-bearing for every apiKey consumer, and the
// comment on applyAPIKeyHeaderDefault's multi-scheme branch ("the field stays required") reads as though
// required were the thing that changes between the two branches. It is not -- `header` is required in
// both; what changes is whether a default satisfies it.
//
// Measured against a real fleet by the openstack-ironic-operator-kog session, whose Ironic documents
// declare apiKey exclusively (7 of 7, 0 bearer) and whose served Configuration CRDs carry
// `apiKey.required = [header, tokenRef]` with `default: "X-Auth-Token"` on 4 of 5.
func TestApiKeyHeaderIsBothRequiredAndDefaulted(t *testing.T) {
	oasText, err := os.ReadFile("testdata/parentdefault/apikey.yaml")
	require.NoError(t, err)

	cr := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "probe3", Namespace: "default"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			ResourceGroup: "probe3.ogen.krateo.io",
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
	require.NotNil(t, res.ConfigurationCRD)

	blob, err := json.MarshalIndent(res.ConfigurationCRD, "", "  ")
	require.NoError(t, err)
	var root map[string]any
	require.NoError(t, json.Unmarshal(blob, &root))

	// Walk to the apiKey shape rather than asserting on the serialised blob, so a key moving is a failure
	// rather than a silent pass on a substring that happens to still appear somewhere.
	apiKey := findSchemaNode(t, root, "apiKey")
	require.NotNil(t, apiKey, "the generated Configuration CRD must carry an apiKey shape")

	req, _ := apiKey["required"].([]any)
	var reqNames []string
	for _, r := range req {
		if s, ok := r.(string); ok {
			reqNames = append(reqNames, s)
		}
	}
	props, _ := apiKey["properties"].(map[string]any)
	hdr, _ := props["header"].(map[string]any)
	require.NotNil(t, hdr, "apiKey must declare a header property")

	t.Logf("apiKey.required      = %v", reqNames)
	t.Logf("apiKey.header.default = %v", hdr["default"])

	require.Contains(t, reqNames, "header",
		"header is required -- if this stops being true the authored default is no longer doing the work this test describes")
	require.Equal(t, "X-Probe-Token", hdr["default"],
		"the authored default must be present, because it is what satisfies the required check when header is omitted")
}

// findSchemaNode returns the first schema node under root having a property of the given name's shape,
// searching by key rather than by a fixed path so a nesting change surfaces as a nil rather than a panic.
func findSchemaNode(t *testing.T, node any, key string) map[string]any {
	t.Helper()
	switch n := node.(type) {
	case map[string]any:
		if props, ok := n["properties"].(map[string]any); ok {
			if hit, ok := props[key].(map[string]any); ok {
				return hit
			}
		}
		for _, v := range n {
			if got := findSchemaNode(t, v, key); got != nil {
				return got
			}
		}
	case []any:
		for _, v := range n {
			if got := findSchemaNode(t, v, key); got != nil {
				return got
			}
		}
	}
	return nil
}
