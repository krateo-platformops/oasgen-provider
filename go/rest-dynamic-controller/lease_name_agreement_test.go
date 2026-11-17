//go:build unit

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// THE ONE INVARIANT THIS CHANGE CANNOT SURVIVE BREAKING.
//
// A generated controller computes its Lease name in Go (generatedControllerLeaseName) and is PERMITTED to
// touch that Lease by a resourceNames entry in a YAML template. If the two ever disagree, every generated
// controller fails to acquire and controller-runtime EXITS -- N kinds, not one provider -- and nothing
// else in this repository would notice: the Go side has no idea what the RBAC says, and the chart has no
// idea what the Go does.
//
// So this reads the TEMPLATE'S OWN TEXT and derives the expected shape from it, rather than restating the
// convention in a string literal here. A test that hardcodes "resource-group-version" would pass after
// someone reorders the template, which is the failure it exists to catch.
func TestTheLeaseNameMatchesTheGeneratedRole(t *testing.T) {
	// The Role lives in the chart, outside this module. Not found is a FAILURE and never a skip: a skip
	// would make this test unable to fail, which is worse than not having it.
	path := filepath.Join("..", "..", "helm", "oasgen-provider", "assets", "rdc", "rbac", "role.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the generated Role template at %s: %v\n"+
			"This test must read the template rather than a copy of its convention, so it cannot run "+
			"without it.", path, err)
	}
	text := string(raw)

	// Stage 1 is helm's: `{{`{{ .resource }}`}}` renders to `{{ .resource }}`. Undo that escaping so what
	// remains is the template the PROVIDER renders at runtime.
	provider := strings.NewReplacer("{{`", "", "`}}", "").Replace(text)

	// The grant's resourceNames entry, and the Role's own metadata.name. Both must be present and equal:
	// the grant permitting a DIFFERENT name than the Role is called would be its own defect.
	grant := regexp.MustCompile(`(?m)^\s*resourceNames:\s*\n\s*-\s*(\S.*?)\s*$`).FindStringSubmatch(provider)
	if grant == nil {
		t.Fatal("no `resourceNames:` entry in the generated Role. The Lease grant must be pinned to this " +
			"controller's own Lease: a blanket leases grant would let any generated controller take any " +
			"other's, which is the opposite of what issue 771 is for.")
	}
	name := regexp.MustCompile(`(?m)^\s*name:\s*(\S.*?)\s*$`).FindStringSubmatch(provider)
	if name == nil {
		t.Fatal("no `name:` in the generated Role's metadata")
	}
	if grant[1] != name[1] {
		t.Fatalf("the Lease grant and the Role's own name differ:\n  grant: %s\n  name:  %s\n"+
			"They are the same convention and must be the same string.", grant[1], name[1])
	}

	// Substitute sentinels for the three provider-side values, so the comparison is about ORDER and
	// SEPARATORS rather than about any real resource.
	expected := strings.NewReplacer(
		"{{ .resource }}", "RES",
		"{{ .apiGroup }}", "GRP",
		"{{ .apiVersion }}", "VER",
	).Replace(grant[1])

	if strings.Contains(expected, "{{") {
		t.Fatalf("the Lease grant carries a template action this test does not know how to substitute: %q\n"+
			"Teach it the new action rather than loosening the comparison -- an unsubstituted action would "+
			"make every comparison below pass vacuously.", expected)
	}

	got := generatedControllerLeaseName("RES", "GRP", "VER")
	if got != expected {
		t.Errorf("the Go Lease name and the Role's grant disagree:\n  go:   %s\n  role: %s\n"+
			"Every generated controller would fail to acquire its Lease and exit.", got, expected)
	}
}

// THE CONTROL FOR THE TEST ABOVE: it must FAIL when the two sides disagree. Without this, a substitution
// that silently produced an empty string on both sides would pass and prove nothing.
func TestTheAgreementCheckCanFail(t *testing.T) {
	wrongOrder := strings.NewReplacer(
		"{{ .resource }}", "RES", "{{ .apiGroup }}", "GRP", "{{ .apiVersion }}", "VER",
	).Replace("{{ .apiVersion }}-{{ .apiGroup }}-{{ .resource }}")

	if got := generatedControllerLeaseName("RES", "GRP", "VER"); got == wrongOrder {
		t.Fatalf("the Go name equals a REVERSED template order (%s), so the comparison in "+
			"TestTheLeaseNameMatchesTheGeneratedRole cannot distinguish right from wrong", wrongOrder)
	}

	if generatedControllerLeaseName("", "", "") == generatedControllerLeaseName("RES", "GRP", "VER") {
		t.Fatal("the name function ignores its arguments, so the agreement test would pass on anything")
	}
}
