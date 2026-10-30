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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+q.Encode(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("creating request: %w", err)
	}
	if c.token != nil {
		tok, err := c.token(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("acquiring snowplow token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// http.Client.Do wraps transport failures in a *url.Error whose string embeds the full request URL —
		// including the extras query. Strip it so the request data never reaches error logs.
		return nil, "", fmt.Errorf("calling snowplow: %w", stripURL(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("snowplow returned %d: %s", resp.StatusCode, string(body))
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
	failedStages = StageOutcomes(resp.Header)
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
