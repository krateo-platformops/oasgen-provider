package restclient

import (
	"testing"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/hostpolicy"
)

// #379 part 2 made HostPolicy required on UnstructuredClient, and a nil one REFUSES rather than permits --
// an unguarded credentialed request succeeds, so failing open would be invisible. These helpers give a
// test the narrowest policy that lets its own server through, and nothing wider.
//
// Deliberately NOT a package-level default or a global switch. Each test states which host it permits, so
// a test cannot start passing because some other test widened the policy -- and the production invariant
// stays visible in the test: the permitted set comes from the same place `Server` does.

// allowedPolicy returns a policy permitting rawURL and nothing else.
//
// The loopback seam is enabled because httptest binds 127.0.0.1, which the production guard refuses
// unconditionally and must keep refusing. See Policy.AllowLoopbackForTesting.
func allowedPolicy(t *testing.T, rawURL string) *hostpolicy.Policy {
	t.Helper()
	p, _, err := hostpolicy.Parse(nil, []string{rawURL}, false)
	if err != nil {
		t.Fatalf("building a test host policy for %q: %v", rawURL, err)
	}
	return p.AllowLoopbackForTesting()
}

// allowServer points the client's policy at its own Server, which is what BuildClient does in production
// from the document's servers[0].
func allowServer(t *testing.T, u *UnstructuredClient) {
	t.Helper()
	// The SAME collection production uses, not a reimplementation: base servers[0] plus every
	// per-operation override. A helper that permitted only u.Server failed a case exercising an override,
	// which is the cheap direction for that drift to go -- the expensive one is a suite that passes while
	// production refuses everything.
	// BOTH: the document's hosts (the shared production rule, which covers per-operation overrides) AND
	// the client's current Server. In production those are the same thing -- Server comes from the
	// document -- but a test overrides Server to point at its httptest listener after construction, so
	// using only the document's hosts refuses the very server the test just started. Using only Server
	// misses per-operation overrides. Both, or a third of the cases fail either way.
	hosts := DocumentHosts(u.DocScheme)
	if u.Server != "" {
		hosts = append(hosts, u.Server)
	}
	if len(hosts) == 0 {
		t.Fatal("no host to permit: the client has neither a document nor a Server, so this helper would " +
			"build a policy that refuses everything and the test would fail for the wrong reason")
	}
	p, _, err := hostpolicy.Parse(nil, hosts, false)
	if err != nil {
		t.Fatalf("building a test host policy for %v: %v", hosts, err)
	}
	u.HostPolicy = p.AllowLoopbackForTesting()
}
