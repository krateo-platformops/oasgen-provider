//go:build unit

package pagination

import (
	"net/http"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPageResult_ExhaustedIsTheOnlyAbsence is the contract this refactor exists to establish.
//
// Only Exhausted may be reported to the caller as "the resource is not there", because the reconciler
// CREATES on not-found. Indeterminate must never reach that path: a search that stopped early is not a
// search that finished, and treating them alike duplicates an external resource that was already there
// but on a page nobody read (#119).
//
// The table is deliberately exhaustive over the enum. If a fourth state is ever added, this fails until
// someone decides which side of the line it falls on — which is the point.
func TestPageResult_ExhaustedIsTheOnlyAbsence(t *testing.T) {
	cases := []struct {
		r        PageResult
		mayBe404 bool
		why      string
	}{
		{MorePages, false, "there is more to read; concluding anything would be premature"},
		{Exhausted, true, "the collection genuinely ended — the only authoritative absence"},
		{Indeterminate, false, "could not tell; 404 here creates a duplicate of an unseen resource"},
	}
	for _, tc := range cases {
		t.Run(tc.r.String(), func(t *testing.T) {
			assert.Equal(t, tc.mayBe404, tc.r == Exhausted, tc.why)
		})
	}
}

// TestContinuationToken_BodyMode covers the case that was a commented-out stub. It mattered because the
// stub did not error — it fell through to "no token", i.e. Exhausted — so a token declared in the body
// silently ended the walk after page one and reported the resource absent.
func TestContinuationToken_BodyMode(t *testing.T) {
	newP := func() Paginator {
		p := NewContinuationTokenPaginator(&getter.ContinuationTokenConfig{
			Response: getter.ContinuationTokenResponse{TokenIn: "body", TokenPath: "meta.nextToken"},
		})
		p.Init()
		return p
	}

	t.Run("token present in the body means more pages", func(t *testing.T) {
		v, err := newP().Next(Page{
			Response: &http.Response{Header: http.Header{}},
			Body:     []byte(`{"meta":{"nextToken":"abc"},"items":[]}`),
		})
		require.NoError(t, err)
		assert.Equal(t, MorePages, v, "a body token must be read, not silently ignored")
	})

	t.Run("token absent from the body is genuine exhaustion", func(t *testing.T) {
		v, err := newP().Next(Page{
			Response: &http.Response{Header: http.Header{}},
			Body:     []byte(`{"meta":{},"items":[]}`),
		})
		require.NoError(t, err)
		assert.Equal(t, Exhausted, v, "the server no longer offering a token IS the end of the collection")
	})

	t.Run("unreadable body is Indeterminate, not exhaustion", func(t *testing.T) {
		v, err := newP().Next(Page{
			Response: &http.Response{Header: http.Header{}},
			Body:     []byte(`not json at all`),
		})
		assert.Error(t, err)
		assert.Equal(t, Indeterminate, v,
			"a body we cannot parse says nothing about whether more pages exist; Exhausted here would "+
				"report the resource absent and trigger a create")
	})
}

// TestResponseValue_AbsenceIsNotFailure pins the split that lets strategies make their own call.
//
// "The header is not there" is ordinary — for a next-token it is how a collection ends. "This path is
// unparseable" is a misconfiguration. Collapsing them here would force the decision into the primitive,
// where it cannot know what absence means for the caller.
func TestResponseValue_AbsenceIsNotFailure(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("X-Token", "t1")

	t.Run("header present", func(t *testing.T) {
		v, present, err := ResponseValue{In: "header", Path: "X-Token"}.Resolve(Page{Response: resp})
		require.NoError(t, err)
		assert.True(t, present)
		assert.Equal(t, "t1", v)
	})

	t.Run("header absent: not present, NOT an error", func(t *testing.T) {
		_, present, err := ResponseValue{In: "header", Path: "X-Missing"}.Resolve(Page{Response: resp})
		require.NoError(t, err, "an absent header is ordinary, not a failure")
		assert.False(t, present)
	})

	t.Run("numeric body value renders as an integer, not scientific notation", func(t *testing.T) {
		v, present, err := ResponseValue{In: "body", Path: "total_pages"}.Resolve(
			Page{Body: []byte(`{"total_pages":12}`)})
		require.NoError(t, err)
		require.True(t, present)
		assert.Equal(t, "12", v, "a page total formatted as 1.2e+01 would compare wrong everywhere")
	})

	t.Run("unsupported location is an error", func(t *testing.T) {
		_, _, err := ResponseValue{In: "cookie", Path: "x"}.Resolve(Page{})
		assert.Error(t, err)
	})
}
