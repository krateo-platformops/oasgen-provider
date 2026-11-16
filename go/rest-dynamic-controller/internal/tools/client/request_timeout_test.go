//go:build unit || integration

package restclient

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDefaultRequestTimeoutAppliesAndACallerStillOverridesIt pins both halves of the knob, because
// either one alone is satisfied by a broken implementation.
//
// A test that only proved "a short default times out" would pass under an implementation that ignored
// the caller's own Timeout and shortened the async poll and the lookup runner with it. A test that only
// proved "a caller's Timeout is honoured" would pass under the hardcoded 30s this replaces. So the three
// arms are: the default applies, a longer caller value WINS, and a long default lets the same bare
// client through -- the third being the control that makes the first arm's failure attributable to the
// default rather than to the server, the policy or the request.
func TestDefaultRequestTimeoutAppliesAndACallerStillOverridesIt(t *testing.T) {
	const serverDelay = 300 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(serverDelay)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	u := &UnstructuredClient{Server: srv.URL, HostPolicy: allowedPolicy(t, srv.URL)}

	saved := DefaultRequestTimeout
	t.Cleanup(func() { DefaultRequestTimeout = saved })

	newReq := func() *http.Request {
		req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
		require.NoError(t, err)
		return req
	}

	t.Run("a bare client takes the DEFAULT, and a short one cuts the request", func(t *testing.T) {
		DefaultRequestTimeout = 50 * time.Millisecond
		_, err := u.do(&http.Client{}, newReq(), nil, false)
		require.Error(t, err,
			"a bare &http.Client{} -- which is what every CRUD and dispatch call site constructs -- must "+
				"take DefaultRequestTimeout; without this there is no way to bound those calls at all")
	})

	t.Run("CONTROL: a long default lets the SAME bare client through", func(t *testing.T) {
		DefaultRequestTimeout = 5 * time.Second
		resp, err := u.do(&http.Client{}, newReq(), nil, false)
		require.NoError(t, err,
			"the arm above must fail BECAUSE of the default. If this also fails, the server, the host "+
				"policy or the request is at fault and the first arm proved nothing")
		require.NoError(t, resp.Body.Close())
	})

	t.Run("a caller's own Timeout WINS over a short default", func(t *testing.T) {
		DefaultRequestTimeout = 50 * time.Millisecond
		resp, err := u.do(&http.Client{Timeout: 5 * time.Second}, newReq(), nil, false)
		require.NoError(t, err,
			"async.RequestTimeout and lookupRequestTimeout are set deliberately by their call sites; a "+
				"default that overrode them would silently shorten two paths that chose their own bound")
		require.NoError(t, resp.Body.Close())
	})
}
