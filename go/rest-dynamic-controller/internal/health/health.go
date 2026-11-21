// Package health serves the controller's readiness over HTTP, because nothing was serving it.
//
// WHY THIS EXISTS. unstructured-runtime has carried `(*Controller).ReadyzCheck(*http.Request) error`
// for a long time, and it is already an http-shaped handler.
//
// WHAT IT ACTUALLY FAILS ON, because this comment said something else until a consumer asked:
//
//	the informer has not completed its first successful list   -> NOT ready
//	the lease cannot be READ OR WRITTEN (RBAC, API error)      -> NOT ready
//	a PEER legitimately holds the lease and we are waiting      -> READY
//
// readyz.go:43 states the third row explicitly -- "a controller WAITING for the Lease is Ready ... but
// one that cannot read or write the Lease can never reconcile, and is not" -- and
// ownershipReadyzError() returns non-nil only when ownershipState.lastErr() is set.
//
// THIS COMMENT HAD IT INVERTED, and so did main.go's flag help, values.schema.json's description and
// the 0.85.0 log section: all four said a replica waiting for the lease reports NotReady. An operator
// with two replicas would have expected a NotReady standby, seen Ready, and concluded the probe was
// broken. The code was always right; one wrong belief was written down four times.
//
// NOTHING IN THIS REPO CALLED ReadyzCheck before this package --
// measured, 0 occurrences, against a control of 12 `builder.` calls in main.go -- and the chart set no
// probe, so a generated controller that cannot acquire its Lease reported Running with 0 restarts while
// reconciling nothing.
//
// A consumer measured that on a real cluster, 6 of 6 controllers:
//
//	avail=1 ready=1 restarts=0 waiting=-
//	each logging "Waiting for the ownership lease before reconciling"
//	and never    "Holding the ownership lease; starting workers"
//
// with an in-cluster control -- the provider's own leader-election Role, same authorizer and namespace,
// admitting one shape and refusing the other. Four pod signals, identical values, opposite truths.
//
// READINESS ONLY, DELIBERATELY. Waiting for a Lease is HEALTHY: exactly one replica holds it and the
// others are correct to wait. A liveness probe wired to this check would restart every waiting replica
// in a loop, turning a working design into a crash loop. That distinction is the whole point of having
// two probe kinds, and it is the one a reader is most likely to collapse.
//
// NOT A BUILDER OPTION, because the runtime does not offer one. Metrics go through
// builder.WithMetrics and the runtime owns that lifecycle; there is no equivalent hook for readiness,
// so this package owns its own server and shuts it down with the context.
package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
)

// ReadyFunc is the check a listener serves. It matches (*controller.Controller).ReadyzCheck exactly, so
// the controller's own method can be passed without an adapter -- and an adapter is where a second
// implementation of "what ready means" would appear.
type ReadyFunc func(*http.Request) error

// shutdownGrace bounds the wait for in-flight probe requests. A readiness probe is a sub-second GET, so
// this is generous; it exists so a hung connection cannot delay process exit indefinitely.
const shutdownGrace = 2 * time.Second

// THE BIND IS THE CALLER'S, VIA Listen, and that is the point rather than a detail: if binding fails --
// a port already in use, a permission problem -- the caller finds out before it starts serving, instead
// of discovering it as a probe failure minutes later. A background goroutine that logs its own bind
// error is how a health endpoint comes to be absent while everything reports fine, which is the defect
// class this package exists to close.
// Listen binds addr and returns the listener, so a caller can fail fast on a bind error and THEN serve
// in a goroutine.
//
// SPLIT FROM Serve BECAUSE THE ALTERNATIVE IS A RACE. The first version of this had one blocking
// function, so the caller bound a probe listener to test the port, closed it, and let Serve bind again --
// and between the close and the re-bind anything could take the port. A test-then-use pair on a shared
// resource is a race whether or not it is retried, and binding exactly once is the only form that does
// not have one.
func Listen(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("health: binding %s: %w", addr, err)
	}
	return ln, nil
}

// Serve answers readiness on ln until ctx is done, then shuts down. It always closes ln.
func Serve(ctx context.Context, log logging.Logger, ln net.Listener, ready ReadyFunc) error {
	if ready == nil {
		_ = ln.Close()
		return errors.New("health: no readiness check supplied, so the endpoint would always answer 200")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := ready(r); err != nil {
			// The body carries the reason. A probe ignores it; a human running curl does not, and
			// "why is this pod NotReady" is the question this endpoint exists to answer.
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, "not ready: %v\n", err)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	srv := &http.Server{
		Handler: mux,
		// A probe whose headers cannot be read in 10s is a probe nobody is waiting for.
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		serr := srv.Serve(ln)
		if errors.Is(serr, http.ErrServerClosed) {
			serr = nil
		}
		errCh <- serr
	}()

	log.Info("Readiness endpoint serving", "address", ln.Addr().String(), "path", "/readyz")

	select {
	case serr := <-errCh:
		// The server stopped on its own, which is never expected while ctx is live.
		return fmt.Errorf("health: listener stopped unexpectedly: %w", serr)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if serr := srv.Shutdown(shutdownCtx); serr != nil {
		return fmt.Errorf("health: shutting down: %w", serr)
	}
	return nil
}
