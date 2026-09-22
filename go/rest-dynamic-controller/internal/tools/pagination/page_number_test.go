//go:build unit || integration

package pagination

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"sigs.k8s.io/yaml"
)

func items(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = map[string]any{"id": i}
	}
	return out
}

func headerResp(kv ...string) *http.Response {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return &http.Response{Header: h}
}

func pageNumberCfg(maxPages int, size int, resp *getter.PageNumberResponse) *getter.PageNumberConfig {
	return &getter.PageNumberConfig{
		Request: getter.PageNumberRequest{
			PageIn:    "query",
			PagePath:  "page",
			StartPage: 1,
			SizeIn:    "query",
			SizePath:  "per_page",
			PageSize:  size,
		},
		Response: resp,
		MaxPages: maxPages,
	}
}

// TestPageNumberNeverConcludesAbsenceFromAMissingSignal is the forbidding test for #119.
//
// Every row here is a case where the paginator has NO trustworthy basis to say the collection ended. The
// assertion is not that it returns some error -- it is specifically that it never returns Exhausted,
// because Exhausted is the only verdict the findby loop turns into a 404, and the reconciler CREATES on
// a 404. A row flipping to Exhausted does not mean "a test got stricter"; it means this code would
// duplicate an external resource it never finished searching for.
func TestPageNumberNeverConcludesAbsenceFromAMissingSignal(t *testing.T) {
	cases := []struct {
		name string
		cfg  *getter.PageNumberConfig
		page Page
	}{
		{
			name: "declared header is absent from the response",
			cfg: pageNumberCfg(10, 2, &getter.PageNumberResponse{
				Header: &getter.PageNumberHeaderSignal{Name: "Link", Matches: `rel="next"`},
			}),
			page: Page{Response: headerResp("Content-Type", "application/json"), Items: items(2)},
		},
		{
			name: "declared header signal but no response at all",
			cfg: pageNumberCfg(10, 2, &getter.PageNumberResponse{
				Header: &getter.PageNumberHeaderSignal{Name: "Link", Matches: `rel="next"`},
			}),
			page: Page{Items: items(2)},
		},
		{
			name: "declared body total is absent from the body",
			cfg: pageNumberCfg(10, 2, &getter.PageNumberResponse{
				Body: &getter.PageNumberBodySignal{TotalItemsPath: "total_count"},
			}),
			page: Page{Body: []byte(`{"values":[{"id":1}]}`), Items: items(2)},
		},
		{
			name: "declared body total is not a number",
			cfg: pageNumberCfg(10, 2, &getter.PageNumberResponse{
				Body: &getter.PageNumberBodySignal{TotalPagesPath: "total_pages"},
			}),
			page: Page{Body: []byte(`{"total_pages":"lots"}`), Items: items(2)},
		},
		{
			name: "body is not a JSON object so no path can be read",
			cfg: pageNumberCfg(10, 2, &getter.PageNumberResponse{
				Body: &getter.PageNumberBodySignal{TotalPagesPath: "total_pages"},
			}),
			page: Page{Body: []byte(`[{"id":1}]`), Items: items(2)},
		},
		{
			name: "totalItems declared without a page size to derive pages from",
			cfg: pageNumberCfg(10, 0, &getter.PageNumberResponse{
				Body: &getter.PageNumberBodySignal{TotalItemsPath: "total_count"},
			}),
			page: Page{Body: []byte(`{"total_count":90}`), Items: items(2)},
		},
		{
			name: "short-page rule selected but no page size was requested",
			cfg:  pageNumberCfg(10, 0, nil),
			page: Page{Items: items(2)},
		},
		{
			name: "empty response block is the same as none, and still needs a page size",
			cfg:  pageNumberCfg(10, 0, &getter.PageNumberResponse{}),
			page: Page{Items: items(2)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPageNumberPaginator(tc.cfg)
			p.Init()

			got, err := p.Next(tc.page)
			if got == Exhausted {
				t.Fatalf("returned Exhausted -- the findby loop turns that into a 404 and the reconciler "+
					"CREATES on 404, so this would duplicate an external resource. Want Indeterminate. err=%v", err)
			}
			if got != Indeterminate {
				t.Fatalf("got %v, want Indeterminate", got)
			}
			if err == nil {
				t.Fatal("Indeterminate with a nil error: the reason the walk could not conclude has to reach the user")
			}
		})
	}
}

