package pagination

import (
	"fmt"
	"net/http"
	"strconv"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

// continuationTokenPaginator implements the Paginator interface for continuationToken-based pagination.
type continuationTokenPaginator struct {
	config      *getter.ContinuationTokenConfig
	nextToken   string
	isFirstCall bool
}

// NewContinuationTokenPaginator creates a new paginator for the continuation token strategy.
func NewContinuationTokenPaginator(config *getter.ContinuationTokenConfig) Paginator {
	return &continuationTokenPaginator{
		config: config,
	}
}

// Init resets the paginator's state for a new sequence of calls.
func (p *continuationTokenPaginator) Init() {
	//log.Print("Initializing continuationTokenPaginator")
	p.nextToken = ""
	p.isFirstCall = true
}

// UpdateRequest adds the pagination token to the http.Request.
func (p *continuationTokenPaginator) UpdateRequest(req *http.Request) error {
	// Don't add a token on the very first call or if the token is empty.
	if p.isFirstCall || p.nextToken == "" {
		p.isFirstCall = false
		return nil
	}

	cfg := p.config.Request
	switch cfg.TokenIn {
	case "query":
		q := req.URL.Query()
		q.Set(cfg.TokenPath, p.nextToken)
		req.URL.RawQuery = q.Encode()
	case "header":
		req.Header.Set(cfg.TokenPath, p.nextToken)
	default:
		return fmt.Errorf("unsupported tokenIn for request: %s", cfg.TokenIn)
	}

	return nil
}

// Next extracts the next token from the response and reports whether another page exists.
//
// The body case used to be a commented-out stub that fell through to "no token", so a token declared
// in the body silently ended the walk after page one -- the exact silent-termination this type system
// now forbids. It goes through the shared ResponseValue primitive instead.
func (p *continuationTokenPaginator) Next(page Page) (PageResult, error) {
	verdict, token, err := p.hasMore(page)
	if verdict != MorePages || err != nil {
		p.nextToken = ""
		return verdict, err
	}

	// Another page exists. May we go and get it?
	//
	// Checked only AFTER a mechanism has said more pages exist, exactly as pageNumber does it, so a
	// collection that genuinely ends on the last permitted page is Exhausted rather than truncated.
	// And hitting the bound is Indeterminate, NEVER Exhausted: "I was told to stop looking" is a
	// different answer from "there is nothing there", and only the second may reach the caller as a 404.
	// On a dispatch kind the difference is a re-run, because the remedy for not-found is to invoke again.
	// MaxPages <= 0 means NO BOUND WAS DECLARED, which is the DESIGNED default for this strategy: the
	// token-absence rule terminates a walk correctly without one, and four live header-token findby
	// kinds rely on that.
	//
	// The guard is load-bearing rather than defensive, and it was written while maxPages was briefly
	// required. `1 >= 0` is true on the first page, so without it every walk with no declared bound
	// returns Indeterminate -- which would have been a hard runtime break on controller upgrade, with
	// no apply involved, because the apiserver validates on write and not on read.
	//
	// Caught by this package's own pre-existing ShouldContinueHeader test, which builds a config with no
	// maxPages. That test is the control for this case: it must keep passing.
	pagesFetched := page.Index + 1
	if p.config.MaxPages > 0 && pagesFetched >= p.config.MaxPages {
		p.nextToken = ""
		return Indeterminate, fmt.Errorf(
			"continuationToken: stopped after the configured maxPages=%d; more pages exist but were not "+
				"scanned, so this is NOT a conclusion that the resource is absent -- raise maxPages or "+
				"narrow the search", p.config.MaxPages)
	}

	p.nextToken = token
	return MorePages, nil
}

// hasMore applies whichever end-of-collection mechanism was declared, and returns the token to use for
// the next request when there is one.
//
// Two mechanisms, and the declared one wins:
//
//   - hasMorePath set: a boolean in the body is AUTHORITATIVE. This exists because token absence is not
//     every API's terminator. Zendesk's pagination reference documents exactly one end signal -- repeat
//     "until the meta[has_more] property is false" -- and says nothing about whether after_cursor is
//     present, null or absent on the last page, so relying on absence there would be relying on
//     undocumented behaviour. Against such an API the absence rule never fires and the walk runs to
//     maxPages on every call.
//   - hasMorePath absent: token ABSENCE is the end, which is this strategy's original rule and still
//     the common one.
//
// The rule shared with pageNumber: a mechanism that was DECLARED but cannot be read yields
// Indeterminate. The author said "this is how you will know when to stop"; if that signal is not there,
// the honest answer is that we do not know -- not that the collection ended.
func (p *continuationTokenPaginator) hasMore(page Page) (PageResult, string, error) {
	cfg := p.config.Response

	token, tokenPresent, terr := ResponseValue{In: cfg.TokenIn, Path: cfg.TokenPath}.Resolve(page)
	if terr != nil {
		// A malformed declaration (unknown location, unparseable path, non-JSON body) tells us nothing
		// about whether more pages exist. It must not read as "the collection ended".
		return Indeterminate, "", fmt.Errorf("continuationToken: %w", terr)
	}

	if cfg.HasMorePath == "" {
		// The absence rule. A token that is simply ABSENT is how this strategy legitimately signals the
		// end: the server stops sending one. That is a genuine Exhausted, not an Indeterminate.
		if tokenPresent && token != "" {
			return MorePages, token, nil
		}
		return Exhausted, "", nil
	}

	// hasMorePath is declared, so it decides -- always read from the BODY, because a boolean "are there
	// more" is a body field in every API this was built for, and a header would need its own truthiness
	// rules.
	raw, present, herr := ResponseValue{In: "body", Path: cfg.HasMorePath}.Resolve(page)
	if herr != nil {
		return Indeterminate, "", fmt.Errorf("continuationToken: reading hasMorePath %q: %w", cfg.HasMorePath, herr)
	}
	if !present {
		return Indeterminate, "", fmt.Errorf(
			"continuationToken: declared hasMorePath %q is absent from the response body, so whether more "+
				"pages exist cannot be determined; this is not a conclusion that the collection ended",
			cfg.HasMorePath)
	}
	more, perr := strconv.ParseBool(raw)
	if perr != nil {
		return Indeterminate, "", fmt.Errorf(
			"continuationToken: hasMorePath %q holds %q, which is not a boolean; cannot tell whether more "+
				"pages exist", cfg.HasMorePath, raw)
	}

	if !more {
		// AUTHORITATIVE: the collection has ended even if a cursor is still present. Zendesk returns one
		// on the last page, and treating that as "more pages" is what ran the walk to maxPages.
		return Exhausted, "", nil
	}

	// It says more pages exist. We need a token to ask for one.
	//
	// Indeterminate rather than continuing without a token: sending the next request with no token
	// re-fetches the FIRST page, which would loop until maxPages and then report a bound exhaustion,
	// blaming the operator's cap for a server contradiction. Saying so here names the real problem.
	if !tokenPresent || token == "" {
		return Indeterminate, "", fmt.Errorf(
			"continuationToken: hasMorePath %q says more pages exist but no token was found at %q, so the "+
				"next page cannot be requested; the two signals disagree", cfg.HasMorePath, cfg.TokenPath)
	}
	return MorePages, token, nil
}
