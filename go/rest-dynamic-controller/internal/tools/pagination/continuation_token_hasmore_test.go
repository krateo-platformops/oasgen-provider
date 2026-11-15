package pagination

import (
	"strings"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

func ctCfg(hasMorePath string, maxPages int) *getter.ContinuationTokenConfig {
	return &getter.ContinuationTokenConfig{
		Request:  getter.ContinuationTokenRequest{TokenIn: "query", TokenPath: "page[after]"},
		Response: getter.ContinuationTokenResponse{TokenIn: "body", TokenPath: "meta.after_cursor", HasMorePath: hasMorePath},
		MaxPages: maxPages,
	}
}

// TestContinuationTokenEndSignals is the table the fourth zendesk gap exists for.
//
// The rows that matter most are the two where the token and hasMore DISAGREE: Zendesk returns a cursor
// on the last page and signals the end only through meta.has_more, so a cursor-present/has_more-false
// page must be Exhausted. Under the absence rule alone that same page reads as MorePages, which is what
// ran the walk to maxPages on every call.
func TestContinuationTokenEndSignals(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hasMore   string
		body      string
		want      PageResult
		errSubstr string
	}{
		{
			name:    "absence rule: token present means more pages",
			hasMore: "",
			body:    `{"meta":{"after_cursor":"abc"}}`,
			want:    MorePages,
		},
		{
			name:    "absence rule: token absent means the collection ended",
			hasMore: "",
			body:    `{"meta":{}}`,
			want:    Exhausted,
		},
		{
			// THE ZENDESK CASE. Without hasMorePath this row is MorePages and the walk never ends.
			name:    "hasMore false ENDS the walk even with a cursor still present",
			hasMore: "meta.has_more",
			body:    `{"meta":{"has_more":false,"after_cursor":"still-here"}}`,
			want:    Exhausted,
		},
		{
			name:    "hasMore true with a token continues",
			hasMore: "meta.has_more",
			body:    `{"meta":{"has_more":true,"after_cursor":"abc"}}`,
			want:    MorePages,
		},
		{
			// The mirror of the Zendesk case: the signals disagree the other way.
			name:      "hasMore true with NO token is Indeterminate, not a continuation",
			hasMore:   "meta.has_more",
			body:      `{"meta":{"has_more":true}}`,
			want:      Indeterminate,
			errSubstr: "the two signals disagree",
		},
		{
			name:      "a declared hasMorePath absent from the body is Indeterminate, never Exhausted",
			hasMore:   "meta.has_more",
			body:      `{"meta":{"after_cursor":"abc"}}`,
			want:      Indeterminate,
			errSubstr: "is absent from the response body",
		},
		{
			name:      "a non-boolean hasMorePath is Indeterminate",
			hasMore:   "meta.has_more",
			body:      `{"meta":{"has_more":"yes","after_cursor":"abc"}}`,
			want:      Indeterminate,
			errSubstr: "is not a boolean",
		},
		{
			// has_more false and no cursor: both signals agree the collection ended.
			name:    "hasMore false with no cursor is Exhausted",
			hasMore: "meta.has_more",
			body:    `{"meta":{"has_more":false}}`,
			want:    Exhausted,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewContinuationTokenPaginator(ctCfg(tc.hasMore, 50))
			p.Init()
			got, err := p.Next(Page{Body: []byte(tc.body), Index: 0})
			if got != tc.want {
				t.Fatalf("verdict = %v, want %v (err=%v)", got, tc.want, err)
			}
			if tc.errSubstr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.errSubstr) {
				t.Fatalf("error = %v, want one containing %q", err, tc.errSubstr)
			}
		})
	}
}

// TestContinuationTokenMaxPages pins the ORDERING that pageNumber's comment calls out: the bound is
// checked only after a mechanism has said more pages exist, so a collection ending exactly on the last
// permitted page is Exhausted rather than truncated.
func TestContinuationTokenMaxPages(t *testing.T) {
	t.Run("the bound is Indeterminate, never Exhausted", func(t *testing.T) {
		p := NewContinuationTokenPaginator(ctCfg("meta.has_more", 2))
		p.Init()
		// page index 1 means two pages fetched, which reaches maxPages=2
		got, err := p.Next(Page{Body: []byte(`{"meta":{"has_more":true,"after_cursor":"abc"}}`), Index: 1})
		if got != Indeterminate {
			t.Fatalf("verdict = %v, want Indeterminate", got)
		}
		if err == nil || !strings.Contains(err.Error(), "maxPages=2") {
			t.Fatalf("the error must name the bound and its value, got %v", err)
		}
		if !strings.Contains(err.Error(), "NOT a conclusion that the resource is absent") {
			t.Fatalf("the error must say the bound is not absence, got %v", err)
		}
	})

	t.Run("a collection ending ON the last permitted page is Exhausted, not truncated", func(t *testing.T) {
		p := NewContinuationTokenPaginator(ctCfg("meta.has_more", 2))
		p.Init()
		// Same index that would trip the bound above, but the mechanism says the collection ended.
		// If the bound were checked FIRST this would be Indeterminate -- a false alarm on a complete walk.
		got, err := p.Next(Page{Body: []byte(`{"meta":{"has_more":false,"after_cursor":"x"}}`), Index: 1})
		if got != Exhausted || err != nil {
			t.Fatalf("verdict = %v, err = %v; want Exhausted with no error", got, err)
		}
	})

	t.Run("under the bound, the walk continues", func(t *testing.T) {
		p := NewContinuationTokenPaginator(ctCfg("meta.has_more", 50))
		p.Init()
		got, err := p.Next(Page{Body: []byte(`{"meta":{"has_more":true,"after_cursor":"abc"}}`), Index: 0})
		if got != MorePages || err != nil {
			t.Fatalf("verdict = %v, err = %v; want MorePages", got, err)
		}
	})
}

// TestIndeterminateIsNotAbsence guards the property the whole three-valued result exists for: no path
// in this strategy may turn "I stopped looking" into "the collection ended".
func TestIndeterminateIsNotAbsence(t *testing.T) {
	bodies := []string{
		`{"meta":{"has_more":true}}`,                       // signals disagree
		`{"meta":{"after_cursor":"abc"}}`,                  // declared signal missing
		`{"meta":{"has_more":"yes","after_cursor":"abc"}}`, // not a boolean
	}
	for _, b := range bodies {
		p := NewContinuationTokenPaginator(ctCfg("meta.has_more", 50))
		p.Init()
		got, err := p.Next(Page{Body: []byte(b), Index: 0})
		if got == Exhausted {
			t.Fatalf("body %s yielded Exhausted; an unreadable or contradictory signal must never read as absence", b)
		}
		if got != Indeterminate || err == nil {
			t.Fatalf("body %s: verdict = %v, err = %v; want Indeterminate with an error", b, got, err)
		}
	}
}
