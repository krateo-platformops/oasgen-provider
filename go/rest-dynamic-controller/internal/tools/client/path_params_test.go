package restclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pb33f/libopenapi"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
)

func boolp(b bool) *bool { return &b }

// Path-level parameters apply to every operation in the item. We used to fetch pathItem and discard it,
// examining only the operation's own block -- so a document that factors shared parameters up to the path
// item, which is the idiomatic form, presented ZERO parameters to validation and its correct
// `required: true` was never consulted (#361).
//
// Six documents were observed in exactly that shape, all declaring `required: true`, all unchecked. The
// first diagnosis blamed the vendor's document; the declarations were present and correct.
func TestMergeParametersInheritsPathLevel(t *testing.T) {
	inherited := []*v3.Parameter{{Name: "id", In: "path", Required: boolp(true)}}

	got := mergeParameters(inherited, nil)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1: a path-level parameter must reach validation even when the operation "+
			"declares none of its own -- that is the case that shipped an unsubstituted {id} to the wire", len(got))
	}
	if got[0].Name != "id" || got[0].In != "path" {
		t.Errorf("got %q in %q, want id in path", got[0].Name, got[0].In)
	}
}

// Per the specification an operation-level entry overrides an inherited one when name AND in both match.
func TestMergeParametersOperationOverridesOnNameAndIn(t *testing.T) {
	inherited := []*v3.Parameter{{Name: "id", In: "path", Required: boolp(false)}}
	own := []*v3.Parameter{{Name: "id", In: "path", Required: boolp(true)}}

	got := mergeParameters(inherited, own)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1: the operation's entry replaces the inherited one rather than "+
			"appearing alongside it", len(got))
	}
	if got[0].Required == nil || !*got[0].Required {
		t.Error("the inherited entry won: an operation-level declaration must take precedence")
	}
}

// BOTH halves of the key matter. `id` in `path` and `id` in `query` are DISTINCT parameters, so neither
// should displace the other -- keying on name alone would silently drop one.
func TestMergeParametersKeysOnNameAndInTogether(t *testing.T) {
	inherited := []*v3.Parameter{{Name: "id", In: "path", Required: boolp(true)}}
	own := []*v3.Parameter{{Name: "id", In: "query", Required: boolp(true)}}

	got := mergeParameters(inherited, own)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: `id` in path and `id` in query are different parameters; keying on "+
			"name alone drops the inherited path parameter and reopens #361", len(got))
	}
}

// Unchanged behaviour for documents that declare everything at the operation level.
func TestMergeParametersWithNoInheritedSet(t *testing.T) {
	own := []*v3.Parameter{{Name: "a", In: "query"}, {Name: "b", In: "header"}}

	if got := mergeParameters(nil, own); len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
	if got := mergeParameters([]*v3.Parameter{}, own); len(got) != 2 {
		t.Errorf("empty inherited: len = %d, want 2", len(got))
	}
}

// A nil entry in either slice must not panic -- these come from a parsed vendor document.
func TestMergeParametersToleratesNilEntries(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic on a nil parameter entry: %v. These slices come from a parsed vendor "+
				"document, and a panic here takes the controller pod down", r)
		}
	}()
	got := mergeParameters([]*v3.Parameter{nil, {Name: "id", In: "path"}}, []*v3.Parameter{nil})
	if len(got) < 1 {
		t.Error("the non-nil inherited parameter was dropped")
	}
}

