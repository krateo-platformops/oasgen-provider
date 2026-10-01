// oasgen-render previews what oasgen-provider would generate for a set of RestDefinitions, without applying
// anything: POST /render with the RestDefinitions and their OAS documents, get back the CRDs, the
// Configuration CRDs, the validation findings and the skipped security schemes. It needs no Kubernetes access
// at all -- the documents arrive in the request -- so its ServiceAccount carries no RBAC.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/krateo-platformops/oasgen-provider/internal/tools/loghandler"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/render/server"
	"github.com/krateo-platformops/plumbing/env"
)

// Build is stamped at link time via -ldflags "-X main.Build=...", as for the manager binary.
var Build string

func main() {
	addr := flag.String("addr", env.String("OASGEN_RENDER_ADDR", ":8081"), "Address to serve /render and /healthz on.")
	maxBody := flag.Int64("max-body-bytes", int64(env.Int("OASGEN_RENDER_MAX_BODY_BYTES", int(server.DefaultMaxBodyBytes))), "Largest /render request body accepted.")
	debug := flag.Bool("debug", env.Bool("OASGEN_RENDER_DEBUG", false), "Run with debug logging.")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	// The handler stamps service.name=oasgen-provider (the image's service); component tells the two
	// processes apart.
	log := slog.New(loghandler.NewJSONHandler(level, os.Stderr)).With("component", "oasgen-render")

	h := server.New(log)
	h.MaxBodyBytes = *maxBody

	srv := &http.Server{
		Addr:              *addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		// Generation over a large vendor document takes seconds, not minutes.
		WriteTimeout: 2 * time.Minute,
		IdleTimeout:  2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Error("shutdown", "error", err)
		}
	}()

	log.Info("oasgen-render listening", "addr", *addr, "build", Build, "maxBodyBytes", *maxBody)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serving", "error", err)
		os.Exit(1)
	}
}
