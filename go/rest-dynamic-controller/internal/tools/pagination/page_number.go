package pagination

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

// pageNumberPaginator implements ?page=N&per_page=M pagination.
//
// This is the strategy the great majority of collection endpoints actually use -- 162 of the 219
// array-returning GETs in GitHub's own OpenAPI document, none of which use a continuation token. Until
// it existed, findby against any of them read page one and reported not-found for everything beyond it,
// and because the reconciler CREATES on not-found, that meant duplicating an external resource that was
// sitting on page two (#119).
//
// Nothing below knows a vendor's spelling. Every varying part -- 0- vs 1-based numbering, the parameter
// names, and above all how the last page is recognised -- comes from the declaration, because an engine
// that special-cased GitHub would just relocate the bug to the next API.
type pageNumberPaginator struct {
	config *getter.PageNumberConfig
	// page is the number to send on the NEXT request.
	page int
}

// NewPageNumberPaginator creates a paginator for the pageNumber strategy.
func NewPageNumberPaginator(config *getter.PageNumberConfig) Paginator {
	return &pageNumberPaginator{config: config}
}

// Init resets the paginator to the first page of a new walk.
func (p *pageNumberPaginator) Init() {
	p.page = p.config.Request.StartPage
}

// UpdateRequest sets the page number, and the page size when one is declared.
//
// The page number is sent on the FIRST request too, unlike a continuation token, which by nature cannot
// exist before a first response. Sending it explicitly means the walk does not depend on the server's
// idea of which page "no page parameter" means -- with 0-based APIs that is exactly where an off-by-one
// silently skips or repeats the page a match was on.
func (p *pageNumberPaginator) UpdateRequest(req *http.Request) error {
	cfg := p.config.Request

	if cfg.PageIn != "query" {
		return fmt.Errorf("unsupported pageIn %q: only \"query\" is supported", cfg.PageIn)
	}

	q := req.URL.Query()
	q.Set(cfg.PagePath, strconv.Itoa(p.page))

	if cfg.PageSize > 0 {
		if cfg.SizeIn != "" && cfg.SizeIn != "query" {
			return fmt.Errorf("unsupported sizeIn %q: only \"query\" is supported", cfg.SizeIn)
		}
		if cfg.SizePath == "" {
			return fmt.Errorf("pageSize is set but sizePath is empty: there is no parameter to put it in")
		}
		q.Set(cfg.SizePath, strconv.Itoa(cfg.PageSize))
	}

	req.URL.RawQuery = q.Encode()
	return nil
}

// Next judges the page just fetched.
//
// Read the ordering carefully: maxPages is checked only AFTER a mechanism has said more pages exist, so
// a collection that genuinely ends on the last permitted page is Exhausted rather than truncated. And
// hitting the bound yields Indeterminate, never Exhausted -- "I was told to stop looking" is a different
// answer from "there is nothing there", and only the second one may reach the caller as a 404.
func (p *pageNumberPaginator) Next(page Page) (PageResult, error) {
	verdict, err := p.hasMore(page)
	if err != nil || verdict != MorePages {
		return verdict, err
	}

	// Another page exists. May we go and get it?
	pagesFetched := page.Index + 1
	if pagesFetched >= p.config.MaxPages {
		return Indeterminate, fmt.Errorf(
			"pageNumber: stopped after the configured maxPages=%d; more pages exist but were not scanned, "+
				"so this is NOT a conclusion that the resource is absent -- raise maxPages or narrow the search",
			p.config.MaxPages)
	}

	p.page++
	return MorePages, nil
}

