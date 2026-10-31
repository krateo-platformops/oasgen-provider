package restclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The 401/403 split is the whole point of this classification, so it is asserted first and in isolation.
//
// 401 MUST stay retryable: credential rotation is delegated to a secret manager, the controller reads the
// Secret on every reconcile with no cache, and a request that fails against a token being replaced is
// expected to succeed against its replacement. If a 401 ever classified as denied, every rotation would
// become an operator ticket -- the exact failure this architecture is built to avoid.
func TestA401IsNeverPermanentBecauseRotationIsToleratedByRetrying(t *testing.T) {
	err := &StatusError{StatusCode: http.StatusUnauthorized, Header: http.Header{}}

	if IsAuthorizationDenied(err) {
		t.Fatal("a 401 classified as a permanent denial: rotation would stop being survivable")
	}
	if _, throttled := RetryAfter(err); throttled {
		t.Error("a 401 classified as a throttle; it is neither throttle nor denial, just retryable")
	}
}

func TestABare403IsADenial(t *testing.T) {
	if !IsAuthorizationDenied(&StatusError{StatusCode: http.StatusForbidden, Header: http.Header{}}) {
		t.Error("a 403 with no rate-limit signal must be a denial; retrying cannot change a decision")
	}
}

// GitHub answers BOTH rate-limit kinds with "403 or 429" and its docs warn that continuing to request while
// limited may get the integration banned. A "403 is permanent" rule without this check would break throttle
// recovery for one of this project's primary blueprints.
func TestAThrottleDressedAsA403IsNotADenial(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header http.Header
		want   time.Duration
	}{
		{
			name:   "primary limit: remaining 0 with no reset",
			header: http.Header{"X-Ratelimit-Remaining": {"0"}},
			want:   defaultThrottleRetryAfter,
		},
		{
			name:   "secondary limit: Retry-After in seconds",
			header: http.Header{"Retry-After": {"45"}},
			want:   45 * time.Second,
		},
		{
			name:   "Retry-After present but unparseable still identifies a throttle",
			header: http.Header{"Retry-After": {"Mon, 02 Nov 2026 19:00:00 GMT"}},
			want:   defaultThrottleRetryAfter,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := &StatusError{StatusCode: http.StatusForbidden, Header: tc.header}

			d, throttled := RetryAfter(err)
			if !throttled {
				t.Fatalf("classified as a denial; a throttle retried never would need a human to clear it")
			}
			if d != tc.want {
				t.Errorf("delay = %v, want %v", d, tc.want)
			}
			if IsAuthorizationDenied(err) {
				t.Error("IsAuthorizationDenied true for a throttle: the two classifications must not overlap")
			}
		})
	}
}

// x-ratelimit-remaining is sent on SUCCESSFUL GitHub responses too, so its presence means the API has
// limits -- not that one was hit. Keying on presence rather than the value 0 would make every 403 from a
// rate-limited API look transient, including a real denial.
func TestRemainingAboveZeroIsStillADenial(t *testing.T) {
	err := &StatusError{StatusCode: http.StatusForbidden, Header: http.Header{
		"X-Ratelimit-Remaining": {"4999"},
		"X-Ratelimit-Reset":     {"1793558400"},
	}}

	if _, throttled := RetryAfter(err); throttled {
		t.Error("remaining=4999 treated as a throttle: presence of the header is not evidence a limit was hit")
	}
	if !IsAuthorizationDenied(err) {
		t.Error("want a denial: quota remains, so the refusal is about permission")
	}
}

func TestResetAlreadyPastRetriesPromptly(t *testing.T) {
	past := time.Now().Add(-5 * time.Minute).Unix()
	d, throttled := RetryAfter(&StatusError{StatusCode: http.StatusForbidden, Header: http.Header{
		"X-Ratelimit-Remaining": {"0"},
		"X-Ratelimit-Reset":     {itoa(past)},
	}})

	if !throttled {
		t.Fatal("want a throttle")
	}
	if d != 0 {
		t.Errorf("delay = %v, want 0: the window has already rolled over, so waiting a default minute "+
			"would idle against a limit that no longer applies", d)
	}
}

