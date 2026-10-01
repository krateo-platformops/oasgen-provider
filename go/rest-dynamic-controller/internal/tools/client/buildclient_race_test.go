//go:build unit || integration

package restclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// TestBuildClientIsConcurrencySafe is a regression test for a shared-temp-directory race.
//
// BuildClient used a FIXED path, /tmp/rest-dynamic-controller, created per call and removed on return
// with `defer os.RemoveAll(basePath)` — which deletes the whole directory, including the files other
// in-flight calls are still using. BuildClient runs once per reconcile from four sites in
// restResources.go, and the controller runs REST_CONTROLLER_WORKERS (default 5) concurrently, so one
// worker returning deleted another's download mid-flight:
//
//	failed to download file: creating destination file:
//	open /tmp/rest-dynamic-controller/<doc>.yaml: no such file or directory
//
// What made it survive unnoticed is that its rate tracks CONCURRENCY, not correctness. Staggered by the
// 3-minute resync it almost never fired; after a pod restart, when every CR of a kind reconciles at
// once, it fired constantly — 852 times in 61 minutes on a live cluster. It then looked like a
// regression in a release that had not touched this module at all.
//
// This test fails reliably against the old code and passes against the per-call MkdirTemp.
func TestBuildClientIsConcurrencySafe(t *testing.T) {
	oas, err := os.ReadFile("../../../testdata/restdefinitions/cm/oas.yaml")
	if err != nil {
		t.Skipf("sample OAS not available: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(oas)
	}))
	defer srv.Close()

	kube := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())

	const workers, iterations = 16, 10
	var wg sync.WaitGroup
	errs := make(chan error, workers*iterations)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if _, berr := BuildClient(context.Background(), kube, srv.URL+"/oas.yaml"); berr != nil {
					errs <- berr
				}
			}
		}()
	}
	wg.Wait()
	close(errs)

	var missing, other int
	var sample string
	for e := range errs {
		if strings.Contains(e.Error(), "no such file or directory") {
			missing++
		} else {
			other++
		}
		if sample == "" {
			sample = e.Error()
		}
	}

	assert.Zero(t, missing,
		"%d of %d concurrent BuildClient calls lost their download to another call's cleanup — the "+
			"temp directory is shared again. Sample: %s", missing, workers*iterations, sample)
	require.Zero(t, other, "unexpected non-race failure: %s", sample)
}

// Each call must leave nothing behind: the directory it made is the directory it removes.
func TestBuildClientCleansUpItsOwnDirectory(t *testing.T) {
	oas, err := os.ReadFile("../../../testdata/restdefinitions/cm/oas.yaml")
	if err != nil {
		t.Skipf("sample OAS not available: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(oas)
	}))
	defer srv.Close()

	before, _ := os.ReadDir(os.TempDir())
	countOurs := func(entries []os.DirEntry) int {
		n := 0
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "rest-dynamic-controller-") {
				n++
			}
		}
		return n
	}
	start := countOurs(before)

	for i := 0; i < 5; i++ {
		_, _ = BuildClient(context.Background(), dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), srv.URL+"/oas.yaml")
	}

	after, _ := os.ReadDir(os.TempDir())
	assert.Equal(t, start, countOurs(after),
		"BuildClient leaked temp directories; a per-call directory must be removed by its own call")
}
