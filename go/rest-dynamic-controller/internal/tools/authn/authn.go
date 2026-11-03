// Package authn is a small client for the Kubernetes intra-service auth login strategy: the
// controller presents its own projected (audience-bound) ServiceAccount token to authn's
// /serviceaccount/login endpoint and receives an authn-issued JWT. That JWT is the Bearer the
// snowplow client uses to resolve RESTActions under the controller's identity.
//
// Token() satisfies snowplow.TokenFunc: it reads the projected SA token fresh on every miss
// (projected tokens rotate on disk), exchanges it for a JWT, and caches the JWT until shortly
// before its own expiry so the exchange is amortised across reconciles.
package authn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

// DefaultTokenPath is the conventional mount path of the projected ServiceAccount token.
// The controller Deployment projects a token bound to the authn audience at this path.
const DefaultTokenPath = "/var/run/secrets/krateo.io/serviceaccount/token"

// refreshSkew is how long before a cached JWT's expiry we proactively re-exchange, so a token
// is never handed out moments before it would be rejected downstream.
const refreshSkew = 60 * time.Second

// Client exchanges a projected ServiceAccount token for an authn-issued JWT and caches it.
type Client struct {
	server     string
	tokenPath  string
	httpClient *http.Client

	now func() time.Time // injectable clock for tests

	mu     sync.Mutex
	cached string
	expiry time.Time
}

// New returns an authn client for the given base URL (e.g.
// http://authn.krateo-system.svc.cluster.local:8082). tokenPath is the projected SA token
// file; if empty, DefaultTokenPath is used.
func New(server, tokenPath string) *Client {
	if tokenPath == "" {
		tokenPath = DefaultTokenPath
	}
	return &Client{
		server:     server,
		tokenPath:  tokenPath,
		httpClient: &http.Client{Timeout: 30 * time.Second, CheckRedirect: refuseRedirects},
		now:        time.Now,
	}
}

// WithHTTPClient swaps the transport, typically for an httptest server. The redirect policy is
// RE-INSTALLED on whatever is handed in: it belongs to this package, not to the caller (#401).
func (c *Client) WithHTTPClient(h *http.Client) *Client {
	if h != nil {
		h.CheckRedirect = refuseRedirects
	}
	c.httpClient = h
	return c
}

// Token returns a valid authn JWT, exchanging the projected SA token when the cache is empty
// or near expiry. It satisfies snowplow.TokenFunc.
func (c *Client) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cached != "" && c.now().Before(c.expiry.Add(-refreshSkew)) {
		return c.cached, nil
	}

	jwt, exp, err := c.exchange(ctx)
	if err != nil {
		return "", err
	}
	c.cached = jwt
	c.expiry = exp
	return jwt, nil
}

// exchange reads the projected SA token and POSTs it to authn's /serviceaccount/login,
// returning the issued JWT and its expiry.
// Invalidate drops the cached JWT so the next Token call re-exchanges.
//
// It exists for ONE event, and it is the only one that invalidates a JWT before its exp: authn rotating
// its signing key. A new `kid` in the JWKS makes every outstanding token fail at the verifier (snowplow)
// while this cache still considers it fresh, because the cache keys on TIME ALONE. Without this the
// controller presents a dead token until refreshSkew -- an outage lasting up to the token's remaining
// lifetime, cleared instantly by a pod restart, which makes it look like a transient (#369).
//
// authn has no JWT revocation, so ordinary expiry needs nothing from this: refreshSkew already handles it.
// This is specifically for the early-invalidation case the cache cannot observe.
//
// NOT a credential-rotation mechanism. The projected ServiceAccount token is read from disk on every
// exchange and nothing is minted or stored here that was not already; this only stops an existing cache
// insisting on a value the verifier has rejected.
func (c *Client) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cached = ""
	c.expiry = time.Time{}
}

