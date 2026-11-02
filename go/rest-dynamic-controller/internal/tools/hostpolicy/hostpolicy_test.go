package hostpolicy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("test fixture %q is not a URL: %v", raw, err)
	}
	return u
}

// THE DEFAULT IS THE WHOLE DESIGN. An empty allowlist must be a no-op for every existing install -- the
// document's own hosts keep working -- AND a refusal for anything else. Not "permit anything" and not
// "configuration error": requiring the value would turn every single-host provider into a config error on
// upgrade for a capability it does not use.
func TestEmptyAllowlistPermitsTheDocumentsHostAndNothingElse(t *testing.T) {
	p, err := Parse(nil, []string{"https://api.github.com"}, false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := p.PermitsURL(mustURL(t, "https://api.github.com/repos/o/r")); err != nil {
		t.Errorf("the document's own host must be permitted with no allowlist, or every existing install "+
			"becomes a config error on upgrade: %v", err)
	}
	if err := p.PermitsURL(mustURL(t, "https://evil.example.net/repos/o/r")); err == nil {
		t.Error("a host that is neither the document's nor in the allowlist must be refused")
	}
}

// A policy that was never configured must refuse, not permit. An unguarded credentialed request SUCCEEDS,
// so failing open would be invisible -- which is how the unguarded path survived unnoticed for 17 months.
func TestNilAndZeroPolicyRefuse(t *testing.T) {
	var nilPolicy *Policy
	if err := nilPolicy.PermitsURL(mustURL(t, "https://api.github.com/x")); err == nil {
		t.Error("a nil Policy must refuse: an unguarded request succeeds, so failing open is invisible")
	}
	zero := &Policy{}
	if err := zero.PermitsURL(mustURL(t, "https://api.github.com/x")); err == nil {
		t.Error("the zero Policy must refuse")
	}
}

func TestMatching(t *testing.T) {
	cases := []struct {
		name      string
		allowlist []string
		target    string
		permitted bool
		why       string
	}{
		{"exact host", []string{"ghe.corp.example.com"}, "https://ghe.corp.example.com/api/v3", true, ""},
		{"different host", []string{"ghe.corp.example.com"}, "https://evil.example.net/api/v3", false, ""},

		// Port participates. A different port on the same host is a different service, and on a shared
		// host it can be an attacker-run listener.
		{"entry without port permits 443 only", []string{"ghe.corp.example.com"}, "https://ghe.corp.example.com:8443/x", false,
			"an entry with no port must mean the scheme's default port only"},
		{"entry with port permits that port", []string{"ghe.corp.example.com:8443"}, "https://ghe.corp.example.com:8443/x", true, ""},
		{"entry with port does not permit 443", []string{"ghe.corp.example.com:8443"}, "https://ghe.corp.example.com/x", false, ""},

		// Scheme participates asymmetrically: no prefix means https only. Cleartext is asked for.
		{"no prefix refuses http", []string{"ghe.corp.example.com"}, "http://ghe.corp.example.com/x", false,
			"an entry with no scheme must permit https only; cleartext must be explicit"},
		{"http prefix permits http", []string{"http://ghe.corp.example.com"}, "http://ghe.corp.example.com/x", true, ""},
		{"http prefix does not permit https", []string{"http://ghe.corp.example.com"}, "https://ghe.corp.example.com/x", false, ""},

		// Wildcard: one or more leading labels, and NOT the bare suffix.
		{"wildcard one label", []string{"*.ghe.example.com"}, "https://a.ghe.example.com/x", true, ""},
		{"wildcard two labels", []string{"*.ghe.example.com"}, "https://a.b.ghe.example.com/x", true, ""},
		{"wildcard does not match the suffix itself", []string{"*.ghe.example.com"}, "https://ghe.example.com/x", false,
			"an operator who wants both must list both, or a wildcard silently widens"},
		{"wildcard does not match a sibling", []string{"*.ghe.example.com"}, "https://xghe.example.com/x", false,
			"suffix matching must be label-aligned, not string-aligned"},

		// Normalisation, in both directions.
		{"trailing dot on the request", []string{"ghe.corp.example.com"}, "https://ghe.corp.example.com./x", true, ""},
		{"uppercase in the request", []string{"ghe.corp.example.com"}, "https://GHE.CORP.EXAMPLE.COM/x", true, ""},
		{"uppercase in the entry", []string{"GHE.CORP.EXAMPLE.COM"}, "https://ghe.corp.example.com/x", true, ""},

		// Userinfo. buildPath can produce this shape; Hostname() discards the userinfo, a prefix check
		// would not.
		{"userinfo cannot smuggle a host", []string{"api.github.com"}, "https://api.github.com@evil.example.net/x", false,
			"the host is what follows the @; matching the raw string would permit the attacker's host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Parse(tc.allowlist, nil, false)
			if err != nil {
				t.Fatalf("Parse(%v): %v", tc.allowlist, err)
			}
			err = p.PermitsURL(mustURL(t, tc.target))
			if tc.permitted && err != nil {
				t.Errorf("want permitted, got refused: %v\n%s", err, tc.why)
			}
			if !tc.permitted && err == nil {
				t.Errorf("want refused, got permitted.\n%s", tc.why)
			}
		})
	}
}