// The end-to-end half: a request whose path template has no value for a placeholder must be REFUSED rather
// than sent.
//
// Measured on a live mock as `GET /v2.1/flavors/%7Bid%7D` twelve times, where `%7Bid%7D` is `{id}` (#361).
// Worse than a failure, because a real API answers it plausibly -- Nova returns 404 itemNotFound, which is
// indistinguishable from "this resource does not exist", so the controller concludes something about
// existence from a URL it should never have built.
//
// The same reporter later confirmed that substitution works correctly WHEN A VALUE EXISTS: two requests in
// the same run carried the real identifier. So the defect is exactly the empty case, which is what this
// asserts on both sides.
func TestARequestWithAnUnresolvedPlaceholderIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a request reached the server with path %q -- it should have been refused before "+
			"sending, because the {id} placeholder has no value", r.URL.RequestURI())
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := createTestClient(t)
	client.Server = srv.URL

	_, err := client.Call(context.Background(), srv.Client(), "/api/test/{id}",
		&RequestConfiguration{Method: "GET", Parameters: map[string]string{}})
	if err == nil {
		t.Fatal("want an error: a path template with no value for {id} must not be sent")
	}
	if !strings.Contains(err.Error(), "unresolved path parameter") {
		t.Errorf("error = %q, want it to name the unresolved parameter so an operator can see WHICH "+
			"value is missing rather than only that the request failed", err)
	}
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("error = %q, want it to name `id`", err)
	}
}

// And the control: with a value present, the request goes out substituted. Without this, the guard above
// could pass by refusing everything.
func TestARequestWithAResolvedPlaceholderIsSent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := createTestClient(t)
	client.Server = srv.URL

	_, err := client.Call(context.Background(), srv.Client(), "/api/test/{id}",
		&RequestConfiguration{Method: "GET", Parameters: map[string]string{"id": "real-id-42"}})
	if err != nil {
		t.Fatalf("a resolved path must be sent, got %v", err)
	}
	if got != "/api/test/real-id-42" {
		t.Errorf("server saw %q, want /api/test/real-id-42 -- substitution must still work, which the "+
			"reporter confirmed it does when a value exists", got)
	}
}

// THE PREMISE TEST, and it exists because mutation-testing caught its absence.
//
// Every TestMergeParameters* case above calls mergeParameters DIRECTLY, so all of them keep passing if
// ValidateRequest never uses it -- which is exactly what the original defect was. Reverting the call site
// to `getDoc.Parameters` left the whole suite green until this test existed.
//
// So this drives ValidateRequest against a document in the shape that was failing: `parameters` declared
// on the PATH ITEM, the operation declaring none of its own, `required: true` present and correct.
func TestValidateRequestConsultsPathLevelParameters(t *testing.T) {
	const doc = `
openapi: 3.0.3
info: {title: t, version: "1.0"}
paths:
  /flavors/{id}:
    parameters:
      - name: id
        in: path
        required: true
        schema: {type: string}
    get:
      operationId: getFlavor
      responses: {"200": {description: ok}}
    delete:
      operationId: deleteFlavor
      responses: {"204": {description: gone}}
`
	d, err := libopenapi.NewDocument([]byte(strings.TrimSpace(doc)))
	if err != nil {
		t.Fatal(err)
	}
	model, errs := d.BuildV3Model()
	if model == nil {
		t.Fatalf("no model: %v", errs)
	}
	client := &UnstructuredClient{DocScheme: model, Server: "https://example.com"}

	// Both verbs inherit the path-level parameter, so both must reject its absence. Asserting on only one
	// would miss a merge applied to a single operation.
	for _, method := range []string{"GET", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			err := client.ValidateRequest(method, "/flavors/{id}", map[string]string{}, nil, nil, nil)
			if err == nil {
				t.Fatalf("ValidateRequest accepted %s with `id` absent. The parameter is declared "+
					"`required: true` on the PATH ITEM, which applies to every operation in it -- so "+
					"ignoring the path-level block lets a correct document through unchecked, and the "+
					"unsubstituted template reaches the wire (#361)", method)
			}
			if !strings.Contains(err.Error(), "id") {
				t.Errorf("error = %q, want it to name `id`", err)
			}
		})
	}

	// And the control: supplied, it validates. Without this the test could pass by rejecting everything.
	if err := client.ValidateRequest("GET", "/flavors/{id}", map[string]string{"id": "x"}, nil, nil, nil); err != nil {
		t.Errorf("ValidateRequest rejected a request with `id` supplied: %v", err)
	}
}
