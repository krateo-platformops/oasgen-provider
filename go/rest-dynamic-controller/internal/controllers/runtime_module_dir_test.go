package restResources

import (
	"os/exec"
	"strings"
	"testing"
)

// runtimeModuleDir locates unstructured-runtime in the module cache, so a test can assert something about
// the DEPENDENCY rather than about a copy of it. Asking `go list` rather than hard-coding a version keeps
// the guard pointed at whatever is actually selected.
func runtimeModuleDir(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}",
		"github.com/krateo-platformops/unstructured-runtime").Output()
	if err != nil {
		t.Skipf("could not locate the unstructured-runtime module (%v); skipping rather than failing, "+
			"since an unavailable module cache is an environment problem and not a defect", err)
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		t.Skip("go list returned no directory for unstructured-runtime")
	}
	return dir
}
