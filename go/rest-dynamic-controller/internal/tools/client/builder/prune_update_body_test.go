//go:build unit || integration

package builder

import (
	"context"
	"testing"

	"github.com/pb33f/libopenapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

// ISSUE 960. A nested field the CREATE schema has and the UPDATE schema lacks used to be sent on every spec
// edit, because processFields assigns the whole nested value under its top-level key. Neutron runs
// _update_segmentation_id whenever provider:segmentation_id is in the body, even unchanged.
//
// These tests drive the real path -- a real document, APICallBuilder, BuildCallConfig -- rather than the
// prune in isolation, so each one fails against the code before the fix on its ASSERTION and not on a
// missing symbol.
const pruneSpec = `
openapi: 3.0.3
info: {title: nets, version: "1.0"}
components:
  schemas:
    Provider:
      type: object
      properties:
        network_type: {type: string}
        physical_network: {type: string}
paths:
  /nets:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                name: {type: string}
                create_only: {type: string}
                provider:
                  type: object
                  properties:
                    network_type: {type: string}
                    physical_network: {type: string}
                    segmentation_id: {type: integer}
                required_nest: {type: object}
                free_ap: {type: object}
                free_bare: {type: object}
                free_mixed: {type: object}
                rules: {type: array}
                refd: {type: object}
                either: {type: object}
                composed: {type: object}
      responses: {"201": {description: ok}}
    put:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                name: {type: string}
                provider:
                  type: object
                  properties:
                    network_type: {type: string}
                    physical_network: {type: string}
                required_nest:
                  type: object
                  required: [secret]
                  properties:
                    visible: {type: string}
                free_ap:
                  type: object
                  additionalProperties: true
                free_bare:
                  type: object
                free_mixed:
                  type: object
                  additionalProperties: true
                  properties:
                    known: {type: string}
                rules:
                  type: array
                  items:
                    type: object
                    properties:
                      id: {type: string}
                refd:
                  $ref: '#/components/schemas/Provider'
                either:
                  oneOf:
                    - type: object
                      properties: {a: {type: string}}
                    - type: object
                      properties: {b: {type: string}}
                composed:
                  allOf:
                    - type: object
                      required: [must]
                      properties: {x: {type: string}}
                    - type: object
                      properties: {y: {type: string}}
      responses: {"200": {description: ok}}
`

func updateBody(t *testing.T, spec map[string]interface{}, mappings ...getter.FieldMappingItem) (map[string]interface{}, *unstructured.Unstructured) {
	t.Helper()
	doc, err := libopenapi.NewDocument([]byte(pruneSpec))
	require.NoError(t, err)
	model, errs := doc.BuildV3Model()
	require.Empty(t, errs)
	cli := &restclient.UnstructuredClient{DocScheme: model}

	info := &getter.Info{Resource: getter.Resource{VerbsDescription: []getter.VerbsDescription{
		{Action: "create", Method: "POST", Path: "/nets"},
		{Action: "update", Method: "PUT", Path: "/nets"},
	}}}
	_, callInfo, err := APICallBuilder(cli, info, apiaction.Update)
	require.NoError(t, err)
	require.NotNil(t, callInfo)
	callInfo.FieldMapping = mappings

	mg := &unstructured.Unstructured{Object: map[string]interface{}{"spec": spec}}
	cfg := BuildCallConfig(context.Background(), callInfo, mg, nil, nil)
	require.NotNil(t, cfg)
	body, ok := cfg.Body.(map[string]interface{})
	require.True(t, ok)
	return body, mg
}

func TestPrune_NestedFieldAbsentFromUpdateSchemaIsNotSent(t *testing.T) {
	body, _ := updateBody(t, map[string]interface{}{
		"provider": map[string]interface{}{"network_type": "vxlan", "segmentation_id": int64(1234)},
	})
	prov := body["provider"].(map[string]interface{})
	assert.NotContains(t, prov, "segmentation_id",
		"update schema does not list provider.segmentation_id, so it must not be sent on an update")
}

func TestPrune_NestedFieldPresentInBothIsRetained(t *testing.T) {
	body, _ := updateBody(t, map[string]interface{}{
		"provider": map[string]interface{}{"network_type": "vxlan", "segmentation_id": int64(1234)},
	})
	assert.Equal(t, "vxlan", body["provider"].(map[string]interface{})["network_type"])
}

// Clause 1: required is never pruned, at any depth, even absent from properties.
func TestPrune_RequiredNestedFieldAbsentFromPropertiesIsRetained(t *testing.T) {
	body, _ := updateBody(t, map[string]interface{}{
		"required_nest": map[string]interface{}{"secret": "s", "visible": "v", "stray": "x"},
	})
	n := body["required_nest"].(map[string]interface{})
	assert.Equal(t, "s", n["secret"], "required but not in properties: must be kept")
	assert.Equal(t, "v", n["visible"])
	assert.NotContains(t, n, "stray", "control: an unlisted, non-required key IS pruned")
}

func TestPrune_RequiredFromAllOfMemberIsRetainedAndOtherMembersMerge(t *testing.T) {
	body, _ := updateBody(t, map[string]interface{}{
		"composed": map[string]interface{}{"must": "m", "x": "1", "y": "2", "stray": "z"},
	})
	n := body["composed"].(map[string]interface{})
	assert.Equal(t, map[string]interface{}{"must": "m", "x": "1", "y": "2"}, n)
}

// Clause 3: a free-form object passes through WHOLE. Three different schema shapes, three arms.
func TestPrune_FreeFormAdditionalPropertiesTruePassesThroughWhole(t *testing.T) {
	in := map[string]interface{}{"a": "1", "b": map[string]interface{}{"c": "2"}}
	body, _ := updateBody(t, map[string]interface{}{"free_ap": in})
	assert.Equal(t, in, body["free_ap"])
}

func TestPrune_BareTypeObjectPassesThroughWhole(t *testing.T) {
	in := map[string]interface{}{"a": "1", "b": map[string]interface{}{"c": "2"}}
	body, _ := updateBody(t, map[string]interface{}{"free_bare": in})
	assert.Equal(t, in, body["free_bare"])
}

func TestPrune_AdditionalPropertiesTrueWithPropertiesKeepsUnlistedKeys(t *testing.T) {
	in := map[string]interface{}{"known": "k", "unlisted": "u"}
	body, _ := updateBody(t, map[string]interface{}{"free_mixed": in})
	assert.Equal(t, in, body["free_mixed"])
}

// Clause 4: arrays, oneOf/anyOf, $ref are resolved before comparison.
func TestPrune_ArrayOfObjectsIsPrunedPerElement(t *testing.T) {
	body, _ := updateBody(t, map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"id": "1", "extra": "x"},
			map[string]interface{}{"id": "2", "extra": "y"},
		},
	})
	assert.Equal(t, []interface{}{
		map[string]interface{}{"id": "1"},
		map[string]interface{}{"id": "2"},
	}, body["rules"])
}

