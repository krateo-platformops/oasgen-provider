package snowplow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// An authn signing-key rotation is the ONE event that invalidates a JWT before its exp: a new kid in the
// JWKS makes every outstanding token fail at the verifier while the cache that issued it still considers
// it fresh, because that cache keys on TIME ALONE. Without invalidation the controller presents a dead
// token until refreshSkew -- an outage lasting up to the token's remaining lifetime, cleared by a pod
// restart, which makes it look like a transient (#369).
func TestA401InvalidatesTheTokenAndRetriesOnce(t *testing.T) {
	var calls, invalidations int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			// The rotation: the first token is stale.
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"token signature invalid"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":{"api":{"ok":true}}}`))
	}))
	defer srv.Close()

	tokens := []string{"stale-jwt", "fresh-jwt"}
	var idx int32
	c := New(srv.URL, func(context.Context) (string, error) {
		i := atomic.LoadInt32(&idx)
		return tokens[i], nil
	}).WithTokenInvalidator(func() {
		atomic.AddInt32(&invalidations, 1)
		atomic.StoreInt32(&idx, 1) // what Invalidate causes: the next Token re-exchanges
	})
	c.WithHTTPClient(srv.Client())

	status, _, err := c.Resolve(context.Background(), ApiRef{Name: "a", Namespace: "n"}, nil)
	if err != nil {
		t.Fatalf("a 401 followed by a successful retry must succeed, got %v", err)
	}
	if status == nil {
		t.Error("no status returned from the retry")
	}
	if got := atomic.LoadInt32(&invalidations); got != 1 {
		t.Errorf("invalidations = %d, want 1: without dropping the cache the retry would present the "+
			"same dead token and a key rotation would not self-heal", got)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("requests = %d, want 2 (the 401 and one retry)", got)
	}
}

// EXACTLY ONCE. snowplow can answer 401 for reasons a fresh token cannot fix -- a misconfigured audience,
// a mapping that no longer exists -- and retrying those in a loop turns a clear failure into a hot one.
// One attempt distinguishes "my token went stale" from "my identity is wrong", then reports the latter.
func TestAPersistent401IsRetriedOnlyOnceThenReported(t *testing.T) {
	var calls, invalidations int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"audience mismatch"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, func(context.Context) (string, error) { return "jwt", nil }).
		WithTokenInvalidator(func() { atomic.AddInt32(&invalidations, 1) })
	c.WithHTTPClient(srv.Client())

	_, _, err := c.Resolve(context.Background(), ApiRef{Name: "a", Namespace: "n"}, nil)
	if err == nil {
		t.Fatal("a persistent 401 must be reported, not retried away")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("requests = %d, want exactly 2. More means a loop: an identity problem a new token "+
			"cannot fix would be retried against snowplow on every reconcile", got)
	}
	if got := atomic.LoadInt32(&invalidations); got != 1 {
		t.Errorf("invalidations = %d, want 1: the cache must not be dropped repeatedly for a failure "+
			"that is not about the token", got)
	}
}

// With no invalidator — no authn URL configured, so no cache exists — a 401 must pass straight through
// rather than panic on a nil func.
func TestA401WithNoInvalidatorIsReportedWithoutRetrying(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(srv.URL, nil) // no token, no invalidator
	c.WithHTTPClient(srv.Client())

	if _, _, err := c.Resolve(context.Background(), ApiRef{Name: "a", Namespace: "n"}, nil); err == nil {
		t.Fatal("want an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("requests = %d, want 1: with nothing to invalidate there is nothing a retry could "+
			"change, so retrying would only double the load", got)
	}
}
