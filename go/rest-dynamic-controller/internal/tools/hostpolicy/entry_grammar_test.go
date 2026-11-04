package hostpolicy

import (
	"net/url"
	"strings"
	"testing"
)

// #402: two entry forms parsed into something other than what they visibly said. Neither widened the
// permitted set -- each yielded one narrower than written, or no policy at all -- so neither was a
// security issue. Both produced a failure that pointed away from its cause.
//
// THE ASSERTIONS ARE ON MATCH BEHAVIOUR, not on "Parse returned no error". `example.com/*` parsed
// successfully before this fix, so a test shaped that way passes on the broken behaviour.
func TestAnOperatorEntryWithAPathIsRefusedRatherThanReinterpreted(t *testing.T) {
	// The form that made this worth fixing: the `*` sat in the discarded path, so the checks that exist
	// to reject a misplaced `*` never saw it, and the entry became an EXACT match for example.com.
	_, _, err := Parse([]string{"example.com/*"}, nil, false)
	if err == nil {
		t.Fatal("`example.com/*` was accepted. Before #402 it parsed into an exact entry for " +
			"example.com, so sub.example.com was refused while the values file visibly contained " +
			"something that looks like it should match")
	}
	for _, want := range []string{"a path plays no part", `"example.com"`, `"*.example.com"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q. A refusal that does not say what to write "+
				"instead is barely better than the silent reinterpretation it replaces", err.Error(), want)
		}
	}

	// A bare trailing slash is not a path and must still be accepted, or every operator who copies a
	// hostname with a trailing slash gets a startup failure for nothing.
	if _, _, err := Parse([]string{"example.com/"}, nil, false); err != nil {
		t.Errorf("`example.com/` must be accepted: a bare trailing slash carries no path: %v", err)
	}
}

// THE INTERACTION THAT MAKES THE FIX SAFE, and the reason the rule is not unconditional.
//
// parseEntry serves BOTH of Parse's inputs. A DOCUMENT server URL legitimately carries a path -- GHES
// serves its REST API under /api/v3 -- so applying the operator rule to document hosts would DISCARD
// every path-bearing servers[0], and with #403 in place it would discard them loudly: the controller
// would report its own document's server as unusable and then refuse every request to it.
//
// That would have been a far worse defect than the one #402 fixes, introduced by fixing it.
func TestADocumentServerURLWithAPathIsStillPermitted(t *testing.T) {
	p, discarded, err := Parse(nil, []string{"https://ghe.corp.example.com/api/v3"}, false)
	if err != nil {
		t.Fatalf("a document server URL with a path must parse: %v", err)
	}
	if len(discarded) != 0 {
		t.Fatalf("the document's own server URL was DISCARDED: %+v. A GHES document declares its API "+
			"under /api/v3, so this would refuse every request the controller legitimately makes",
			discarded)
	}
	u, _ := url.Parse("https://ghe.corp.example.com/api/v3/repos/o/r")
	if perr := p.PermitsURL(u); perr != nil {
		t.Errorf("the document's own host is not permitted: %v", perr)
	}
}

// The scheme prefix test was a byte-exact lowercase HasPrefix while the host was lowercased later, so
// case was handled for the host and not for the scheme within one entry. `HTTPS://Example.COM` fell
// through to "unsupported scheme: only http:// and https:// are permitted" -- confusing precisely
// because the operator had written one of them.
func TestAnEntryIsCaseInsensitiveInTheSchemeAsWellAsTheHost(t *testing.T) {
	for _, raw := range []string{"HTTPS://Example.COM", "HTTP://Example.COM", "Example.COM", "https://example.com"} {
		p, _, err := Parse([]string{raw}, nil, false)
		if err != nil {
			t.Errorf("Parse(%q) failed: %v", raw, err)
			continue
		}
		scheme := "https"
		if strings.HasPrefix(strings.ToLower(raw), "http://") {
			scheme = "http"
		}
		u, _ := url.Parse(scheme + "://example.com/x")
		if perr := p.PermitsURL(u); perr != nil {
			t.Errorf("entry %q does not permit %s://example.com: %v", raw, scheme, perr)
		}
	}

	// And a genuinely unsupported scheme must still be refused, in any case, or the lowercasing has
	// widened what parses instead of only normalising it.
	for _, raw := range []string{"FTP://example.com", "ftp://example.com", "Gopher://example.com"} {
		if _, _, err := Parse([]string{raw}, nil, false); err == nil {
			t.Errorf("Parse(%q) was accepted; only http:// and https:// are permitted", raw)
		}
	}
}

// A wildcard with a path is the same operator mistake and must be refused the same way -- and the
// suggestion must not end up as `*.*.example.com`.
func TestAWildcardEntryWithAPathIsRefusedWithASensibleSuggestion(t *testing.T) {
	_, _, err := Parse([]string{"*.example.com/api"}, nil, false)
	if err == nil {
		t.Fatal("`*.example.com/api` was accepted")
	}
	if strings.Contains(err.Error(), "*.*.") {
		t.Errorf("the suggested entry is malformed: %q", err.Error())
	}
	if !strings.Contains(err.Error(), `"*.example.com"`) {
		t.Errorf("error = %q, want it to suggest `*.example.com`", err.Error())
	}
}