func TestPrune_RefdSubSchemaIsResolvedAndPruned(t *testing.T) {
	body, _ := updateBody(t, map[string]interface{}{
		"refd": map[string]interface{}{"network_type": "vlan", "segmentation_id": int64(7)},
	})
	assert.Equal(t, map[string]interface{}{"network_type": "vlan"}, body["refd"])
}

func TestPrune_OneOfBranchesAreUnioned(t *testing.T) {
	body, _ := updateBody(t, map[string]interface{}{
		"either": map[string]interface{}{"a": "1", "b": "2", "c": "3"},
	})
	assert.Equal(t, map[string]interface{}{"a": "1", "b": "2"}, body["either"])
}

// REGRESSION, must pass before AND after: a top-level field the update schema lacks stays excluded.
func TestPrune_TopLevelFieldAbsentFromUpdateSchemaStillExcluded(t *testing.T) {
	body, _ := updateBody(t, map[string]interface{}{"name": "n", "create_only": "c"})
	assert.Equal(t, "n", body["name"])
	assert.NotContains(t, body, "create_only")
}

// The prune must copy: the values in the assembled body alias the custom resource.
func TestPrune_DoesNotMutateTheCustomResource(t *testing.T) {
	spec := map[string]interface{}{
		"provider": map[string]interface{}{"network_type": "vxlan", "segmentation_id": int64(1234)},
	}
	_, mg := updateBody(t, spec)
	got := mg.Object["spec"].(map[string]interface{})["provider"].(map[string]interface{})
	assert.Contains(t, got, "segmentation_id", "the CR the reconciler holds must keep its own field")
}

