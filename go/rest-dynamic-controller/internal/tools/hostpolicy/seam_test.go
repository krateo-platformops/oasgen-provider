package hostpolicy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// THE GUARD. AllowLoopbackForTesting relaxes an SSRF refusal, so the one thing that must stay true is
// that no production code calls it.
//
// This is a test rather than a compile-time barrier because Go has none to offer across packages: the
// method has to be exported for tests in other packages to reach it, and an exported method is callable
// from anywhere. So the property is asserted here, in CI, in the same shape as the ExistenceChecker
// assertion in exists.go -- a property of the codebase that would otherwise drift silently.
//
// The control is what makes a zero meaningful: without proving the scan finds callers of SOMETHING, a
// zero is indistinguishable from a scan that walked the wrong tree or matched nothing by construction.
func TestAllowLoopbackForTestingHasNoProductionCaller(t *testing.T) {
	root := moduleRoot(t)

	var prodHits, testHits, controlHits int
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		text := string(b)
		isTest := strings.HasSuffix(path, "_test.go")

		// The declaration itself lives in a non-test file and is not a call.
		callCount := strings.Count(text, "AllowLoopbackForTesting()") -
			strings.Count(text, "func (p *Policy) AllowLoopbackForTesting()")
		if callCount > 0 {
			if isTest {
				testHits += callCount
			} else {
				prodHits += callCount
				t.Errorf("AllowLoopbackForTesting is called from a NON-TEST file: %s. That relaxes the "+
					"loopback refusal in production, which makes 127.0.0.1 -- and anything resolving "+
					"there, including a Service in the controller's own namespace -- reachable by a "+
					"credentialed request", path)
			}
		}
		// CONTROL: a method that IS called from non-test code, so a zero above is a measurement.
		controlHits += strings.Count(text, "PermitsURL(")
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	if controlHits == 0 {
		t.Fatalf("CONTROL FAILED: zero PermitsURL( occurrences under %s, so this scan could not have "+
			"found a caller of anything. The zero above proves nothing", root)
	}
	if testHits == 0 {
		t.Errorf("no TEST calls AllowLoopbackForTesting either (control: %d PermitsURL hits). The seam "+
			"exists so request-making tests can use a policy-enforced client; if nothing uses it, it is "+
			"dead code relaxing a security refusal for no benefit", controlHits)
	}
	t.Logf("AllowLoopbackForTesting: %d production callers, %d test callers (control: %d PermitsURL hits)",
		prodHits, testHits, controlHits)
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, serr := os.Stat(filepath.Join(dir, "go.mod")); serr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find the module root: the scan would have walked the wrong tree, and a zero from " +
		"the wrong tree looks exactly like a true zero")
	return ""
}

