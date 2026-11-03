// Package snowplow is a small HTTP client for resolving a RESTAction through snowplow's
// /call endpoint (a synchronous in-cluster service call during reconcile). It mirrors the
// composition-dynamic-controller integration: snowplow resolves the referenced RESTAction
// under the caller's identity (an authn-issued service JWT, see the Token provider) and
// returns the resolved CR's .status — the keyed `.api.<callName>` response map — which the
// rest-dynamic-controller feeds into the managed resource's status.
package snowplow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ApiRef identifies the RESTAction to resolve.
type ApiRef struct {
	Name      string
	Namespace string
}

// TokenFunc returns the Bearer token to authenticate to snowplow (an authn-issued service
// JWT). It is called per resolve so a rotated/expired token is refreshed by the provider.
type TokenFunc func(ctx context.Context) (string, error)

// Client resolves RESTActions via snowplow's /call endpoint.
type Client struct {
	server     string
	httpClient *http.Client
	token      TokenFunc
	// invalidate drops the token provider's cache. Called ONCE per Resolve on a 401, so an authn
	// signing-key rotation is self-healing rather than an outage until the cached JWT nears expiry
	// (#369). nil when the caller supplies no token or no invalidator.
	invalidate func()
}

// New returns a snowplow client for the given base URL (e.g.
// http://snowplow.krateo-system.svc.cluster.local:8081). token provides the Bearer JWT (nil
// for an unauthenticated snowplow, e.g. in tests).
func New(server string, token TokenFunc) *Client {
	return &Client{
		server:     server,
		httpClient: &http.Client{Timeout: 60 * time.Second},
		token:      token,
	}
}

// WithTokenInvalidator sets the callback used to drop a stale cached token when snowplow answers 401.
//
// Separate from New rather than a parameter, so an existing caller that passes only a TokenFunc keeps
// working unchanged and a nil invalidator means "retry nothing" rather than a panic.
func (c *Client) WithTokenInvalidator(f func()) *Client { c.invalidate = f; return c }

func (c *Client) WithHTTPClient(h *http.Client) *Client { c.httpClient = h; return c }

const restActionAPIVersion = "templates.krateo.io/v1"

// maxExtrasBytes bounds the JSON-encoded extras. Extras ride on the /call GET query string, so an
// unbounded payload (e.g. a very large spec on the create path) could exceed URL/header limits and 414.
// This surfaces a clear error instead. ~48KiB leaves ample headroom under a typical 60KiB header cap.
const maxExtrasBytes = 48 * 1024

