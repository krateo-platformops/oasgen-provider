//go:build unit

package builder

import (
	"context"
	"testing"

	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client/apiaction"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/pb33f/libopenapi"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// #463. A dotted additionalStatusFields entry such as `owner.login` renders the status schema NESTED, so
// the top-level status key is `owner`. processFields fills a request parameter by matching a TOP-LEVEL
// field name, so the value handed over is the whole map -- and the four string-valued destinations
// (Parameters, Query, Headers, Cookies) rendered it with fmt.Sprintf("%v"), putting the literal
// `map[login:octocat]` on the wire. That is a syntactically valid URL addressing something that does not
// exist, so the vendor 404s and the reconciler acts on not-found by CREATING (#117).
//
// These drive the real BuildCallConfig over a real document, for the reason header_population_test.go
// states: a hand-built ReqParams would assert that a branch copies a value, not that the value reaches
// ReqParams at all.

const nonScalarDoc = `
openapi: 3.0.0
info: {title: t, version: "1"}
paths:
  /repos/{repo}:
    get:
      parameters:
        - name: repo
          in: path
          required: true
          schema: {type: string}
        - name: owner
          in: query
          schema: {type: string}
        - name: X-Owner
          in: header
          schema: {type: string}
        - name: sess
          in: cookie
          schema: {type: string}
      responses:
        "200": {description: ok}
`

func nonScalarCallInfo(t *testing.T) *CallInfo {
	t.Helper()
	d, err := libopenapi.NewDocument([]byte(nonScalarDoc))
	if err != nil {
		t.Fatalf("fixture is not a document: %v", err)
	}
	v3Doc, errs := d.BuildV3Model()
	if len(errs) > 0 {
		t.Fatalf("fixture did not build: %v", errs)
	}
	cli := &restclient.UnstructuredClient{DocScheme: v3Doc}
	info := &getter.Info{
		Resource: getter.Resource{
			Kind:        "Repo",
			Identifiers: []string{"repo"},
			VerbsDescription: []getter.VerbsDescription{
				{Action: string(apiaction.Get), Method: "GET", Path: "/repos/{repo}"},
			},
		},
	}
	_, callInfo, err := APICallBuilder(cli, info, apiaction.Get)
	if err != nil {
		t.Fatalf("APICallBuilder: %v", err)
	}
	if callInfo == nil {
		t.Fatal("no CallInfo")
	}
	// THE PREMISE. If these names are not in ReqParams, every case below passes trivially because the
	// branch never fires. Assert the input before the behaviour.
	for name, present := range map[string]bool{
		"repo":    callInfo.ReqParams.Parameters.Contains("repo"),
		"owner":   callInfo.ReqParams.Query.Contains("owner"),
		"X-Owner": callInfo.ReqParams.Headers.Contains("X-Owner"),
		"sess":    callInfo.ReqParams.Cookies.Contains("sess"),
	} {
		if !present {
			t.Fatalf("ReqParams does not contain %q, so these cases cannot exercise the population branch", name)
		}
	}
	return callInfo
}

func crWithStatus(status map[string]interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec":   map[string]interface{}{},
		"status": status,
	}}
	u.SetKind("Repo")
	u.SetName("repo-1")
	u.SetNamespace("default")
	return u
}

// TestScalarParamValue pins the renderer. Each accepted type is one unstructured data can actually hold:
// k8s decodes JSON numbers as int64 or float64, never int.
func TestScalarParamValue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  string
		ok    bool
	}{
		{"string", "octocat", "octocat", true},
		{"bool", true, "true", true},
		{"int64", int64(42), "42", true},
		{"float64", 2.5, "2.5", true},
		{"nil renders empty, NOT <nil>", nil, "", true},
		{"map is refused", map[string]interface{}{"login": "octocat"}, "", false},
		{"slice is refused", []interface{}{"a", "b"}, "", false},
		{"an unconsidered type fails closed", struct{ A int }{1}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := scalarParamValue(tc.value)
			if ok != tc.ok || got != tc.want {
				t.Errorf("scalarParamValue(%#v) = (%q, %v), want (%q, %v)", tc.value, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestANestedStatusValueRefusesTheRequest covers all FOUR string-valued destinations. The first pass of
// this fix guarded only Parameters and Query; Headers and Cookies were populated by #305 and have the
// identical ending, so each gets its own arm rather than one arm standing for the set.
func TestANestedStatusValueRefusesTheRequest(t *testing.T) {
	nested := map[string]interface{}{"login": "octocat"}

	for _, tc := range []struct{ name, field string }{
		{"path parameter", "repo"},
		{"query parameter", "owner"},
		{"header", "X-Owner"},
		{"cookie", "sess"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ci := nonScalarCallInfo(t)
			cfg := BuildCallConfig(context.Background(), ci, crWithStatus(map[string]interface{}{tc.field: nested}), nil, nil)
			if cfg == nil {
				t.Fatal("no RequestConfiguration")
			}
			if cfg.BuildErr == nil {
				t.Fatalf("BuildErr is nil for a composite %s value: the request would carry %q",
					tc.name, "map[login:octocat]")
			}
			// The rendered map must not have been written anywhere.
			for dest, m := range map[string]map[string]string{
				"Parameters": cfg.Parameters, "Query": cfg.Query, "Headers": cfg.Headers, "Cookies": cfg.Cookies,
			} {
				if v, ok := m[tc.field]; ok {
					t.Errorf("%s[%s] was written as %q despite the refusal", dest, tc.field, v)
				}
			}
		})
	}
}

// TestAScalarStatusValueStillPopulates is the control, and without it the arms above pass for a
// provider that refuses everything.
func TestAScalarStatusValueStillPopulates(t *testing.T) {
	ci := nonScalarCallInfo(t)
	cfg := BuildCallConfig(context.Background(), ci, crWithStatus(map[string]interface{}{
		"repo": "hello-world", "owner": "octocat", "X-Owner": "octocat", "sess": "abc",
	}), nil, nil)
	if cfg == nil {
		t.Fatal("no RequestConfiguration")
	}
	if cfg.BuildErr != nil {
		t.Fatalf("BuildErr set for all-scalar values: %v", cfg.BuildErr)
	}
	for _, tc := range []struct {
		dest, field, want string
		got               map[string]string
	}{
		{"Parameters", "repo", "hello-world", cfg.Parameters},
		{"Query", "owner", "octocat", cfg.Query},
		{"Headers", "X-Owner", "octocat", cfg.Headers},
		{"Cookies", "sess", "abc", cfg.Cookies},
	} {
		if got := tc.got[tc.field]; got != tc.want {
			t.Errorf("%s[%s] = %q, want %q", tc.dest, tc.field, got, tc.want)
		}
	}
}
