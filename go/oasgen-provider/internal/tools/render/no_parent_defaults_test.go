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
	"github.com/krateo-platformops/plumbing/crdgen"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// A document carrying the EXACT shape crdgen stamps: `properties` is an optional object (its parent
// declares no `required` naming it), it has no required properties of its own, and a descendant is
// defaulted. The defaulted child is named `default` on purpose -- that is the real payload measured on
// a vendor API, documented "Only one default vpc for region is admissible".
const shapedSpec = `
openapi: 3.0.0
info: {title: vpcs, version: "1.0"}
components:
  schemas:
    VpcPropertiesDto:
      type: object
      properties:
        default: {type: boolean, default: true, description: "Only one default vpc for region is admissible."}
        preset: {type: boolean, default: false}
    VpcDto:
      type: object
      properties:
        name: {type: string}
        properties: {$ref: '#/components/schemas/VpcPropertiesDto'}
      required: [name]
paths:
  /vpcs:
    post:
      operationId: createVpc
      requestBody: {content: {application/json: {schema: {$ref: '#/components/schemas/VpcDto'}}}}
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/VpcDto'}}}}
  /vpcs/{id}:
    get:
      operationId: getVpc
      parameters: [{name: id, in: path, required: true, schema: {type: string}}]
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: '#/components/schemas/VpcDto'}}}}
`

const shapedRD = `
apiVersion: ogen.krateo.io/v1alpha1
kind: RestDefinition
metadata: {name: vpc, namespace: default}
spec:
  resourceGroup: example.com
  oasPath: /dev/null
  resource:
    kind: Vpc
    identifiers: [name]
    verbsDescription:
      - action: create
        method: POST
        path: /vpcs
      - action: get
        method: GET
        path: /vpcs/{id}
`

func renderShaped(t *testing.T) []byte {
	t.Helper()
	rdJSON, err := yaml.YAMLToJSON([]byte(shapedRD))
	require.NoError(t, err)
	cr := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, json.Unmarshal(rdJSON, cr))

	doc, err := oas2jsonschema.NewLibOASParser().Parse([]byte(shapedSpec))
	require.NoError(t, err)

	res, err := render.CRDs(context.Background(), cr, render.TargetGVK(cr, doc),
		oas2jsonschema.NewDocumentSet(doc), render.HasSecuritySchemes(doc))
	require.NoError(t, err)
	require.NotNil(t, res.CRD)

	out, err := json.Marshal(res.CRD)
	require.NoError(t, err)
	return out
}

// THE ASSERTION ON OUR OWN ARTIFACT.
//
// THE GOLDEN TEST CANNOT MAKE THIS CLAIM. None of this repo's 18 documents carries the shape -- the
// screened count is 0 -- so the golden CRD is byte-identical whether NoParentDefaults is set or not.
// Asserting through the golden would be an arm that passes for a reason unrelated to the flag, which is
// the same vacuity that let a mutant survive in #484 earlier today.
func TestAVendorDefaultIsNotMaterialisedOntoAnOmittedParent(t *testing.T) {
	got := renderShaped(t)

	// The child default is DOCUMENTATION and may stay in the schema; what must not appear is a default
	// on the optional PARENT, which is what makes the apiserver materialise it.
	var crd map[string]any
	require.NoError(t, json.Unmarshal(got, &crd))

	props := dig(t, crd, "spec", "versions")
	versions, ok := props.([]any)
	require.True(t, ok, "versions should be a list")
	require.NotEmpty(t, versions)

	specSchema := dig(t, versions[0].(map[string]any), "schema", "openAPIV3Schema", "properties", "spec")
	specMap, ok := specSchema.(map[string]any)
	require.True(t, ok)

	inner, ok := specMap["properties"].(map[string]any)
	require.True(t, ok, "spec should carry properties")

	parent, ok := inner["properties"].(map[string]any)
	require.True(t, ok, "the shaped document's optional object should be present as spec.properties; "+
		"if this fails the FIXTURE stopped exercising the shape, not the flag")

	_, stamped := parent["default"]
	require.False(t, stamped,
		"spec.properties must NOT carry `default: {}`. A vendor's default DESCRIBES what the server "+
			"assumes; materialising it makes the apiserver store values the author never wrote, RDC "+
			"sends top-level spec values whole, and the write is IRREVERSIBLE -- reverting the CRD does "+
			"not remove it and managedFields never owns it. got parent: %v", parent)
}

