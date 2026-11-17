package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/krateo-platformops/audit"
	"github.com/krateo-platformops/plumbing/env"
	"github.com/krateo-platformops/plumbing/kubeutil/event"
	"github.com/krateo-platformops/plumbing/kubeutil/eventrecorder"
	"github.com/krateo-platformops/plumbing/ptr"
	"github.com/krateo-platformops/unstructured-runtime/pkg/controller/builder"
	"github.com/krateo-platformops/unstructured-runtime/pkg/logging"
	"github.com/krateo-platformops/unstructured-runtime/pkg/metrics/server"
	"github.com/krateo-platformops/unstructured-runtime/pkg/telemetry"
	logglobal "go.opentelemetry.io/otel/log/global"

	restResources "github.com/krateo-platformops/rest-dynamic-controller/internal/controllers"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/authn"
	restclient "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/client"
	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/hostpolicy"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/snowplow"
	"github.com/krateo-platformops/unstructured-runtime/pkg/controller"
	"github.com/krateo-platformops/unstructured-runtime/pkg/pluralizer"
	"github.com/krateo-platformops/unstructured-runtime/pkg/workqueue"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	serviceName               = "rest-dynamic-controller"
	defaultOtelExportInterval = 30 * time.Second
	// auditScopeName is the OTel instrumentation scope this controller's audit rows carry. It is the
	// MODULE PATH plus "/audit", matching every other writer in the platform; see SetAuditEmitter.
	auditScopeName = "github.com/krateo-platformops/rest-dynamic-controller/audit"
)

// otelExportState names what the endpoint and the three channel switches add up to.
//
// EXTRACTED SO A TEST CAN ASSERT THE DECISION ITSELF. The first version of this lived inline in a switch
// in main(), and its test re-implemented the same switch -- which asserts a copy and proves nothing about
// the code that runs. Taking the decision out is the difference between a test of this logic and a test
// of the test's own arithmetic.
func otelExportState(hasEndpoint, metrics, traces, logs bool) string {
	anyChannel := metrics || traces || logs
	switch {
	case hasEndpoint && !anyChannel:
		return otelEndpointWithoutChannel
	case anyChannel && !hasEndpoint:
		return otelChannelWithoutEndpoint
	default:
		return otelExportConsistent
	}
}

const (
	// The endpoint says WHERE and nothing about WHETHER: a cluster that looks instrumented, exporting
	// nothing. 34 generated controllers on krateo-057 were in this state (issue 729).
	otelEndpointWithoutChannel = "endpoint-without-channel"
	// Telemetry is produced and discarded. WORSE than the above, because a consumer sees no rows and
	// cannot distinguish "export broken" from "nothing happened" -- an audit trail that stops arriving
	// reads as an absence of activity.
	otelChannelWithoutEndpoint = "channel-without-endpoint"
	// Either both sides are configured, or neither is. Neither is a misconfiguration.
	otelExportConsistent = "consistent"
)

// otelEndpointForLog reports whether an OTLP endpoint is configured, WITHOUT printing it.
//
// The value itself is withheld deliberately: on a node-local collector it is derived from the pod's
// status.hostIP, so logging it would put a node address into every generated controller's startup line
// on every restart -- 34 of them here. "set" and "unset" is the whole of what a reader needs to decide
// whether the switches above can do anything, and it is the only part that cannot be guessed.
func otelEndpointForLog() string {
	if env.String("OTEL_EXPORTER_OTLP_ENDPOINT", "") == "" {
		return "unset"
	}
	return "set"
}

var (
	Build string
)

