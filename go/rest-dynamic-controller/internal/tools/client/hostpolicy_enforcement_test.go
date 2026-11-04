package restclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/hostpolicy"
)

// #379 part 2: the policy has to be reached from the request path, not merely carried on the client.
// Part 1 shipped the policy with nothing calling it, so "the package exists and is tested" was true while
// every request was still unguarded.

// A host the document does not name and the operator did not allow must be REFUSED, and the error must
// name the host rather than surfacing as a transport failure an operator would read as the vendor's fault.
func TestCallRefusesAHostTheDocumentDoesNotName(t *testing.T) {
	var attackerHits int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attackerHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()

	client := createTestClient(t) // document server is https://example.com; allowServer permits that only
	client.Server = attacker.URL  // deliberately WITHOUT re-running allowServer

	_, err := client.Call(context.Background(), &http.Client{}, "/api/test", &RequestConfiguration{Method: "GET"})
	if err == nil {
		t.Fatal("a request to a host neither the document nor the allowlist names must be refused")
	}
	if !strings.Contains(err.Error(), "refusing to send a credentialed request") {
		t.Errorf("the error must say the request was refused and why, not look like a transport failure "+
			"an operator would read as the vendor's fault: %v", err)
	}
	// GROUND TRUTH. The client's own error is not the measurement -- whether the credential left the
	// process is. This counter is the only thing that can tell a refusal from a failed connection.
	if got := atomic.LoadInt32(&attackerHits); got != 0 {
		t.Errorf("the unpermitted host was reached %d times: the request left the process, which is the "+
			"whole defect", got)
	}
}

// THE REGRESSION TEST FOR THE HOLE THIS REFACTOR CLOSED.
//
// The debug round-tripper used to wrap the CALLER's transport and assign it back onto the caller's
// client. That made cli.Transport non-nil, so the request went out on it instead of the policy's -- and
// enabling krateo.io/connector-verbose silently bypassed the guard. In the one configuration an operator
// turns on when something is already wrong.
//
// Found by the production-Transport guard test rather than by review, and this pins it.
func TestTheGuardStillAppliesWithDebugEnabled(t *testing.T) {
	var attackerHits int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attackerHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()

	client := createTestClient(t)
	client.Server = attacker.URL
	client.Debug = true // the configuration that used to disable the guard
	client.PrettyJSON = true

	_, err := client.Call(context.Background(), &http.Client{}, "/api/test", &RequestConfiguration{Method: "GET"})
	if err == nil {
		t.Fatal("with Debug on, an unpermitted host must STILL be refused")
	}
	if !strings.Contains(err.Error(), "refusing to send a credentialed request") {
		t.Errorf("Debug must not change which error comes back: %v", err)
	}
	if got := atomic.LoadInt32(&attackerHits); got != 0 {
		t.Errorf("with Debug on, the unpermitted host was reached %d times -- verbose mode is bypassing "+
			"the guard, which is exactly the hole this test exists for", got)
	}
}

// And the permitted case must still WORK with Debug on, or the test above would pass with a guard that
// refuses everything whenever debugging is enabled.
func TestAPermittedHostStillWorksWithDebugEnabled(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	client := createTestClient(t)
	client.Server = srv.URL
	allowServer(t, client)
	client.Debug = true

	_, err := client.Call(context.Background(), &http.Client{}, "/api/test", &RequestConfiguration{Method: "GET"})
	// The document may not declare /api/test, so an error about the path is acceptable here; what is not
	// acceptable is the policy refusing a permitted host.
	if err != nil && strings.Contains(err.Error(), "refusing to send a credentialed request") {
		t.Fatalf("Debug must not cause a PERMITTED host to be refused: %v", err)
	}
	_ = hits
}

// DocumentHosts is the rule BuildClient and the test helpers share. If it ever stopped collecting
// per-operation overrides, production would refuse a request the document explicitly permits -- and the
// only existing signal was one table case in another file.
func TestDocumentHostsCollectsPerOperationOverrides(t *testing.T) {
	client := createTestClient(t)
	hosts := DocumentHosts(client.DocScheme)

	// The fixture declares base `http://api.example.com/v1` and a per-operation override
	// `http://override.example.com`. Asserting BOTH is the point: the base alone would pass with a
	// collection that ignores overrides, which is the version that turns this guard from a no-op into an
	// outage for any document that uses one.
	var sawBase, sawOverride bool
	for _, h := range hosts {
		if strings.Contains(h, "api.example.com") {
			sawBase = true
		}
		if strings.Contains(h, "override.example.com") {
			sawOverride = true
		}
	}
	if !sawBase {
		t.Errorf("the base servers[0] is missing from %v", hosts)
	}
	if !sawOverride {
		t.Errorf("the PER-OPERATION servers[0] override is missing from %v. Production would then refuse "+
			"a host the document explicitly names, for every document that uses one", hosts)
	}

	p, _, err := hostpolicy.Parse(nil, hosts, false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, permitted := range []string{"http://api.example.com/v1/x", "http://override.example.com/api/override"} {
		req, _ := http.NewRequest(http.MethodGet, permitted, nil)
		if perr := p.PermitsURL(req.URL); perr != nil {
			t.Errorf("a policy built from the document's own hosts must permit %s: %v", permitted, perr)
		}
	}
	// CONTROL: a host the document does NOT name must still be refused, or the assertions above would
	// pass with a policy that permits everything.
	req, _ := http.NewRequest(http.MethodGet, "http://not-in-the-document.example.net/x", nil)
	if perr := p.PermitsURL(req.URL); perr == nil {
		t.Error("CONTROL FAILED: a host the document does not name was permitted, so the assertions above " +
			"prove nothing about what the collection contains")
	}

	// And the hardcoded Server in createTestClient is NOT the document's server -- which is why the test
	// helper has to permit both. Recorded because asserting against client.Server here is the mistake
	// this case was first written with.
	if strings.Contains(client.Server, "api.example.com") {
		t.Errorf("createTestClient's Server (%q) now matches the document's; this case's note about the "+
			"helper needing both is stale", client.Server)
	}
}