// WHAT THE SEAM BUYS: a request-making test can now use a policy-enforced client against httptest, which
// binds 127.0.0.1. Without it, every such test is refused before the handler ever runs -- so the guard
// would have shipped with its request path untested, which is how an unguarded request got here.
func TestAPolicyEnforcedClientReachesAnHTTPTestServerWithTheSeam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("reached"))
	}))
	defer srv.Close()

	host := hostOf(t, srv.URL)
	p, _, err := Parse([]string{"http://" + host}, nil, false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p.AllowLoopbackForTesting()

	resp, err := p.Client(5 * time.Second).Get(srv.URL + "/thing")
	if err != nil {
		t.Fatalf("a policy-enforced client with the seam must reach an httptest server: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// AND THE PREMISE: without the seam the same request MUST be refused. If it succeeded, the seam would be
// doing nothing and the case above would pass for the wrong reason.
func TestTheSameRequestIsRefusedWithoutTheSeam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	p, _, err := Parse([]string{"http://" + hostOf(t, srv.URL)}, nil, false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// No AllowLoopbackForTesting.
	resp, err := p.Client(5 * time.Second).Get(srv.URL + "/thing")
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("without the seam a loopback destination must be refused; if this passes, the loopback " +
			"refusal is not reached and the production guarantee is not what the package claims")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Errorf("the refusal must name loopback so an operator is not left guessing: %v", err)
	}
}

// THE SEAM RELAXES LOOPBACK ONLY. A test must not be able to make the cloud-metadata endpoint reachable
// by reaching for the same switch.
func TestTheSeamDoesNotRelaxLinkLocalOrMulticast(t *testing.T) {
	for _, s := range []string{"169.254.169.254", "fe80::1", "224.0.0.1", "0.0.0.0", "::"} {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test fixture %q is not an IP", s)
		}
		if err := denyIPWithLoopback(ip, true); err == nil {
			t.Errorf("%s was permitted with the seam on. The seam must relax loopback and nothing else: "+
				"169.254.169.254 is the cloud-metadata endpoint", s)
		}
	}
	// CONTROL: loopback IS relaxed, so the cases above are not passing because the seam does nothing.
	if err := denyIPWithLoopback(net.ParseIP("127.0.0.1"), true); err != nil {
		t.Errorf("CONTROL FAILED: loopback must be relaxed with the seam on, or this case proves nothing "+
			"about what the seam does and does not cover: %v", err)
	}
	// And still refused with it off.
	if err := denyIPWithLoopback(net.ParseIP("127.0.0.1"), false); err == nil {
		t.Error("loopback must be refused with the seam off")
	}
}

// TLS verification against the HOSTNAME is still NOT asserted here, and this records why rather than
// leaving a gap someone has to rediscover.
//
// The seam fixes the loopback problem. It does not fix the RESOLUTION problem: to prove verification uses
// the request URL's host rather than the dialled address, the test needs a certificate naming a HOSTNAME
// and a request to that hostname resolving to a permitted address. DialContext resolves through
// net.DefaultResolver, which is not injectable, and a test cannot add a name to the system resolver.
//
// So the options are a second seam (an injectable resolver) or an out-of-tree probe. It was verified
// out of tree -- an HTTPS server on a real RFC1918 address, SNI read from the server's own
// GetCertificate callback as "vendor.probe.example" while the socket went to 192.168.1.11, leaf with no
// IP SAN, and a negative control rejecting a certificate for a different name.
//
// This is a placeholder-free statement of a known gap: there is no skipped test here pretending to cover
// it, because a skipped test and a passing test are indistinguishable in a green run.
func TestTLSHostnameVerificationIsNotCoveredHere(t *testing.T) {
	t.Log("TLS-against-hostname is verified out of tree only; closing it in-package needs an injectable " +
		"resolver, which is a separate decision for #379 part 2")
}

func hostOf(t *testing.T, raw string) string {
	t.Helper()
	i := strings.Index(raw, "://")
	if i < 0 {
		t.Fatalf("server URL %q has no scheme", raw)
	}
	return raw[i+3:]
}

// THE RESOLVER BRANCH, which the case above does NOT reach.
//
// Found by mutation: ignoring the seam in DialContext's resolved-address loop left
// TestAPolicyEnforcedClientReachesAnHTTPTestServerWithTheSeam PASSING. The reason is that an httptest
// URL is `http://127.0.0.1:port` -- a LITERAL IP -- so DialContext takes its `net.ParseIP(host) != nil`
// shortcut and never resolves anything. The resolver loop, where a hostname's addresses are checked one
// by one, was untested.
//
// A hostname that resolves to loopback is the realistic shape for a dev install or a port-forward, so
// this is the branch most likely to matter in practice. Requesting `localhost` instead of the literal IP
// forces it.
func TestTheSeamAppliesOnTheResolverPathToo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, port, err := net.SplitHostPort(hostOf(t, srv.URL))
	if err != nil {
		t.Fatalf("splitting %q: %v", srv.URL, err)
	}
	// THE PREMISE: localhost must resolve, and must resolve to loopback. If it does not, this case is
	// exercising something other than the branch it names.
	addrs, lerr := net.DefaultResolver.LookupIPAddr(t.Context(), "localhost")
	if lerr != nil || len(addrs) == 0 {
		t.Skipf("localhost does not resolve here (%v), so this case cannot exercise the resolver branch "+
			"-- skipping rather than passing vacuously", lerr)
	}
	for _, a := range addrs {
		if !a.IP.IsLoopback() {
			t.Fatalf("localhost resolves to %s, which is not loopback: this case would then be testing "+
				"an ordinary permitted address rather than the seam", a.IP)
		}
	}

	target := "http://localhost:" + port
	p, _, err := Parse([]string{"http://localhost:" + port}, nil, false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p.AllowLoopbackForTesting()

	resp, err := p.Client(5 * time.Second).Get(target + "/thing")
	if err != nil {
		t.Fatalf("a hostname resolving to loopback must be reachable with the seam on -- this is the "+
			"resolver branch of DialContext, not the literal-IP shortcut: %v", err)
	}
	defer resp.Body.Close()

	// And refused without it, or the case above proves nothing about the seam.
	strict, _, err := Parse([]string{"http://localhost:" + port}, nil, false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	resp2, err := strict.Client(5 * time.Second).Get(target + "/thing")
	if resp2 != nil {
		resp2.Body.Close()
	}
	if err == nil {
		t.Error("without the seam, a hostname resolving to loopback must be refused on the resolver path")
	} else if !strings.Contains(err.Error(), "loopback") {
		t.Errorf("the refusal must name loopback: %v", err)
	}
}
