package restclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"

	"github.com/pb33f/libopenapi"
	orderedmap "github.com/pb33f/libopenapi/orderedmap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildPath_Basic(t *testing.T) {
	baseUrl := "https://api.example.com/v1"
	path := "/users/{userId}/posts/{postId}"
	parameters := map[string]string{
		"userId": "42",
		"postId": "99",
	}
	query := map[string]string{
		"sort": "desc",
		"page": "2",
	}

	got := buildPath(baseUrl, path, parameters, query)
	if got == nil {
		t.Fatalf("buildPath returned nil")
	}

	got, err := url.Parse(got.String())
	if err != nil {
		t.Fatalf("failed to parse base URL: %v", err)
	}

	expectedPath := "/v1/users/42/posts/99"
	if got.Path != "/v1/users/42/posts/99" {
		t.Errorf("expected path %q, got %q", expectedPath, got.Path)
	}

	wantQuery := url.Values{"sort": {"desc"}, "page": {"2"}}.Encode()
	if got.RawQuery != wantQuery && got.RawQuery != "page=2&sort=desc" {
		t.Errorf("expected query %q, got %q", wantQuery, got.RawQuery)
	}
}

func TestBuildPath_WithPathParamsWithSlashes(t *testing.T) {
	baseUrl := "https://api.example.com/v1"
	path := "/users/{userId}/posts/{postId}"
	parameters := map[string]string{
		"userId": "42/123",
		"postId": "99",
	}
	query := map[string]string{}

	got := buildPath(baseUrl, path, parameters, query)
	if got == nil {
		t.Fatalf("buildPath returned nil")
	}

	fmt.Println("Got Path:", got.RawQuery)

	wantPath := baseUrl + "/users/42%2F123/posts/99"
	if got.String() != wantPath {
		t.Errorf("expected path %q, got %q", wantPath, got.String())
	}
}

func TestBuildPath_NoParams(t *testing.T) {
	baseUrl := "https://api.example.com"
	path := "/status"
	parameters := map[string]string{}
	query := map[string]string{}

	got := buildPath(baseUrl, path, parameters, query)
	if got == nil {
		t.Fatalf("buildPath returned nil")
	}
	got, err := url.Parse(got.String())
	if err != nil {
		t.Fatalf("failed to parse base URL: %v", err)
	}

	wantPath := "/status"
	if got.Path != wantPath {
		t.Errorf("expected path %q, got %q", wantPath, got.Path)
	}
	if got.RawQuery != "" {
		t.Errorf("expected empty query, got %q", got.RawQuery)
	}
}

func TestBuildPath_InvalidBaseURL(t *testing.T) {
	baseUrl := "://bad-url"
	path := "/foo"
	parameters := map[string]string{}
	query := map[string]string{}

	got := buildPath(baseUrl, path, parameters, query)
	if got != nil {
		t.Errorf("expected nil for invalid baseUrl, got %v", got)
	}
}

func TestBuildPath_MultipleQueryParams(t *testing.T) {
	baseUrl := "https://api.example.com"
	path := "/search"
	parameters := map[string]string{}
	query := map[string]string{
		"q":    "golang",
		"lang": "en",
	}

	got := buildPath(baseUrl, path, parameters, query)
	if got == nil {
		t.Fatalf("buildPath returned nil")
	}

	got, err := url.Parse(got.String())
	if err != nil {
		t.Fatalf("failed to parse base URL: %v", err)
	}

	wantPath := "/search"
	if got.Path != wantPath {
		t.Errorf("expected path %q, got %q", wantPath, got.Path)
	}

	parsedQuery, _ := url.ParseQuery(got.RawQuery)
	expectedQuery := url.Values{"q": {"golang"}, "lang": {"en"}}
	if !reflect.DeepEqual(parsedQuery, expectedQuery) {
		t.Errorf("expected query %v, got %v", expectedQuery, parsedQuery)
	}
}

