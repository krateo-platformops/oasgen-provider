package snowplow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// THE STATUS CODE CANNOT DRIVE THIS DECISION, and these cases exist because an earlier version thought it
// could. That version retried any 401, justified by "safe to re-issue because /call is a GET". The
// snowplow session falsified it by measurement against their real middleware and a real RESTAction
// pipeline:
//
//   - /call accepts POST, PUT, PATCH and DELETE, the verb is passed through, and a RESTAction can declare
//     write stages that are dispatched and audited per stage whatever the outer method is.
//   - A RESTAction whose stage 1 POSTs (201, audited) and whose stage 2 takes an upstream 401 answers 401
//     for the WHOLE /call -- after the POST was sent and audited.
//   - Those post-dispatch 401s carry reason "Unauthorized" and NO details, so they are byte-shaped like a
//     token rejection.
//
// So a 401 with no cause is indistinguishable from one raised after a write landed, and retrying it
// duplicates an audited side effect. The discriminator is the CAUSE: only snowplow's own pre-dispatch
// credential rejection carries details.causes[0].reason, and an upstream 401 cannot forge one, because
// details planted in an upstream body are dropped before the response is built.
//
// Every case below is about that distinction. The bodies are the shapes snowplow measured, not invented
// ones.

// bodyCredentialMissing is snowplow's pre-dispatch "no credential reached the auth layer" rejection: the
// one case where nothing can have been dispatched, so re-issuing is safe.
const bodyCredentialMissing = `{"kind":"Status","status":"Failure","reason":"Unauthorized","code":401,` +
	`"message":"unauthorized","details":{"causes":[{"reason":"SessionCredentialMissing",` +
	`"message":"no session credential"}]}}`

// bodyStepUp carries a cause, but it means STEP UP, not retry.
const bodyStepUp = `{"kind":"Status","status":"Failure","reason":"Unauthorized","code":401,` +
	`"details":{"causes":[{"reason":"StepUpRequired","message":"step up"}]}}`

// bodyNoCause is both a token rejection AND a post-dispatch upstream 401. That is the whole point: they
// are the same bytes.
const bodyNoCause = `{"kind":"Status","status":"Failure","reason":"Unauthorized","code":401,` +
	`"message":"token signature invalid"}`

// A pre-dispatch credential rejection is the case the invalidation exists for: an authn signing-key
// rotation makes every outstanding token fail at the verifier while the cache that issued it still
// considers it fresh, because that cache keys on TIME ALONE. Without invalidation the controller presents
// a dead token until refreshSkew (#369).
func TestACredentialMissing401InvalidatesTheTokenAndRetriesOnce(t *testing.T) {
	var calls, invalidations int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := atomic.AddInt32(&calls, 1); n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(bodyCredentialMissing))
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
		return tokens[atomic.LoadInt32(&idx)], nil
	}).WithTokenInvalidator(func() {
		atomic.AddInt32(&invalidations, 1)
		atomic.StoreInt32(&idx, 1) // what Invalidate causes: the next Token re-exchanges
	})
	c.WithHTTPClient(srv.Client())

	status, _, err := c.Resolve(context.Background(), ApiRef{Name: "a", Namespace: "n"}, nil)
	if err != nil {
		t.Fatalf("a credential-missing 401 followed by a successful retry must succeed, got %v", err)
	}
	if status == nil {
		t.Error("no status returned from the retry")
	}
	if got := atomic.LoadInt32(&invalidations); got != 1 {
		t.Errorf("invalidations = %d, want 1: without dropping the cache the retry presents the same "+
			"dead token and the rotation does not self-heal", got)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("requests = %d, want 2 (the 401 and one retry)", got)
	}
}

// THE SAFETY CASE. A 401 with no cause may have been raised AFTER a write stage was dispatched and
// audited, and nothing in the response distinguishes that from a token rejection. Retrying performs the
// write twice.
//
// This is the case the previous implementation got wrong, and it is worth more than the positive case
// above: the positive case failing means a rotation does not self-heal, which is an outage. This one
// failing means a duplicated audited side effect, which cannot be undone.
func TestA401WithNoCauseIsNotRetried(t *testing.T) {
	var calls, invalidations int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(bodyNoCause))
	}))
	defer srv.Close()

	c := New(srv.URL, func(context.Context) (string, error) { return "jwt", nil }).
		WithTokenInvalidator(func() { atomic.AddInt32(&invalidations, 1) })
	c.WithHTTPClient(srv.Client())

	if _, _, err := c.Resolve(context.Background(), ApiRef{Name: "a", Namespace: "n"}, nil); err == nil {
		t.Fatal("a 401 must still be reported as an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("requests = %d, want exactly 1. A second request re-issues a /call whose write stage "+
			"may already have been dispatched and audited, duplicating it", got)
	}
	if got := atomic.LoadInt32(&invalidations); got != 0 {
		t.Errorf("invalidations = %d, want 0: dropping the JWT for a 401 that is not about the token "+
			"forces an unnecessary re-exchange against authn on every such failure", got)
	}
}

