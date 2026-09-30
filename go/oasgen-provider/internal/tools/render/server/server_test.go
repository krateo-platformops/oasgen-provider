package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const oasPath = "configmap://gh-system/repo/repo.yaml"

func repoDoc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../../../samples/usage_guide/assets/repo.yaml")
	require.NoError(t, err)
	return string(b)
}

// restDefinition is the examples/github-repo RestDefinition with its verbs overridable.
func restDefinition(name, kind string, verbs ...map[string]any) map[string]any {
	if len(verbs) == 0 {
		verbs = []map[string]any{
			{"action": "create", "method": "POST", "path": "/orgs/{org}/repos"},
			{"action": "get", "method": "GET", "path": "/repos/{org}/{name}"},
			{"action": "update", "method": "PATCH", "path": "/repos/{org}/{name}"},
			{"action": "delete", "method": "DELETE", "path": "/repos/{org}/{name}"},
		}
	}
	return map[string]any{
		"apiVersion": "ogen.krateo.io/v1alpha1",
		"kind":       "RestDefinition",
		"metadata":   map[string]any{"name": name, "namespace": "gh-system"},
		"spec": map[string]any{
			"oasPath":       oasPath,
			"resourceGroup": "github.ogen.krateo.io",
			"resource":      map[string]any{"kind": kind, "verbsDescription": verbs},
		},
	}
}

func post(t *testing.T, h http.Handler, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/render", bytes.NewReader(b)))
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) Response {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

func errorsOf(resp Response) []Problem {
	var out []Problem
	for _, p := range resp.Errors {
		if p.Severity == SeverityError {
			out = append(out, p)
		}
	}
	return out
}

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	New(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestRender_RejectsNonPostAndBadBodies(t *testing.T) {
	h := New(nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/render", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/render", strings.NewReader("{not json")))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = post(t, h, map[string]any{"restDefinitions": []any{}, "oas": map[string]string{}})
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	h.MaxBodyBytes = 64
	rec = post(t, h, map[string]any{"restDefinitions": []any{restDefinition("a", "Repo")}, "oas": map[string]string{oasPath: repoDoc(t)}})
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestRender_ListsAreNeverNull(t *testing.T) {
	rec := post(t, New(nil), map[string]any{"restDefinitions": []any{map[string]any{"kind": "ConfigMap"}}})
	require.Equal(t, http.StatusOK, rec.Code)
	for _, key := range []string{`"crds":[]`, `"configurationCrds":[]`, `"skippedSecuritySchemes":[]`} {
		assert.Contains(t, rec.Body.String(), key)
	}
}

func TestRender_MissingDocumentNamesTheOASPath(t *testing.T) {
	resp := decode(t, post(t, New(nil), map[string]any{
		"restDefinitions": []any{restDefinition("gh-repo", "Repo")},
		"oas":             map[string]string{"configmap://gh-system/repo/other.yaml": repoDoc(t)},
	}))
	errs := errorsOf(resp)
	require.Len(t, errs, 1)
	assert.Equal(t, "gh-system/gh-repo", errs[0].RestDefinition)
	assert.Equal(t, "spec.oasPath", errs[0].Field)
	assert.Contains(t, errs[0].Message, oasPath)
	assert.Empty(t, resp.CRDs)
}

func TestRender_UnparseableDocument(t *testing.T) {
	resp := decode(t, post(t, New(nil), map[string]any{
		"restDefinitions": []any{restDefinition("gh-repo", "Repo")},
		"oas":             map[string]string{oasPath: "this is not an OpenAPI document"},
	}))
	errs := errorsOf(resp)
	require.Len(t, errs, 1)
	assert.Equal(t, "spec.oasPath", errs[0].Field)
}

// The controller refuses a poll path the OAS does not declare (validateAsyncPollPaths); render must refuse it
// the same way and point at the field.
func TestRender_AsyncPollPathIsAFieldError(t *testing.T) {
	create := map[string]any{
		"action": "create", "method": "POST", "path": "/orgs/{org}/repos",
		"async": map[string]any{"poll": map[string]any{
			"path": "/nowhere/{operationId}", "statusPath": "status", "successValues": []string{"done"},
		}},
	}
	get := map[string]any{"action": "get", "method": "GET", "path": "/repos/{org}/{name}"}
	resp := decode(t, post(t, New(nil), map[string]any{
		"restDefinitions": []any{restDefinition("gh-repo", "Repo", create, get)},
		"oas":             map[string]string{oasPath: repoDoc(t)},
	}))
	errs := errorsOf(resp)
	require.Len(t, errs, 1)
	assert.Equal(t, "spec.resource.verbsDescription[0].async.poll.path", errs[0].Field)
	assert.Contains(t, errs[0].Message, "not a path declared in the OAS document")
	assert.Empty(t, resp.CRDs)
}

// ValidateSchemas findings are warnings: the controller logs them and applies anyway, so render still
// returns the CRD. A get path the document lacks makes the create-vs-get comparison fail.
func TestRender_ValidateSchemasFindingsAreWarnings(t *testing.T) {
	create := map[string]any{"action": "create", "method": "POST", "path": "/orgs/{org}/repos"}
	get := map[string]any{"action": "get", "method": "GET", "path": "/not/in/the/document"}
	resp := decode(t, post(t, New(nil), map[string]any{
		"restDefinitions": []any{restDefinition("gh-repo", "Repo", create, get)},
		"oas":             map[string]string{oasPath: repoDoc(t)},
	}))
	assert.Empty(t, errorsOf(resp))
	require.NotEmpty(t, resp.Errors)
	for _, p := range resp.Errors {
		assert.Equal(t, SeverityWarning, p.Severity)
		assert.Equal(t, "gh-system/gh-repo", p.RestDefinition)
	}
	assert.Len(t, resp.CRDs, 1)
}

func TestRender_TwoRestDefinitionsOneCRDIsRefused(t *testing.T) {
	resp := decode(t, post(t, New(nil), map[string]any{
		"restDefinitions": []any{restDefinition("first", "Repo"), restDefinition("second", "Repo")},
		"oas":             map[string]string{oasPath: repoDoc(t)},
	}))
	// One finding per contested CRD: the resource CRD and the Configuration CRD.
	errs := errorsOf(resp)
	require.Len(t, errs, 2)
	for _, e := range errs {
		assert.Equal(t, "gh-system/second", e.RestDefinition)
		assert.Contains(t, e.Message, "gh-system/first")
	}
	assert.Len(t, resp.CRDs, 1)
	assert.Equal(t, "gh-system/first", resp.CRDs[0].Annotations["krateo.io/owned-by-restdefinition"])
}

func TestRender_OneBadRestDefinitionDoesNotSinkTheBatch(t *testing.T) {
	resp := decode(t, post(t, New(nil), map[string]any{
		"restDefinitions": []any{map[string]any{"kind": "ConfigMap"}, restDefinition("gh-repo", "Repo")},
		"oas":             map[string]string{oasPath: repoDoc(t)},
	}))
	errs := errorsOf(resp)
	require.Len(t, errs, 1)
	assert.Equal(t, "restDefinitions[0]", errs[0].RestDefinition)
	assert.Equal(t, "kind", errs[0].Field)
	assert.Len(t, resp.CRDs, 1)
	assert.Len(t, resp.ConfigurationCRDs, 1)
}