// Resolve resolves the referenced RESTAction with the given extras (the per-instance
// request-extras merged over the RESTAction's inline apiRef.extras by snowplow) and returns
// the keyed `.api` response map from the resolved CR's status.
// Resolve returns the RESTAction's composed .status, the raw value of snowplow's failed-stage header (empty
// when no stage failed -- see StageOutcomes), and an error. The failed-stage value is returned rather than
// logged here because this client has no logger by design: it reports what snowplow said and lets the caller,
// which has the resource context, decide how to surface it.
func (c *Client) Resolve(ctx context.Context, ref ApiRef, extras map[string]any) (status map[string]any, failedStages string, err error) {
	if ref.Name == "" || ref.Namespace == "" {
		return nil, "", fmt.Errorf("apiRef name and namespace are required")
	}

	u, err := url.JoinPath(c.server, "/call")
	if err != nil {
		return nil, "", fmt.Errorf("joining snowplow url: %w", err)
	}
	q := url.Values{}
	q.Set("apiVersion", restActionAPIVersion)
	q.Set("resource", "restactions")
	q.Set("namespace", ref.Namespace)
	q.Set("name", ref.Name)
	if len(extras) > 0 {
		b, err := json.Marshal(extras)
		if err != nil {
			return nil, "", fmt.Errorf("encoding extras: %w", err)
		}
		if len(b) > maxExtrasBytes {
			return nil, "", fmt.Errorf("extras too large (%d bytes > %d): the resource spec is too big to forward inline; have the RESTAction fetch it instead", len(b), maxExtrasBytes)
		}
		q.Set("extras", string(b))
	}

	// issue builds and sends the request, acquiring a token each time so a retry picks up a fresh one.
	issue := func() (int, []byte, http.Header, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+q.Encode(), nil)
		if err != nil {
			return 0, nil, nil, fmt.Errorf("creating request: %w", err)
		}
		if c.token != nil {
			tok, terr := c.token(ctx)
			if terr != nil {
				return 0, nil, nil, fmt.Errorf("acquiring snowplow token: %w", terr)
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			// http.Client.Do wraps transport failures in a *url.Error whose string embeds the full request
			// URL — including the extras query. Strip it so the request data never reaches error logs.
			return 0, nil, nil, fmt.Errorf("calling snowplow: %w", stripURL(err))
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		// The headers travel with the body: X-Snowplow-Stage-Outcomes is read after the call, and on a
		// retry it must be the RETRY's header rather than the discarded 401's.
		return resp.StatusCode, b, resp.Header, nil
	}

	code, body, hdr, err := issue()
	if err != nil {
		return nil, "", err
	}

	// A 401 can mean the token this controller presented was rejected -- the cache that produced it keys on
	// TIME ALONE, so after an authn signing-key rotation it returns the same dead JWT until refreshSkew
	// (#369). Dropping the cache and re-issuing makes that self-healing.
	//
	// IT CAN ALSO MEAN A WRITE ALREADY HAPPENED, which is why the status code alone must not drive this.
	//
	// The previous version of this code retried on any 401, justified by "safe to re-issue because /call is
	// a GET: no body, no side effect from the request itself". The snowplow session falsified that twice,
	// by measurement against their real middleware and a real RESTAction pipeline:
	//
	//  1. /call accepts POST, PUT, PATCH and DELETE; the verb is passed through; and a RESTAction can
	//     declare write stages that are dispatched and audited per stage whatever the outer method is. So
	//     the method never licensed the retry.
	//  2. A RESTAction whose stage 1 POSTs (201, audited) and whose stage 2 takes an upstream 401 answers
	//     401 for the WHOLE /call -- after the POST was sent and audited. Re-issuing then performs the
	//     write a second time.
	//
	// And those post-dispatch 401s are byte-shaped like a token rejection: reason "Unauthorized", no
	// details. So the status code cannot distinguish "my token is stale" from "a write already landed",
	// and a wrong guess duplicates an audited side effect.
	//
	// THE DISCRIMINATOR IS THE CAUSE, not the code. Only snowplow's own pre-dispatch credential rejection
	// carries details.causes[0].reason, and an upstream 401 CANNOT forge one: details planted in an
	// upstream body are dropped before the response is built. That guarantee is what makes a positive
	// signal usable here. A 401 with no cause therefore means "nothing is known about whether dispatch
	// happened", and the only safe reading of that is "it may have".
	//
	// So: retry ONLY on SessionCredentialMissing, and EXACTLY ONCE.
	//
	//   SessionCredentialMissing  no credential reached the auth layer -> nothing dispatched -> retry
	//   StepUpRequired            a cause, but it means STEP UP, not retry -> do not
	//   no cause at all           token rejection OR post-dispatch upstream 401, indistinguishable -> do not
	//
	// Exactly once because snowplow can answer 401 for reasons a fresh token cannot fix -- a misconfigured
	// audience, a mapping that no longer exists -- and retrying those in a loop turns a clear failure into
	// a hot one.
	//
	// NOT 503. An unknown `kid` (a rotated-out authn signing key) answers 503, so the rotation case #372
	// was written for is not reachable through a 401 at all today -- see #376. 503 is ALSO snowplow's
	// answer for "JWKS unreachable", and while authn is down the two are indistinguishable, so dropping the
	// JWT on a 503 would discard valid tokens during an authn outage and stampede authn as it recovers.
	// The fix belongs upstream, in plumbing's jwtutil, where an unknown kid after a successful key fetch
	// can be told apart from no key set at all.
	if code == http.StatusUnauthorized && c.invalidate != nil && retryableAuthCause(body) {
		c.invalidate()
		code, body, hdr, err = issue()
		if err != nil {
			return nil, "", err
		}
	}

	if code != http.StatusOK {
		return nil, "", fmt.Errorf("snowplow returned %d: %s", code, string(body))
	}

	// snowplow returns the resolved RESTAction CR; .status is the keyed .api response map.
	var cr struct {
		Status map[string]any `json:"status"`
	}
	if err := json.Unmarshal(body, &cr); err != nil {
		return nil, "", fmt.Errorf("decoding snowplow response: %w", err)
	}
	// Surface the per-stage outcomes BEFORE returning. They were previously discarded with the rest of the
	// response headers, which meant a RESTAction whose teardown stage failed looked identical to one that
	// succeeded -- snowplow answers 200 either way (#292).
	//
	// READ THIS BEFORE USING outcomes: the header lists only the stages that FAILED. An empty or absent
	// header therefore means "no stage reported a failure", NOT "work was done". A stage that ran, fetched,
	// and iterated over ZERO items succeeds and appears nowhere here -- which is exactly the case that
	// produced #292, where every layer reported success and only a verification GET knew otherwise. Treat a
	// non-empty value as real evidence of failure; never treat an empty one as evidence of completion.
	failedStages = StageOutcomes(hdr)
	if cr.Status == nil {
		return map[string]any{}, failedStages, nil
	}
	return cr.Status, failedStages, nil
}

// StageOutcomesHeader is the response header snowplow uses to report per-stage results. Exported so a caller
// asserting on it names the same string this package reads.
const StageOutcomesHeader = "X-Snowplow-Stage-Outcomes"

// StageOutcomes returns the raw stage-outcomes header, or "" when snowplow reported no failed stage.
//
// snowplow emits "[]" when every stage succeeded, which is indistinguishable from success by design, so both
// "" and "[]" are normalized to "". See the warning in Resolve: an empty result is not evidence that any work
// was performed.
func StageOutcomes(h http.Header) string {
	v := strings.TrimSpace(h.Get(StageOutcomesHeader))
	if v == "" || v == "[]" {
		return ""
	}
	return v
}

// stripURL unwraps a *url.Error to its underlying cause, dropping the request URL (which carries the extras
// query) so it cannot leak into logs. Non-url.Error values pass through unchanged.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// causeSessionCredentialMissing is the only pre-dispatch cause that a retry can fix. snowplow guarantees an
// upstream 401 cannot forge a cause, so its PRESENCE is evidence that snowplow rejected the request before
// dispatching anything.
const causeSessionCredentialMissing = "SessionCredentialMissing"

// retryableAuthCause reports whether a 401 body carries snowplow's own pre-dispatch credential-missing
// cause, which is the only case where re-issuing cannot repeat a side effect.
//
// It is deliberately a POSITIVE test. The alternative shape -- "retry unless the body says a stage ran" --
// would retry every reason snowplow adds later, including post-dispatch ones that do not exist yet. An
// unparseable body, an empty body, a body with no details, and a body with a different cause all answer
// false, because "I could not tell" and "it may already have been dispatched" must be the same answer here.
func retryableAuthCause(body []byte) bool {
	var st struct {
		Details struct {
			Causes []struct {
				Reason string `json:"reason"`
			} `json:"causes"`
		} `json:"details"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return false
	}
	for _, c := range st.Details.Causes {
		if c.Reason == causeSessionCredentialMissing {
			return true
		}
	}
	return false
}
