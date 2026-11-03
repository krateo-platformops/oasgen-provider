package restclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `do` preserves a caller-supplied Transport, because a custom Transport means a mock RoundTripper that
// never dials -- and guarding a request that never leaves the process is pointless while breaking a
// legitimate test technique.
//
// That is only safe while NO PRODUCTION SITE supplies one. A production Transport would keep the URL
// check (PermitsURL still runs) and lose the resolved-address check and the per-hop redirect check -- so
// rebinding would stop being caught. This asserts the premise rather than trusting it, in the same shape
// as the AllowLoopbackForTesting guard in hostpolicy.
func TestNoProductionSiteSuppliesAnHTTPTransport(t *testing.T) {
	// SCOPED TO internal/controllers ON PURPOSE. The 14 client construction sites all live there, and they
	// are the ones whose Transport would reach `do`. An earlier version of this test scanned the whole
	// module for "Transport:" and flagged restclient.go -- which is the file that INSTALLS the policy
	// transport, not one that bypasses it. A predicate that cannot tell the guard from a breach of it is
	// not a guard.
	root := moduleRootFor(t)
	dir := filepath.Join(root, "internal", "controllers")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("%s is not readable, so a zero from this scan would mean nothing: %v", dir, err)
	}

	var prod, control int
	err := filepath.Walk(dir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return werr
		}
		if strings.HasSuffix(path, "_test.go") || strings.Contains(path, "mockserver") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		text := string(b)
		// CONTROL: these files demonstrably construct http.Clients, so a zero below is a measurement.
		control += strings.Count(text, "http.Client{")
		// `Transport:` -- the struct-field assignment -- not the bare word. This predicate has now been
		// wrong twice: module-wide it flagged restclient.go, which INSTALLS the policy transport, and
		// scoped-but-bare it flagged exists.go for the comment "Transport failure, 5xx, timeout". A guard
		// that cries wolf gets disabled by the third person who sees it fail, so the predicate has to be
		// as precise as the property.
		if n := strings.Count(text, "Transport:"); n > 0 {
			prod += n
			t.Errorf("%s mentions Transport. A construction site that supplies one makes `do` use it "+
				"instead of the policy's, so the request would skip the resolved-address check and the "+
				"per-hop redirect check -- DNS rebinding would stop being caught, while PermitsURL would "+
				"still pass. Route it through HostPolicy.Transport() or justify the exception here", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	if control == 0 {
		t.Fatalf("CONTROL FAILED: zero `http.Client{` occurrences under %s. The 14 construction sites are "+
			"supposed to be there, so this scan found nothing and the zero above proves nothing", dir)
	}
	t.Logf("construction sites supplying a Transport: %d (control: %d http.Client{ hits under %s)",
		prod, control, dir)
}

func moduleRootFor(t *testing.T) string {
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
	t.Fatal("could not find the module root: a zero from the wrong tree looks exactly like a true zero")
	return ""
}
