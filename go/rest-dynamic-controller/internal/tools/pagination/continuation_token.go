package pagination

import (
	"fmt"
	"net/http"

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
	cfg := p.config.Response

	v := ResponseValue{In: cfg.TokenIn, Path: cfg.TokenPath}
	token, present, err := v.Resolve(page)
	if err != nil {
		// A malformed declaration (unknown location, unparseable path, non-JSON body) tells us nothing
		// about whether more pages exist. It must not read as "the collection ended".
		p.nextToken = ""
		return Indeterminate, fmt.Errorf("continuationToken: %w", err)
	}

	if present && token != "" {
		p.nextToken = token
		return MorePages, nil
	}

	// A token that is simply ABSENT is how this strategy legitimately signals the end: the server stops
	// sending one. That is a genuine Exhausted, not an Indeterminate -- unlike a malformed declaration
	// above, which cannot distinguish the two.
	p.nextToken = ""
	return Exhausted, nil
}