// hasMore applies whichever end-of-collection mechanism was declared.
//
// Each branch decides between MorePages and Exhausted, or gives up with Indeterminate. The one rule
// running through all three: a mechanism that was DECLARED but cannot be read yields Indeterminate. The
// author said "this is how you will know when to stop"; if that signal is not there, the honest answer
// is that we do not know -- not that the collection ended.
func (p *pageNumberPaginator) hasMore(page Page) (PageResult, error) {
	resp := p.config.Response

	// No response block: the short-page rule. A page holding fewer items than were requested is the last
	// one. This is the common default and the reason `response` is optional -- but it is a comparison
	// against the requested size, so without pageSize there is nothing to compare with, and inventing a
	// comparison would be guessing at the exact point where guessing creates duplicates.
	if resp == nil || (resp.Header == nil && resp.Body == nil) {
		if p.config.Request.PageSize <= 0 {
			return Indeterminate, fmt.Errorf(
				"pageNumber: no response signal declared and no request.pageSize set, so the short-page rule " +
					"has nothing to compare against; declare response.header, response.body, or request.pageSize")
		}
		if len(page.Items) < p.config.Request.PageSize {
			return Exhausted, nil
		}
		// A full page. There may or may not be another one; fetching it and finding it empty is how this
		// rule terminates, and an empty page is short, so the walk still ends.
		return MorePages, nil
	}

	if resp.Header != nil {
		if page.Response == nil {
			return Indeterminate, fmt.Errorf("pageNumber: header signal %q declared but there is no response to read it from", resp.Header.Name)
		}
		raw := page.Response.Header.Values(resp.Header.Name)
		if len(raw) == 0 {
			// Header absent. Unlike a continuation token -- whose absence IS the end signal by design --
			// this header was declared as the way to recognise "more pages exist", and a declared source
			// that is not there means the declaration is wrong or the server changed. Deciding "ended"
			// from a signal we never saw is precisely the collapse this package exists to prevent.
			return Indeterminate, fmt.Errorf(
				"pageNumber: declared header %q is absent from the response, so whether more pages exist "+
					"cannot be determined; this is not a conclusion that the collection ended", resp.Header.Name)
		}
		for _, v := range raw {
			if strings.Contains(v, resp.Header.Matches) {
				return MorePages, nil
			}
		}
		// The header IS present and does not carry the marker. That is the mechanism working: GitHub's
		// Link header drops rel="next" on the last page.
		return Exhausted, nil
	}

	// Body signal: compare a total the API reports against how far we have walked.
	total, verdict, err := p.readTotal(page)
	if verdict != MorePages || err != nil {
		return verdict, err
	}

	pagesFetched := page.Index + 1
	if resp.Body.TotalPagesPath != "" {
		if pagesFetched >= total {
			return Exhausted, nil
		}
		return MorePages, nil
	}

	// totalItems: pages are derived, so a page size is required to derive them.
	size := p.config.Request.PageSize
	if size <= 0 {
		return Indeterminate, fmt.Errorf(
			"pageNumber: totalItemsPath is declared but request.pageSize is not set, so the number of pages " +
				"cannot be derived from the number of items")
	}
	if pagesFetched*size >= total {
		return Exhausted, nil
	}
	return MorePages, nil
}

// readTotal resolves the declared total from the body. The MorePages verdict it returns on success
// means only "carry on with the comparison"; the caller decides the real outcome.
func (p *pageNumberPaginator) readTotal(page Page) (int, PageResult, error) {
	body := p.config.Response.Body

	path := body.TotalPagesPath
	if path == "" {
		path = body.TotalItemsPath
	}
	if path == "" {
		return 0, Indeterminate, fmt.Errorf("pageNumber: response.body declares neither totalPagesPath nor totalItemsPath")
	}

	v := ResponseValue{In: "body", Path: path}
	raw, present, err := v.Resolve(page)
	if err != nil {
		return 0, Indeterminate, fmt.Errorf("pageNumber: %w", err)
	}
	if !present {
		return 0, Indeterminate, fmt.Errorf(
			"pageNumber: declared body path %q is absent from the response, so whether more pages exist "+
				"cannot be determined; this is not a conclusion that the collection ended", path)
	}

	total, cerr := strconv.Atoi(strings.TrimSpace(raw))
	if cerr != nil {
		// A total that is not a number is a broken declaration, not an ending.
		return 0, Indeterminate, fmt.Errorf("pageNumber: body path %q holds %q, which is not a whole number", path, raw)
	}
	if total < 0 {
		return 0, Indeterminate, fmt.Errorf("pageNumber: body path %q holds a negative total %d", path, total)
	}
	return total, MorePages, nil
}