// TestPageNumberMaxPagesIsNotAbsence pins the outcome of the bound the AUTHOR sets. Truncating a search
// at a configured limit is a decision about effort; it says nothing about whether the resource exists,
// and reporting it as absence would create a duplicate with the added insult of being deliberate.
func TestPageNumberMaxPagesIsNotAbsence(t *testing.T) {
	cfg := pageNumberCfg(3, 2, &getter.PageNumberResponse{
		Header: &getter.PageNumberHeaderSignal{Name: "Link", Matches: `rel="next"`},
	})
	p := NewPageNumberPaginator(cfg)
	p.Init()

	// Pages 1 and 2 each advertise a next page and are allowed to proceed.
	for i := 0; i < 2; i++ {
		got, err := p.Next(Page{Response: headerResp("Link", `<https://x/?page=2>; rel="next"`), Items: items(2), Index: i})
		if err != nil || got != MorePages {
			t.Fatalf("page %d: got %v err %v, want MorePages", i, got, err)
		}
	}

	// The third page still advertises a next page, but maxPages=3 is reached.
	got, err := p.Next(Page{Response: headerResp("Link", `<https://x/?page=4>; rel="next"`), Items: items(2), Index: 2})
	if got == Exhausted {
		t.Fatal("maxPages reported as Exhausted: 'I was told to stop looking' became 'it is not there'")
	}
	if got != Indeterminate {
		t.Fatalf("got %v, want Indeterminate", got)
	}
	if err == nil || !strings.Contains(err.Error(), "maxPages") {
		t.Fatalf("the error must name maxPages so the author knows which knob to turn, got %v", err)
	}
}

