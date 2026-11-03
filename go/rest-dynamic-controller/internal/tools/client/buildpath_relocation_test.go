package restclient

import (
	"net/url"
	"testing"

	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/hostpolicy"
)

// WHICH DOCUMENT SHAPES LET A PATH PARAMETER MOVE THE REQUEST HOST.
//
// This is channel 1 of #379: buildPath substitutes a path parameter's VALUE into the path and then joins
// the result onto the host with pathutil.Join, which Cleans. A `..` therefore eats a segment, and enough
// of them eat the host itself, so a CR field can choose the destination server for a credential the CR's
// author does not hold. The policy at restclient.do refuses the resulting request, which is the fix.
//
// THIS TEST EXISTS BECAUSE THE FIX IS NOT THE WHOLE ANSWER CONSUMERS NEED. Five consumers had to decide
// whether they had been exposed before the fix shipped, and I told one of them -- whose 123 paths all
// begin with a FIXED segment -- that the channel was "live for you". It was not, and they falsified it
// with a measurement I should have made first. The condition is structural and cheap to state, so it is
// stated here as executable fact rather than left in a release note where the next person has to re-derive
// it. The error cost nothing only because that consumer refused to act on an unmeasured claim.
//
// THE CONDITION, which the cases below pin in both directions:
//
//	the host moves  <=>  there are at least as many settable `..` parameters strictly to the LEFT of the
//	                     substitution point as there are path segments to its left, COUNTING the host and
//	                     every segment the base URL's own path contributes.
//
// Two consequences that are not obvious from that sentence and are each pinned by a case:
//
//   - one parameter is ALWAYS exactly one segment. url.PathEscape leaves `.` alone but escapes `/`, so a
//     value of "../.." arrives as "..%2F.." and eats nothing. A single parameter can never close a
//     two-segment gap, however it is spelled.
//   - a base URL that contributes a path prefix ABSORBS one `..` per segment, so it makes relocation
//     harder, not easier. https://host/v1 is strictly safer here than https://host. This is the opposite
//     of the intuition, and it matters for the non-default base URL work (#374): a GHES base of
//     https://host/api/v3 adds two absorbing segments.
func TestWhichShapesAllowAPathParameterToMoveTheHost(t *testing.T) {
	const safeBase = "https://api.example.com"
	const prefixedBase = "https://api.example.com/v1"

	cases := []struct {
		name string
		base string
		path string
		// params is the attacker-chosen substitution.
		params map[string]string
		// wantHost is the host the request actually goes to. Asserting the HOST rather than "did it
		// change" is deliberate: "changed" would pass for a relocation to a different wrong host, and the
		// identity of the destination is the whole question.
		wantHost string
	}{
		// --- shapes that CANNOT be relocated: the first path segment is fixed ---
		{
			// The shape five consumers' documents actually have. One `..` eats the fixed segment and
			// stops; there is no settable parameter to its left to eat the host.
			name:     "fixed first segment, one parameter: unreachable",
			base:     safeBase,
			path:     "/folders/{id}",
			params:   map[string]string{"id": ".."},
			wantHost: "api.example.com",
		},
		{
			// The spelling that looks like it should work and cannot. PathEscape escapes the separator.
			name:     "a parameter cannot supply its own separators",
			base:     safeBase,
			path:     "/folders/{id}",
			params:   map[string]string{"id": "../../evil.com"},
			wantHost: "api.example.com",
		},
		{
			// Two parameters, but the leftmost is still not at index 0, so the fixed segment absorbs one.
			name:     "fixed first segment, two parameters: still unreachable",
			base:     safeBase,
			path:     "/projects/{pid}/folders/{id}",
			params:   map[string]string{"pid": "..", "id": ".."},
			wantHost: "api.example.com",
		},

		// --- shapes that CAN be relocated ---
		{
			// The weak variant: the host becomes the next FIXED segment. Not attacker-chosen, but in a
			// cluster with a search domain "users" is a name that may resolve, so it is a channel.
			name:     "leading parameter: host becomes the next fixed segment",
			base:     safeBase,
			path:     "/{a}/users",
			params:   map[string]string{"a": ".."},
			wantHost: "users",
		},
		{
			// The full variant: one parameter eats the host, the next supplies the replacement. This is
			// the shape the credential must never be sent to.
			name:     "leading parameter plus a second: attacker chooses the host",
			base:     safeBase,
			path:     "/{a}/{b}",
			params:   map[string]string{"a": "..", "b": "evil.example.net"},
			wantHost: "evil.example.net",
		},
		{
			name:     "relocation survives a trailing fixed segment",
			base:     safeBase,
			path:     "/{a}/{b}/x",
			params:   map[string]string{"a": "..", "b": "evil.example.net"},
			wantHost: "evil.example.net",
		},

		// --- the base URL's own path absorbs one `..` per segment ---
		{
			// The SAME path and parameters as the attacker-chosen case above, made safe purely by the
			// base URL contributing one segment. This is the case that reverses the intuition.
			name:     "a one-segment base path absorbs the relocation",
			base:     prefixedBase,
			path:     "/{a}/{b}",
			params:   map[string]string{"a": "..", "b": "evil.example.net"},
			wantHost: "api.example.com",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := buildPath(tc.base, tc.path, tc.params, nil)
			if u == nil {
				t.Fatalf("buildPath returned nil for %q + %q; a nil here would make the case vacuous "+
					"rather than passing", tc.base, tc.path)
			}
			if got := u.Hostname(); got != tc.wantHost {
				t.Errorf("host = %q, want %q (url %q).\n"+
					"If a shape listed here as unreachable now relocates, channel 1 of #379 has widened "+
					"and the consumers who audited themselves as safe on the fixed-first-segment rule "+
					"are no longer safe -- they were told that rule explicitly.",
					got, tc.wantHost, u.String())
			}
		})
	}
}