func (c *Client) exchange(ctx context.Context) (string, time.Time, error) {
	saToken, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("reading projected service account token %q: %w", c.tokenPath, err)
	}

	u, err := url.JoinPath(c.server, "/serviceaccount/login")
	if err != nil {
		return "", time.Time{}, fmt.Errorf("joining authn url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+string(saToken))
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("calling authn: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("authn returned %d: %s", resp.StatusCode, string(body))
	}

	var out struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", time.Time{}, fmt.Errorf("decoding authn response: %w", err)
	}
	if out.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("authn response carried no accessToken")
	}

	// Derive expiry from the JWT's exp claim so caching tracks the real lifetime; fall back to
	// a conservative window if the token is unparseable (still usable, just refreshed sooner).
	exp := jwtExpiry(out.AccessToken)
	if exp.IsZero() {
		exp = c.now().Add(5 * time.Minute)
	}
	return out.AccessToken, exp, nil
}

// jwtExpiry decodes (without verifying) the JWT payload and returns its exp claim as a time.
// The signature is irrelevant here — the controller only consumes the token, it does not trust it.
// A zero time is returned when the token has no parseable exp.
func jwtExpiry(token string) time.Time {
	parts := splitJWT(token)
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

func splitJWT(token string) []string {
	out := make([]string, 0, 3)
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			out = append(out, token[start:i])
			start = i + 1
		}
	}
	out = append(out, token[start:])
	return out
}

// refuseRedirects is the CheckRedirect this package installs on every client it uses, and the package
// owns it rather than the caller.
//
// WHY THE PACKAGE OWNS IT. `WithHTTPClient` exists so a test can swap the TRANSPORT for an httptest
// server. It is not a hook for changing the redirect policy, and a caller that supplied a client without
// one would silently lose the guarantee below. So it is installed in New AND re-installed on whatever
// WithHTTPClient is handed (#401).
//
// WHY REFUSING OUTRIGHT IS THE RIGHT ANSWER RATHER THAN PINNING THE HOST. This client POSTs to one fixed,
// operator-configured path on one known in-cluster service. The authn session checked their source: authn
// has no http.Redirect and no Location header anywhere, routes with its own exact-path matcher rather than
// ServeMux (so there is no trailing-slash or path-cleaning redirect), and /serviceaccount/login answers
// 200, 401, 403 or 500 only. The single 3xx anywhere is LDAP's 300 AmbiguousUser, which this controller
// never calls, which carries no Location, and which Go would not follow. They confirmed no legitimate
// redirect exists or is planned. So a hop here is never legitimate, and refusing is simpler than pinning.
//
// WHY net/http's OWN STRIP IS NOT THE PROTECTION IT LOOKS LIKE -- the measurement this exists for. The
// standard library drops Authorization when a redirect leaves the original host, but "leaves the original
// host" PERMITS A SUBDOMAIN and ignores the port:
//
//	origin            -> redirect target     destination received
//	krateo.test       -> evil.krateo.test    Bearer <token>   <- ARRIVED
//	localhost         -> localhost:other     Bearer <token>   <- ARRIVED
//	evil.krateo.test  -> krateo.test         stripped
//	krateo.test       -> attacker.test       stripped            (control)
//
// That is deliberate, documented behaviour, not a bug: client.go's shouldCopyHeaderOnRedirect carries the
// comment "Permit sending auth/cookie headers from \"foo.com\" to \"sub.foo.com\"", and the port drops out
// because it compares url.Hostname(). No Go release will close it. So relying on the strip protects one
// header name and only against a non-subdomain change -- and the credential here is the pod's projected
// ServiceAccount token.
//
// The one case this refusal breaks is an operator who points the URL at an external ingress hostname that
// 308s http->https on the same host. Failing loudly there is the right outcome: the fix is to configure
// the https URL, not to follow the hop.
func refuseRedirects(_ *http.Request, _ []*http.Request) error {
	return fmt.Errorf("redirects are not allowed when presenting a credential to %s", "the projected ServiceAccount token to authn")
}