func TestBuildPath_PathWithNoLeadingSlash(t *testing.T) {
	baseUrl := "https://api.example.com/api"
	path := "foo/bar"
	parameters := map[string]string{}
	query := map[string]string{}

	got := buildPath(baseUrl, path, parameters, query)
	if got == nil {
		t.Fatalf("buildPath returned nil")
	}

	got, err := url.Parse(got.String())
	if err != nil {
		t.Fatalf("failed to parse base URL: %v", err)
	}

	wantPath := "/api/foo/bar"
	if got.Path != wantPath {
		t.Errorf("expected path %q, got %q", wantPath, got.Path)
	}
}
func TestUnstructuredClient_isInResource(t *testing.T) {
	tests := []struct {
		name    string
		client  *UnstructuredClient
		value   string
		fields  []string
		want    bool
		wantErr bool
		errMsg  string
	}{
		{
			name: "nil resource",
			client: &UnstructuredClient{
				Resource: nil,
			},
			value:   "test",
			fields:  []string{"field"},
			want:    false,
			wantErr: true,
			errMsg:  "resource is nil",
		},
		{
			name: "value found in spec",
			client: &UnstructuredClient{
				Resource: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"spec": map[string]interface{}{
							"field": "test-value",
						},
					},
				},
			},
			value:   "test-value",
			fields:  []string{"field"},
			want:    true,
			wantErr: false,
		},
		{
			name: "value found in nested spec field",
			client: &UnstructuredClient{
				Resource: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"spec": map[string]interface{}{
							"nested": map[string]interface{}{
								"field": "nested-value",
							},
						},
					},
				},
			},
			value:   "nested-value",
			fields:  []string{"nested", "field"},
			want:    true,
			wantErr: false,
		},
		{
			name: "value not found in spec, found in status",
			client: &UnstructuredClient{
				Resource: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"spec": map[string]interface{}{
							"field": "spec-value",
						},
						"status": map[string]interface{}{
							"field": "status-value",
						},
					},
				},
			},
			value:   "status-value",
			fields:  []string{"field"},
			want:    true,
			wantErr: false,
		},
		{
			name: "value not found in spec or status",
			client: &UnstructuredClient{
				Resource: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"spec": map[string]interface{}{
							"field": "spec-value",
						},
						"status": map[string]interface{}{
							"field": "status-value",
						},
					},
				},
			},
			value:   "missing-value",
			fields:  []string{"field"},
			want:    false,
			wantErr: false,
		},
		{
			name: "field not found in spec, no status",
			client: &UnstructuredClient{
				Resource: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"spec": map[string]interface{}{
							"otherField": "value",
						},
					},
				},
			},
			value:   "test-value",
			fields:  []string{"field"},
			want:    false,
			wantErr: false,
		},
		{
			name: "field not found in spec or status",
			client: &UnstructuredClient{
				Resource: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"spec": map[string]interface{}{
							"otherField": "value",
						},
						"status": map[string]interface{}{
							"otherField": "status-value",
						},
					},
				},
			},
			value:   "test-value",
			fields:  []string{"field"},
			want:    false,
			wantErr: false,
		},
		{
			name: "empty fields slice",
			client: &UnstructuredClient{
				Resource: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"spec": map[string]interface{}{
							"field": "value",
						},
					},
				},
			},
			value:   "test-value",
			fields:  []string{},
			want:    false,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.client.isInResource(tt.value, tt.fields...)

			if tt.wantErr {
				if err == nil {
					t.Errorf("isInResource() expected error but got none")
					return
				}
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("isInResource() error = %v, want error containing %q", err, tt.errMsg)
				}
				return
			}

			if err != nil {
				t.Errorf("isInResource() unexpected error = %v", err)
				return
			}

			if got != tt.want {
				t.Errorf("isInResource() = %v, want %v", got, tt.want)
			}
		})
	}
}
func TestUnstructuredClient_RequestedBody(t *testing.T) {
	tests := []struct {
		name       string
		client     *UnstructuredClient
		httpMethod string
		path       string
		want       []string
		wantErr    bool
		errMsg     string
	}{
		{
			name: "nil doc scheme",
			client: &UnstructuredClient{
				DocScheme: nil,
			},
			httpMethod: "GET",
			path:       "/test",
			want:       nil,
			wantErr:    true,
		},
		{
			name: "path not found",
			client: &UnstructuredClient{
				DocScheme: &libopenapi.DocumentModel[v3.Document]{
					Model: v3.Document{
						Paths: &v3.Paths{
							PathItems: orderedmap.New[string, *v3.PathItem](),
						},
					},
				},
			},
			httpMethod: "GET",
			path:       "/nonexistent",
			want:       nil,
			wantErr:    true,
			errMsg:     "path not found: /nonexistent",
		},
		{
			name: "operation not found",
			client: &UnstructuredClient{
				DocScheme: &libopenapi.DocumentModel[v3.Document]{
					Model: v3.Document{
						Paths: &v3.Paths{
							PathItems: func() *orderedmap.Map[string, *v3.PathItem] {
								m := orderedmap.New[string, *v3.PathItem]()
								pathItem := &v3.PathItem{
									Get:  nil,
									Post: nil,
								}
								m.Set("/test", pathItem)
								return m
							}(),
						},
					},
				},
			},
			httpMethod: "DELETE",
			path:       "/test",
			want:       nil,
			wantErr:    true,
			errMsg:     "operation not found: DELETE",
		},
		{
			name: "no request body",
			client: &UnstructuredClient{
				DocScheme: &libopenapi.DocumentModel[v3.Document]{
					Model: v3.Document{
						Paths: &v3.Paths{
							PathItems: func() *orderedmap.Map[string, *v3.PathItem] {
								m := orderedmap.New[string, *v3.PathItem]()
								pathItem := &v3.PathItem{
									Get: &v3.Operation{
										RequestBody: nil,
									},
								}
								m.Set("/test", pathItem)
								return m
							}(),
						},
					},
				},
			},
			httpMethod: "GET",
			path:       "/test",
			want:       nil,
			wantErr:    false,
		},
		{
			name: "no application/json content",
			client: &UnstructuredClient{
				DocScheme: &libopenapi.DocumentModel[v3.Document]{
					Model: v3.Document{
						Paths: &v3.Paths{
							PathItems: func() *orderedmap.Map[string, *v3.PathItem] {
								m := orderedmap.New[string, *v3.PathItem]()
								content := orderedmap.New[string, *v3.MediaType]()
								content.Set("text/plain", &v3.MediaType{})
								pathItem := &v3.PathItem{
									Post: &v3.Operation{
										RequestBody: &v3.RequestBody{
											Content: content,
										},
									},
								}
								m.Set("/test", pathItem)
								return m
							}(),
						},
					},
				},
			},
			httpMethod: "POST",
			path:       "/test",
			want:       []string{},
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.client.RequestedBody(tt.httpMethod, tt.path)

			if tt.wantErr {
				if err == nil {
					t.Errorf("RequestedBody() expected error but got none")
					return
				}
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("RequestedBody() error = %v, want error containing %q", err, tt.errMsg)
				}
				return
			}

			if err != nil {
				t.Errorf("RequestedBody() unexpected error = %v", err)
				return
			}

			if tt.want == nil && got != nil {
				t.Errorf("RequestedBody() = %v, want nil", got)
				return
			}

			if tt.want != nil && got == nil {
				t.Errorf("RequestedBody() = nil, want %v", tt.want)
				return
			}

			if got != nil {
				for _, wantItem := range tt.want {
					if !got.Contains(wantItem) {
						t.Errorf("RequestedBody() missing expected item %q", wantItem)
					}
				}
			}
		})
	}
}

