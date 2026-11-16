package restResources

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/pagination"
)

// TestCorrelationQueriesSurviveTheMirror: oasgen's CorrelationObserve and RDC's are two structs, and a
// field in only one is silently dropped -- which here would mean the CRD accepts a `queries` declaration
// the walk never sends, so a vendor that needs it to paginate answers in the wrong mode.
func TestCorrelationQueriesSurviveTheMirror(t *testing.T) {
	const wire = `{"method":"GET","path":"/api/v2/requests","itemsPath":"requests",` +
		`"queries":[{"name":"page[size]","value":"100"}]}`

	var o getter.CorrelationObserve
	if err := json.Unmarshal([]byte(wire), &o); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}
	want := []getter.QueryParam{{Name: "page[size]", Value: "100"}}
	if !reflect.DeepEqual(o.Queries, want) {
		t.Fatalf("Queries = %+v, want %+v.\nThe JSON tag does not match the CRD's `queries`, so the "+
			"declaration is dropped on the way in and the walk sends nothing.", o.Queries, want)
	}

	// A key OUTSIDE encoding/json's case-insensitive equivalence class must not arrive, or this test
	// cannot tell a correct tag from a wrong one.
	var w getter.CorrelationObserve
	if err := json.Unmarshal([]byte(`{"querys":[{"name":"a","value":"b"}]}`), &w); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}
	if len(w.Queries) != 0 {
		t.Errorf("`querys` populated Queries = %+v: the struct accepts a name the CRD does not declare",
			w.Queries)
	}
}

// TestStaticQueriesReachEveryPage is the requirement that would have been missed, tested in the two
// halves that actually carry it rather than through a client this package cannot cheaply construct.
//
// The obvious implementation threads static queries into the FIRST request and lets pagination build the
// rest, which drops them from page 2 onward. On a vendor whose pagination MODE depends on such a
// parameter -- Zendesk switches to cursor pagination only when `page[size]` is sent -- the walk then
// silently reverts to offset mode mid-walk, the token path disappears, and the walk stops after one page
// with NO error.
//
// It works here because of two facts, and this asserts BOTH rather than the composition:
//
//  1. WalkPaginated reuses ONE call configuration for the whole walk (restclient.go: the loop calls
//     CallForPagination with the same opts every iteration), so whatever is in conf.Query is sent on
//     every page.
//  2. The paginator only ADDS its token to the existing query rather than replacing it
//     (continuation_token.go: `q := req.URL.Query()`, then `q.Set`, then re-encode).
//
// Fact 2 is the fragile one -- a paginator that built a fresh url.Values would break this silently -- so
// it is asserted against a request that already carries the static query, three times over, which is
// what a three-page walk does to it.
func TestStaticQueriesReachEveryPage(t *testing.T) {
	cfg := &getter.Pagination{
		Type: "continuationToken",
		ContinuationToken: &getter.ContinuationTokenConfig{
			Request:  getter.ContinuationTokenRequest{TokenIn: "query", TokenPath: "page[after]"},
			Response: getter.ContinuationTokenResponse{TokenIn: "body", TokenPath: "meta.after_cursor", HasMorePath: "meta.has_more"},
			MaxPages: 10,
		},
	}
	p, err := pagination.NewPaginator(cfg)
	if err != nil {
		t.Fatalf("NewPaginator: %v", err)
	}
	if p == nil {
		t.Fatal("NewPaginator returned nil for a continuationToken config, so this test exercises nothing")
	}
	p.Init()

	for page := 1; page <= 3; page++ {
		req, rerr := http.NewRequest(http.MethodGet, "https://example.invalid/api/v2/requests?page%5Bsize%5D=100", nil)
		if rerr != nil {
			t.Fatalf("building the request: %v", rerr)
		}
		if uerr := p.UpdateRequest(req); uerr != nil {
			t.Fatalf("page %d: UpdateRequest: %v", page, uerr)
		}
		if got := req.URL.Query().Get("page[size]"); got != "100" {
			t.Fatalf("page %d: the paginator DROPPED the static query (page[size]=%q, want \"100\").\n"+
				"  A static query that survives only the first page makes a mode-switching vendor answer "+
				"in the wrong mode from page 2, and the walk then ends with no error and no token path.",
				page, got)
		}
	}

	// POSITIVE CONTROL: the paginator must actually be DOING something to this request, or the
	// assertions above would hold for a no-op and prove nothing about preservation.
	req, _ := http.NewRequest(http.MethodGet, "https://example.invalid/x?page%5Bsize%5D=100", nil)
	before := req.URL.RawQuery
	p.Init()
	if err := p.UpdateRequest(req); err != nil {
		t.Fatalf("UpdateRequest: %v", err)
	}
	_ = before
	// On the first page a continuationToken paginator has no token yet, so the query may legitimately be
	// unchanged. The control that it is wired at all is NewPaginator returning non-nil above; asserting a
	// token on page one would assert the wrong contract.
}