// generatedControllerLeaseName builds the Lease name for one generated controller.
//
// IT MUST EQUAL THE GENERATED ROLE'S NAME. assets/rdc/rbac/role.yaml is
// `{{ .resource }}-{{ .apiGroup }}-{{ .apiVersion }}` and grants the Lease by that resourceName, so this
// is the Go side of one agreement rather than a second convention. Extracted as a function so a test can
// assert it against the template's text; inline it and the agreement becomes untestable.
//
// An apiGroup contains dots, which are legal in a Kubernetes object name -- and the Role itself has used
// this exact shape as its own name since before this change, so it is proven as a name rather than
// assumed.
func generatedControllerLeaseName(resource, apiGroup, apiVersion string) string {
	return fmt.Sprintf("%s-%s-%s", resource, apiGroup, apiVersion)
}

func main() {
	// Flags
	kubeconfig := flag.String("kubeconfig",
		env.String("KUBECONFIG", ""),
		"absolute path to the kubeconfig file")
	debug := flag.Bool("debug",
		env.Bool("REST_CONTROLLER_DEBUG", false),
		"dump verbose output")
	prettyJSONDebug := flag.Bool("pretty-json-debug",
		env.Bool("REST_CONTROLLER_PRETTY_JSON_DEBUG", false),
		"globally enable pretty-print JSON formatting in HTTP debug output (response bodies)")
	// #379: the operator half of the endpoint host policy. BOTH DEFAULT TO THE NO-OP: an empty allowlist
	// means "no override permitted; use the hosts the OAS document itself names", which is where requests
	// already go -- so an existing install needs no change and is still fixed. Requiring a value would
	// turn every single-host provider into a config error on upgrade for a capability it does not use.
	endpointAllowlist := flag.String("endpoint-allowlist",
		env.String("REST_CONTROLLER_ENDPOINT_ALLOWLIST", ""),
		"Comma-separated hosts a CREDENTIALED request may reach, in addition to the hosts the OAS document "+
			"names. Entries are host[:port], optionally prefixed http:// (no prefix means https only), and "+
			"may use a single leading *. wildcard. Empty (the default) permits only the document's hosts. "+
			"A comma is safe as the separator because a host[:port] never contains one.")
	// Separate from the allowlist rather than a `*` entry, so "permit anything" shows up in a values diff
	// instead of hiding as punctuation in a list. The unconditional refusals -- loopback, link-local,
	// multicast, unspecified -- still apply.
	allowAnyHost := flag.Bool("allow-any-host",
		env.Bool("REST_CONTROLLER_ALLOW_ANY_HOST", false),
		"Disable endpoint-allowlist matching entirely. The unconditional refusals (loopback, link-local, "+
			"multicast, unspecified) still apply.")
	snowplowURL := flag.String("snowplow-url",
		env.String("URL_SNOWPLOW", ""),
		"snowplow base URL for resolving observeApiRef RESTActions; empty disables observeApiRef resolution")
	authnURL := flag.String("authn-url",
		env.String("URL_AUTHN", ""),
		"authn base URL for exchanging the projected ServiceAccount token for a service JWT (empty = call snowplow unauthenticated)")
	saTokenPath := flag.String("serviceaccount-token-path",
		env.String("REST_CONTROLLER_SERVICEACCOUNT_TOKEN_PATH", authn.DefaultTokenPath),
		"path to the projected (authn-audience) ServiceAccount token used to authenticate to authn")
	workers := flag.Int("workers",
		env.Int("REST_CONTROLLER_WORKERS", 5),
		"number of workers")
	resyncInterval := flag.Duration("resync-interval",
		env.Duration("REST_CONTROLLER_RESYNC_INTERVAL", time.Minute*3),
		"resync interval")
	requestTimeout := flag.Duration("request-timeout",
		env.Duration("REST_CONTROLLER_REQUEST_TIMEOUT", restclient.DefaultRequestTimeout),
		"timeout for an external API request whose call site sets none -- every create, update, delete, "+
			"get, findby and dispatch. The async poll and lookup runner set their own and are unaffected")
	resourceGroup := flag.String("group",
		env.String("REST_CONTROLLER_GROUP", ""),
		"resource api group")
	// labelSelector scopes this controller's watch to the instances its version owns.
	//
	// Every served CRD version runs its own controller, and selecting by served apiVersion cannot separate
	// them: every served endpoint returns every object. The krateo.io/oas-version label is the contract
	// instead, and oasgen-provider computes the selector because it is the side that knows the whole
	// version topology (crd/generation.VersionSelector).
	//
	// Empty means watch everything, which is correct when only one version is served -- every instance is
	// then either unlabelled or labelled with it.
	labelSelector := flag.String("label-selector",
		env.String("REST_CONTROLLER_LABEL_SELECTOR", ""),
		"Label selector scoping this controller to the instances its CRD version owns.")
	resourceVersion := flag.String("version",
		env.String("REST_CONTROLLER_VERSION", ""),
		"resource api version")
	resourceName := flag.String("resource",
		env.String("REST_CONTROLLER_RESOURCE", ""),
		"resource plural name")
	// ownership makes this controller reconcile only while it holds a coordination.k8s.io Lease, so a
	// second process for the same Kind x version waits instead of reconciling alongside (issue 771).
	//
	// DEFAULT TRUE, because this is a correctness setting and not an availability one. A rolling update is
	// two writers by construction -- the old pod and the new one both reconcile until the old terminates --
	// so an opt-in default would leave the ordinary case unprotected. Same reasoning as the provider's own
	// leader election, which #781 defaulted on for the same reason.
	//
	// It needs the namespaced Lease grant in the generated Role, and POD_NAME / POD_NAMESPACE from the
	// downward API. Both ship in the same change; see the EXIT below for what happens when they do not.
	ownership := flag.Bool("ownership",
		env.Bool("REST_CONTROLLER_OWNERSHIP", true),
		"Hold a coordination.k8s.io Lease while reconciling, so only one process per Kind x version writes.")
	namespace := flag.String("namespace",
		env.String("REST_CONTROLLER_NAMESPACE", ""),
		"namespace to watch, empty for all namespaces")
	maxErrorRetryInterval := flag.Duration("max-error-retry-interval",
		env.Duration("REST_CONTROLLER_MAX_ERROR_RETRY_INTERVAL", 90*time.Second),
		"The maximum interval between retries when an error occurs. This should be less than the half of the resync interval.")
	minErrorRetryInterval := flag.Duration("min-error-retry-interval",
		env.Duration("REST_CONTROLLER_MIN_ERROR_RETRY_INTERVAL", 1*time.Second),
		"The minimum interval between retries when an error occurs. This should be less than max-error-retry-interval.")
	maxErrorRetry := flag.Int("max-error-retries",
		env.Int("REST_CONTROLLER_MAX_ERROR_RETRIES", 5),
		"How many times to retry the processing of a resource when an error occurs before giving up and dropping the resource.")
	metricsServerPort := flag.Int("metrics-server-port",
		env.Int("REST_CONTROLLER_METRICS_SERVER_PORT", 0),
		"The address to bind the metrics server to. If empty, metrics server is disabled.")
	otelEnabled := flag.Bool("otel-enabled",
		env.Bool("OTEL_ENABLED", false),
		"Enable OTLP metrics export for provider-runtime reconcile telemetry.")
	otelTracingEnabled := flag.Bool("otel-tracing-enabled",
		env.Bool("OTEL_TRACING_ENABLED", false),
		"Enable OTLP trace export (distributed reconcile traces).")
	otelServiceName := flag.String("otel-service-name",
		env.String("OTEL_SERVICE_NAME", serviceName),
		"The service name attached to exported OTLP metrics/traces.")
	otelExportInterval := flag.Duration("otel-export-interval",
		env.Duration("OTEL_EXPORT_INTERVAL", defaultOtelExportInterval),
		"The interval used to export OTLP metrics.")
	// Separate from -otel-tracing-enabled on purpose. Audit events are a COMPLIANCE signal, not
	// diagnostics: an operator may want them exported continuously while traces stay off, and may
	// equally want traces while debugging without shipping audit records. Tying them to one flag would
	// force a choice between the two that nothing about the implementation requires.
	otelLogsEnabled := flag.Bool("otel-logs-enabled",
		env.Bool("OTEL_LOGS_ENABLED", false),
		"Enable OTLP log export, which carries the Krateo AuditEvent for each external write (C8).")
	deploymentName := flag.String("deployment-name",
		env.String("DEPLOYMENT_NAME", ""),
		"The deployment name for stable resource identification in metrics.")
	// SERVICE_VERSION wins if the deployment sets it; otherwise fall back to the stamp compiled in at
	// build time (main.Build, set from the Dockerfile's BUILD_VERSION ARG). Build was already compiled
	// in and only logged at startup, so the value existed but never reached telemetry -- service.version
	// stayed empty and the collector filled it from the chart label, which cannot distinguish two images
	// built from different commits at the same release (#127).
	serviceVersion := env.String("SERVICE_VERSION", "") // image version, stamped as service.version on metrics/traces
	if serviceVersion == "" {
		serviceVersion = Build
	}

	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "Flags:")
		flag.PrintDefaults()
	}

	flag.Parse()

	// Set BEFORE any UnstructuredClient exists, and never again. See restclient.DefaultRequestTimeout for
	// why this is a package var rather than a field threaded through NewHandler.
	if *requestTimeout > 0 {
		restclient.DefaultRequestTimeout = *requestTimeout
	}

	// THE STARTUP HARD-FAIL ON THIS CONTROLLER'S OWN ServiceAccount IDENTITY IS GONE (#540).
	//
	// It existed because RDC self-provisioned secretRef RBAC bound to that identity (#31) and could not
	// discover it alone, so a version-skew deployment had to fail loud rather than run with secretRef
	// silently broken. oasgen-provider provisions the grant now (#510), RDC binds nothing, and the
	// identity is no longer read anywhere -- so the check was about to become a crash-loop over a value
	// nothing used. REST_CONTROLLER_SERVICEACCOUNT_NAME/_NAMESPACE are now ignored if set; they are
	// environment keys rather than args, so an older chart passing them cannot fail startup.
	//
	// The provider still needs its OWN copy of this identity, for the auth-secret Role's RoleBinding
	// subject. That is rendered provider-side and is unaffected.

	logLevel := slog.LevelInfo
	if *debug {
		logLevel = slog.LevelDebug
	}

	// JSON logs on stderr in the OTel log model (RFC3339Nano "timestamp", SeverityText +
	// SeverityNumber, trace_id/span_id when a reconcile span is in context), compatible with
	// logs-ingester. The shared handler lives in unstructured-runtime (pkg/logging) so every dynamic
	// controller is consistent; "service" is kept alongside the OTel "service.name".
	slogHandler := logging.NewOTelJSONHandler(logLevel, os.Stderr,
		slog.String("service.name", serviceName),
		slog.String("service", serviceName),
	)

	log := logging.NewLogrLogger(logr.FromSlogHandler(slog.New(slogHandler).Handler()))

	// Kubernetes configuration
	var cfg *rest.Config
	var err error
	if len(*kubeconfig) > 0 {
		cfg, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	} else {
		cfg, err = builder.GetConfig()
	}
	if err != nil {
		log.Error(err, "Building kubeconfig.")
		os.Exit(1)
	}

	var handler controller.ExternalClient

	pluralizer := pluralizer.New()
	var swg getter.Getter
	swg, err = getter.Dynamic(cfg, pluralizer)
	if err != nil {
		log.Error(err, "Creating definition getter.")
		os.Exit(1)
	}

	log.WithValues("build", Build).
		WithValues("debug", *debug).
		WithValues("prettyJSONDebug", *prettyJSONDebug).
		WithValues("resyncInterval", *resyncInterval).
		WithValues("group", *resourceGroup).
		WithValues("version", *resourceVersion).
		WithValues("resource", *resourceName).
		WithValues("namespace", *namespace).
		WithValues("maxErrorRetryInterval", *maxErrorRetryInterval).
		WithValues("minErrorRetryInterval", *minErrorRetryInterval).
		WithValues("maxErrorRetry", *maxErrorRetry).
		WithValues("workers", *workers).
		WithValues("otelEnabled", *otelEnabled).
		WithValues("otelTracingEnabled", *otelTracingEnabled).
		WithValues("otelServiceName", *otelServiceName).
		WithValues("otelExportInterval", *otelExportInterval).
		WithValues("otelLogsEnabled", *otelLogsEnabled).
		WithValues("deploymentName", *deploymentName).
		WithValues("otelExporterEndpoint", otelEndpointForLog()).
		Info("Starting.", "serviceName", serviceName)

	// THE ENDPOINT IS NOT THE SWITCH, AND THE "Starting." LINE ABOVE COULD NOT SAY SO (issue 729).
	//
	// All three channel switches were already on that line. What was missing is the ENDPOINT, and the
	// relationship between them: OTEL_EXPORTER_OTLP_ENDPOINT says WHERE and says nothing about WHETHER.
	// 34 generated controllers on krateo-057 had the endpoint set, every channel defaulting false, and
	// exported nothing -- while "Metrics server disabled" (a different thing: the Prometheus scrape
	// listener) read to an operator as the OTLP export being off for a reason they understood.
	//
	// WHY A SEPARATE LINE RATHER THAN MORE FIELDS. The "Starting." line carries ~18 values. A reader
	// cannot see a RELATIONSHIP between five of them, which is the whole defect: each field was already
	// correct and the conclusion took reading all five and knowing that the endpoint is not a switch.
	// These lines state the conclusion.
	//
	// BOTH DIRECTIONS ARE REAL AND THEY FAIL DIFFERENTLY:
	//
	//	endpoint set, every channel off   -> a cluster that looks instrumented and exports nothing.
	//	                                     Silent. This is what 057 was doing.
	//	a channel on, no endpoint         -> rows are GENERATED and go nowhere. Worse, because the
	//	                                     consumer cannot distinguish "export broken" from
	//	                                     "nothing happened" -- an audit trail that stops arriving
	//	                                     reads as an absence of activity.
	//
	// Warn, not Info: each is a configuration that does not do what its author intended.
	hasEndpoint := env.String("OTEL_EXPORTER_OTLP_ENDPOINT", "") != ""
	switch otelExportState(hasEndpoint, *otelEnabled, *otelTracingEnabled, *otelLogsEnabled) {
	case otelEndpointWithoutChannel:
		log.Warn("OTEL_EXPORTER_OTLP_ENDPOINT is set but every export channel is off, so nothing is "+
			"exported. The endpoint says WHERE, not WHETHER: set OTEL_ENABLED (metrics), "+
			"OTEL_TRACING_ENABLED (traces) or OTEL_LOGS_ENABLED (the audit trail) to export anything",
			"endpoint", otelEndpointForLog())
	case otelChannelWithoutEndpoint:
		log.Warn("An export channel is enabled but OTEL_EXPORTER_OTLP_ENDPOINT is unset, so telemetry "+
			"is produced and discarded. A consumer sees no rows, which is indistinguishable from no "+
			"activity rather than from a misconfiguration",
			"metrics", *otelEnabled, "traces", *otelTracingEnabled, "logs", *otelLogsEnabled)
	default:
		log.Debug("OTLP export configuration is self-consistent",
			"endpoint", otelEndpointForLog(), "metrics", *otelEnabled,
			"traces", *otelTracingEnabled, "logs", *otelLogsEnabled)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), []os.Signal{
		os.Interrupt,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGKILL,
		syscall.SIGHUP,
		syscall.SIGQUIT,
	}...)
	defer cancel()

	// OpenTelemetry: everything is gated OFF by default and is a no-op when the
	// OTEL_* env/flags are unset — no exporter, no provider swap, byte-identical
	// behavior. Tag this controller's telemetry resource with the GVR it manages so
	// metrics/traces are attributable to a specific dynamic CR type.
	gvrAttr := fmt.Sprintf("krateo.io/rest-gvr=%s/%s/%s", *resourceGroup, *resourceVersion, *resourceName)
	if existing := os.Getenv("OTEL_RESOURCE_ATTRIBUTES"); existing != "" {
		gvrAttr = existing + "," + gvrAttr
	}
	os.Setenv("OTEL_RESOURCE_ATTRIBUTES", gvrAttr)

	telemetryConfig := telemetry.Config{
		Enabled:        *otelEnabled,
		TracingEnabled: *otelTracingEnabled,
		ServiceName:    *otelServiceName,
		ExportInterval: *otelExportInterval,
		DeploymentName: *deploymentName,
		Version:        serviceVersion,
	}

	// OTLP reconcile-throughput metrics (provider_runtime.reconcile.* /
	// controller_reconcile_*). Disabled config returns a no-op recorder + shutdown.
	telemetryMetrics, telemetryShutdown, err := telemetry.Setup(context.Background(), log, telemetryConfig)
	if err != nil {
		log.Error(err, "Cannot initialize OpenTelemetry metrics")
		os.Exit(1)
	}
	defer telemetryShutdown(context.Background())

	// OTLP logs, which is the transport the audit emitter writes through. SetupOTLPLogs installs a
	// GLOBAL LoggerProvider, so the emitter is built from logglobal rather than from a handle returned
	// here; a disabled config installs nothing and the emitter degrades to a no-op.
	//
	// Hard-fails on error rather than continuing, matching the metrics setup above. An audit trail that
	// silently is not being written is worse than a pod that will not start: the whole point of C8 is
	// that an external write leaves a record, and "no records" and "no writes" are indistinguishable
	// after the fact.
	logsShutdown, err := telemetry.SetupOTLPLogs(context.Background(), *otelLogsEnabled, *otelServiceName)
	if err != nil {
		log.Error(err, "Cannot initialize OpenTelemetry logs")
		os.Exit(1)
	}
	defer logsShutdown(context.Background())

	// Distributed reconcile traces (gated OTEL_TRACING_ENABLED). Installs the W3C
	// propagator even when disabled so an inbound traceparent is honored; returns a
	// no-op shutdown otherwise.
	tracingShutdown, err := telemetry.SetupTracing(context.Background(), log, telemetryConfig)
	if err != nil {
		log.Error(err, "Cannot initialize OpenTelemetry tracing")
		os.Exit(1)
	}
	defer tracingShutdown(context.Background())

	// Kubernetes Event recorder for the reconciled dynamic CR (success/error
	// transitions on Create/Update/Delete).
	rec, err := eventrecorder.Create(ctx, cfg, serviceName, nil)
	if err != nil {
		log.Error(err, "Creating event recorder.")
		os.Exit(1)
	}
	apiRecorder := event.NewAPIRecorder(rec)

	hostPolicyCfg := hostpolicy.Config{
		Allowlist:    splitAllowlist(*endpointAllowlist),
		AllowAnyHost: *allowAnyHost,
	}
	if *allowAnyHost {
		// Loud, once, at startup. An operator who sets this has disabled host matching for every
		// credentialed request, and the only place that is visible otherwise is a values diff nobody
		// re-reads.
		log.Info("endpoint host matching is DISABLED (-allow-any-host): a credentialed request may be " +
			"sent to any host except loopback, link-local, multicast and unspecified")
	}

	handler = restResources.NewHandler(cfg, log, swg, *pluralizer, *prettyJSONDebug, hostPolicyCfg)
	if handler == nil {
		log.Error(fmt.Errorf("handler is nil"), "Creating handler for controller.")
		os.Exit(1)
	}
	if h, ok := handler.(interface {
		SetEventRecorder(event.Recorder)
	}); ok {
		h.SetEventRecorder(apiRecorder)
	}

	// The C8 audit emitter (#253). Built from the GLOBAL LoggerProvider that SetupOTLPLogs installed,
	// because that is the handle it publishes; with -otel-logs-enabled off nothing was installed, the
	// global is a no-op provider, and audit.Emitter is nil-safe regardless.
	if h, ok := handler.(interface {
		SetAuditEmitter(*audit.Emitter)
	}); ok {
		// THE SCOPE IS THE MODULE PATH, not the bare service name (issue 744).
		//
		// Every other writer in the platform -- core-provider, the cdc, authn, the gateway projection --
		// passes `github.com/krateo-platformops/<component>/audit`, and this passed
		// `rest-dynamic-controller/audit`. The observability session put it plainly: "a writer's scope is
		// how a reader tells writers apart", and a scope that does not match the convention makes this
		// controller's rows look like they came from somewhere else.
		//
		// Routing does NOT depend on it -- that keys on event.name=audit, which is why the rows arrived
		// correctly and the mismatch went unnoticed for several releases. So this is legibility for a
		// consumer rather than a correctness fix, and it is exactly the half of 744 that survived: the
		// schema_version half turned out not to be a defect at all (the stamp predates our pin, pinned in
		// CI by TestEmitAuditCarriesTheSchemaVersion).
		h.SetAuditEmitter(audit.New(logglobal.GetLoggerProvider().Logger(auditScopeName)))
	}

	// Wire the snowplow client used to resolve observeApiRef RESTActions under the controller's own authn
	// identity (mirrors composition-dynamic-controller). Enabled only when a snowplow URL is configured; the
	// authn client provides the Bearer JWT when an authn URL is set.
	if *snowplowURL != "" {
		var tokenFunc snowplow.TokenFunc
		// invalidateToken drops the authn client's cached JWT. Captured here because the snowplow client
		// is the only thing that sees a 401, and the authn client is the only thing that can act on it --
		// without this wiring an authn signing-key rotation leaves the controller presenting a dead token
		// until refreshSkew (#369). nil when no authn URL is set, in which case there is no cache to drop.
		var invalidateToken func()
		if *authnURL != "" {
			authnClient := authn.New(*authnURL, *saTokenPath)
			tokenFunc = authnClient.Token
			invalidateToken = authnClient.Invalidate
		}
		if h, ok := handler.(interface{ SetSnowplowClient(*snowplow.Client) }); ok {
			h.SetSnowplowClient(snowplow.New(*snowplowURL, tokenFunc).WithTokenInvalidator(invalidateToken))
			log.Info("observeApiRef resolution enabled", "snowplow", *snowplowURL, "authn", *authnURL)
		}
	}

	opts := []builder.FuncOption{
		builder.WithLogger(log),
		builder.WithNamespace(*namespace),
		builder.WithResyncInterval(*resyncInterval),
		builder.WithGlobalRateLimiter(workqueue.NewExponentialTimedFailureRateLimiter[any](*minErrorRetryInterval, *maxErrorRetryInterval)),
		builder.WithMaxRetries(*maxErrorRetry),
		builder.WithTelemetryMetrics(telemetryMetrics),
	}

	// OWNERSHIP: hold a Lease while reconciling, so one Kind x version has one writer (issue 771).
	//
	// THE LEASE NAME IS THE GENERATED ROLE'S NAME, deliberately and not coincidentally.
	// assets/rdc/rbac/role.yaml is named `{{ .resource }}-{{ .apiGroup }}-{{ .apiVersion }}` and grants
	// this Lease by that same resourceName, so the string computed here and the string the RBAC permits
	// are derived from the same three values. If they ever diverge the controller cannot acquire and
	// EXITS -- a failure with N generated controllers rather than one provider -- which is why
	// TestTheLeaseNameMatchesTheGeneratedRole asserts the Go format against the template's own text
	// rather than against a copy of it.
	//
	// THE NAMESPACE IS THE POD'S OWN, from the downward API, and NOT the -namespace flag: that one is the
	// WATCHED namespace and is empty for a cluster-wide watch, which would put the Lease in the empty
	// namespace. The pod's actual namespace cannot drift from where the pod runs; a templated guess can.
	//
	// THE IDENTITY IS THE POD NAME, which is what controller-runtime's own leader election uses and what
	// makes a holder readable in `kubectl get lease`.
	if *ownership {
		podName := env.String("POD_NAME", "")
		podNamespace := env.String("POD_NAMESPACE", "")
		if podName == "" || podNamespace == "" {
			// NOT a warn-and-continue. Ownership is a correctness setting: carrying on without the Lease
			// is exactly the concurrent-writer state it exists to prevent, and it would be invisible --
			// the controller would work, and two of them would work twice. A half-populated downward API
			// is a deployment defect, so it fails loudly here instead.
			log.Error(fmt.Errorf("POD_NAME=%q POD_NAMESPACE=%q", podName, podNamespace),
				"Ownership is enabled but the pod's own identity is not in the environment. The generated "+
					"Deployment supplies both through the downward API (fieldRef metadata.name and "+
					"metadata.namespace). Set them, or disable ownership with "+
					"REST_CONTROLLER_OWNERSHIP=false, which permits concurrent writers.")
			os.Exit(1)
		}
		leaseName := generatedControllerLeaseName(*resourceName, *resourceGroup, *resourceVersion)
		log.Info("Ownership enabled", "lease", podNamespace+"/"+leaseName, "identity", podName)
		opts = append(opts, builder.WithOwnership(podNamespace, leaseName, podName))
	} else {
		log.Info("Ownership disabled: this controller does not hold a Lease, so concurrent writers are possible")
	}

	metricsServerBindAddress := ""
	if ptr.Deref(metricsServerPort, 0) != 0 {
		log.Info("Metrics server enabled", "bindAddress", fmt.Sprintf(":%d", *metricsServerPort))
		metricsServerBindAddress = fmt.Sprintf(":%d", *metricsServerPort)
		opts = append(opts, builder.WithMetrics(server.Options{
			BindAddress: metricsServerBindAddress,
		}))
	} else {
		log.Info("Metrics server disabled")
	}

	if *labelSelector != "" {
		if _, perr := labels.Parse(*labelSelector); perr != nil {
			log.Error(perr, "Parsing label selector.", "selector", *labelSelector)
			os.Exit(1)
		}
		opts = append(opts, builder.WithListWatcher(controller.ListWatcherConfiguration{
			LabelSelector: labelSelector,
		}))
		log.Info("Watching only the instances this version owns", "labelSelector", *labelSelector)
	}

	controller, err := builder.Build(ctx, builder.Configuration{
		Config: cfg,
		GVR: schema.GroupVersionResource{
			Group:    *resourceGroup,
			Version:  *resourceVersion,
			Resource: *resourceName,
		},
		ProviderName: serviceName,
	}, opts...)
	if err != nil {
		log.Error(err, "Building controller.")
		os.Exit(1)
	}
	controller.SetExternalClient(handler)

	err = controller.Run(ctx, *workers)
	if err != nil {
		log.Error(err, "Running controller.")
		os.Exit(1)
	}
}

// splitAllowlist turns the comma-separated flag into entries, dropping empties and trimming space.
//
// A comma is a safe separator here and that is a property of the values rather than a convention: an
// allowlist entry is host[:port] with an optional scheme prefix and an optional leading "*." label, and
// none of those can contain a comma. So there is no value a user could legitimately write that this
// splitting would corrupt -- which is the question to ask of any delimiter.
//
// An empty or whitespace-only flag yields nil rather than []string{""}, because a single empty entry
// would be rejected by hostpolicy.Parse and turn the DEFAULT configuration into a startup failure.
func splitAllowlist(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