// updatableBodySpec mirrors the shape that produced an unfixable drift loop in production
// (Aruba Cloud Subnet): the CREATE body carries type/default/network, the UPDATE body carries
// `default` only, and both wrap their fields in the allOf+$ref indirection the real documents use.
const updatableBodySpec = `
openapi: 3.0.1
info: {title: t, version: "1.0.0"}
paths:
  /things:
    post:
      requestBody:
        content:
          application/json:
            schema: {$ref: '#/components/schemas/CreateDto'}
      responses: {'201': {description: ok}}
  /things/{id}:
    put:
      requestBody:
        content:
          application/json:
            schema: {$ref: '#/components/schemas/UpdateDto'}
      responses: {'200': {description: ok}}
    delete:
      responses: {'204': {description: ok}}
components:
  schemas:
    CreateDto:
      allOf:
        - type: object
          properties:
            metadata: {$ref: '#/components/schemas/Meta'}
            properties: {$ref: '#/components/schemas/CreateProps'}
    UpdateDto:
      allOf:
        - type: object
          properties:
            metadata: {$ref: '#/components/schemas/Meta'}
            properties: {$ref: '#/components/schemas/UpdateProps'}
    Meta:
      type: object
      properties:
        name: {type: string}
    CreateProps:
      type: object
      properties:
        type: {type: string}
        default: {type: boolean}
        network: {$ref: '#/components/schemas/Network'}
    UpdateProps:
      type: object
      properties:
        default: {type: boolean}
    Network:
      type: object
      properties:
        address: {type: string}
`