// The policy must refuse exactly the relocated requests the test above produces, and permit the others.
// Without this, the pair above is a description of a vulnerability with no assertion that anything stops
// it -- two good halves, never compared.
func TestTheRelocatedRequestsAreTheOnesThePolicyRefuses(t *testing.T) {
	// ASK THE POLICY, not a reimplementation of it. A closure comparing u.Hostname() to the expected
	// string would pass whatever the real Parse/PermitsURL did, which makes the assertion about this test
	// rather than about the shipped guard -- the failure mode where a rule and the artifact it produces
	// are each verified alone and never against each other.
	//
	// Built the way BuildClient builds it with no operator configuration: empty allowlist, the document's
	// own host as the derived set, allowAny false.
	policy, err := hostpolicy.Parse(nil, []string{"https://api.example.com"}, false)
	if err != nil {
		t.Fatalf("the document-derived policy must build: %v", err)
	}
	permitted := func(raw string) bool {
		u, perr := url.Parse(raw)
		if perr != nil {
			t.Fatalf("test URL %q does not parse: %v", raw, perr)
		}
		return policy.PermitsURL(u) == nil
	}

	relocated := buildPath("https://api.example.com", "/{a}/{b}",
		map[string]string{"a": "..", "b": "evil.example.net"}, nil)
	legitimate := buildPath("https://api.example.com", "/folders/{id}",
		map[string]string{"id": "abc-123"}, nil)

	if relocated == nil || legitimate == nil {
		t.Fatal("buildPath returned nil; the comparison below would be vacuous")
	}
	if permitted(relocated.String()) {
		t.Errorf("the relocated request %q is inside the document-derived permitted set, so the policy "+
			"would not refuse it", relocated.String())
	}
	if !permitted(legitimate.String()) {
		t.Errorf("the legitimate request %q is OUTSIDE the document-derived permitted set. A guard that "+
			"refuses the ordinary call is not a safer guard, it is a broken controller -- and it would "+
			"look like a pass to anyone checking only that the attack was refused", legitimate.String())
	}
}
