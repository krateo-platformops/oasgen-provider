//go:build integration
// +build integration

package restResources

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/client-go/dynamic"

	"github.com/gobuffalo/flect"
	customcondition "github.com/krateo-platformops/rest-dynamic-controller/internal/controllers/condition"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"

	"github.com/krateo-platformops/unstructured-runtime/pkg/controller"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/krateo-platformops/unstructured-runtime/pkg/pluralizer"
	unstructuredtools "github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured"
	"github.com/krateo-platformops/unstructured-runtime/pkg/tools/unstructured/condition"

	"github.com/krateo-platformops/plumbing/e2e"
	xenv "github.com/krateo-platformops/plumbing/env"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/e2e-framework/klient/decoder"
	"sigs.k8s.io/e2e-framework/klient/k8s/resources"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/envfuncs"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/support/kind"
)

type FakePluralizer struct {
}

var _ pluralizer.PluralizerInterface = &FakePluralizer{}

func (p FakePluralizer) GVKtoGVR(gvk schema.GroupVersionKind) (schema.GroupVersionResource, error) {
	return schema.GroupVersionResource{
		Group:    gvk.Group,
		Version:  gvk.Version,
		Resource: flect.Pluralize(strings.ToLower(gvk.Kind)),
	}, nil
}

var (
	testenv       env.Environment
	clusterName   string
	mockServerCmd *exec.Cmd
)

const (
	testdataPath  = "../../testdata"
	manifestsPath = "../../manifests"
	namespace     = "default"
	altNamespace  = "demo-system"
	wsUrl         = "http://localhost:30007"
)