func TestUnstructuredClient_UpdatableBodyPaths(t *testing.T) {
	doc, err := libopenapi.NewDocument([]byte(updatableBodySpec))
	if err != nil {
		t.Fatalf("NewDocument() error = %v", err)
	}
	v3Doc, errs := doc.BuildV3Model()
	if len(errs) > 0 {
		t.Fatalf("BuildV3Model() errors = %v", errs)
	}
	client := &UnstructuredClient{DocScheme: v3Doc}

	sorted := func(in []string) []string {
		out := append([]string(nil), in...)
		sort.Strings(out)
		return out
	}

	tests := []struct {
		name        string
		method      string
		path        string
		want        []string
		wantErr     bool
		mustNotHave []string
	}{
		{
			// The whole point: create-only fields must NOT be comparable, or drift on them
			// produces an update that cannot carry them and the difference returns forever.
			name:        "update body yields only its own leaf paths",
			method:      "PUT",
			path:        "/things/{id}",
			want:        []string{"metadata.name", "properties.default"},
			mustNotHave: []string{"properties.type", "properties.network.address"},
		},
		{
			name:   "create body is richer, proving the asymmetry",
			method: "POST",
			path:   "/things",
			want:   []string{"metadata.name", "properties.default", "properties.network.address", "properties.type"},
		},
		{
			// No body means nothing is fixable by this verb, so nothing is comparable.
			name:   "operation without a request body yields no comparable fields",
			method: "DELETE",
			path:   "/things/{id}",
			want:   nil,
		},
		{name: "unknown path is an error", method: "PUT", path: "/nope", wantErr: true},
		{name: "unknown method is an error", method: "PATCH", path: "/things/{id}", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := client.UpdatableBodyPaths(tt.method, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("UpdatableBodyPaths() expected an error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("UpdatableBodyPaths() error = %v", err)
			}
			if !reflect.DeepEqual(sorted(got), sorted(tt.want)) {
				t.Errorf("UpdatableBodyPaths() = %v, want %v", sorted(got), sorted(tt.want))
			}
			for _, bad := range tt.mustNotHave {
				for _, g := range got {
					if g == bad {
						t.Errorf("UpdatableBodyPaths() returned %q, which the update body cannot express", bad)
					}
				}
			}
		})
	}
}

// TestBuildPath_QueryValueEscapedExactlyOnce pins the escaping contract for query parameter VALUES:
// buildPath receives raw values (every writer in builder.go — static per-verb queries, field mappings
// and spec/status auto-population — passes the CR value verbatim), so the value must reach the wire
// percent-encoded EXACTLY ONCE.
//
// The real case is RepoContent's read verb: a git ref named "builder/sock-shop" is sent as ?ref=.
// Escaped once it is "builder%2Fsock-shop", which GitHub decodes back to the branch name. Escaped
// twice it is "builder%252Fsock-shop", which GitHub reads as a literal ref named "builder%2Fsock-shop"
// and answers 404 — the defect the github-provider-kog plugin works around by PathUnescape-ing in a
// loop until the value stops changing.
func TestBuildPath_QueryValueEscapedExactlyOnce(t *testing.T) {
	baseUrl := "https://api.github.com"
	path := "/repos/{owner}/{repo}/contents/{path}"
	parameters := map[string]string{
		"owner": "krateo-platformops",
		"repo":  "oas",
		"path":  "values.yaml",
	}
	query := map[string]string{
		"ref": "builder/sock-shop",
	}

	got := buildPath(baseUrl, path, parameters, query)
	if got == nil {
		t.Fatalf("buildPath returned nil")
	}

	// On the wire: RawQuery is what net/http writes verbatim into the request line.
	const wantRawQuery = "ref=builder%2Fsock-shop"
	if got.RawQuery != wantRawQuery {
		t.Errorf("expected raw query %q, got %q", wantRawQuery, got.RawQuery)
	}

	// At the server: one decode must return the original value, with no residual escaping.
	if v := got.Query().Get("ref"); v != "builder/sock-shop" {
		t.Errorf("expected the server to decode ref back to %q, got %q", "builder/sock-shop", v)
	}
}

// TestBuildPath_QueryValueSpecialCharsEscapedOnce covers the rest of the characters that a single
// spurious url.QueryEscape pass would mangle: '%' would become '%25' twice over, and a space would
// survive as a literal '+' rather than being decoded back to a space.
func TestBuildPath_QueryValueSpecialCharsEscapedOnce(t *testing.T) {
	cases := map[string]string{
		"slash":   "builder/sock-shop",
		"space":   "hello world",
		"plus":    "a+b",
		"percent": "100%",
		"amp":     "a&b=c",
		"unicode": "caffè",
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			got := buildPath("https://api.example.com", "/search", map[string]string{}, map[string]string{"q": raw})
			if got == nil {
				t.Fatalf("buildPath returned nil")
			}
			if v := got.Query().Get("q"); v != raw {
				t.Errorf("expected one decode of the wire value to yield %q, got %q (raw query %q)", raw, v, got.RawQuery)
			}
		})
	}
}

