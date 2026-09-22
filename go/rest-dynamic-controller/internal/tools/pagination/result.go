package pagination

import "net/http"

// PageResult is a paginator's verdict on a page it has just seen.
//
// This exists as a THREE-valued type rather than the bool it replaced, and that is the whole point of
// it. `ShouldContinue(...) (bool, error)` used `false` for two different situations -- "the collection
// genuinely ended" and "I have nothing left to go on" -- and the findby loop turned either into a 404.
// The reconciler CREATES on 404, so a search that stopped early was indistinguishable from one that
// finished, and the difference was a duplicate external resource (#119).
//
// That conflation is the same one behind the delete-path bugs (#77/#98/#101), the wedge in #122 and the
// CRD guard in #125: "I could not determine" spelled identically to "there is nothing". Making it a
// type means a strategy cannot express the ambiguity by accident -- it has to choose Indeterminate,
// which can never become a 404.
type PageResult int

const (
	// MorePages: another page exists and the paginator has advanced its state to fetch it.
	MorePages PageResult = iota

	// Exhausted: the collection genuinely ended. This is the ONLY result that may be reported to the
	// caller as absence, and therefore the only one that can lead to a create.
	Exhausted

	// Indeterminate: the paginator could not tell. A declared source was missing from the response, a
	// value did not parse, or the strategy has no basis to decide. NEVER absence: the caller surfaces
	// this as an error so the search is retried rather than concluded.
	Indeterminate
)

func (r PageResult) String() string {
	switch r {
	case MorePages:
		return "MorePages"
	case Exhausted:
		return "Exhausted"
	case Indeterminate:
		return "Indeterminate"
	default:
		return "Unknown"
	}
}

// Page is everything a paginator needs to judge the page that was just fetched.
//
// Items is carried here, already extracted, because the caller has parsed the body anyway and its
// extractor (ExtractItemsFromResponse) handles three different response shapes. A strategy that needed
// the item count -- the "a short page is the last page" rule -- would otherwise have to re-implement
// that from raw bytes, giving two extractors that drift apart. One extraction, one definition of how
// many items a page held.
type Page struct {
	// Response is the HTTP response, for header-based signals.
	Response *http.Response

	// Body is the raw response body, for path-based extraction.
	Body []byte

	// Items are the collection's items on this page, already extracted by the caller.
	Items []any

	// Index is how many pages have been fetched so far, 0-based for the first page.
	Index int
}