// psField reads one ps field for a pid ("command=" or "ppid=").
func psField(pid, field string) string {
	out, err := exec.Command("ps", "-p", pid, "-o", field).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// portHolders returns the PIDs listening on port together with their command lines.
func portHolders(port int) map[string]string {
	out := map[string]string{}
	output, err := exec.Command("lsof", "-ti", fmt.Sprintf(":%d", port)).Output()
	if err != nil {
		return out // lsof exits non-zero when nothing holds the port
	}
	for _, pid := range strings.Fields(strings.TrimSpace(string(output))) {
		if c := psField(pid, "command="); c != "" {
			out[pid] = c
		}
	}
	return out
}

// isOurMockServer reports whether the process holding the port is a mock server this suite started.
//
// The old predicate was strings.Contains(cmdLine, "main"): too broad (any command line containing
// that substring was a kill candidate) and, as it turns out, too narrow for the process that actually
// matters. `go run internal/controllers/mockserver/main.go` compiles to the go-build cache and execs
// the result as a CHILD, and it is the child that binds the port. Its command line carries no hint of
// its origin -- it looks like:
//
//	/Users/<me>/Library/Caches/go-build/f7/f7968fa0…-d/main
//
// so the only reliable signals are the go-build cache path itself, or the PARENT's command line,
// which is still the readable `go run …/mockserver/main.go`. Checking both is what makes an orphan
// from an interrupted run recognisable; matching on neither is why the port was never reclaimed.
func isOurMockServer(pid, cmdLine string) bool {
	if strings.Contains(cmdLine, "mockserver") {
		return true
	}
	if strings.Contains(cmdLine, "/go-build/") && strings.HasSuffix(cmdLine, "/main") {
		return true
	}
	if ppid := psField(pid, "ppid="); ppid != "" && ppid != "1" {
		if strings.Contains(psField(ppid, "command="), "mockserver") {
			return true
		}
	}
	return false
}

// reclaimPort kills mock servers left behind by an interrupted run and reports whether the port ended
// up free. It deliberately refuses to kill anything it cannot identify as ours -- Docker, a database,
// a colleague's server -- and says so, rather than guessing.
func reclaimPort(port int) error {
	for pid, cmdLine := range portHolders(port) {
		if !isOurMockServer(pid, cmdLine) {
			return fmt.Errorf("port %d is held by PID %s which is NOT a mock server from this suite (%s); "+
				"refusing to kill it -- free the port or stop that process", port, pid, cmdLine)
		}
		// Kill the whole process group: `go run` leaves the compiled child behind otherwise.
		if p, err := strconv.Atoi(pid); err == nil {
			if pgid, gerr := syscall.Getpgid(p); gerr == nil {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
			_ = syscall.Kill(p, syscall.SIGKILL)
		}
	}

	// Killing is asynchronous; wait briefly for the socket to be released.
	for i := 0; i < 20; i++ {
		if len(portHolders(port)) == 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if h := portHolders(port); len(h) > 0 {
		return fmt.Errorf("port %d still held after kill: %v", port, h)
	}
	return nil
}

func startMockServer() error {
	// Reclaim the port from a mock server left behind by an interrupted run. Failing here is
	// deliberate: continuing into the health-check loop below produces a 30-second wait followed by a
	// cascade of "connection refused" assertion failures that name neither the port nor the cause.
	if err := reclaimPort(30007); err != nil {
		return fmt.Errorf("cannot start mock server: %w", err)
	}

	// Avvia il mock server come processo separato
	mockServerCmd = exec.Command("go", "run", "internal/controllers/mockserver/main.go")
	// Own process group, so stopMockServer can kill `go run` AND the compiled child it execs.
	// Without this the child outlives the suite and holds :30007 against the next run.
	mockServerCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Redirect output per debug in un file di log
	logFile, err := os.Create("mockserver.log")
	if err != nil {
		return fmt.Errorf("failed to create log file: %w", err)
	}
	mockServerCmd.Stdout = logFile
	mockServerCmd.Stderr = logFile
	mockServerCmd.Dir = "../.." // Vai alla root del progetto

	if err := mockServerCmd.Start(); err != nil {
		return fmt.Errorf("failed to start mock server: %w", err)
	}

	// Aspetta che il server sia pronto
	for i := 0; i < 30; i++ {
		resp, err := http.Get("http://localhost:30007/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(time.Second)
	}

	// Surface WHY it did not come up. The reason is in mockserver.log -- typically
	// "listen tcp :30007: bind: address already in use" -- and without it the failure presents as
	// unrelated connection errors in whichever test runs first.
	tail := ""
	if b, rerr := os.ReadFile("mockserver.log"); rerr == nil {
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) > 6 {
			lines = lines[len(lines)-6:]
		}
		tail = "\n  mockserver.log:\n    " + strings.Join(lines, "\n    ")
	}
	stopMockServer()
	return fmt.Errorf("mock server did not become healthy on :30007 within 30s%s", tail)
}

func stopMockServer() {
	if mockServerCmd != nil && mockServerCmd.Process != nil {
		// Signal the process GROUP: `go run` forwards nothing to the child it execs, so signalling
		// only the parent leaves the actual listener alive holding :30007.
		if pgid, err := syscall.Getpgid(mockServerCmd.Process.Pid); err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
		} else {
			mockServerCmd.Process.Signal(syscall.SIGTERM)
		}

		// Aspetta un po'
		done := make(chan error, 1)
		go func() {
			done <- mockServerCmd.Wait()
		}()

		select {
		case <-done:
			// Processo terminato correttamente
		case <-time.After(2 * time.Second):
			// Timeout, forza il kill -- again on the whole group.
			if pgid, err := syscall.Getpgid(mockServerCmd.Process.Pid); err == nil {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
			mockServerCmd.Process.Kill()
			mockServerCmd.Wait()
		}

		mockServerCmd = nil
	}

	// Cleanup finale per essere sicuri
	if err := reclaimPort(30007); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
}

func TestMain(m *testing.M) {
	xenv.SetTestMode(true)

	clusterName = "krateo-rest-resources-test"
	testenv = env.New()

	testenv.Setup(
		// Remove a cluster left behind by an interrupted run before creating ours. Finish() -- which
		// stops the mock server and destroys the cluster -- does not execute when the process is
		// killed, so both leak, and the next run inherits them. The name is specific to this suite,
		// so deleting it cannot disturb a cluster anyone is using for something else.
		func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
			out, err := exec.Command("kind", "get", "clusters").Output()
			if err != nil {
				return ctx, nil // kind not on PATH is the provider's problem to report, not ours
			}
			for _, c := range strings.Fields(string(out)) {
				if c == clusterName {
					fmt.Fprintf(os.Stderr, "removing leftover kind cluster %q from a previous run\n", clusterName)
					_ = exec.Command("kind", "delete", "cluster", "--name", clusterName).Run()
				}
			}
			return ctx, nil
		},
		envfuncs.CreateCluster(kind.NewProvider(), clusterName),
		e2e.CreateNamespace(namespace),
		e2e.CreateNamespace(altNamespace),

		func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
			if err := startMockServer(); err != nil {
				return ctx, err
			}

			time.Sleep(2 * time.Second)

			return ctx, nil
		},
	).Finish(
		func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
			stopMockServer()
			return ctx, nil
		},
		envfuncs.DestroyCluster(clusterName),
	)

	os.Exit(testenv.Run(m))
}