// TestBuildPath_PathParamEscapedExactlyOnce is the path-side counterpart. Path parameters were audited
// alongside the query defect and are NOT double-escaped: buildPath applies url.PathEscape once, and the
// later url.Parse(parsed.String()) round-trip preserves the escaped form in RawPath instead of
// re-encoding it. This test pins that so the query fix cannot regress it.
func TestBuildPath_PathParamEscapedExactlyOnce(t *testing.T) {
	got := buildPath("https://api.example.com", "/repos/{owner}/{repo}/contents/{path}",
		map[string]string{"owner": "krateo-platformops", "repo": "oas", "path": "charts/values.yaml"},
		map[string]string{})
	if got == nil {
		t.Fatalf("buildPath returned nil")
	}

	const wantEscaped = "/repos/krateo-platformops/oas/contents/charts%2Fvalues.yaml"
	if got.EscapedPath() != wantEscaped {
		t.Errorf("expected escaped path %q, got %q", wantEscaped, got.EscapedPath())
	}
	const wantDecoded = "/repos/krateo-platformops/oas/contents/charts/values.yaml"
	if got.Path != wantDecoded {
		t.Errorf("expected the server to decode the path to %q, got %q", wantDecoded, got.Path)
	}
}

// --- #288: a request body declared under a non-JSON media type must not lose its schema ---

// clientFromSpec builds a client the way production does: parse a real document and let libopenapi
// resolve the schema proxies. Hand-constructing base.CreateSchemaProxy trees looks simpler and is not
// equivalent -- an unresolved proxy makes prop.Value().Schema() nil, which panics inside
// populateFromAllOf. A media-type test in particular has to exercise the parse path it is about.
func clientFromSpec(t *testing.T, spec string) *UnstructuredClient {
	t.Helper()
	doc, err := libopenapi.NewDocument([]byte(spec))
	if err != nil {
		t.Fatalf("NewDocument() error = %v", err)
	}
	v3Doc, errs := doc.BuildV3Model()
	if len(errs) > 0 {
		t.Fatalf("BuildV3Model() errors = %v", errs)
	}
	return &UnstructuredClient{DocScheme: v3Doc}
}

func mediaSpec(entries string) string {
	return `openapi: 3.0.3
info: {title: t, version: 1.0.0}
servers: [{url: "https://api.example.com"}]
paths:
  /test:
    post:
      requestBody:
        content:
` + entries + `
      responses: {"200": {description: ok}}
`
}

const patchOnlyBody = `          application/json-patch+json:
            schema:
              type: object
              properties:
                op: {type: string}
                path: {type: string}
                value: {type: string}`

// TestRequestedBodyResolvesANonJSONMediaType is the #288 regression.
//
// The lookup was hardcoded to `application/json` and a miss returned an empty set with a NIL error. A
// document declaring its body as application/json-patch+json therefore reported no body fields, the
// builder composed against nothing, and the request was still sent -- a write that succeeds while
// carrying a subset of what the CR asked for, which is worse than a 415.
func TestRequestedBodyResolvesANonJSONMediaType(t *testing.T) {
	cli := clientFromSpec(t, mediaSpec(patchOnlyBody))

	got, err := cli.RequestedBody("POST", "/test")

	require.NoError(t, err)
	require.NotNil(t, got, "the schema must be found under the media type the document declares")
	for _, want := range []string{"op", "path", "value"} {
		assert.True(t, got.Contains(want), "expected body field %q to be reported", want)
	}
}

// TestRequestedBodyStillPrefersApplicationJSON -- a document declaring BOTH must resolve exactly as it did
// before this change, so no existing document's schema moves.
func TestRequestedBodyStillPrefersApplicationJSON(t *testing.T) {
	cli := clientFromSpec(t, mediaSpec(`          application/json:
            schema:
              type: object
              properties:
                jsonField: {type: string}
          application/json-patch+json:
            schema:
              type: object
              properties:
                patchField: {type: string}`))

	got, err := cli.RequestedBody("POST", "/test")

	require.NoError(t, err)
	assert.True(t, got.Contains("jsonField"), "application/json must win when declared")
	assert.False(t, got.Contains("patchField"), "the non-JSON entry must not be consulted")
}

