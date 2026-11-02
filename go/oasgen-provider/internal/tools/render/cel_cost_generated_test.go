package render_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	"sigs.k8s.io/yaml"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/oas2jsonschema"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/render"
)

// `uniqueItems` reaches the generated CRD and plumbing/crdgen translates it three ways:
//
//	scalar items            -> x-kubernetes-list-type: set   native, no cost budget
//	non-scalar + maxItems   -> a CEL x-kubernetes-validations uniqueness rule
//	otherwise               -> dropped
//
// THE MIDDLE BRANCH IS THE ONE THAT CAN MAKE THE APISERVER REFUSE THE WHOLE CRD (#270). CEL uniqueness is
// inherently O(n^2) and Kubernetes costs x-kubernetes-validations STATICALLY, against the bounds declared
// on the fields a rule traverses. Over budget is not a validation that fails to fire -- the
// CustomResourceDefinition does not register, the RestDefinition fails, and on a Krateo install that is a
// managed child, so the platform never converges. That is #169, found the hard way.
//
// What existed before this test: validation_keywords_test.go covers `items: {type: string}`, which is
// SCALAR and therefore routes to the `set` branch. TestGeneratedCRDIsWithinCELCostBudget validates the
// SHIPPED RestDefinition CRD, a different artifact from one generated out of an arbitrary document. So the
// CEL branch had no test asserting the apiserver would accept its output.
//
// This asks the authority rather than a reimplementation: the same
// validation.ValidateCustomResourceDefinition the apiserver runs at install time.
func TestGeneratedCRDWithUniqueItemsOnObjectsIsAcceptedByTheApiserver(t *testing.T) {
	const oasText = `
openapi: 3.0.3
info: {title: things, version: "1.0"}
paths:
  /things:
    post:
      operationId: createThing
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                name: {type: string}
                # The shape under test: NON-SCALAR items with maxItems, which routes to CEL.
                rules:
                  type: array
                  uniqueItems: true
                  maxItems: 32
                  items:
                    type: object
                    properties:
                      key: {type: string}
                      value: {type: string}
                      weight: {type: integer}
      responses:
        '201': {description: created}
  /things/{id}:
    get:
      operationId: getThing
      parameters:
        - name: id
          in: path
          required: true
          schema: {type: string}
      responses:
        '200': {description: ok}
`

	rd := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, yaml.Unmarshal([]byte(`
apiVersion: ogen.krateo.io/v1alpha1
kind: RestDefinition
metadata:
  name: thing
spec:
  oasPath: /tmp/unused.yaml
  resourceGroup: example.krateo.io
  resource:
    kind: Thing
    identifiers: [name]
    verbsDescription:
      - action: create
        method: POST
        path: /things
      - action: get
        method: GET
        path: /things/{id}
`), rd))

	doc, err := oas2jsonschema.NewLibOASParser().Parse([]byte(strings.TrimSpace(oasText)))
	require.NoError(t, err)

	res, err := render.CRDs(context.Background(), rd, render.TargetGVK(rd, doc), oas2jsonschema.NewDocumentSet(doc), render.HasSecuritySchemes(doc))
	require.NoError(t, err)
	require.NotNil(t, res.CRD, "no CRD was generated, so this test would assert nothing")

	// The premise: the generated CRD must actually contain the array we are testing. Without this, a
	// generator that silently dropped `rules` would make the budget check below pass trivially.
	raw, err := yaml.Marshal(res.CRD)
	require.NoError(t, err)
	require.Contains(t, string(raw), "rules",
		"the `rules` array is absent from the generated CRD: the shape under test never reached it, so "+
			"an accepted CRD here says nothing about the CEL branch")

	assertApiserverAccepts(t, res.CRD)
	if res.ConfigurationCRD != nil {
		assertApiserverAccepts(t, res.ConfigurationCRD)
	}
}

// assertApiserverAccepts runs a generated CRD through the apiserver's own validator, which is what decides
// at install time whether it registers at all. Two separate limits apply -- a per-rule cost and a total
// for the whole schema -- and neither is visible by inspecting the rule text.
func assertApiserverAccepts(t *testing.T, crd *apiextensionsv1.CustomResourceDefinition) {
	t.Helper()

	var internal apiextensions.CustomResourceDefinition
	require.NoError(t, apiextensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(crd, &internal, nil))

	// Populate status.storedVersions as the apiserver's own defaulting does on CREATE. render.CRDs
	// produces no status -- correctly, since status is the apiserver's -- and validating without it fails
	// on "must have at least one stored version", which is a property of my harness rather than of the
	// generated CRD. Reproducing the defaulting keeps the COST checks (the point of this test) under the
	// real validator instead of reaching for a spec-only entry point that skips them.
	for _, v := range internal.Spec.Versions {
		if v.Storage {
			internal.Status.StoredVersions = append(internal.Status.StoredVersions, v.Name)
		}
	}
	require.NotEmpty(t, internal.Status.StoredVersions,
		"no version is marked storage, so the apiserver would reject this for a reason unrelated to CEL cost")

	var problems []string
	for _, e := range validation.ValidateCustomResourceDefinition(context.Background(), &internal) {
		problems = append(problems, e.Error())
	}
	require.Empty(t, problems,
		"the apiserver would REFUSE to install this generated CRD, which fails the RestDefinition and, on "+
			"a Krateo install, leaves a managed child that never converges (#169, #270):\n  %s",
		strings.Join(problems, "\n  "))
}