// Retry-After is vendor-controlled input. An unclamped week would park a resource with no indication why.
func TestAnAbsurdRetryAfterIsClamped(t *testing.T) {
	d, throttled := RetryAfter(&StatusError{StatusCode: http.StatusForbidden, Header: http.Header{
		"Retry-After": {"604800"}, // one week
	}})

	if !throttled {
		t.Fatal("want a throttle")
	}
	if d != maxThrottleRetryAfter {
		t.Errorf("delay = %v, want it clamped to %v", d, maxThrottleRetryAfter)
	}
}

// A 429 means "too many requests" by its own definition, so it is a throttle even with nothing to say so.
func TestABare429IsAThrottleNotADenial(t *testing.T) {
	err := &StatusError{StatusCode: http.StatusTooManyRequests}

	if _, throttled := RetryAfter(err); !throttled {
		t.Error("a bare 429 must be a throttle; the status code alone carries that meaning")
	}
	if IsAuthorizationDenied(err) {
		t.Error("a 429 is not a 403 and must never be a denial")
	}
}

// THE DANGEROUS FAILURE MODE, asserted directly.
//
// With Header nil, RetryAfter cannot see any rate-limit signal, so EVERY 403 classifies as a denial --
// including a GitHub throttle, which would then stop permanently instead of clearing by itself. The
// classifier would be silently inverted for the one vendor it was written to protect, and every unit test
// above would still pass because they construct the header themselves.
//
// So this asserts the wiring rather than the logic: that a 403 built by the real request path carries the
// response's headers through to the classifier.
func TestNilHeaderWouldInvertTheClassifierForThrottles(t *testing.T) {
	denied := IsAuthorizationDenied(&StatusError{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"X-Ratelimit-Remaining": {"0"}},
	})
	deniedWithoutHeaders := IsAuthorizationDenied(&StatusError{
		StatusCode: http.StatusForbidden,
	})

	if denied {
		t.Error("a throttle with headers classified as denied")
	}
	if !deniedWithoutHeaders {
		t.Fatal("precondition failed: a headerless 403 is expected to classify as denied, which is " +
			"exactly why StatusError.Header must be populated at every construction site")
	}
	// Documents the consequence, so a future reader sees why the restclient_test wiring assertion exists.
	t.Log("a dropped Header turns every throttle into a permanent stop; the wiring is asserted in " +
		"TestStatusErrorCarriesResponseHeaders")
}

func itoa(v int64) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b []byte
	for v > 0 {
		b = append([]byte{digits[v%10]}, b...)
		v /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// TestStatusErrorCarriesResponseHeaders asserts the WIRING, not the logic, and it is the test that can
// actually fail if someone adds a third StatusError construction site and forgets the headers.
//
// Every other test in this file builds the header map itself, so all of them would keep passing while the
// real request path dropped it -- and a dropped header inverts the classifier for exactly the vendor it
// protects: a GitHub throttle would classify as a permanent denial and stop forever instead of clearing.
//
// It drives a real request against a server answering 403 with the primary-rate-limit headers, which is
// GitHub's documented shape, and asserts the resulting error classifies as a THROTTLE.
func TestStatusErrorCarriesResponseHeaders(t *testing.T) {
	reset := time.Now().Add(90 * time.Second).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", itoa(reset))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer srv.Close()

	client := createTestClient(t)
	client.Server = srv.URL

	_, err := client.Call(context.Background(), srv.Client(), "/api/test",
		&RequestConfiguration{Method: "GET"})
	if err == nil {
		t.Fatal("a 403 must error")
	}

	se := new(StatusError)
	if !errors.As(err, &se) {
		t.Fatalf("want a *StatusError, got %T", err)
	}
	if se.Header == nil {
		t.Fatal("StatusError.Header is nil from the real request path: every 403 would classify as a " +
			"permanent denial, including a rate limit that clears by itself")
	}

	d, throttled := RetryAfter(err)
	if !throttled {
		t.Fatal("a 403 carrying GitHub's primary rate-limit headers classified as a denial, so the " +
			"resource would stop permanently on a limit that resets in 90 seconds")
	}
	if d <= 0 || d > 2*time.Minute {
		t.Errorf("delay = %v, want roughly the 90s until reset", d)
	}
	if IsAuthorizationDenied(err) {
		t.Error("IsAuthorizationDenied true for a rate limit")
	}
}