// TestRequestedBodyRefusesAmbiguousMediaTypes is the control arm, and the reason the fix is not simply
// "take the first entry".
//
// With two or more non-JSON media types, WHICH schema applies depends on what the caller will send, which
// this layer does not know. Guessing would reintroduce the silent-wrong-schema failure the fix exists to
// remove -- so it refuses out loud rather than picks.
func TestRequestedBodyRefusesAmbiguousMediaTypes(t *testing.T) {
	cli := clientFromSpec(t, mediaSpec(`          application/json-patch+json:
            schema:
              type: object
              properties:
                a: {type: string}
          application/merge-patch+json:
            schema:
              type: object
              properties:
                b: {type: string}`))

	_, err := cli.RequestedBody("POST", "/test")

	require.Error(t, err, "two candidate schemas and no way to choose must be an error, not a guess")
	assert.Contains(t, err.Error(), "declares 2 media types")
	assert.Contains(t, err.Error(), "application/json-patch+json")
	assert.Contains(t, err.Error(), "application/merge-patch+json",
		"the message must name the candidates, so the author can declare which applies")
}

// TestRequestedBodyTreatsASchemalessBodyAsEmpty -- a text/plain body has no JSON fields to enumerate, so
// an empty set is a true answer rather than a dropped one.
//
// Pinned because the FIRST version of the #288 fix errored here, which would have rejected documents that
// are correct. An existing test case caught it; this one names the intent so it is not re-broken.
func TestRequestedBodyTreatsASchemalessBodyAsEmpty(t *testing.T) {
	cli := clientFromSpec(t, mediaSpec(`          text/plain: {}`))

	got, err := cli.RequestedBody("POST", "/test")

	require.NoError(t, err, "a body with no schema is not an error")
	assert.Equal(t, 0, len(got), "and reports no fields")
}

// --- #307: a buffered body must declare its length, not be chunked ---

// TestRequestDeclaresContentLength asserts the framing a server actually receives.
//
// The request is built as a struct literal, so http.NewRequest's type assertion never runs and nothing
// derives ContentLength -- io.NopCloser would defeat that assertion anyway. Left unset it is 0 with a
// non-nil Body, which net/http reads as "length unknown" and sends Transfer-Encoding: chunked.
//
// Asserted through a real server rather than by inspecting the Request, because the Request's fields are
// what we set and the ENCODING is what the server sees. A test reading back req.ContentLength would pass
// against a transport that chunked anyway.
func TestRequestDeclaresContentLength(t *testing.T) {
	type seen struct {
		transferEncoding []string
		contentLength    int64
		body             string
	}
	got := make(chan seen, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- seen{transferEncoding: r.TransferEncoding, contentLength: r.ContentLength, body: string(b)}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	payload := map[string]any{"target": "power on"}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPut, srv.URL, nil)
	require.NoError(t, err)
	// Reproduce the production construction exactly: NopCloser over a bytes.Reader, length set by hand.
	req.Body = io.NopCloser(bytes.NewReader(raw))
	req.ContentLength = int64(len(raw))

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	s := <-got
	assert.Empty(t, s.transferEncoding,
		"a fully buffered body must NOT be chunked: some HTTP front ends refuse chunked requests (#307)")
	assert.Equal(t, int64(len(raw)), s.contentLength,
		"the server must receive the declared length")
	assert.JSONEq(t, string(raw), s.body,
		"and the body must arrive intact -- a ContentLength disagreeing with the bytes truncates or hangs")
}

// TestChunkedIsWhatHappensWithoutContentLength is the control arm: it pins that the bug being fixed is
// real and that the assertion above is not true for free.
//
// Without ContentLength, net/http chunks. If this ever stops chunking, the test above stops proving
// anything and should be revisited rather than trusted.
func TestChunkedIsWhatHappensWithoutContentLength(t *testing.T) {
	got := make(chan []string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		got <- r.TransferEncoding
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	raw := []byte(`{"target":"power on"}`)
	req, err := http.NewRequest(http.MethodPut, srv.URL, nil)
	require.NoError(t, err)
	req.Body = io.NopCloser(bytes.NewReader(raw))
	// ContentLength deliberately NOT set -- the pre-fix construction.

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, []string{"chunked"}, <-got,
		"without ContentLength net/http chunks -- this is the behaviour #307 removes")
}