// TestPageNumberExhaustedOnlyWhenTheMechanismSaysSo covers the other half: when a declared mechanism
// genuinely reports the end, the walk must conclude. Indeterminate everywhere would be safe and useless.
func TestPageNumberExhaustedOnlyWhenTheMechanismSaysSo(t *testing.T) {
	cases := []struct {
		name string
		cfg  *getter.PageNumberConfig
		page Page
		want PageResult
	}{
		{
			name: "header present without the marker is the last page",
			cfg: pageNumberCfg(10, 2, &getter.PageNumberResponse{
				Header: &getter.PageNumberHeaderSignal{Name: "Link", Matches: `rel="next"`},
			}),
			page: Page{Response: headerResp("Link", `<https://x/?page=1>; rel="prev"`), Items: items(2)},
			want: Exhausted,
		},
		{
			name: "header carrying the marker means another page",
			cfg: pageNumberCfg(10, 2, &getter.PageNumberResponse{
				Header: &getter.PageNumberHeaderSignal{Name: "Link", Matches: `rel="next"`},
			}),
			page: Page{Response: headerResp("Link", `<https://x/?page=2>; rel="next"`), Items: items(2)},
			want: MorePages,
		},
		{
			name: "marker split across repeated headers is still found",
			cfg: pageNumberCfg(10, 2, &getter.PageNumberResponse{
				Header: &getter.PageNumberHeaderSignal{Name: "Link", Matches: `rel="next"`},
			}),
			page: Page{Response: headerResp("Link", `<https://x/?page=1>; rel="prev"`, "Link", `<https://x/?page=2>; rel="next"`), Items: items(2)},
			want: MorePages,
		},
		{
			name: "totalPages reached",
			cfg: pageNumberCfg(10, 2, &getter.PageNumberResponse{
				Body: &getter.PageNumberBodySignal{TotalPagesPath: "total_pages"},
			}),
			page: Page{Body: []byte(`{"total_pages":1}`), Items: items(2)},
			want: Exhausted,
		},
		{
			name: "totalPages not reached",
			cfg: pageNumberCfg(10, 2, &getter.PageNumberResponse{
				Body: &getter.PageNumberBodySignal{TotalPagesPath: "total_pages"},
			}),
			page: Page{Body: []byte(`{"total_pages":4}`), Items: items(2)},
			want: MorePages,
		},
		{
			name: "totalItems consumed by the pages fetched so far",
			cfg: pageNumberCfg(10, 2, &getter.PageNumberResponse{
				Body: &getter.PageNumberBodySignal{TotalItemsPath: "total_count"},
			}),
			page: Page{Body: []byte(`{"total_count":2}`), Items: items(2)},
			want: Exhausted,
		},
		{
			name: "totalItems with a remainder still to fetch",
			cfg: pageNumberCfg(10, 2, &getter.PageNumberResponse{
				Body: &getter.PageNumberBodySignal{TotalItemsPath: "total_count"},
			}),
			page: Page{Body: []byte(`{"total_count":3}`), Items: items(2)},
			want: MorePages,
		},
		{
			name: "nested totalItems path, same dialect as async.poll.statusPath",
			cfg: pageNumberCfg(10, 2, &getter.PageNumberResponse{
				Body: &getter.PageNumberBodySignal{TotalItemsPath: "meta.pagination.total"},
			}),
			page: Page{Body: []byte(`{"meta":{"pagination":{"total":2}}}`), Items: items(2)},
			want: Exhausted,
		},
		{
			name: "zero total is a genuine empty collection",
			cfg: pageNumberCfg(10, 2, &getter.PageNumberResponse{
				Body: &getter.PageNumberBodySignal{TotalItemsPath: "total_count"},
			}),
			page: Page{Body: []byte(`{"total_count":0}`), Items: nil},
			want: Exhausted,
		},
		{
			name: "short page ends the walk when no signal is declared",
			cfg:  pageNumberCfg(10, 5, nil),
			page: Page{Items: items(3)},
			want: Exhausted,
		},
		{
			name: "full page continues under the short-page rule",
			cfg:  pageNumberCfg(10, 5, nil),
			page: Page{Items: items(5)},
			want: MorePages,
		},
		{
			name: "empty page is short, so an over-run terminates rather than spinning",
			cfg:  pageNumberCfg(10, 5, nil),
			page: Page{Items: nil},
			want: Exhausted,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPageNumberPaginator(tc.cfg)
			p.Init()
			got, err := p.Next(tc.page)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPageNumberUpdateRequest pins what actually goes on the wire, including on the FIRST request --
// with a 0-based API, leaving the first page implicit is exactly where an off-by-one skips the page a
// match was sitting on.
func TestPageNumberUpdateRequest(t *testing.T) {
	cfg := pageNumberCfg(10, 25, nil)
	cfg.Request.StartPage = 0

	p := NewPageNumberPaginator(cfg)
	p.Init()

	newReq := func() *http.Request {
		u, _ := url.Parse("https://api.example.com/things?filter=active")
		return &http.Request{URL: u, Header: http.Header{}}
	}

	req := newReq()
	if err := p.UpdateRequest(req); err != nil {
		t.Fatalf("first request: %v", err)
	}
	q := req.URL.Query()
	if q.Get("page") != "0" {
		t.Fatalf("first request page = %q, want %q -- the start page must be sent explicitly", q.Get("page"), "0")
	}
	if q.Get("per_page") != "25" {
		t.Fatalf("per_page = %q, want 25", q.Get("per_page"))
	}
	if q.Get("filter") != "active" {
		t.Fatalf("pagination clobbered an existing query parameter: filter = %q", q.Get("filter"))
	}

	// Advance once and confirm the number moves.
	if _, err := p.Next(Page{Items: items(25)}); err != nil {
		t.Fatalf("Next: %v", err)
	}
	req = newReq()
	if err := p.UpdateRequest(req); err != nil {
		t.Fatalf("second request: %v", err)
	}
	if got := req.URL.Query().Get("page"); got != "1" {
		t.Fatalf("second request page = %q, want 1", got)
	}
}

func TestPageNumberUpdateRequestRejectsUnsupportedLocations(t *testing.T) {
	cfg := pageNumberCfg(10, 25, nil)
	cfg.Request.PageIn = "header"
	p := NewPageNumberPaginator(cfg)
	p.Init()
	u, _ := url.Parse("https://api.example.com/things")
	if err := p.UpdateRequest(&http.Request{URL: u, Header: http.Header{}}); err == nil {
		t.Fatal("pageIn=header was accepted: unsupported surface must fail loudly, not paginate wrongly")
	}

	cfg = pageNumberCfg(10, 25, nil)
	cfg.Request.SizePath = ""
	p = NewPageNumberPaginator(cfg)
	p.Init()
	u, _ = url.Parse("https://api.example.com/things")
	if err := p.UpdateRequest(&http.Request{URL: u, Header: http.Header{}}); err == nil {
		t.Fatal("pageSize with no sizePath was accepted: there is no parameter to put it in")
	}
}

// TestNewPaginatorPageNumber covers the factory's refusals. maxPages having NO default is the point: a
// zero must be rejected, never quietly replaced with a bound nobody chose.
func TestNewPaginatorPageNumber(t *testing.T) {
	ok := pageNumberCfg(10, 25, nil)

	cases := []struct {
		name    string
		cfg     *getter.Pagination
		wantErr string
	}{
		{
			name: "valid",
			cfg:  &getter.Pagination{Type: "pageNumber", PageNumber: ok},
		},
		{
			name:    "missing config block",
			cfg:     &getter.Pagination{Type: "pageNumber"},
			wantErr: "config block is missing",
		},
		{
			name:    "both strategies declared",
			cfg:     &getter.Pagination{Type: "pageNumber", PageNumber: ok, ContinuationToken: &getter.ContinuationTokenConfig{}},
			wantErr: "also set",
		},
		{
			name:    "continuationToken with a stray pageNumber block",
			cfg:     &getter.Pagination{Type: "continuationToken", ContinuationToken: &getter.ContinuationTokenConfig{}, PageNumber: ok},
			wantErr: "also set",
		},
		{
			name:    "maxPages unset",
			cfg:     &getter.Pagination{Type: "pageNumber", PageNumber: pageNumberCfg(0, 25, nil)},
			wantErr: "no default",
		},
		{
			name:    "maxPages above the hard ceiling",
			cfg:     &getter.Pagination{Type: "pageNumber", PageNumber: pageNumberCfg(MaxFindByPages+1, 25, nil)},
			wantErr: "ceiling",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewPaginator(tc.cfg)
			if tc.wantErr == "" {
				if err != nil || p == nil {
					t.Fatalf("got p=%v err=%v, want a paginator", p, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want an error containing %q, got none", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestPageNumberWalksAServerOfMorePagesThanOne is the end-to-end shape of the actual bug: a match that
// lives on a later page. Before pageNumber existed, this returned not-found from page one.
func TestPageNumberWalksAServerOfMorePagesThanOne(t *testing.T) {
	const pageSize = 2
	// 5 items over 3 pages; the target is on the last one.
	all := []string{"a", "b", "c", "d", "target"}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		start := 0
		switch page {
		case "1":
			start = 0
		case "2":
			start = 2
		case "3":
			start = 4
		default:
			t.Errorf("unexpected page %q", page)
		}
		end := start + pageSize
		if end > len(all) {
			end = len(all)
		}
		if start < len(all) && end > start+0 && len(all) > start+pageSize {
			w.Header().Set("Link", `<`+r.URL.String()+`>; rel="next"`)
		}
		body := `{"values":[`
		for i, v := range all[start:end] {
			if i > 0 {
				body += ","
			}
			body += `{"name":"` + v + `"}`
		}
		body += `]}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	cfg := pageNumberCfg(10, pageSize, &getter.PageNumberResponse{
		Header: &getter.PageNumberHeaderSignal{Name: "Link", Matches: `rel="next"`},
	})
	p := NewPageNumberPaginator(cfg)
	p.Init()

	found := false
	for i := 0; i < 5 && !found; i++ {
		u, _ := url.Parse(srv.URL + "/things")
		req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
		if err := p.UpdateRequest(req); err != nil {
			t.Fatalf("UpdateRequest: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		_ = resp.Body.Close()
		body := buf[:n]

		if strings.Contains(string(body), `"target"`) {
			found = true
			break
		}

		// The item count is what the short-page rule and this test both need; the real loop gets it from
		// ExtractItemsFromResponse.
		count := strings.Count(string(body), `{"name":`)
		verdict, err := p.Next(Page{Response: resp, Body: body, Items: items(count), Index: i})
		if err != nil {
			t.Fatalf("page %d: %v", i, err)
		}
		if verdict == Exhausted {
			t.Fatalf("declared the collection exhausted on page %d, but the match is on page 3 -- "+
				"the findby loop would 404 here and the reconciler would create a duplicate", i+1)
		}
		if verdict != MorePages {
			t.Fatalf("page %d: got %v", i, verdict)
		}
	}

	if !found {
		t.Fatal("never reached the item on page 3")
	}
}

// TestMaxFindByPagesMatchesTheShippedCRD ties this package's hard ceiling to the number the CRD admits.
//
// These are two halves of one limit: the CRD caps what an author may WRITE in maxPages, and this
// constant caps what the findby loop will WALK. If they drift, a RestDefinition passes admission and is
// then cut short by a different, invisible bound -- and a search that stops early is the thing this
// whole package exists to keep from looking like absence.
//
// The fixture read here is rest-dynamic-controller's copy of the CRD oasgen-provider generates. CI's
// crd_drift guard keeps that copy honest, so asserting against it is equivalent to asserting against
// the shipped CRD without reaching across modules.
func TestMaxFindByPagesMatchesTheShippedCRD(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/crds/ogen.krateo.io_restdefinitions.yaml")
	if err != nil {
		t.Fatalf("reading the CRD fixture: %v", err)
	}

	var crd map[string]any
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatalf("parsing the CRD fixture: %v", err)
	}

	node := crd["spec"].(map[string]any)["versions"].([]any)[0].(map[string]any)["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)
	for _, step := range []string{"properties", "spec", "properties", "resource", "properties", "verbsDescription"} {
		next, ok := node[step].(map[string]any)
		if !ok {
			t.Fatalf("walking the CRD stopped at %q", step)
		}
		node = next
	}
	maxPages, ok := node["items"].(map[string]any)["properties"].(map[string]any)["pagination"].(map[string]any)["properties"].(map[string]any)["pageNumber"].(map[string]any)["properties"].(map[string]any)["maxPages"].(map[string]any)
	if !ok {
		t.Fatal("the CRD has no pageNumber.maxPages: this module and oasgen-provider have drifted apart")
	}

	got, ok := maxPages["maximum"].(float64)
	if !ok {
		t.Fatalf("maxPages has no numeric maximum: %#v", maxPages["maximum"])
	}
	if int(got) != MaxFindByPages {
		t.Fatalf("CRD admits maxPages up to %d but this loop walks at most %d: an author could configure "+
			"a bound that a different, invisible one cuts short", int(got), MaxFindByPages)
	}
}
