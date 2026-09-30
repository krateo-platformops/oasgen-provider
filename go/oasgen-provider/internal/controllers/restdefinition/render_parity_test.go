package restdefinition

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	definitionv1alpha1 "github.com/krateo-platformops/oasgen-provider/apis/restdefinitions/v1alpha1"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/oas2jsonschema"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/render/server"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

// TestRenderMatchesControllerCreate is the acceptance test for oasgen-render (#157): the repo's
// examples/github-repo, sent through the /render handler, yields exactly the CRDs the controller's Create
// writes for the same RestDefinition and the same OAS document.
//
// The controller side is the real Create against a fake API server holding the example's ConfigMap, so it
// fetches the document through filegetter exactly as it does in a cluster. The render side gets the same
// bytes inline under the same oasPath key.
func TestRenderMatchesControllerCreate(t *testing.T) {
	ctx := context.Background()

	rdYAML, err := os.ReadFile("../../../../../examples/github-repo/restdefinition.yaml")
	require.NoError(t, err)
	oasText, err := os.ReadFile("../../../samples/usage_guide/assets/repo.yaml")
	require.NoError(t, err)

	rdJSON, err := yaml.YAMLToJSON(rdYAML)
	require.NoError(t, err)
	cr := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, json.Unmarshal(rdJSON, cr))
	require.Equal(t, "configmap://gh-system/repo/repo.yaml", cr.Spec.OASPath, "the example's oasPath changed; update this test")

	// --- controller path -------------------------------------------------------------------------------
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, apiextensionsv1.AddToScheme(scheme))
	require.NoError(t, definitionv1alpha1.SchemeBuilder.AddToScheme(scheme))
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "gh-system", Name: "repo"},
		Data:       map[string]string{"repo.yaml": string(oasText)},
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cm, cr.DeepCopy()).
		WithStatusSubresource(&definitionv1alpha1.RestDefinition{}).
		Build()
	live := &definitionv1alpha1.RestDefinition{}
	require.NoError(t, kube.Get(ctx, client.ObjectKeyFromObject(cr), live))

	e := &external{kube: kube, log: logging.NewNopLogger(), rec: record.NewFakeRecorder(100), parser: oas2jsonschema.NewLibOASParser()}
	require.NoError(t, e.Create(ctx, live))

	var applied apiextensionsv1.CustomResourceDefinitionList
	require.NoError(t, kube.List(ctx, &applied))
	appliedByName := map[string]apiextensionsv1.CustomResourceDefinition{}
	for _, c := range applied.Items {
		appliedByName[c.Name] = c
	}

	// --- render path -----------------------------------------------------------------------------------
	body, err := json.Marshal(map[string]any{
		"restDefinitions": []json.RawMessage{rdJSON},
		"oas":             map[string]string{cr.Spec.OASPath: string(oasText)},
	})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	server.New(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/render", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp server.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	for _, p := range resp.Errors {
		require.NotEqual(t, server.SeverityError, p.Severity, "render refused what the controller applied: %+v", p)
	}

	// The example's document declares a bearer scheme, so both CRDs are generated.
	require.Len(t, resp.CRDs, 1)
	require.Len(t, resp.ConfigurationCRDs, 1)
	require.Len(t, appliedByName, 2, "the controller applied a different number of CRDs")
	require.Equal(t, "Repo", resp.CRDs[0].Spec.Names.Kind)
	require.Equal(t, "RepoConfiguration", resp.ConfigurationCRDs[0].Spec.Names.Kind)

	for _, rendered := range append(resp.CRDs, resp.ConfigurationCRDs...) {
		got, ok := appliedByName[rendered.Name]
		require.True(t, ok, "rendered %s, which the controller did not apply", rendered.Name)
		// The fake API server stamps a resourceVersion on create, and a typed List drops each item's TypeMeta
		// (which PrepareForApply sets, and the apiserver receives); nothing else is expected to differ.
		require.Equal(t, apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition"), rendered.GroupVersionKind())
		got.ResourceVersion = ""
		got.TypeMeta = rendered.TypeMeta
		require.Equal(t, crdJSON(t, &got), crdJSON(t, rendered), "rendered %s differs from the applied one", rendered.Name)
	}
}

func crdJSON(t *testing.T, c *apiextensionsv1.CustomResourceDefinition) string {
	t.Helper()
	b, err := json.MarshalIndent(c, "", "  ")
	require.NoError(t, err)
	return string(b)
}