// An explicit inBody mapping is a deliberate instruction and is NOT subject to the schema check.
func TestPrune_ExplicitInBodyMappingStillWrites(t *testing.T) {
	body, _ := updateBody(t,
		map[string]interface{}{"alias": int64(99)},
		getter.FieldMappingItem{InCustomResource: "spec.alias", InBody: "provider.segmentation_id"},
	)
	assert.Equal(t, int64(99), body["provider"].(map[string]interface{})["segmentation_id"],
		"a mapping the author wrote must win over the schema prune")
}

// THE REPORTED SHAPE (issue 960). Neutron has NO nested `provider` object: the attributes are FLAT keys
// whose NAMES contain a colon, directly inside the `network` envelope. The prune walks schema and body as
// trees keyed by exact strings and never builds or splits a path, so a colon is just a character.
const neutronSpec = `
openapi: 3.0.3
info: {title: neutron, version: "1.0"}
paths:
  /networks:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                network:
                  type: object
                  properties:
                    name: {type: string}
                    description: {type: string}
                    "provider:network_type": {type: string}
                    "provider:segmentation_id": {type: integer}
                    "router:external": {type: boolean}
      responses: {"201": {description: ok}}
    put:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                network:
                  type: object
                  properties:
                    name: {type: string}
                    description: {type: string}
                    "router:external": {type: boolean}
      responses: {"200": {description: ok}}
`

func neutronBody(t *testing.T, spec string, network map[string]interface{}) map[string]interface{} {
	t.Helper()
	doc, err := libopenapi.NewDocument([]byte(spec))
	require.NoError(t, err)
	model, errs := doc.BuildV3Model()
	require.Empty(t, errs)
	info := &getter.Info{Resource: getter.Resource{VerbsDescription: []getter.VerbsDescription{
		{Action: "create", Method: "POST", Path: "/networks"},
		{Action: "update", Method: "PUT", Path: "/networks"},
	}}}
	_, callInfo, err := APICallBuilder(&restclient.UnstructuredClient{DocScheme: model}, info, apiaction.Update)
	require.NoError(t, err)
	mg := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{"network": network}}}
	cfg := BuildCallConfig(context.Background(), callInfo, mg, nil, nil)
	require.NotNil(t, cfg)
	return cfg.Body.(map[string]interface{})
}

// The reported defect: editing only `description` still sent provider:segmentation_id, and Neutron runs
// _update_segmentation_id whenever that key is present, even unchanged.
func TestPrune_ColonNamedFlatKeyAbsentFromUpdateSchemaIsNotSent(t *testing.T) {
	body := neutronBody(t, neutronSpec, map[string]interface{}{
		"name":                     "n",
		"description":              "edited",
		"provider:network_type":    "vxlan",
		"provider:segmentation_id": int64(1234),
		"router:external":          false,
	})
	net := body["network"].(map[string]interface{})
	assert.NotContains(t, net, "provider:segmentation_id")
	assert.NotContains(t, net, "provider:network_type")
	assert.Equal(t, "edited", net["description"], "control: a listed key survives")
	assert.Equal(t, false, net["router:external"], "control: a listed COLON-named key survives")
	assert.Equal(t, "n", net["name"])
}

// If the update schema declares the envelope FREE-FORM, the prune must not fire: the vendor document says it
// accepts keys it does not enumerate. That would mean the reported defect has another cause, and this test
// pins that the prune does not pretend otherwise.
func TestPrune_FreeFormEnvelopeIsNotPrunedEvenWithColonKeys(t *testing.T) {
	spec := `
openapi: 3.0.3
info: {title: neutron, version: "1.0"}
paths:
  /networks:
    put:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                network: {type: object, additionalProperties: true}
      responses: {"200": {description: ok}}
`
	in := map[string]interface{}{"description": "d", "provider:segmentation_id": int64(1234)}
	body := neutronBody(t, spec, in)
	assert.Equal(t, in, body["network"])
}
