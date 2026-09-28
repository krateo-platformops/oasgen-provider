package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/go-logr/logr"

	"github.com/krateo-platformops/oasgen-provider/internal/controllers"
	"github.com/krateo-platformops/oasgen-provider/internal/tools/loghandler"
	"github.com/krateo-platformops/plumbing/env"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/krateo-platformops/oasgen-provider/apis"
	"github.com/krateo-platformops/provider-runtime/pkg/controller"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/krateo-platformops/provider-runtime/pkg/ratelimiter"
	"github.com/krateo-platformops/provider-runtime/pkg/telemetry"

	oteltelemetry "github.com/krateo-platformops/oasgen-provider/internal/tools/telemetry"
	"github.com/stoewer/go-strcase"
)

// Build is stamped at link time via -ldflags "-X main.Build=..." from the Dockerfile's BUILD_VERSION
// ARG, which the release workflow supplies. Empty in a plain `go build`, which is fine: the fallback
// below simply does not fire and SERVICE_VERSION behaves as it always did.
var Build string

const (
	providerName              = "oasgen"
	defaultOtelExportInterval = 30 * time.Second
)

func main() {
	envVarPrefix := fmt.Sprintf("%s_PROVIDER", strcase.UpperSnakeCase(providerName))

	debug := flag.Bool("debug", env.Bool(fmt.Sprintf("%s_DEBUG", envVarPrefix), false), "Run with debug logging.")
	syncPeriod := flag.Duration("sync", env.Duration(fmt.Sprintf("%s_SYNC", envVarPrefix), time.Hour*1), "Controller manager sync period such as 300ms, 1.5h, or 2h45m")
	pollInterval := flag.Duration("poll", env.Duration(fmt.Sprintf("%s_POLL_INTERVAL", envVarPrefix), time.Minute*3), "Poll interval controls how often an individual resource should be checked for drift.")
	maxReconcileRate := flag.Int("max-reconcile-rate", env.Int(fmt.Sprintf("%s_MAX_RECONCILE_RATE", envVarPrefix), 3), "The number of concurrent reconciles for each controller. This is the maximum number of resources that can be reconciled at the same time.")
	leaderElection := flag.Bool("leader-election", env.Bool(fmt.Sprintf("%s_LEADER_ELECTION", envVarPrefix), false), "Use leader election for the controller manager.")
	metricsEnabled := flag.Bool("otel-enabled", env.Bool("OTEL_ENABLED", false), "Enable OTLP metrics export for provider-runtime telemetry.")
	tracingEnabled := flag.Bool("otel-tracing-enabled", env.Bool("OTEL_TRACING_ENABLED", false), "Enable OTLP trace export (distributed reconcile traces).")
	metricsServiceName := flag.String("otel-service-name", fmt.Sprintf("%s-provider", strcase.KebabCase(providerName)), "The service name attached to exported OTLP metrics/traces.")
	metricsExportInterval := flag.Duration("otel-export-interval", env.Duration("OTEL_EXPORT_INTERVAL", defaultOtelExportInterval), "The interval used to export OTLP metrics.")
	maxErrorRetryInterval := flag.Duration("max-error-retry-interval", env.Duration(fmt.Sprintf("%s_MAX_ERROR_RETRY_INTERVAL", envVarPrefix), 1*time.Minute), "The maximum interval between retries when an error occurs. This should be less than the half of the poll interval.")
	minErrorRetryInterval := flag.Duration("min-error-retry-interval", env.Duration(fmt.Sprintf("%s_MIN_ERROR_RETRY_INTERVAL", envVarPrefix), 1*time.Second), "The minimum interval between retries when an error occurs. This should be less than max-error-retry-interval.")

	flag.Parse()

	log.Default().SetOutput(os.Stderr)

	logLevel := slog.LevelInfo
	if *debug {
		logLevel = slog.LevelDebug
	}

	// Emit logs as one JSON object per line (RFC3339Nano UTC `timestamp` plus a
	// `service` attribute) so they can be ingested by logs-ingester. The handler
	// also bridges any active span's trace_id/span_id onto each record.
	logrlog := logr.FromSlogHandler(loghandler.NewJSONHandler(logLevel, os.Stderr))
	logger := logging.NewLogrLogger(logrlog)

	// Set the logger for controller-runtime. This only has to log in INFO level as all debug logs are handled by our logger above.
	ctrl.SetLogger(logr.FromSlogHandler(loghandler.NewJSONHandler(slog.LevelInfo, os.Stderr)))

	if maxErrorRetryInterval.Seconds() == 0 {
		retryInterval := (*pollInterval / 2)
		maxErrorRetryInterval = &retryInterval
	} else if maxErrorRetryInterval.Seconds() >= pollInterval.Seconds() {
		retryInterval := (*pollInterval / 2)
		maxErrorRetryInterval = &retryInterval

		logger.Info("[WARNING] max-error-retry-interval is greater than or equal to poll interval, setting to half of poll interval", "max-error-retry-interval", maxErrorRetryInterval.String())
	}

	if minErrorRetryInterval.Seconds() >= maxErrorRetryInterval.Seconds() {
		retryInterval := 1 * time.Second
		minErrorRetryInterval = &retryInterval

		logger.Info("[WARNING] min-error-retry-interval is greater than or equal to max-error-retry-interval, setting to 1 second", "min-error-retry-interval", minErrorRetryInterval.String())
	}

	logger.Debug("Starting",
		"sync-period", syncPeriod.String(),
		"poll-interval", pollInterval.String(),
		"max-reconcile-rate", *maxReconcileRate,
		"leader-election", *leaderElection,
		"max-error-retry-interval", maxErrorRetryInterval.String(),
		"min-error-retry-interval", minErrorRetryInterval.String(),
		"otel-enabled", *metricsEnabled,
		"otel-service-name", *metricsServiceName,
		"otel-export-interval", metricsExportInterval.String())

	telemetryEnabled := *metricsEnabled
	telemetryExportInterval := *metricsExportInterval

	// Identify BOTH the metrics (provider-runtime) and trace pipelines via the standard OTel env
	// BEFORE telemetry.Setup runs: provider-runtime builds its metrics resource from
	// resource.Default() and IGNORES cfg.ServiceName, so service.name/version must arrive via
	// OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES (both read by resource.Default at Setup time).
	// This keeps the metrics and trace resources identical. oasgen-provider is the operator, so it
	// carries no composition-id.
	os.Setenv("OTEL_SERVICE_NAME", *metricsServiceName)
	// SERVICE_VERSION wins if the deployment sets it; otherwise fall back to the stamp compiled in at
	// build time. The fallback is what makes this work with no chart change, and it carries the BUILD
	// rather than the chart version -- which is the point: a chart version cannot distinguish two images
	// built from different commits at the same release (#127).
	if os.Getenv("SERVICE_VERSION") == "" && Build != "" {
		os.Setenv("SERVICE_VERSION", Build)
	}
	if sv := os.Getenv("SERVICE_VERSION"); sv != "" {
		attrs := "service.version=" + sv
		if existing := os.Getenv("OTEL_RESOURCE_ATTRIBUTES"); existing != "" {
			attrs = existing + "," + attrs
		}
		os.Setenv("OTEL_RESOURCE_ATTRIBUTES", attrs)
	}

	telemetryMetrics, telemetryShutdown, err := telemetry.Setup(context.Background(), logger, telemetry.Config{
		Enabled:        telemetryEnabled,
		ServiceName:    *metricsServiceName,
		ExportInterval: telemetryExportInterval,
	})
	if err != nil {
		logger.Error(err, "Cannot initialize OpenTelemetry metrics")
		os.Exit(1)
	}
	defer func() {
		if err := telemetryShutdown(context.Background()); err != nil {
			logger.Error(err, "Cannot shutdown OpenTelemetry metrics")
		}
	}()

	// Distributed reconcile traces (gated OTEL_TRACING_ENABLED, default off). Reuses the
	// OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES set above, so the trace + metrics resources
	// are identical (same service.name/version).
	tracingShutdown, err := oteltelemetry.SetupTracing(context.Background(), logger, *tracingEnabled)
	if err != nil {
		logger.Error(err, "Cannot initialize OpenTelemetry tracing")
		os.Exit(1)
	}
	defer func() {
		if err := tracingShutdown(context.Background()); err != nil {
			logger.Error(err, "Cannot shutdown OpenTelemetry tracing")
		}
	}()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		logger.Error(err, "Cannot get API server rest config")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		LeaderElection:   *leaderElection,
		LeaderElectionID: fmt.Sprintf("leader-election-%s-provider", strcase.KebabCase(providerName)),
		Cache: cache.Options{
			SyncPeriod: syncPeriod,
		},
		Metrics: metricsserver.Options{
			BindAddress: ":8080",
		},
	})
	if err != nil {
		logger.Error(err, "Cannot create controller manager")
		os.Exit(1)
	}

	o := controller.Options{
		Logger:                  logger,
		MaxConcurrentReconciles: *maxReconcileRate,
		PollInterval:            *pollInterval,
		GlobalRateLimiter:       ratelimiter.NewGlobalExponential(*minErrorRetryInterval, *maxErrorRetryInterval),
		// QueueWaitRecorder feeds the provider_runtime.reconcile queue-wait series.
		// telemetryMetrics is a working no-op recorder when OTEL_ENABLED is false.
		QueueWaitRecorder: telemetryMetrics,
	}

	if err := apis.AddToScheme(mgr.GetScheme()); err != nil {
		logger.Error(err, "Cannot add APIs to scheme")
		os.Exit(1)
	}
	if err := controllers.Setup(mgr, o, telemetryMetrics); err != nil {
		logger.Error(err, "Cannot setup controllers")
		os.Exit(1)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "Cannot start controller manager")
		os.Exit(1)
	}
}
