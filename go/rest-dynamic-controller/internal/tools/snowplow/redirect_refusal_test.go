package snowplow

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// THE CREDENTIAL HERE IS THE AUTHN JWT, obtained with the pod's projected ServiceAccount token (#401).
//
// It was presented on a plain &http.Client{Timeout: ...} with no CheckRedirect. net/http's own
// Authorization strip does not cover that: it PERMITS a hop to a SUBDOMAIN of the origin host and ignores
// the port. client.go's shouldCopyHeaderOnRedirect says so in a comment -- "Permit sending auth/cookie
// headers from \"foo.com\" to \"sub.foo.com\"" -- and the port drops out because it compares
// url.Hostname(). It is deliberate, documented behaviour, so no Go release will close it.
//
// Driven through Resolve, the real entry point, rather than by calling the HTTP client directly: the
// guarantee that matters is the one a caller gets, and a test that builds its own request would pass
// even if Resolve used a different client.
//
// THE DESTINATION PROVES IT CAN DETECT A HEADER FIRST. "The destination saw nothing" is the PASSING
// answer, so an instrument that cannot see headers at all would report success -- a signal whose failure
// to apply is indistinguishable from its applying.
func TestTheAuthnJWTDoesNotFollowARedirect(t *testing.T) {
	const jwt = "AUTHN-JWT-SENTINEL"

	var destinationSaw string
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationSaw = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":{"stolen":true}}`))
	}))
	defer destination.Close()
	_, destPort, _ := net.SplitHostPort(strings.TrimPrefix(destination.URL, "http://"))

	const originHost = "snowplow.krateo.test"
	const destHost = "evil." + originHost
	var originSaw string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originSaw = r.Header.Get("Authorization")
		http.Redirect(w, r, "http://"+destHost+":"+destPort+"/stolen", http.StatusFound)
	}))
	defer origin.Close()
	_, originPort, _ := net.SplitHostPort(strings.TrimPrefix(origin.URL, "http://"))

	// anyHost dials 127.0.0.1 on whatever port the URL names, so arbitrary hostnames are reachable
	// without touching /etc/hosts. net/http makes the header decision on the NAME before resolution, so
	// this changes reachability only.
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
		req.Header.Set("Authorization", "Bearer "+jwt)
		resp, err := (&http.Client{Transport: anyHost()}).Do(req)
		if err != nil {
			t.Fatalf("the premise request must reach the destination: %v", err)
		}
		_ = resp.Body.Close()
		if !strings.Contains(destinationSaw, jwt) {
			t.Fatalf("the destination did not record a credential sent to it directly (saw %q). The "+
				"assertion below reads its silence as proof the JWT did not travel", destinationSaw)
		}
	})

	t.Run("the JWT does not reach a subdomain of the configured host", func(t *testing.T) {
		destinationSaw, originSaw = "", ""
		c := New("http://"+originHost+":"+originPort, func(context.Context) (string, error) {
			return jwt, nil
		}).WithHTTPClient(&http.Client{Transport: anyHost()})

		_, _, err := c.Resolve(context.Background(), ApiRef{Name: "a", Namespace: "n"}, nil)
		if err == nil {
			t.Fatal("Resolve must fail: a 302 from snowplow is never legitimate, so following it " +
				"silently is the defect and succeeding would mean the hop was taken")
		}
		if !strings.Contains(err.Error(), "redirects are not allowed") {
			t.Errorf("error = %q, want it to name the redirect refusal; any other failure would make "+
				"the ground-truth assertions below pass for the wrong reason", err.Error())
		}

		// GROUND TRUTH, read from the servers rather than from the client's own report.
		if strings.Contains(destinationSaw, jwt) {
			t.Errorf("the authn JWT reached %s, a host no operator configured", destHost)
		}
		if destinationSaw != "" {
			t.Errorf("the destination was contacted at all (Authorization=%q); the hop must be refused "+
				"before any request leaves", destinationSaw)
		}
		if !strings.Contains(originSaw, jwt) {
			t.Errorf("the configured host never received the credential (saw %q), so this case did not "+
				"exercise a credentialed request at all", originSaw)
		}
	})
}

// The redirect policy belongs to the package, not to the caller. WithHTTPClient exists to swap the
// TRANSPORT for a test server -- every existing caller in this package's tests passes srv.Client() --
// and a caller handing in a bare client must not silently lose the guarantee.
func TestWithHTTPClientReinstallsTheRedirectPolicy(t *testing.T) {
	supplied := &http.Client{}
	c := New("http://snowplow.invalid", nil).WithHTTPClient(supplied)
	if c.httpClient.CheckRedirect == nil {
		t.Fatal("WithHTTPClient left CheckRedirect nil, so a caller-supplied client follows redirects " +
			"and presents the authn JWT on every hop (#401)")
	}
	if err := c.httpClient.CheckRedirect(nil, nil); err == nil {
		t.Error("the installed CheckRedirect permitted a hop. A non-nil CheckRedirect returning nil is " +
			"indistinguishable from none, which is why this asserts behaviour and not the field")
	}
	if New("http://snowplow.invalid", nil).httpClient.CheckRedirect == nil {
		t.Error("New built a client with no redirect policy; WithHTTPClient is the override, not the " +
			"only place the guarantee exists")
	}
}
