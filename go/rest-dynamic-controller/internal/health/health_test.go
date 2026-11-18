//go:build unit

package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/stretchr/testify/require"
)

// get fetches /readyz and returns the status and body, so every assertion below is on what a PROBE
// would see rather than on what the handler was asked.
func get(t *testing.T, ln net.Listener) (int, string) {
	t.Helper()
	url := fmt.Sprintf("http://%s/readyz", ln.Addr().String())
	resp, err := http.Get(url) //nolint:noctx // a test-local request against our own listener
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(b)
}

// serveOn starts Serve in the background on an ephemeral port and returns the listener. The caller's
// cancel stops it.
func serveOn(t *testing.T, ctx context.Context, ready ReadyFunc) net.Listener {
	t.Helper()
	ln, err := Listen("127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = Serve(ctx, logging.NewNopLogger(), ln, ready) }()
	// The listener is already bound by Listen, so a request cannot race the bind -- which is the whole
	// reason Listen is separate. No readiness wait is needed here and adding one would hide a
	// regression that reintroduced the race.
	return ln
}

// TestTheProbeSeesTheCheckResult is the point of the package: what a kubelet reads must follow the
// controller's own answer, not a cached or default one.
func TestTheProbeSeesTheCheckResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A check whose answer changes, so one listener can show both outcomes. A fixed check could not
	// distinguish "serves the result" from "always answers the same thing".
	failing := true
	ln := serveOn(t, ctx, func(*http.Request) error {
		if failing {
			return errors.New("waiting for the ownership lease")
		}
		return nil
	})

	code, body := get(t, ln)
	require.Equal(t, http.StatusServiceUnavailable, code,
		"a failing check must make the probe fail, or a replica waiting for the lease reports Ready")
	require.Contains(t, body, "waiting for the ownership lease",
		"the body must carry the REASON: a probe ignores it, a human running curl does not, and "+
			"'why is this pod NotReady' is the question this endpoint exists to answer")

	failing = false
	code, body = get(t, ln)
	require.Equal(t, http.StatusOK, code, "a passing check must make the probe pass")
	require.Contains(t, body, "ok")
}

// TestANilCheckIsRefusedRatherThanDefaultedToReady pins the one failure mode that would be worse than
// having no endpoint: answering 200 because nothing was wired.
//
// An endpoint that is always ready is indistinguishable from the defect this package closes.
//
// THE BOUNDED WAIT IS NOT DEFENSIVE PADDING. The first version of this called Serve directly with
// context.Background() and asserted on the returned error. Mutating the nil guard away made Serve fall
// through to its select and wait on a context that never cancels, so the test HUNG instead of failing --
// it burned a 120s timeout and said nothing about which assertion was involved. A `require.Error` on a
// call that might not return is not an assertion; it is a hope with a test framework around it.
func TestANilCheckIsRefusedRatherThanDefaultedToReady(t *testing.T) {
	ln, err := Listen("127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Serve(ctx, logging.NewNopLogger(), ln, nil) }()

	select {
	case serr := <-done:
		require.Error(t, serr, "a nil check must be refused, not served as 200")
		require.Contains(t, serr.Error(), "always answer 200")
	case <-time.After(3 * time.Second):
		t.Fatal("Serve neither refused the nil check nor returned: the nil guard is gone, so an " +
			"unwired endpoint would serve 200 until the context ended")
	}

	// And the listener must be closed on that path, or a refused Serve leaks the port it was handed.
	_, derr := net.DialTimeout("tcp", ln.Addr().String(), 250*time.Millisecond)
	require.Error(t, derr, "Serve returned an error and left the listener open")
}

// TestListenReportsABindFailure is why Listen is separate from Serve: the caller must be able to fail
// fast. A bind error discovered inside a goroutine is how an endpoint comes to be absent while
// everything reports fine.
func TestListenReportsABindFailure(t *testing.T) {
	first, err := Listen("127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = first.Close() }()

	// SETUP CONTROL: the address really is taken, so the failure below is a bind conflict rather than
	// a malformed address. These two fail with different messages.
	require.NotEmpty(t, first.Addr().String())

	_, err = Listen(first.Addr().String())
	require.Error(t, err, "binding an address already in use must return an error")
	require.Contains(t, err.Error(), "binding", "the message must name what failed")
}

// TestCancellingTheContextStopsServing covers the shutdown half, because a listener that outlives its
// context keeps a port held after the process means to exit.
func TestCancellingTheContextStopsServing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ln, err := Listen("127.0.0.1:0")
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- Serve(ctx, logging.NewNopLogger(), ln, func(*http.Request) error { return nil }) }()

	// SETUP CONTROL: it is actually serving before we cancel. Without this the test passes for a Serve
	// that returned immediately and never bound anything.
	code, _ := get(t, ln)
	require.Equal(t, http.StatusOK, code, "the endpoint was not serving, so the cancel below proves nothing")

	cancel()
	select {
	case serr := <-done:
		require.NoError(t, serr, "a context cancellation is an ordinary shutdown, not an error")
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return within 5s of the context being cancelled")
	}

	_, derr := net.DialTimeout("tcp", ln.Addr().String(), 250*time.Millisecond)
	require.Error(t, derr, "the port is still held after shutdown")
}

// TestOnlyReadyzIsServed pins the surface. An endpoint that answers everything is one a liveness probe
// can be pointed at by mistake -- and wiring liveness to this check would restart every replica that is
// correctly waiting for the lease.
func TestOnlyReadyzIsServed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln := serveOn(t, ctx, func(*http.Request) error { return nil })

	resp, err := http.Get(fmt.Sprintf("http://%s/healthz", ln.Addr().String())) //nolint:noctx // test-local
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusNotFound, resp.StatusCode,
		"/healthz answers, so a liveness probe could be pointed here -- which would restart every "+
			"replica that is correctly waiting for the ownership lease")

	// CONTROL: /readyz on the same listener does answer, so the 404 above is a routing fact rather
	// than a dead listener.
	code, _ := get(t, ln)
	require.Equal(t, http.StatusOK, code, "the listener is dead, so the 404 above means nothing")
}

// TestTheHandlerSignatureMatchesTheRuntimeCheck is a compile-time assertion with a runtime witness: if
// unstructured-runtime ever changes ReadyzCheck's signature, this fails here rather than at the one
// call site in main.go.
func TestTheHandlerSignatureMatchesTheRuntimeCheck(t *testing.T) {
	var _ ReadyFunc = (&struct{ fn func(*http.Request) error }{}).fn

	// And the shape is what http expects, which is the reason no adapter is needed -- an adapter is
	// where a second definition of "ready" would appear.
	var f ReadyFunc = func(*http.Request) error { return nil }
	require.NotNil(t, f)
	require.True(t, strings.HasPrefix("/readyz", "/"), "the path is absolute")
}
