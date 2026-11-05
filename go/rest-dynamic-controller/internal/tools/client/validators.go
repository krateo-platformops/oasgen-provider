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
	// Synthesised marks a status this package INVENTED rather than received. Only the findby no-match
	// paths set it, where a successful listing contained nothing matching the CR's identifiers.
	//
	// It exists because a 404 we made up and a 404 the vendor sent mean different things to different
	// callers, and the difference is unrecoverable for one of them (#371):
	//
	//	Observe  "nothing matched" -> not-exists is CORRECT. Creating is the intent and no duplicate is
	//	         possible yet, so it must keep seeing a plain NotFound.
	//	Exists   "nothing matched" is NOT proof of absence. The MATCHING can fail for reasons unrelated to
	//	         existence -- identifiers absent from the list response, an envelope shape that resolves to
	//	         the wrong thing, a findby query that cannot be evaluated. Create-recovery is the one caller
	//	         for whom a wrong `false` DUPLICATES A LIVE VENDOR RESOURCE.
	//
	// So IsNotFoundError deliberately still returns true for these: every existing caller keeps its
	// behaviour, and only a caller that asks the narrower question gets the narrower answer.
	Synthesised bool
	// BodySnippet is the vendor's own response text, read off the failed response and capped, and it is
	// DELIBERATELY NOT PART OF Error(). That omission is the whole mechanism (#433).
	//
	// Every error a reconciler returns lands verbatim in the Synced condition message -- and, on
	// unstructured-runtime, on Ready's too -- where it is readable by anyone with `get` on the resource.
	// That is a far wider audience than a Secret's RBAC, and a vendor error body is attacker-influenced
	// text of unknown content that may echo whatever was submitted. provider-runtime#28 (merged, 499ea69)
	// makes it a rule: providers summarise in the returned error and log the full body at Info, capped.
	//
	// WHY A FIELD RATHER THAN A SANITISER. There is no way to ensure a vendor puts safe values in
	// predictable keys: a vendor may send prose under `code`, or a credential under `field`. A key-name
	// allowlist would be a heuristic that LOOKS like a guarantee, which this package already argues
	// against -- "a redactor that misses is worse than none: it invites the assumption that what got
	// through is safe." The only text safe by construction is text we generated ourselves.
	//
	// So the split is by PROVENANCE: Error() carries only our own words (the operation and the status
	// code), and the vendor's text is reachable only by a caller that names this field. Nothing can leak
	// it by accident, because the accident -- %v on the error -- no longer reaches it.
	//
	// Same precedent as Header above, and the same as DiscardedServerURLs in clienttools.go: nothing in
	// this package logs, so the layer that has a logger reads the field and says so. That layer must log
	// it at Info, not Debug: rest-dynamic-controller defaults REST_CONTROLLER_DEBUG to false and the
	// Deployment oasgen renders passes no -debug, so Debug reaches nobody on a default install -- which
	// would discard the explanation rather than relocate it, re-creating #334.
	BodySnippet string
}

func (e *StatusError) Error() string {
	// A SYNTHESISED STATUS MUST NOT READ LIKE A VENDOR RESPONSE (#461).
	//
	// This type already knows the difference -- Synthesised is set only on the findby no-match paths --
	// and callers branch on it (exists.go consults IsSynthesisedNotFound BEFORE IsNotFoundError, because
	// the latter is true for both). The rendering threw that distinction away: an invented 404 came out
	// as `unexpected status: 404: item not found`, which is exactly what a vendor 404 looks like.
	//
	// That is not a cosmetic problem. The message is the only thing an operator has when deciding
	// whether to clear krateo.io/external-create-pending, and clearing it wrongly lets the reconciler
	// create a resource that already exists. A reader who trusts the most concrete-looking token --
	// a 404 saying "item not found" -- reaches precisely the conclusion the calling branch exists to
	// prevent.
	//
	// Measured by the openstack-ironic session on a live fleet: the resource this message called
	// "item not found" was `provision_state=active`, `power_state=power on`, `last_error=None`, and no
	// request had reached the endpoint at all. The token was not merely unproven, it was FALSE about the
	// world.
	//
	// Only this branch is new. A real vendor status still renders exactly as before, byte for byte,
	// because every existing caller and test reads that form and none of them is wrong.
	if e.Synthesised {
		if e.Inner != nil {
			return fmt.Sprintf("no item matched: status %d was synthesised by this controller and NOT "+
				"received from the vendor (%v)", e.StatusCode, e.Inner)
		}
		return fmt.Sprintf("no item matched: status %d was synthesised by this controller and NOT "+
			"received from the vendor", e.StatusCode)
	}
	if e.Inner != nil {
		return fmt.Sprintf("unexpected status: %d: %v", e.StatusCode, e.Inner)
	}
	return fmt.Sprintf("unexpected status: %d:", e.StatusCode)
}

func (e *StatusError) Unwrap() error {
	return e.Inner
}

// VendorBodySnippet returns the vendor's own response text carried by err, or "" if there is none.
//
// THE ONLY WAY TO REACH IT, deliberately. It is not in Error(), so a caller that wants the vendor's
// explanation has to name this function -- which makes putting it somewhere inappropriate a visible act
// rather than a side effect of %v or %w (#433).
//
// Log the result at INFO, never into a condition and never at Debug. A condition is readable by anyone
// with `get` on the resource; Debug reaches nobody on a default rest-dynamic-controller install. And never
// log a credential at any level: Krateo exports logs to the platform's log store, so a log line is
// narrower than a condition but is not private.
func VendorBodySnippet(err error) string {
	se := new(StatusError)
	if err == nil || !errors.As(err, &se) {
		return ""
	}
	return se.BodySnippet
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

// IsSynthesisedNotFound reports whether err is a 404 THIS PACKAGE invented from a successful listing that
// contained no matching item -- as opposed to one the vendor sent.
//
// A caller that can safely treat "nothing matched" as absence should use IsNotFoundError, which returns
// true for both. A caller for whom a wrong negative is unrecoverable should check this FIRST and treat it
// as "I could not determine" (#371).
func IsSynthesisedNotFound(err error) bool {
	se := new(StatusError)
	if err == nil || !errors.As(err, &se) {
		return false
	}
	return se.Synthesised && se.StatusCode == http.StatusNotFound
}