// A bare "*" is rejected rather than honoured, so "allow everything" cannot hide as punctuation in a
// values list. It has to be spelled as its own field, where it shows up in a diff.
func TestParseRejectsDangerousEntries(t *testing.T) {
	for _, bad := range []string{"*", "", "   ", "*foo.example.com", "foo.*.example.com", "ftp://example.com", "*."} {
		if _, err := Parse([]string{bad}, nil, false); err == nil {
			t.Errorf("Parse(%q) must fail", bad)
		}
	}
	p, err := Parse(nil, nil, true)
	if err != nil {
		t.Fatalf("allowAny must parse: %v", err)
	}
	if err := p.PermitsURL(mustURL(t, "https://anything.example.net/x")); err != nil {
		t.Errorf("allowAnyHost must permit any host, since that is what the operator asked for: %v", err)
	}
}

// CHANNEL 1, at the policy layer. `buildPath` joins the host as the first element of a path.Join, so a
// `..` in a path-parameter value can promote a later segment to be the new host. Whatever URL that
// produces, the policy must refuse it -- the point being that the refusal does not depend on recognising
// the `..`, only on the destination.
func TestARelocatedHostIsRefusedWhateverProducedIt(t *testing.T) {
	p, err := Parse(nil, []string{"https://api.github.com"}, false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// The shapes measured on buildPath, plus the single-label case that resolves through a pod's DNS
	// search domain to a Service in the controller's own namespace.
	for _, relocated := range []string{
		"https://evil.example.com/repos",
		"https://collaborators/x",
		"https://evil.example.com:443/repos",
	} {
		if err := p.PermitsURL(mustURL(t, relocated)); err == nil {
			t.Errorf("%s was permitted; a relocated host must be refused regardless of how the URL was built", relocated)
		}
	}
}

// CHANNEL 3, end to end against real servers. A vendor 302 to another host must not be followed. This is
// the channel that needs NO CR write at all, and the one a URL-validation design cannot close, because
// the redirect target is never parsed by our own code.
func TestARedirectToAnUnpermittedHostIsRefused(t *testing.T) {
	var attackerHits int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attackerHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()

	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/stolen", http.StatusFound)
	}))
	defer vendor.Close()

	// Permit the vendor only, and exercise CheckRedirect WITHOUT the dial guard. Both servers are on
	// 127.0.0.1, so a policy-enforced dialer would refuse them for being loopback -- which would make
	// this case pass for a reason that has nothing to do with redirects. Using a plain client with only
	// CheckRedirect installed isolates the redirect decision from the IP decision.
	strict, err := Parse([]string{"http://" + vendorHostPort(t, vendor.URL)}, nil, false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: strict.CheckRedirect}

	req, _ := http.NewRequest(http.MethodGet, vendor.URL+"/start", nil)
	req.Header.Set("X-Api-Key", "VENDOR-SECRET")
	resp, err := client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Error("following a redirect to an unpermitted host must fail")
	} else if !strings.Contains(err.Error(), "refusing to follow a redirect") {
		t.Errorf("the error must say a redirect was refused, so an operator is not left debugging a host "+
			"they never configured: %v", err)
	}
	if got := atomic.LoadInt32(&attackerHits); got != 0 {
		t.Errorf("the attacker server was reached %d times. The credential left the controller -- which is "+
			"the whole defect, and ground truth is this counter rather than the client's error", got)
	}
}

// A permitted redirect must still be FOLLOWED. #132 is a renamed vendor repository served as a 307, and
// refusing to follow it leaves the CR permanently in ReconcileError -- so a guard that blocks every
// redirect trades one defect for another.
func TestARedirectWithinThePermittedHostIsFollowed(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			http.Redirect(w, r, "/moved", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p, err := Parse([]string{"http://" + vendorHostPort(t, srv.URL)}, nil, false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: p.CheckRedirect}
	resp, err := client.Get(srv.URL + "/start")
	if err != nil {
		t.Fatalf("a same-host redirect must be followed, or #132 regresses: %v", err)
	}
	resp.Body.Close()
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("hits = %d, want 2 (the redirect and the target)", got)
	}
}

// The unconditional denies no allowlist entry can override. RFC1918 is deliberately absent: an on-prem
// GHES is routinely private, and refusing private space would make the feature useless for its purpose.
func TestDenyIP(t *testing.T) {
	denied := []string{"127.0.0.1", "::1", "169.254.169.254", "fe80::1", "0.0.0.0", "::", "224.0.0.1",
		"::ffff:127.0.0.1"} // the v4-mapped form must not slip past the v4 checks
	for _, s := range denied {
		if err := denyIP(mustIP(t, s)); err == nil {
			t.Errorf("%s must be denied", s)
		}
	}
	// CONTROL: these must be ALLOWED, or the test above would pass with a function that denies everything.
	for _, s := range []string{"140.82.121.4", "10.1.2.3", "192.168.1.10", "2606:4700::1"} {
		if err := denyIP(mustIP(t, s)); err != nil {
			t.Errorf("%s must be allowed (RFC1918 is deliberately reachable so an on-prem GHES works): %v", s, err)
		}
	}
}

// Even with allowAnyHost, the dial-time denies still apply -- otherwise "allow any host" would hand the
// credential to the cloud-metadata endpoint.
func TestAllowAnyHostStillCannotReachLinkLocal(t *testing.T) {
	if err := denyIP(mustIP(t, "169.254.169.254")); err == nil {
		t.Error("link-local must be denied at the dial regardless of the host list")
	}
}

func mustIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("test fixture %q is not an IP", s)
	}
	return ip
}

func vendorHostPort(t *testing.T, serverURL string) string {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("server URL %q: %v", serverURL, err)
	}
	return u.Host
}
