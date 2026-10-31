package restclient

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type StatusError struct {
	StatusCode int
	Inner      error
	// Header carries the rejected response's headers, because the status code ALONE CANNOT CLASSIFY the
	// failure. GitHub answers a rate limit with 403 or 429 and distinguishes it from a real authorisation
	// denial only in `x-ratelimit-remaining` / `retry-after` -- so a classifier handed just the code would
	// have to guess, and either retry a permanent denial forever or give up on a throttle that clears by
	// itself.
	//
	// It is not included in Error(): a response header set is not a vendor explanation, and the message is
	// read in logs where a header dump is noise.
	Header http.Header
}

func (e *StatusError) Error() string {
	if e.Inner != nil {
		return fmt.Sprintf("unexpected status: %d: %v", e.StatusCode, e.Inner)
	}
	return fmt.Sprintf("unexpected status: %d:", e.StatusCode)
}

func (e *StatusError) Unwrap() error {
	return e.Inner
}

// HasStatusErr returns true if err is a ResponseError caused by any of the codes given.
func HasStatusErr(err error, codes ...int) bool {
	if err == nil {
		return false
	}
	if se := new(StatusError); errors.As(err, &se) {
		for _, code := range codes {
			if se.StatusCode == code {
				return true
			}
		}
	}
	return false
}

func HasValidStatusCode(code int, acceptStatuses ...int) bool {
	for _, c := range acceptStatuses {
		if code == c {
			return true
		}
	}
	return false
}

func IsNotFoundError(err error) bool {
	return HasStatusErr(err, http.StatusNotFound)
}

// Retry classification for rejected statuses.
//
// Three outcomes, and the distinction between the first two is the whole point of this file:
//
//	401  the credential may be REPLACED under us          -> retry, indefinitely
//	403  the credential is not PERMITTED to do this       -> permanent, stop
//	403/429 + rate-limit headers                          -> throttled, retry after the stated delay
//
// 401 MUST stay retryable. Credential rotation is delegated to a secret manager in this architecture: the
// controller never mints or refreshes a token, it reads one from a Secret on every reconcile (there is no
// cache). The way rotation is TOLERATED is precisely that a request failing against a token being replaced
// is retried and succeeds against the new one. Treating a 401 as permanent would convert every rotation
// into an operator ticket.
//
// 403 is the opposite: an authorisation decision has been made about a credential that is not changing.
// Retrying cannot alter the outcome, and until now it was retried on every reconcile forever -- the same
// family as #266 (a permanent render failure should answer), #261 (an unverifiable delete should hold) and
// #309 (a non-converging update should stop), and the one case with no guard (#313).
//
// BUT 403 IS NOT RELIABLY PERMANENT, which is why this is not a two-line fix. GitHub's REST API answers
// BOTH rate-limit kinds with "403 or 429", and its own documentation warns that continuing to send requests
// while limited "may get your integration banned". So a naive "403 stops" rule would break throttle
// recovery for one of this project's primary blueprints, and a naive "403 retries" rule risks a ban. The
// headers are the only channel that separates them.

// throttleHeaders are the signals a vendor uses to say "this is a limit, not a decision".
//
// x-ratelimit-remaining is checked for the VALUE 0 rather than mere presence, because GitHub sends it on
// every successful response too -- presence means the API has limits, not that one was hit.
const (
	headerRetryAfter          = "Retry-After"
	headerRateLimitRemaining  = "X-RateLimit-Remaining"
	headerRateLimitReset      = "X-RateLimit-Reset"
	defaultThrottleRetryAfter = time.Minute
	maxThrottleRetryAfter     = time.Hour
)

// RetryAfter reports whether err is a THROTTLE rather than a denial, and how long to wait.
//
// The delay is clamped to maxThrottleRetryAfter: Retry-After is vendor-controlled input, and a value of a
// week would otherwise park a resource indefinitely with no indication why. A clamped wait retries sooner
// than asked, which is the safe direction for correctness and the unsafe one for a ban -- so the clamp is
// deliberately generous rather than tight.
func RetryAfter(err error) (time.Duration, bool) {
	se := new(StatusError)
	if err == nil || !errors.As(err, &se) {
		return 0, false
	}
	if se.StatusCode != http.StatusForbidden && se.StatusCode != http.StatusTooManyRequests {
		return 0, false
	}
	if se.Header == nil {
		// A 429 means "too many requests" by definition, so it is a throttle even with no headers to say
		// so. A bare 403 is not: with nothing to distinguish it, it is a denial.
		if se.StatusCode == http.StatusTooManyRequests {
			return defaultThrottleRetryAfter, true
		}
		return 0, false
	}

	if v := strings.TrimSpace(se.Header.Get(headerRetryAfter)); v != "" {
		// Retry-After is "delay-seconds | HTTP-date". Only the numeric form is honoured; a date would
		// need a clock comparison against a header whose own timestamp may be skewed, and every vendor
		// in scope sends seconds.
		if secs, convErr := strconv.Atoi(v); convErr == nil && secs >= 0 {
			return clampThrottle(time.Duration(secs) * time.Second), true
		}
		// Present but unparseable still identifies a throttle -- the vendor said to wait, and failing to
		// read the number is no reason to treat it as a permanent denial.
		return defaultThrottleRetryAfter, true
	}

	if strings.TrimSpace(se.Header.Get(headerRateLimitRemaining)) == "0" {
		if v := strings.TrimSpace(se.Header.Get(headerRateLimitReset)); v != "" {
			if epoch, convErr := strconv.ParseInt(v, 10, 64); convErr == nil {
				if d := time.Until(time.Unix(epoch, 0)); d > 0 {
					return clampThrottle(d), true
				}
				// A reset already in the past means the window has rolled over; retry promptly rather
				// than waiting a default minute for a limit that no longer applies.
				return 0, true
			}
		}
		return defaultThrottleRetryAfter, true
	}

	if se.StatusCode == http.StatusTooManyRequests {
		return defaultThrottleRetryAfter, true
	}
	return 0, false
}

func clampThrottle(d time.Duration) time.Duration {
	if d > maxThrottleRetryAfter {
		return maxThrottleRetryAfter
	}
	if d < 0 {
		return 0
	}
	return d
}

// IsAuthorizationDenied reports whether err is a 403 that is NOT a throttle -- an authorisation decision
// about a credential that retrying will not change.
//
// 401 deliberately returns false. See the block comment above: a 401 is how a rotation in flight looks, and
// retrying it is the mechanism by which this architecture tolerates rotation without minting tokens.
func IsAuthorizationDenied(err error) bool {
	if !HasStatusErr(err, http.StatusForbidden) {
		return false
	}
	_, throttled := RetryAfter(err)
	return !throttled
}
