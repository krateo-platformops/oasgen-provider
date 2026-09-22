package pagination

import (
	"net/http"
	"net/http/httptest"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/stretchr/testify/assert"
)

func TestContinuationTokenPaginator(t *testing.T) {
	t.Run("Init", func(t *testing.T) {
		// Pre-set values to check they get reset
		p := &continuationTokenPaginator{
			nextToken:   "some-token",
			isFirstCall: false,
		}
		p.Init()
		assert.Equal(t, "", p.nextToken)
		assert.True(t, p.isFirstCall)
	})

	t.Run("UpdateRequestQueryFirstCall", func(t *testing.T) {
		p := NewContinuationTokenPaginator(&getter.ContinuationTokenConfig{
			Request: getter.ContinuationTokenRequest{
				TokenIn:   "query",
				TokenPath: "continue",
			},
		})
		p.Init()
		req := httptest.NewRequest("GET", "http://example.com", nil)
		err := p.UpdateRequest(req)
		assert.NoError(t, err)
		assert.Equal(t, "", req.URL.Query().Get("continue"))
	})

	t.Run("UpdateRequestQuerySecondCall", func(t *testing.T) {
		p := NewContinuationTokenPaginator(&getter.ContinuationTokenConfig{
			Request: getter.ContinuationTokenRequest{
				TokenIn:   "query",
				TokenPath: "continue",
			},
		})
		p.Init()
		// First call
		req := httptest.NewRequest("GET", "http://example.com", nil)
		err := p.UpdateRequest(req)
		assert.NoError(t, err)
		assert.Equal(t, "", req.URL.Query().Get("continue"))

		// Second call
		cp := p.(*continuationTokenPaginator)
		cp.nextToken = "next-page-token"
		req = httptest.NewRequest("GET", "http://example.com", nil)
		err = p.UpdateRequest(req)
		assert.NoError(t, err)
		assert.Equal(t, "next-page-token", req.URL.Query().Get("continue"))
	})

	t.Run("UpdateRequestHeaderFirstCall", func(t *testing.T) {
		p := NewContinuationTokenPaginator(&getter.ContinuationTokenConfig{
			Request: getter.ContinuationTokenRequest{
				TokenIn:   "header",
				TokenPath: "X-Continue",
			},
		})
		p.Init()
		req := httptest.NewRequest("GET", "http://example.com", nil)
		err := p.UpdateRequest(req)
		assert.NoError(t, err)
		assert.Equal(t, "", req.Header.Get("X-Continue"))
	})

	t.Run("UpdateRequestHeaderSecondCall", func(t *testing.T) {
		p := NewContinuationTokenPaginator(&getter.ContinuationTokenConfig{
			Request: getter.ContinuationTokenRequest{
				TokenIn:   "header",
				TokenPath: "X-Continue",
			},
		})
		p.Init()
		// First call
		req := httptest.NewRequest("GET", "http://example.com", nil)
		err := p.UpdateRequest(req)
		assert.NoError(t, err)

		// Second call
		cp := p.(*continuationTokenPaginator)
		cp.nextToken = "next-page-token"
		req = httptest.NewRequest("GET", "http://example.com", nil)
		err = p.UpdateRequest(req)
		assert.NoError(t, err)
		assert.Equal(t, "next-page-token", req.Header.Get("X-Continue"))
	})

	t.Run("ShouldContinueHeader", func(t *testing.T) {
		p := NewContinuationTokenPaginator(&getter.ContinuationTokenConfig{
			Response: getter.ContinuationTokenResponse{
				TokenIn:   "header",
				TokenPath: "X-Next-Token",
			},
		})
		p.Init()
		resp := &http.Response{
			Header: http.Header{},
		}
		resp.Header.Set("X-Next-Token", "new-token")
		verdict, err := p.Next(Page{Response: resp})
		assert.NoError(t, err)
		assert.Equal(t, MorePages, verdict)
		cp := p.(*continuationTokenPaginator)
		assert.Equal(t, "new-token", cp.nextToken)
	})

	t.Run("ShouldContinueHeaderNoToken", func(t *testing.T) {
		p := NewContinuationTokenPaginator(&getter.ContinuationTokenConfig{
			Response: getter.ContinuationTokenResponse{
				TokenIn:   "header",
				TokenPath: "X-Next-Token",
			},
		})
		p.Init()
		resp := &http.Response{
			Header: http.Header{},
		}
		verdict, err := p.Next(Page{Response: resp})
		assert.NoError(t, err)
		// An ABSENT token is how this strategy legitimately ends: the server stops sending one. That is
		// genuine exhaustion, and the only verdict permitted to become a 404.
		assert.Equal(t, Exhausted, verdict)
		cp := p.(*continuationTokenPaginator)
		assert.Equal(t, "", cp.nextToken)
	})

	t.Run("ShouldContinueUnsupported", func(t *testing.T) {
		p := NewContinuationTokenPaginator(&getter.ContinuationTokenConfig{
			Response: getter.ContinuationTokenResponse{
				TokenIn: "unsupported",
			},
		})
		p.Init()
		resp := &http.Response{}
		verdict, err := p.Next(Page{Response: resp})
		assert.Error(t, err)
		// BEHAVIOUR CHANGE, deliberate: an unsupported location used to return false, which the caller
		// turned into a 404 -- so a misconfigured RestDefinition reported the resource absent and the
		// reconciler created a duplicate. A declaration it cannot read tells us nothing about whether
		// more pages exist, so it is Indeterminate.
		assert.Equal(t, Indeterminate, verdict)
	})
}
