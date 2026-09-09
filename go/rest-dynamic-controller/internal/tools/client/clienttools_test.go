package restclient

import (
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"

	"github.com/pb33f/libopenapi"
	orderedmap "github.com/pb33f/libopenapi/orderedmap"
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
