package pagination

import (
	"encoding/json"
	"fmt"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/async"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/pathparsing"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ResponseValue locates a single scalar in a response -- a header, or a path into the body.
//
// It is ONE primitive shared by every pagination strategy, not a per-strategy helper. continuationToken
// needs it for its next-token, and a page-number strategy needs it for totalPages/totalItems; written
// twice they would diverge, and the body half was already stubbed out ("// Not implemented yet") in the
// one place it existed. A shared primitive completes that case as a side effect rather than leaving it
// a comment.
//
// Body paths use pathparsing.ParsePath, the same dialect as async.poll.statusPath and fieldMapping's
// inCustomResource, so an author writes one kind of path everywhere rather than learning a second.
type ResponseValue struct {
	// In is where the value lives: "header" or "body".
	In string
	// Path is the header name, or a dotted path into the body.
	Path string
}

// Resolve returns the value, whether it was PRESENT, and an error for a malformed request.
//
// The present flag is separate from the error on purpose. "The header is not there" is not a failure --
// for a next-token it is the ordinary way a collection ends -- whereas "this path is not parseable" is.
// Callers decide what absence means for their strategy; conflating the two here would force that
// decision into the wrong place, which is how a missing signal becomes a silent Exhausted.
func (v ResponseValue) Resolve(p Page) (value string, present bool, err error) {
	switch v.In {
	case "header":
		if p.Response == nil {
			return "", false, fmt.Errorf("cannot read header %q: no response", v.Path)
		}
		got := p.Response.Header.Get(v.Path)
		return got, got != "", nil

	case "body":
		segs, perr := pathparsing.ParsePath(v.Path)
		if perr != nil || len(segs) == 0 {
			return "", false, fmt.Errorf("invalid body path %q: %v", v.Path, perr)
		}
		var body map[string]any
		if len(p.Body) == 0 {
			return "", false, nil
		}
		if jerr := json.Unmarshal(p.Body, &body); jerr != nil {
			// A body that is not a JSON object cannot be path-addressed. That is a malformed request
			// for this value, not an absence.
			return "", false, fmt.Errorf("body is not a JSON object, cannot read %q: %w", v.Path, jerr)
		}
		raw, found, nerr := unstructured.NestedFieldNoCopy(body, segs...)
		if nerr != nil {
			return "", false, fmt.Errorf("reading body path %q: %w", v.Path, nerr)
		}
		if !found || raw == nil {
			return "", false, nil
		}
		// RenderScalar so a numeric total reads as "12" rather than fmt's "1.2e+01" -- the same
		// treatment async.poll.statusPath gets, for the same reason.
		return async.RenderScalar(raw), true, nil

	default:
		return "", false, fmt.Errorf("unsupported location %q: expected \"header\" or \"body\"", v.In)
	}
}