// A cause is present, so dispatch demonstrably did not happen -- but StepUpRequired is not something a
// fresh token fixes. A retry would present a token with the same assurance level and fail identically.
func TestA401WithStepUpRequiredIsNotRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(bodyStepUp))
	}))
	defer srv.Close()

	c := New(srv.URL, func(context.Context) (string, error) { return "jwt", nil }).
		WithTokenInvalidator(func() {})
	c.WithHTTPClient(srv.Client())

	if _, _, err := c.Resolve(context.Background(), ApiRef{Name: "a", Namespace: "n"}, nil); err == nil {
		t.Fatal("want an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("requests = %d, want 1: StepUpRequired means step up, not retry", got)
	}
}

// EXACTLY ONCE, even for the retryable cause. snowplow can answer 401 persistently for reasons a fresh
// token cannot fix, and retrying those in a loop turns a clear failure into a hot one.
func TestAPersistentCredentialMissing401IsRetriedOnlyOnceThenReported(t *testing.T) {
	var calls, invalidations int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(bodyCredentialMissing))
	}))
	defer srv.Close()

	c := New(srv.URL, func(context.Context) (string, error) { return "jwt", nil }).
		WithTokenInvalidator(func() { atomic.AddInt32(&invalidations, 1) })
	c.WithHTTPClient(srv.Client())

	if _, _, err := c.Resolve(context.Background(), ApiRef{Name: "a", Namespace: "n"}, nil); err == nil {
		t.Fatal("a persistent 401 must be reported, not retried away")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("requests = %d, want exactly 2. More means a loop against snowplow on every reconcile", got)
	}
	if got := atomic.LoadInt32(&invalidations); got != 1 {
		t.Errorf("invalidations = %d, want 1", got)
	}
}

// With no invalidator -- no authn URL configured, so no cache exists -- a 401 must pass straight through
// rather than panic on a nil func. The body carries the retryable cause, so this case isolates the nil
// invalidator rather than passing for the wrong reason.
func TestA401WithNoInvalidatorIsReportedWithoutRetrying(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(bodyCredentialMissing))
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

// 503 IS NOT ADDED TO THE RETRY, and this pins that deliberately rather than leaving it as an absence.
//
// An unknown `kid` -- a rotated-out authn signing key, which is the exact rotation case #372 was written
// for -- answers 503, not 401 (#376). But 503 is ALSO snowplow's answer for "JWKS unreachable", and while
// authn is down the two are indistinguishable, so dropping the JWT on a 503 would discard valid tokens
// during an authn outage and stampede authn as it recovers. The fix belongs upstream in plumbing's
// jwtutil, where an unknown kid after a successful key fetch can be told apart from no key set at all.
func TestA503DoesNotInvalidateOrRetry(t *testing.T) {
	var calls, invalidations int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"holds no key for kid"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, func(context.Context) (string, error) { return "jwt", nil }).
		WithTokenInvalidator(func() { atomic.AddInt32(&invalidations, 1) })
	c.WithHTTPClient(srv.Client())

	if _, _, err := c.Resolve(context.Background(), ApiRef{Name: "a", Namespace: "n"}, nil); err == nil {
		t.Fatal("want an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&invalidations); got != 0 {
		t.Errorf("invalidations = %d, want 0: a 503 during an authn outage must not discard a valid "+
			"token, or recovery becomes a re-exchange stampede", got)
	}
}

// The predicate directly, including the shapes that must answer false for "I could not tell" rather than
// for a parse accident.
func TestRetryableAuthCause(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"credential missing", bodyCredentialMissing, true},
		{"step up", bodyStepUp, false},
		{"no details at all", bodyNoCause, false},
		{"empty body", ``, false},
		{"not json", `<html>502 Bad Gateway</html>`, false},
		{"details present but causes empty", `{"details":{"causes":[]}}`, false},
		{"unknown cause", `{"details":{"causes":[{"reason":"SomethingNew"}]}}`, false},
		{"cause among several", `{"details":{"causes":[{"reason":"X"},{"reason":"SessionCredentialMissing"}]}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryableAuthCause([]byte(tc.body)); got != tc.want {
				t.Errorf("retryableAuthCause(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
	// The positive shape must be a POSITIVE test, not "anything unrecognised retries". An unknown future
	// cause must not retry, because snowplow may add a post-dispatch one.
	if retryableAuthCause([]byte(`{"details":{"causes":[{"reason":"WriteAlreadyDispatched"}]}}`)) {
		t.Error("an unrecognised cause must not be retryable: a future post-dispatch cause would " +
			"otherwise be retried and duplicate the write it reports")
	}
}
