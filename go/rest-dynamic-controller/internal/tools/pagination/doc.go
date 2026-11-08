// Package pagination walks a paged collection to completion, or says that it could not.
//
// # The principle this package exists to enforce
//
// Never convert "I could not tell" into "no".
//
// Every signal here has three verdicts, not two -- MorePages, Exhausted and Indeterminate -- because the
// expensive failure is not getting the page count wrong. It is concluding that a collection ENDED when the
// truth is that the end could not be determined. A short walk silently returns fewer items than exist, and
// a caller cannot tell a complete small collection from a truncated large one.
//
// So a declared signal that is absent is an error, never an ending:
//
//	a declared source that is not there means the declaration is wrong or the server changed. Deciding
//	"ended" from a signal we never saw is precisely the collapse this package exists to prevent.
//
// The one exception is a continuation token, whose ABSENCE is the end signal by design. That is a property
// of the mechanism, not a convenience, and it is why tokens are modelled separately from page numbers.
//
// # Why this is the top of the package doc
//
// The same refusal is load-bearing elsewhere in this controller: Observe will not conclude that a resource
// is ABSENT from a findby listing that could not answer the question, and reports the inference rather than
// acting on it silently (#485). Two independent places arrived at one rule -- do not let an unanswerable
// question collapse into the negative answer -- which makes it a property of the problem rather than a
// local style choice. It was stated at each site as though it were a local decision; it is not.
//
// # What this package deliberately does NOT do
//
// It never follows a URL taken from a response. The HasMore header signal tests a declared substring (e.g.
// `rel="next"`) for PRESENCE and discards the value; offsets and page numbers are derived from the
// configured request, never parsed out of a link. Three independent reasons, measured against two vendors:
//
//   - A link can point somewhere else entirely. One vendor's `next`/`first`/`last` values carry a
//     cluster-internal hostname over plaintext http; following one would attach the bearer credential to a
//     plaintext request to a name outside the vendor's own API surface.
//   - A vendor's own terminator need not be a page boundary. The same vendor's `last` is `total - limit` --
//     a trailing WINDOW. Walking first -> next -> ... -> last re-serves an item with no concurrency
//     involved at all, and a client that derived a page COUNT from it would never serve the final item.
//   - A missed item is the silent failure. On a findby it is indistinguishable from the resource not
//     existing, which is how a correct client creates something that is already there.
//
// Deriving the request is more work than following a link and it is the reason none of the above can
// happen here.
package pagination