func TestController(t *testing.T) {
	var handler controller.ExternalClient
	var cli dynamic.Interface
	f := features.New("Setup").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			r, err := resources.New(cfg.Client().RESTConfig())
			if err != nil {
				t.Error("Creating resource client.", "error", err)
				return ctx
			}

			cli = dynamic.NewForConfigOrDie(cfg.Client().RESTConfig())

			err = decoder.ApplyWithManifestDir(ctx, r, filepath.Join(testdataPath, "crds"), "*.yaml", nil)
			if err != nil {
				t.Error("Applying crds manifests.", "error", err)
				return ctx
			}

			time.Sleep(2 * time.Second)

			err = decoder.ApplyWithManifestDir(ctx, r, filepath.Join(testdataPath, "rest"), "*.yaml", nil)
			if err != nil {
				t.Error("Applying rest manifests.", "error", err)
				return ctx
			}

			// Read OAS from the cm folder and put in a ConfigMap
			b, err := os.ReadFile(filepath.Join(testdataPath, "restdefinitions", "cm", "oas.yaml"))
			if err != nil {
				t.Error("Reading OAS file.", "error", err)
				return ctx
			}

			cm := corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "sample",
					Namespace: altNamespace,
				},
				Data: map[string]string{
					"openapi.yaml": string(b),
				},
			}
			err = r.Create(ctx, &cm)
			if err != nil {
				t.Error("Creating ConfigMap with OAS.", "error", err)
				return ctx
			}

			err = decoder.ApplyWithManifestDir(ctx, r, filepath.Join(testdataPath, "restdefinitions"), "*.yaml", nil)
			if err != nil {
				t.Error("Applying restdefinition manifests.", "error", err)
				return ctx
			}

			zl := zap.New(zap.UseDevMode(true))
			log := logging.NewLogrLogger(zl.WithName("rest-controller-test"))

			pluralizer := &FakePluralizer{}

			var swg getter.Getter
			swg, err = getter.Dynamic(cfg.Client().RESTConfig(), pluralizer)
			if err != nil {
				log.Debug("Creating chart url info getter.", "error", err)
			}

			handler = NewHandler(cfg.Client().RESTConfig(), log, swg, pluralizer, true)

			return ctx
		}).
		// Test operazione Create
		Assess("Create", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			configPayload := `{"authFailures": false}`
			_, err := http.Post("http://localhost:30007/admin/config", "application/json",
				strings.NewReader(configPayload))
			if err != nil {
				t.Error("Resetting auth config", "error", err)
				return ctx
			}
			resourceName := "sample-1"
			k := cli.Resource(schema.GroupVersionResource{
				Group:    "sample.krateo.io",
				Version:  "v1alpha1",
				Resource: "samples",
			}).Namespace(namespace)

			u, err := k.Get(ctx, resourceName, metav1.GetOptions{})
			if err != nil {
				t.Error("Getting Rest Resource.", "error", err)
				return ctx
			}

			obs, err := handler.Observe(ctx, u)
			if err != nil {
				t.Error("Error during initial observe", "error", err)
				return ctx
			}

			if obs.ResourceExists {
				t.Error("Resource should not exist initially")
				return ctx
			}

			u, err = k.Get(ctx, resourceName, metav1.GetOptions{})
			if err != nil {
				t.Error("Error getting resource for create test", "error", err)
				return ctx
			}
			// Crea la risorsa
			err = handler.Create(ctx, u)
			if err != nil {
				t.Error("Error creating resource", "error", err)
				return ctx
			}

			u, err = k.Get(ctx, resourceName, metav1.GetOptions{})
			if err != nil {
				t.Error("Error getting resource after create", "error", err)
				return ctx
			}

			// Verifica che la risorsa sia stata creata
			time.Sleep(1 * time.Second) // Piccola pausa per la propagazione
			obs, err = handler.Observe(ctx, u)
			if err != nil {
				t.Error("Error observing after create", "error", err)
				return ctx
			}

			if !obs.ResourceExists {
				t.Error("Resource should exist after creation")
				return ctx
			}

			if obs.ResourceUpToDate {
				t.Log("Resource is up to date after creation")
			}

			return ctx
		}).
		Assess("AsyncCreate", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			var resourceNames []string
			resourceNames = append(resourceNames, "sample-async-1", "sample-async-get")

			// Configure the mock server to simulate async creation
			configPayload := `{"asyncOperations": true}`
			_, err := http.Post("http://localhost:30007/admin/config", "application/json",
				strings.NewReader(configPayload))
			if err != nil {
				t.Error("Configuring mock server for async create", "error", err)
				return ctx
			}

			for _, resourceName := range resourceNames {
				k := cli.Resource(schema.GroupVersionResource{
					Group:    "sample.krateo.io",
					Version:  "v1alpha1",
					Resource: "samples",
				}).Namespace(namespace)

				u, err := k.Get(ctx, resourceName, metav1.GetOptions{})
				if err != nil {
					t.Error("Getting Rest Resource.", "error", err)
					return ctx
				}

				obs, err := handler.Observe(ctx, u)
				if err != nil {
					t.Error("Error during initial observe", "error", err)
					return ctx
				}

				if obs.ResourceExists {
					t.Error("Resource should not exist initially")
					return ctx
				}

				// Trigger async create
				err = handler.Create(ctx, u)
				if err != nil {
					t.Error("Error creating resource", "error", err)
					return ctx
				}

				// Poll for resource existence (simulate async propagation)
				var found bool
				for i := 0; i < 10; i++ {
					if i == 5 {
						configPayload := `{"completePendingAsync": true}`
						_, err := http.Post("http://localhost:30007/admin/config", "application/json",
							strings.NewReader(configPayload))
						if err != nil {
							t.Error("Configuring mock server for async create", "error", err)
							return ctx
						}
					}

					time.Sleep(1 * time.Second)
					u, err = k.Get(ctx, resourceName, metav1.GetOptions{})
					if err != nil {
						t.Error("Error getting resource after async create", "error", err)
						return ctx
					}
					obs, err = handler.Observe(ctx, u)

					u, err = k.Get(ctx, resourceName, metav1.GetOptions{})
					if err != nil {
						t.Error("Error getting resource after async create", "error", err)
						return ctx
					}
					if unstructuredtools.IsConditionSet(u, customcondition.Pending()) {
						t.Log("Resource is still pending, waiting for async creation to complete")
						continue
					} else if unstructuredtools.IsConditionSet(u, condition.Available()) {
						found = true
						t.Log("Resource is now available after async creation")
						break
					}

				}
				if !found {
					t.Error("Resource was not created asynchronously within timeout")
					return ctx
				}

				if !obs.ResourceUpToDate {
					t.Error("Resource should be up to date after async creation")
				}
			}

			// Reset mock server config if needed
			configPayload = `{"asyncOperations": false}`
			http.Post("http://localhost:30007/admin/config", "application/json", strings.NewReader(configPayload))

			return ctx
		}).
		Assess("AlreadyExists", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			resourceName := "sample-1-already-exists"
			k := cli.Resource(schema.GroupVersionResource{
				Group:    "sample.krateo.io",
				Version:  "v1alpha1",
				Resource: "samples",
			}).Namespace(namespace)

			u, err := k.Get(ctx, resourceName, metav1.GetOptions{})
			if err != nil {
				t.Error("Error getting resource for already exists test", "error", err)
				return ctx
			}

			obs, err := handler.Observe(ctx, u)
			if err != nil {
				t.Error("Error during initial observe", "error", err)
				return ctx
			}

			if !obs.ResourceExists {
				t.Error("Resource should exist initially")
				return ctx
			}

			if !obs.ResourceUpToDate {
				t.Error("Resource should be up to date initially")
				return ctx
			}
			ok, err := unstructuredtools.IsAvailable(u)
			if err != nil {
				t.Error("Error checking if resource is available", "error", err)
			}
			if !ok {
				t.Error("Resource should be available initially")
			}

			return ctx
		}).
		Assess("Update", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			resourceName := "sample-1"
			k := cli.Resource(schema.GroupVersionResource{
				Group:    "sample.krateo.io",
				Version:  "v1alpha1",
				Resource: "samples",
			}).Namespace(namespace)
			u, err := k.Get(ctx, resourceName, metav1.GetOptions{})
			if err != nil {
				t.Error("Error getting resource for update test", "error", err)
				return ctx
			}

			// Edit the resource
			u.Object["spec"].(map[string]interface{})["description"] = "Updated description"
			// Apply the update on the cluster
			u, err = k.Update(ctx, u, metav1.UpdateOptions{})
			if err != nil {
				t.Error("Error applying update to resource", "error", err)
				return ctx
			}

			// Observe the resource. We expect it to exist but not be up to date
			obs, err := handler.Observe(ctx, u)
			if err != nil {
				t.Error("Error observing resource after update", "error", err)
				return ctx
			}
			if !obs.ResourceExists {
				t.Error("Resource should exist after update")
				return ctx
			}
			if obs.ResourceUpToDate {
				t.Error("Resource should not be up to date after edit")
				return ctx
			}

			err = handler.Update(ctx, u)
			if err != nil {
				t.Error("Error updating resource", "error", err)
				return ctx
			}

			u, err = k.Get(ctx, resourceName, metav1.GetOptions{})
			if err != nil {
				t.Error("Error getting resource after update", "error", err)
				return ctx
			}

			time.Sleep(1 * time.Second)
			obs, err = handler.Observe(ctx, u)
			if err != nil {
				t.Error("Error observing after update", "error", err)
				return ctx
			}

			if !obs.ResourceExists {
				t.Error("Resource should exist after update")
				return ctx
			}
			if !obs.ResourceUpToDate {
				t.Error("Resource should be up to date after update")
			}
			t.Log("Resource is up to date after update")

			return ctx
		}).
		// Test operazione Delete
		Assess("Delete", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			resourceName := "sample-1"
			k := cli.Resource(schema.GroupVersionResource{
				Group:    "sample.krateo.io",
				Version:  "v1alpha1",
				Resource: "samples",
			}).Namespace(namespace)

			u, err := k.Get(ctx, resourceName, metav1.GetOptions{})
			if err != nil {
				t.Error("Error getting resource for delete test", "error", err)
				return ctx
			}

			// Delete the resource
			err = handler.Delete(ctx, u)
			if err != nil {
				t.Error("Error deleting resource", "error", err)
			}

			// curl to check if the resource is deleted
			req, err := http.NewRequest("GET", "http://localhost:30007/resource/sample-1", nil)
			if err != nil {
				t.Error("Error creating HTTP request for resource deletion check", "error", err)
			} else {
				req.Header.Set("Authorization", "Bearer test")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Error("Error checking resource deletion via HTTP", "error", err)
				}

				if resp.StatusCode != http.StatusNotFound {
					t.Error("Resource should be deleted, expected 404 but got", resp.StatusCode)
				}
				if resp.StatusCode != http.StatusNotFound {
					t.Error("Resource should be deleted, expected 404 but got", resp.StatusCode)
				}
			}

			return ctx
		}).
		// A native DELETE that returns 2xx means the deletion was REQUESTED, not that it completed. An API
		// deleting asynchronously with no pollable operation in its OAS answers 204 immediately and keeps
		// the resource around. Releasing the finalizer on that response makes the CR vanish while the
		// resource still exists, and if the deletion then fails there is nothing left to retry -- the
		// resource is orphaned silently, and for billable kinds indefinitely (#77).
		//
		// Delete must therefore hold the finalizer (return an error) while the resource is still
		// observable, exactly as the RESTAction delete branch already does.
		Assess("DeleteHoldsFinalizerWhileResourceLingers", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// Recreate the resource directly on the API, then make DELETE report success without
			// actually removing it.
			body := `{"name":"linger-1","id":"linger-1","description":"still deleting"}`
			creq, _ := http.NewRequest("POST", "http://localhost:30007/resource", strings.NewReader(body))
			creq.Header.Set("Authorization", "Bearer test")
			creq.Header.Set("Content-Type", "application/json")
			cresp, cerr := http.DefaultClient.Do(creq)
			if cerr != nil {
				t.Error("Error seeding lingering resource", "error", cerr)
				return ctx
			}
			cresp.Body.Close()

			if _, err := http.Post("http://localhost:30007/admin/config", "application/json",
				strings.NewReader(`{"lingerOnDelete": true}`)); err != nil {
				t.Error("Error configuring mock server for lingering delete", "error", err)
				return ctx
			}
			defer func() {
				http.Post("http://localhost:30007/admin/config", "application/json",
					strings.NewReader(`{"lingerOnDelete": false}`))
			}()

			u := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "sample.krateo.io/v1alpha1",
				"kind":       "Sample",
				"metadata": map[string]interface{}{
					"name":      "linger-1",
					"namespace": namespace,
				},
				"spec": map[string]interface{}{
					"name": "linger-1",
					"configurationRef": map[string]interface{}{
						"name":      "my-sample-config",
						"namespace": namespace,
					},
				},
				"status": map[string]interface{}{
					"id": "linger-1",
				},
			}}

			err := handler.Delete(ctx, u)
			if err == nil {
				t.Error("Delete must NOT report success while the external resource is still present: " +
					"releasing the finalizer here orphans the resource with nothing left to retry (#77)")
			} else if !strings.Contains(err.Error(), "still present") {
				// Assert the SPECIFIC failure. A bare err != nil is satisfied by any unrelated error
				// (a missing configurationRef, say), which would make this test pass while never
				// reaching the delete path at all.
				t.Errorf("Delete failed for the wrong reason, so this test did not exercise #77: %v", err)
			} else {
				t.Log("Delete correctly held the finalizer while the resource lingered:", err)
			}

			return ctx
		}).
		// A second Delete must also succeed. On an API that deletes asynchronously the finalizer is held
		// until the resource is verified gone (#77), so Delete is retried -- and every retry after the
		// first DELETE took effect gets a 404, because the resource is already gone. Returning that as an
		// error means the finalizer never releases and the CR hangs in Deleting forever, which made
		// resources undeletable through Kubernetes on 0.22.1 (#98). 404 is the success condition here.
		Assess("DeleteIsIdempotentWhenAlreadyGone", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			body := `{"name":"gone-1","id":"gone-1","description":"to be deleted twice"}`
			creq, _ := http.NewRequest("POST", "http://localhost:30007/resource", strings.NewReader(body))
			creq.Header.Set("Authorization", "Bearer test")
			creq.Header.Set("Content-Type", "application/json")
			if cresp, cerr := http.DefaultClient.Do(creq); cerr != nil {
				t.Error("Error seeding resource", "error", cerr)
				return ctx
			} else {
				cresp.Body.Close()
			}

			u := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "sample.krateo.io/v1alpha1",
				"kind":       "Sample",
				"metadata": map[string]interface{}{
					"name":      "gone-1",
					"namespace": namespace,
				},
				"spec": map[string]interface{}{
					"name": "gone-1",
					"configurationRef": map[string]interface{}{
						"name":      "my-sample-config",
						"namespace": namespace,
					},
				},
				"status": map[string]interface{}{
					"id": "gone-1",
				},
			}}

			if err := handler.Delete(ctx, u); err != nil {
				t.Errorf("first Delete should succeed: %v", err)
				return ctx
			}

			// The retry. Before the fix this returned "unexpected status: 404" forever.
			if err := handler.Delete(ctx, u); err != nil {
				t.Errorf("second Delete on an already-absent resource must succeed -- a 404 here is the "+
					"resource being gone, and returning it as an error strands the finalizer (#98): %v", err)
			} else {
				t.Log("Delete is idempotent: a 404 on an already-deleted resource released the finalizer")
			}

			return ctx
		}).
		// An API may answer a DELETE with an error for a resource it has ALREADY removed. Aruba
		// security/Kms returns 400 "Some kms keys are not deleted" while a direct GET returns 404.
		// The delete status code is a proxy; the observe verb is the ground truth, and returning the
		// error without consulting it left the CR in Deleting forever (#101).
		//
		// Only an affirmative, VERIFIED absence releases: where there is no get verb to ask, the
		// original delete error stands, because "could not check" must not become "assume gone". That
		// branch is not covered here -- it needs a RestDefinition fixture with no get verb -- so it
		// rests on code inspection rather than on this test.
		Assess("DeleteReleasesWhenErrorButResourceVerifiablyGone", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			body := `{"name":"kms-1","id":"kms-1","description":"delete errors but removes"}`
			creq, _ := http.NewRequest("POST", "http://localhost:30007/resource", strings.NewReader(body))
			creq.Header.Set("Authorization", "Bearer test")
			creq.Header.Set("Content-Type", "application/json")
			if cresp, cerr := http.DefaultClient.Do(creq); cerr != nil {
				t.Error("Error seeding resource", "error", cerr)
				return ctx
			} else {
				cresp.Body.Close()
			}

			if _, err := http.Post("http://localhost:30007/admin/config", "application/json",
				strings.NewReader(`{"deleteErrorsButRemoves": true}`)); err != nil {
				t.Error("Error configuring mock server", "error", err)
				return ctx
			}
			defer func() {
				http.Post("http://localhost:30007/admin/config", "application/json",
					strings.NewReader(`{"deleteErrorsButRemoves": false}`))
			}()

			u := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "sample.krateo.io/v1alpha1",
				"kind":       "Sample",
				"metadata":   map[string]interface{}{"name": "kms-1", "namespace": namespace},
				"spec": map[string]interface{}{
					"name": "kms-1",
					"configurationRef": map[string]interface{}{
						"name":      "my-sample-config",
						"namespace": namespace,
					},
				},
				"status": map[string]interface{}{"id": "kms-1"},
			}}

			if err := handler.Delete(ctx, u); err != nil {
				t.Errorf("Delete must release the finalizer when the DELETE errored but the resource is "+
					"verifiably absent -- holding it strands the CR in Deleting forever (#101): %v", err)
			} else {
				t.Log("Delete released the finalizer despite the delete error, because the resource is verifiably gone")
			}

			// Confirm the resource really was removed, so this tests the intended path and not a
			// vacuous success against a resource that never existed.
			req, _ := http.NewRequest("GET", "http://localhost:30007/resource/kms-1", nil)
			req.Header.Set("Authorization", "Bearer test")
			if resp, rerr := http.DefaultClient.Do(req); rerr == nil {
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusNotFound {
					t.Errorf("precondition: resource should be gone, got %d", resp.StatusCode)
				}
			}

			return ctx
		}).
		// THE DELETE RULE, as a matrix rather than one case per past incident.
		//
		// Three bugs on this path (#77, #98, #101) were the same defect: a branch that decided the
		// finalizer's fate before consulting whether the resource was actually gone. Each fix moved that
		// check one branch earlier, and each was written with a test for its own symptom. Tested per
		// symptom, the next uncovered branch is invisible -- which is why there was a third.
		//
		// The rule that holds is: the finalizer releases when the resource is OBSERVABLY GONE, whatever
		// the delete call said -- with one inversion, that where absence cannot be established the delete
		// result governs and an error means retry, never "assume gone" (#77's orphan).
		//
		// So the axes are {delete outcome} x {resource actually present} x {verifiable}, and this table
		// is the contract. A new delete behaviour should add a row here, not a bespoke Assess block.
		Assess("DeleteRuleMatrix", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			seed := func(id string) {
				body := fmt.Sprintf(`{"name":%q,"id":%q,"description":"matrix"}`, id, id)
				req, _ := http.NewRequest("POST", "http://localhost:30007/resource", strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer test")
				req.Header.Set("Content-Type", "application/json")
				if resp, err := http.DefaultClient.Do(req); err == nil {
					resp.Body.Close()
				}
			}
			configure := func(payload string) {
				http.Post("http://localhost:30007/admin/config", "application/json", strings.NewReader(payload))
			}
			present := func(id string) bool {
				req, _ := http.NewRequest("GET", "http://localhost:30007/resource/"+id, nil)
				req.Header.Set("Authorization", "Bearer test")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					return false
				}
				defer resp.Body.Close()
				return resp.StatusCode == http.StatusOK
			}
			cr := func(kind, id string) *unstructured.Unstructured {
				return &unstructured.Unstructured{Object: map[string]interface{}{
					"apiVersion": "sample.krateo.io/v1alpha1",
					"kind":       kind,
					"metadata":   map[string]interface{}{"name": id, "namespace": namespace},
					"spec": map[string]interface{}{
						"name": id,
						"configurationRef": map[string]interface{}{
							// Each kind resolves its own <Kind>Configuration, so the no-get fixture
							// needs its own instance -- pointing at my-sample-config would fail at
							// client-info resolution and hold the finalizer for the wrong reason.
							"name":      map[string]string{"Sample": "my-sample-config", "Nogetsample": "my-noget-config"}[kind],
							"namespace": namespace,
						},
					},
					"status": map[string]interface{}{"id": id},
				}}
			}

			cases := []struct {
				name        string
				kind        string // Sample = verifiable (has get); Nogetsample = unverifiable
				id          string
				seedFirst   bool
				serverCfg   string
				wantRelease bool // true => Delete returns nil and the finalizer is released
				why         string
			}{
				{
					name: "success/gone/verifiable", kind: "Sample", id: "mx-a", seedFirst: true,
					serverCfg:   `{"lingerOnDelete": false, "deleteErrorsButRemoves": false}`,
					wantRelease: true, why: "the ordinary case",
				},
				{
					name: "success/still-present/verifiable", kind: "Sample", id: "mx-b", seedFirst: true,
					serverCfg:   `{"lingerOnDelete": true}`,
					wantRelease: false, why: "#77: a 2xx means requested, not completed -- releasing here orphans it",
				},
				{
					name: "404/gone/verifiable", kind: "Sample", id: "mx-c", seedFirst: false,
					serverCfg:   `{"lingerOnDelete": false, "deleteErrorsButRemoves": false}`,
					wantRelease: true, why: "#98: 404 IS the success condition; every retry after a real delete hits this",
				},
				{
					name: "error/gone/verifiable", kind: "Sample", id: "mx-d", seedFirst: true,
					serverCfg:   `{"deleteErrorsButRemoves": true}`,
					wantRelease: true, why: "#101: the observe verb outranks the delete status code",
				},
				{
					name: "error/still-present/unverifiable", kind: "Nogetsample", id: "mx-e", seedFirst: true,
					serverCfg:   `{"lingerOnDelete": false, "deleteErrorsButRemoves": false, "simulateErrors": true}`,
					wantRelease: false, why: "no get verb: absence cannot be established, so the delete error governs -- retry, never assume gone",
				},
			}

			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					configure(`{"lingerOnDelete": false, "deleteErrorsButRemoves": false, "simulateErrors": false}`)
					if tc.seedFirst {
						seed(tc.id)
					}
					configure(tc.serverCfg)
					defer configure(`{"lingerOnDelete": false, "deleteErrorsButRemoves": false, "simulateErrors": false}`)

					err := handler.Delete(ctx, cr(tc.kind, tc.id))
					released := err == nil

					if released != tc.wantRelease {
						t.Errorf("release=%v want=%v (%s)\n  delete error: %v\n  resource still present upstream: %v",
							released, tc.wantRelease, tc.why, err, present(tc.id))
					}
				})
			}

			return ctx
		}).
		// A CR whose create never succeeded has no identifier in status, so the delete path /resource/{id}
		// cannot be built at all. Delete must still release the finalizer: returning the "missing path
		// parameter" error instead strands the CR in Deleting forever, and its namespace with it.
		Assess("DeleteNeverCreated", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			u := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "sample.krateo.io/v1alpha1",
				"kind":       "Sample",
				"metadata": map[string]interface{}{
					"name":      "sample-never-created",
					"namespace": namespace,
				},
				"spec": map[string]interface{}{
					"name":        "sample-never-created",
					"description": "create never succeeded, so status.id was never written",
					"configurationRef": map[string]interface{}{
						"name":      "my-sample-config",
						"namespace": namespace,
					},
				},
				// Deliberately no "status": this is the post-failed-create shape.
			}}

			if err := handler.Delete(ctx, u); err != nil {
				t.Error("Delete of a never-created resource must release the finalizer, got error", "error", err)
			}
			return ctx
		}).Feature()

	testenv.Test(t, f)
}

// Test per la configurazione del mock server
func TestMockServerConfiguration(t *testing.T) {
	// Verifica che il mock server risponda correttamente
	resp, err := http.Get("http://localhost:30007/health")
	if err != nil {
		t.Skip("Mock server not available, skipping configuration test")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Error("Mock server health check failed", "status", resp.StatusCode)
	}

	// Test configurazione errori
	configPayload := `{"simulateErrors": true}`
	resp, err = http.Post("http://localhost:30007/admin/config", "application/json",
		strings.NewReader(configPayload))
	if err != nil {
		t.Error("Failed to configure mock server", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Error("Mock server configuration failed", "status", resp.StatusCode)
	}

	// Ripristina configurazione normale
	configPayload = `{"simulateErrors": false}`
	resp, err = http.Post("http://localhost:30007/admin/config", "application/json",
		strings.NewReader(configPayload))
	if err != nil {
		t.Error("Failed to reset mock server config", "error", err)
		return
	}
	defer resp.Body.Close()
}
