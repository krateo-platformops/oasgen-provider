//go:build unit || integration

package restclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const nilOptsMessage = "restclient: request configuration (opts) is required and must not be nil"

// TestNilOptsIsRejectedAtEveryEntryPoint pins the contract: opts is REQUIRED. Every successful request
// dereferences it, so the old `if opts != nil` guards were dead and advertised a nil that panicked on
// the first success. Each entry point must return the explicit error, not panic.
func TestNilOptsIsRejectedAtEveryEntryPoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "ok"})
	}))
	defer srv.Close()
	client := createTestClient(t)
	client.Server = srv.URL
	allowServer(t, client)
	ctx := context.Background()

	t.Run("Call", func(t *testing.T) {
		var err error
		require.NotPanics(t, func() { _, err = client.Call(ctx, srv.Client(), "/api/test", nil) })
		require.Error(t, err)
		assert.EqualError(t, err, nilOptsMessage)
	})
	t.Run("CallForPagination", func(t *testing.T) {
		var err error
		require.NotPanics(t, func() { _, _, err = client.CallForPagination(ctx, srv.Client(), "/api/test", nil, onePagePaginator{}) })
		require.Error(t, err)
		assert.EqualError(t, err, nilOptsMessage)
	})
	t.Run("FindBy", func(t *testing.T) {
		var err error
		require.NotPanics(t, func() { _, err = client.FindBy(ctx, srv.Client(), "/api/test", nil, nil) })
		require.Error(t, err)
		assert.EqualError(t, err, nilOptsMessage)
	})
	t.Run("CallFindBySingle", func(t *testing.T) {
		var err error
		require.NotPanics(t, func() { _, err = client.CallFindBySingle(ctx, srv.Client(), "/api/test", nil, "", nil) })
		require.Error(t, err)
		assert.EqualError(t, err, nilOptsMessage)
	})
}

// TestNonNilOptsBranchesBehaveIdentically is the regression guard for the guards that were removed: for a
// non-nil opts every formerly-guarded branch must behave exactly as before.
func TestNonNilOptsBranchesBehaveIdentically(t *testing.T) {
	respond := func(code int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "body"})
		}
	}
	setup := func(t *testing.T, code int) (*UnstructuredClient, *httptest.Server) {
		srv := httptest.NewServer(respond(code))
		t.Cleanup(srv.Close)
		c := createTestClient(t)
		c.Server = srv.URL
		allowServer(t, c)
		return c, srv
	}
	ctx := context.Background()

	t.Run("BuildErr is returned verbatim from Call", func(t *testing.T) {
		c, srv := setup(t, 200)
		want := errors.New("assembly failed")
		_, err := c.Call(ctx, srv.Client(), "/api/test", &RequestConfiguration{Method: "GET", BuildErr: want})
		assert.Same(t, want, err)
	})
	t.Run("BuildErr is returned verbatim from CallForPagination", func(t *testing.T) {
		c, srv := setup(t, 200)
		want := errors.New("assembly failed")
		_, _, err := c.CallForPagination(ctx, srv.Client(), "/api/test", &RequestConfiguration{Method: "GET", BuildErr: want}, onePagePaginator{})
		assert.Same(t, want, err)
	})
	t.Run("NotFoundCodes remaps to a 404 StatusError", func(t *testing.T) {
		c, srv := setup(t, 410)
		_, err := c.Call(ctx, srv.Client(), "/api/test", &RequestConfiguration{Method: "GET", NotFoundCodes: []int{410}})
		var se *StatusError
		require.ErrorAs(t, err, &se)
		assert.Equal(t, http.StatusNotFound, se.StatusCode)
	})
	t.Run("TolerateCodes short-circuits to an empty success", func(t *testing.T) {
		c, srv := setup(t, 404)
		resp, err := c.Call(ctx, srv.Client(), "/api/test", &RequestConfiguration{Method: "GET", TolerateCodes: []int{404}})
		require.NoError(t, err)
		assert.Nil(t, resp.ResponseBody)
	})
	t.Run("an undeclared status is an error without SuccessCodes", func(t *testing.T) {
		c, srv := setup(t, 418)
		_, err := c.Call(ctx, srv.Client(), "/api/test", &RequestConfiguration{Method: "GET"})
		var se *StatusError
		require.ErrorAs(t, err, &se)
		assert.Equal(t, 418, se.StatusCode)
	})
	t.Run("SuccessCodes admits the undeclared status on Call", func(t *testing.T) {
		c, srv := setup(t, 418)
		resp, err := c.Call(ctx, srv.Client(), "/api/test", &RequestConfiguration{Method: "GET", SuccessCodes: []int{418}})
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"message": "body"}, resp.ResponseBody)
	})
	t.Run("SuccessCodes admits the undeclared status on CallForPagination", func(t *testing.T) {
		c, srv := setup(t, 418)
		_, _, err := c.CallForPagination(ctx, srv.Client(), "/api/test", &RequestConfiguration{Method: "GET", SuccessCodes: []int{418}}, onePagePaginator{})
		require.NoError(t, err)
	})
	t.Run("an undeclared status is an error on CallForPagination without SuccessCodes", func(t *testing.T) {
		c, srv := setup(t, 418)
		_, _, err := c.CallForPagination(ctx, srv.Client(), "/api/test", &RequestConfiguration{Method: "GET"}, onePagePaginator{})
		require.Error(t, err)
	})
	t.Run("FindBy still marks CollectionRead on a non-nil opts", func(t *testing.T) {
		c, srv := setup(t, 200)
		opts := &RequestConfiguration{Method: "GET"}
		_, _ = c.FindBy(ctx, srv.Client(), "/api/test", opts, nil)
		assert.True(t, opts.CollectionRead)
	})
}
