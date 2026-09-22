package pagination

import (
	"fmt"
	"net/http"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

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
		// Ensure other pagination types block are not set (additional validation).
		//if config.PageNumber != nil {
		//	return nil, fmt.Errorf("pagination type is 'continuationToken' but 'pageNumber' config block is also set")
		//}
		return NewContinuationTokenPaginator(config.ContinuationToken), nil
	// case "pageNumber":
	//     return NewPageNumberPaginator(config.PageNumber), nil
	// other pagination types can be added here
	default:
		return nil, fmt.Errorf("unsupported pagination type: %s", config.Type)
	}
}
