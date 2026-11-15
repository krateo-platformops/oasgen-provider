//go:build unit || integration

package hostpolicy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func req(t *testing.T, method, raw string) *http.Request {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return &http.Request{Method: method, URL: u}
}

// CheckRedirect is a pure function of (req, via), so the method rule is tested directly. The point of
// issue 682 is that net/http has ALREADY applied its rewrite by the time this runs: req.Method is the
// method it is about to send, via's last entry is what was actually sent.
func TestCheckRedirectRefusesOnlyAMethodChangingHop(t *testing.T) {
	p, _, err := Parse(nil, nil, true) // allowAny: the host dimension is tested elsewhere
	require.NoError(t, err)

	t.Run("303 on a POST becomes a GET and is refused", func(t *testing.T) {
		// What GitHub did: POST .../deployment-branch-policies -> 303 -> GET of the collection.
		err := p.CheckRedirect(
			req(t, "GET", "https://api.github.com/repositories/1/environments/e/deployment-branch-policies"),
			[]*http.Request{req(t, "POST", "https://api.github.com/repos/o/r/environments/e/deployment-branch-policies")},
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrMethodChangingRedirect,
			"the create path keys on this sentinel with errors.Is to tell a landed-but-unidentifiable "+
				"create apart from a failed one")
		assert.Contains(t, err.Error(), "POST", "the message must name what was sent")
		assert.Contains(t, err.Error(), "GET", "and what would be sent instead")
	})

	t.Run("307/308 preserve the method and MUST still be followed", func(t *testing.T) {
		// #132: a renamed vendor repository served as a 307. Refusing it leaves the CR permanently in
		// ReconcileError, which is why this rule keys on the method and not on the status code.
		err := p.CheckRedirect(
			req(t, "POST", "https://api.example.com/v2/things"),
			[]*http.Request{req(t, "POST", "https://api.example.com/v1/things")},
		)
		assert.NoError(t, err, "a method-preserving hop to a permitted host is legitimate")
	})

	// CONTROL: the ordinary case this must not disturb. A GET redirected to a GET is the overwhelming
	// majority of redirects and was never the problem.
	t.Run("CONTROL: GET to GET is allowed", func(t *testing.T) {
		err := p.CheckRedirect(
			req(t, "GET", "https://api.example.com/v2/things/1"),
			[]*http.Request{req(t, "GET", "https://api.example.com/v1/things/1")},
		)
		assert.NoError(t, err, "control: if this refuses, every observe that follows a redirect breaks")
	})

	// CONTROL: the pre-existing rules must still fire, or this change could have replaced them.
	t.Run("CONTROL: the hop cap still applies", func(t *testing.T) {
		via := make([]*http.Request, 10)
		for i := range via {
			via[i] = req(t, "GET", "https://api.example.com/a")
		}
		err := p.CheckRedirect(req(t, "GET", "https://api.example.com/b"), via)
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrMethodChangingRedirect,
			"control: the cap is a different refusal and must not be reported as a method change")
	})

	t.Run("CONTROL: a non-permitted host is still refused", func(t *testing.T) {
		strict, _, perr := Parse([]string{"api.example.com"}, nil, false)
		require.NoError(t, perr)
		err := strict.CheckRedirect(
			req(t, "GET", "https://evil.test/a"),
			[]*http.Request{req(t, "GET", "https://api.example.com/a")},
		)
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrMethodChangingRedirect,
			"control: a host refusal is not a method change; the two must stay distinguishable")
	})
}

// THE PROPERTY THE CREATE PATH DEPENDS ON: net/http wraps a CheckRedirect error in *url.Error, and the
// create path matches the sentinel with errors.Is THROUGH the client. If the unwrap did not work, the
// create would fall through to the generic failure path and report a landed create as failed -- which is
// the whole thing this is trying to avoid. So this goes end to end through a real client and server.
func TestTheSentinelSurvivesTheClientWrapping(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			// 303 See Other: net/http follows it with a GET.
			http.Redirect(w, r, srv.URL+"/collection", http.StatusSeeOther)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"total_count":2,"items":[]}`))
	}))
	defer srv.Close()

	p, _, err := Parse(nil, nil, true)
	require.NoError(t, err)
	p.allowLoopback = true // httptest binds 127.0.0.1; the seam exists for exactly this

	_, derr := p.Client(5*1e9).Post(srv.URL+"/things", "application/json", nil)
	require.Error(t, derr, "the hop must be refused rather than followed")
	assert.ErrorIs(t, derr, ErrMethodChangingRedirect,
		"errors.Is must reach the sentinel through *url.Error, because that is how the create path "+
			"recognises it")

	var ue *url.Error
	require.ErrorAs(t, derr, &ue, "control: net/http really did wrap it, so the unwrap above is load-bearing")

	// CONTROL: the same server and policy must serve a plain GET, or the refusal above could be any
	// failure at all.
	resp, gerr := p.Client(5 * time.Second).Get(srv.URL + "/collection")
	require.NoError(t, gerr, "control: a GET with no redirect must succeed through this policy")
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
