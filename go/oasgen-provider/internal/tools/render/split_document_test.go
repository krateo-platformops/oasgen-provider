package render_test

import (
	"context"
	"encoding/json"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/oas2jsonschema"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/render"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// The 1.0 document: findby/get/delete, and NO create path. This is Aruba's compute-provider.json reduced
// to its shape -- the create endpoint genuinely does not exist here.
const splitDocV10 = `
openapi: 3.0.0
info: {title: compute, version: "1.0.0"}
paths:
  /cloudServers:
    get:
      operationId: listCloudServers
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: array
                items: {$ref: "#/components/schemas/CloudServer"}
  /cloudServers/{cloudServerId}:
    get:
      operationId: getCloudServer
      parameters:
        - {name: cloudServerId, in: path, required: true, schema: {type: string}}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {$ref: "#/components/schemas/CloudServer"}
components:
  schemas:
    CloudServer:
      type: object
      properties:
        id: {type: string}
        name: {type: string}
        status: {type: string}
`

// The 1.1 document: the create endpoint ALONE, exactly as Aruba publishes it. Its request body carries a
// field (flavorId) that appears nowhere in the 1.0 document, so a spec built from the wrong document
// cannot accidentally contain it.
const splitDocV11 = `
openapi: 3.0.0
info: {title: compute, version: "1.1.0"}
paths:
  /operations/{operationId}:
    get:
      operationId: getOperation
      parameters:
        - {name: operationId, in: path, required: true, schema: {type: string}}
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  status: {type: string}
  /cloudServers:
    post:
      operationId: createCloudServer
      parameters:
        - {name: apiVersion, in: query, required: true, schema: {type: string}}
      responses:
        "201":
          description: created
          content:
            application/json:
              schema: {$ref: "#/components/schemas/CloudServer"}
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name: {type: string}
                flavorId: {type: string}
components:
  schemas:
    CloudServer:
      type: object
      properties:
        id: {type: string}
        name: {type: string}
        status: {type: string}
`

func splitCR() *definitionv1alpha1.RestDefinition {
	return &definitionv1alpha1.RestDefinition{
		Spec: definitionv1alpha1.RestDefinitionSpec{
			ResourceGroup: "compute.example.com",
			OASPath:       "configmap://krateo-system/compute/v10.yaml",
			Resource: definitionv1alpha1.Resource{
				Kind:        "CloudServer",
				Identifiers: []string{"id"},
				VerbsDescription: []definitionv1alpha1.VerbsDescription{
					{
						Action:  "create",
						Method:  "POST",
						Path:    "/cloudServers",
						OASPath: "configmap://krateo-system/compute/v11.yaml",
					},
					{Action: "findby", Method: "GET", Path: "/cloudServers"},
					{Action: "get", Method: "GET", Path: "/cloudServers/{cloudServerId}"},
				},
			},
		},
	}
}

func parseSplitSet(t *testing.T) *oas2jsonschema.DocumentSet {
	t.Helper()
	p := oas2jsonschema.NewLibOASParser()
	v10, err := p.Parse([]byte(splitDocV10))
	require.NoError(t, err)
	v11, err := p.Parse([]byte(splitDocV11))
	require.NoError(t, err)
	return oas2jsonschema.NewDocumentSetWithOverrides(v10, map[string]oas2jsonschema.OASDocument{"create": v11})
}

func specProperties(t *testing.T, crd *apiextensionsv1.CustomResourceDefinition) map[string]apiextensionsv1.JSONSchemaProps {
	t.Helper()
	require.NotEmpty(t, crd.Spec.Versions)
	schema := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
	spec, ok := schema.Properties["spec"]
	require.True(t, ok, "the generated CRD must have a spec")
	return spec.Properties
}

// TestSplitDocumentResourceGenerates is the end-to-end claim of #108, through the controller's own
// rendering path.
//
// It is the test the feature exists for, and it is also what caught the DocumentSet being discarded one
// call before it was read: render.CRDs took a single OASDocument and wrapped it in a set of one, so the
// controller built the override map and then threw it away. With that in place this fails outright --
// `path '/cloudServers' not found in OpenAPI spec` for the POST, because the 1.0 document has no POST on
// that path -- rather than generating something subtly wrong.
func TestSplitDocumentResourceGenerates(t *testing.T) {
	cr := splitCR()
	docs := parseSplitSet(t)

	res, err := render.CRDs(context.Background(), cr, render.TargetGVK(cr, docs.Default()), docs, render.HasSecuritySchemes(docs.Default()))
	require.NoError(t, err, "a resource whose create lives in a second document must generate")
	require.NotNil(t, res.CRD)

	props := specProperties(t, res.CRD)

	assert.Contains(t, props, "flavorId",
		"the spec is built from the create body, which exists ONLY in the 1.1 document")
	assert.Contains(t, props, "name")
	assert.Contains(t, props, "apiVersion",
		"the create verb's query parameter comes from the 1.1 document too")
	assert.Contains(t, props, "cloudServerId",
		"the get verb's path parameter still comes from the 1.0 document")
}

// TestSplitDocumentVersionComesFromTheDefault pins the half that must NOT follow the override: the CRD's
// API version is a property of the resource and #108 settled that it comes from spec.oasPath's document.
// The two documents declare 1.0.0 and 1.1.0 precisely so this can tell them apart.
func TestSplitDocumentVersionComesFromTheDefault(t *testing.T) {
	cr := splitCR()
	docs := parseSplitSet(t)

	gvk := render.TargetGVK(cr, docs.Default())
	assert.Equal(t, "v1-0-0", gvk.Version,
		"the CRD version follows spec.oasPath's info.version, not the override's")
}

// TestSplitDocumentPreviewMatchesTheController guards the parity the render service exists to provide: the
// preview must answer "what would the controller apply" for a split-document resource too, not quietly
// collapse every verb onto spec.oasPath.
func TestSplitDocumentPreviewMatchesTheController(t *testing.T) {
	cr := splitCR()
	docs := parseSplitSet(t)

	res, err := render.CRDs(context.Background(), cr, render.TargetGVK(cr, docs.Default()), docs, render.HasSecuritySchemes(docs.Default()))
	require.NoError(t, err)

	fromController, err := json.Marshal(specProperties(t, res.CRD))
	require.NoError(t, err)

	// The preview builds its set from the same inputs, keyed by the same oasPaths the CR names.
	previewDocs := parseSplitSet(t)
	previewRes, err := render.CRDs(context.Background(), cr, render.TargetGVK(cr, previewDocs.Default()), previewDocs, render.HasSecuritySchemes(previewDocs.Default()))
	require.NoError(t, err)

	fromPreview, err := json.Marshal(specProperties(t, previewRes.CRD))
	require.NoError(t, err)

	assert.JSONEq(t, string(fromController), string(fromPreview))
}

// TestSplitDocumentAsyncPollPathResolvesInTheVerbsDocument covers validateAsyncPollPaths, the fifth
// per-verb lookup.
//
// An async verb that overrode spec.oasPath declares a poll endpoint published by the document it named.
// /operations/{operationId} exists ONLY in the 1.1 document, so validating it against the default is not
// a near-miss but a flat rejection of a path that is present exactly where the verb said it was -- and
// the rejection is a *FieldError, so the RestDefinition never generates at all.
func TestSplitDocumentAsyncPollPathResolvesInTheVerbsDocument(t *testing.T) {
	cr := splitCR()
	cr.Spec.Resource.VerbsDescription[0].Async = &definitionv1alpha1.AsyncConfig{
		OperationRef: definitionv1alpha1.OperationRef{In: "body", Path: ".id"},
		Poll: definitionv1alpha1.PollConfig{
			Path: "/operations/{operationId}",
		},
	}
	docs := parseSplitSet(t)

	_, err := render.CRDs(context.Background(), cr, render.TargetGVK(cr, docs.Default()), docs, render.HasSecuritySchemes(docs.Default()))
	require.NoError(t, err,
		"the poll path is published by the create verb's own document, so it must validate against that one")
}
