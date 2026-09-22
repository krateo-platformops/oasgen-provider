package pagination

import (
	"fmt"
	"net/http"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

// MaxFindByPages is the hard ceiling on a paginated findby walk -- a runaway backstop, deliberately far
// above any real search. It is NOT the per-resource bound an author configures: that is pageNumber's
// `maxPages`, which must sit at or below this.
//
// It lives here rather than in the client package because both ends need it and it must be ONE number:
// the factory validates a declaration against it, and the findby loop enforces it for strategies that
// carry no bound of their own. Two copies would let a declaration pass validation and then be cut short
// by a different limit.
const MaxFindByPages = 1000

// Paginator defines the interface for a pagination strategy.
type Paginator interface {
	// Init initializes the paginator for a new sequence of calls.
	Init()

	// UpdateRequest modifies an http.Request with the correct parameters for the current page/token.
	UpdateRequest(req *http.Request) error

	// Next judges the page just fetched and, when another exists, advances internal state to fetch it.
	//
	// It returns a THREE-valued PageResult rather than a bool. The bool it replaced meant both "the
	// collection ended" and "I cannot tell", and the caller turned either into a 404 -- which the
	// reconciler acts on by creating. See PageResult for why that had to become unrepresentable.
	Next(p Page) (PageResult, error)
}

// NewPaginator is a factory that returns the correct paginator based on config.
func NewPaginator(config *getter.Pagination) (Paginator, error) {
	if config == nil {
		return nil, nil // No pagination configured
	}

	switch config.Type {
	case "continuationToken":
		// Ensure that the ContinuationToken config is not nil to avoid panics.
		if config.ContinuationToken == nil {
			return nil, fmt.Errorf("pagination type is 'continuationToken' but the continuationToken config block is missing")
		}
		// Two strategies declared at once has no defensible reading: the type field picks one, so the
		// other block is either a leftover or a misunderstanding, and silently honouring the type would
		// hide whichever one the author actually meant. CRDs reject this at admission; this is the guard
		// for RestDefinitions that predate those rules.
		if config.PageNumber != nil {
			return nil, fmt.Errorf("pagination type is 'continuationToken' but a 'pageNumber' config block is also set")
		}
		return NewContinuationTokenPaginator(config.ContinuationToken), nil
	case "pageNumber":
		if config.PageNumber == nil {
			return nil, fmt.Errorf("pagination type is 'pageNumber' but the pageNumber config block is missing")
		}
		if config.ContinuationToken != nil {
			return nil, fmt.Errorf("pagination type is 'pageNumber' but a 'continuationToken' config block is also set")
		}
		// maxPages has no default on purpose -- the author states how far the search may go. Zero means
		// the field was never set (an old RestDefinition, or hand-written JSON), and defaulting it here
		// would silently pick a bound nobody chose. Refuse instead: a walk that cannot start is visible,
		// whereas one that stops at an invented limit looks like absence.
		if config.PageNumber.MaxPages <= 0 {
			return nil, fmt.Errorf("pagination type is 'pageNumber' but maxPages is not set (got %d): it is required and has no default", config.PageNumber.MaxPages)
		}
		if config.PageNumber.MaxPages > MaxFindByPages {
			return nil, fmt.Errorf("pageNumber maxPages=%d exceeds the hard findby ceiling of %d", config.PageNumber.MaxPages, MaxFindByPages)
		}
		return NewPageNumberPaginator(config.PageNumber), nil
	// other pagination types can be added here
	default:
		return nil, fmt.Errorf("unsupported pagination type: %s", config.Type)
	}
}