// THE CONTROL THAT PROVES THE ASSERTION CAN FAIL.
//
// Without this, "no default on the parent" is indistinguishable from a fixture that never had the shape,
// a walker that looks in the wrong place, or a crdgen that stopped stamping altogether. It calls crdgen
// directly with NoParentDefaults UNSET on the same spec schema our renderer produced, and requires the
// stamp to appear -- so the fixture, the path and the library behaviour are all shown to be live.
func TestControlTheSameShapeIsStampedWhenTheFlagIsUnset(t *testing.T) {
	// The same shape as the fixture, hand-built: an optional object (the parent's `required` names only
	// `name`), no required properties of its own, a defaulted descendant. This isolates the LIBRARY's
	// behaviour -- the positive arm above is what proves our renderer still emits the shape, by asserting
	// spec.properties is present before looking for a default on it.
	specSchema := []byte(`{
	  "type": "object",
	  "properties": {
	    "name": {"type": "string"},
	    "properties": {
	      "type": "object",
	      "properties": {
	        "default": {"type": "boolean", "default": true},
	        "preset":  {"type": "boolean", "default": false}
	      }
	    }
	  },
	  "required": ["name"]
	}`)

	out, warnings, err := crdgen.GenerateWithWarnings(crdgen.Options{
		Group:      "example.com",
		Version:    "v1alpha1",
		Kind:       "Vpc",
		SpecSchema: specSchema,
		Managed:    true,
		// NoParentDefaults deliberately UNSET.
	})
	require.NoError(t, err)

	// crdgen.GenerateWithWarnings returns YAML, not JSON. The first version of this control matched a
	// JSON string and FAILED -- which is the control earning its place: a wrong assertion in the arm that
	// is supposed to prove the other arm can fail is worse than no control at all.
	require.Contains(t, string(out), "default: {}",
		"CONTROL FAILED: crdgen did not stamp this shape even with the flag unset, so the positive arm "+
			"above proves nothing -- the shape or the library changed. warnings: %v", warnings)

	found := false
	for _, w := range warnings {
		if strings.Contains(w, "given default {}") {
			found = true
		}
	}
	require.True(t, found, "crdgen should report every stamp as a warning (plumbing#84); got %v", warnings)
}

func dig(t *testing.T, m map[string]any, path ...string) any {
	t.Helper()
	var cur any = m
	for _, p := range path {
		asMap, ok := cur.(map[string]any)
		require.True(t, ok, "not a map at %q", p)
		cur, ok = asMap[p]
		require.True(t, ok, "missing key %q", p)
	}
	return cur
}

// THE SECOND CALL SITE. render.go builds two crdgen.Options literals -- one for the resource CRD and one
// for the Configuration CRD -- and nothing shares them.
//
// MUTATING THE SECOND SITE ALONE BROKE NO TEST until this existed. Removing `NoParentDefaults` from the
// resource literal failed two arms immediately; removing it from the Configuration literal passed
// everything, including the control whose comment claimed it would catch exactly that. A comment asserting
// a safety the test does not provide is worse than no comment, because it stops the next person looking.
//
// The shape here is the commonest way this arises in practice, not a contrivance: a query parameter with a
// default. oasgen nests the configuration schema as {query: {get: {api-version: ...}}} and builds `query`
// and `get` ITSELF with no required list, so both are optional objects with a defaulted descendant. The
// very comment in configuration_builder.go describes a real fleet whose `api-version` defaults to "1".
func TestTheConfigurationCRDAlsoNeverStampsAnOptionalParent(t *testing.T) {
	oasText, err := os.ReadFile("testdata/parentdefault/configdefault.yaml")
	require.NoError(t, err)

	cr := &definitionv1alpha1.RestDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "cfgprobe", Namespace: "default"},
		Spec: definitionv1alpha1.RestDefinitionSpec{
			ResourceGroup: "cfgprobe.ogen.krateo.io",
			Resource: definitionv1alpha1.Resource{
				Kind: "Widget",
				VerbsDescription: []definitionv1alpha1.VerbsDescription{
					{Action: "get", Method: "GET", Path: "/widgets/{id}"},
					{Action: "create", Method: "POST", Path: "/widgets/{id}"},
				},
				ConfigurationFields: []definitionv1alpha1.ConfigurationField{{
					FromOpenAPI:        definitionv1alpha1.FromOpenAPI{Name: "api-version", In: "query"},
					FromRestDefinition: definitionv1alpha1.FromRestDefinition{Actions: []string{"*"}},
				}},
			},
		},
	}

	doc, err := oas2jsonschema.NewLibOASParser().Parse(oasText)
	require.NoError(t, err)
	res, err := render.CRDs(context.Background(), cr, render.TargetGVK(cr, doc),
		oas2jsonschema.NewDocumentSet(doc), render.HasSecuritySchemes(doc))
	require.NoError(t, err)
	require.NotNil(t, res.ConfigurationCRD,
		"configurationFields alone must produce a Configuration CRD; if this fails the FIXTURE stopped "+
			"exercising the second call site, not the flag")

	blob, err := json.MarshalIndent(res.ConfigurationCRD, "", "  ")
	require.NoError(t, err)

	require.NotContains(t, string(blob), `"default": {}`,
		"the Configuration CRD must NOT stamp an optional parent either -- render.go has a SECOND "+
			"crdgen.Options literal and the flag has to be set on both. Configuration CRD:\n%s", string(blob))
}

// CONTROL for the arm above: the same fixture's configuration schema really does carry the shape, so a
// clean result there is a flag doing work rather than a document that could never have stamped.
func TestControlTheConfigurationShapeWouldStampWithTheFlagUnset(t *testing.T) {
	specSchema := []byte(`{
	  "type": "object",
	  "properties": {
	    "query": {
	      "type": "object",
	      "properties": {
	        "get": {
	          "type": "object",
	          "properties": {"api-version": {"type": "string", "default": "1"}}
	        }
	      }
	    }
	  }
	}`)

	out, warnings, err := crdgen.GenerateWithWarnings(crdgen.Options{
		Group:      "cfgprobe.ogen.krateo.io",
		Version:    "v1alpha1",
		Kind:       "WidgetConfiguration",
		SpecSchema: specSchema,
		Managed:    false,
		// NoParentDefaults deliberately UNSET.
	})
	require.NoError(t, err)
	require.Contains(t, string(out), "default: {}",
		"CONTROL FAILED: the configuration shape does not stamp even with the flag unset, so the arm "+
			"above proves nothing. warnings: %v", warnings)
}
