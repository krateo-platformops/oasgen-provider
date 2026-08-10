---
type: Configuration
title: oasgen-provider — configuration
description: The whole config surface — both charts' values, the provider's env/flags, the hand-maintained RDC image pin, and the env contract of every generated RDC instance.
resource: oci://ghcr.io/krateo-platformops/charts/oasgen-provider
tags: [kog, helm, values, env]
timestamp: 2026-08-10T00:00:00Z
---

# Configuration

Two charts: `helm/oasgen-provider` (the provider Deployment + the RDC templates it
renders) and `helm/oasgen-provider-crds` (the `RestDefinition` CRD only, no values
beyond the schema stub). Both ship a `values.schema.json`; unknown keys are rejected.

## Provider chart values (top level)

Standard chart scaffolding (`replicaCount`, `serviceAccount.*`, `podAnnotations`,
`podSecurityContext`, `securityContext`, `service.*`, `ingress.*`, `resources`,
`autoscaling.*`, `volumes`/`volumeMounts` — a writable `/tmp` emptyDir is mounted by
default — `nodeSelector`, `tolerations`, `affinity`), plus:

| Key | Default | Effect |
|---|---|---|
| `image.registry` / `image.repository` | `ghcr.io` / `krateo-platformops/oasgen-provider` | The provider image; `image.tag` defaults to the chart `appVersion`. |
| `global.imageRegistry` | `""` | When set, overrides the registry for **both** the provider and RDC images (mirror / air-gapped installs). |
| `env.*` | `OASGEN_PROVIDER_DEBUG: "false"`, `OASGEN_PROVIDER_MAX_RECONCILE_RATE: 1` | Rendered verbatim into the provider's ConfigMap and injected via `envFrom` — any `OASGEN_PROVIDER_*` / `OTEL_*` variable below can be set here. |
| `rdc.*` | see below | Everything about the generated rest-dynamic-controller instances. |

The pod template carries a `checksum/configmap` annotation over the provider ConfigMap
**and** all three RDC template ConfigMaps, so any config or template change rolls the
provider.

Note: the chart's Deployment passes `--debug` on the container args unconditionally, so
under the stock chart the provider runs with debug logging regardless of
`env.OASGEN_PROVIDER_DEBUG`.

## Provider environment variables / flags

From `go/oasgen-provider/main.go` (every env var has a flag twin):

| Env (flag) | Default | Description |
|---|---|---|
| `OASGEN_PROVIDER_DEBUG` (`--debug`) | `false` | Debug logging (JSON slog; log lines carry `service` and any active trace/span ids). |
| `OASGEN_PROVIDER_SYNC` (`--sync`) | `1h` | Controller-manager sync period. |
| `OASGEN_PROVIDER_POLL_INTERVAL` (`--poll`) | `3m` | How often each RestDefinition is re-checked for drift. |
| `OASGEN_PROVIDER_MAX_RECONCILE_RATE` (`--max-reconcile-rate`) | `3` (chart ConfigMap sets `1`) | Concurrent reconciles per controller. |
| `OASGEN_PROVIDER_LEADER_ELECTION` (`--leader-election`) | `false` | Leader election for the controller manager. |
| `OASGEN_PROVIDER_MAX_ERROR_RETRY_INTERVAL` (`--max-error-retry-interval`) | `1m` | Max backoff between error retries; forced to `poll/2` when ≥ poll interval. |
| `OASGEN_PROVIDER_MIN_ERROR_RETRY_INTERVAL` (`--min-error-retry-interval`) | `1s` | Min backoff; forced to `1s` when ≥ the max. |
| `OTEL_ENABLED` (`--otel-enabled`) | `false` | OTLP metrics export (provider-runtime telemetry). |
| `OTEL_TRACING_ENABLED` (`--otel-tracing-enabled`) | `false` | OTLP trace export (distributed reconcile traces). |
| `OTEL_EXPORT_INTERVAL` (`--otel-export-interval`) | `30s` | OTLP metrics export interval. |
| (`--otel-service-name`) | `oasgen-provider` | `service.name` on exported metrics/traces (propagated via `OTEL_SERVICE_NAME`). |
| `SERVICE_VERSION` | — | When set, appended as `service.version` to `OTEL_RESOURCE_ATTRIBUTES`. |

## `rdc.*` — the generated controllers

The provider renders `helm/oasgen-provider/assets/rdc/` (Deployment, ConfigMap, RBAC —
mounted into the pod as ConfigMaps) once per RestDefinition. The `rdc.*` values
parameterize every instance:

| Key | Default | Effect |
|---|---|---|
| `rdc.image.registry` / `rdc.image.repository` | `ghcr.io` / `krateo-platformops/rest-dynamic-controller` | The RDC image. |
| `rdc.image.tag` | `""` → the chart `appVersion` | **Derived, not pinned.** Empty means the RDC template resolves the tag from `.Chart.AppVersion` (`assets/rdc/deployment.yaml`), so RDC tracks the chart's own version line automatically. This replaced a hand-maintained pin that had to be bumped by hand every release and silently shipped a chart referencing a never-published image ([#62](https://github.com/krateo-platformops/oasgen-provider/issues/62)). It is load-bearing that oasgen-provider and rest-dynamic-controller release in **lockstep at identical versions** — they ship from one tag, so the derived tag always exists. Set an explicit value only to pin an RDC out of lockstep (it must be ≥ `0.12.0`, when the SA-identity env contract landed). |
| `rdc.env.*` | `{}` | Extra env rendered into **every** generated RDC ConfigMap (any `REST_CONTROLLER_*` / `OTEL_*` var below). |
| `rdc.replicaCount`, `rdc.serviceAccount.*`, `rdc.podSecurityContext`, `rdc.securityContext`, `rdc.service.port`, `rdc.resources`, `rdc.autoscaling.*`, … | chart scaffolding | Applied to every generated RDC Deployment. |

Each generated ConfigMap also fixes `HOME: /tmp` (helm needs a writable home) and injects
the RDC's own identity — `REST_CONTROLLER_SERVICEACCOUNT_NAME` /
`REST_CONTROLLER_SERVICEACCOUNT_NAMESPACE` — which RDC uses to authenticate to
authn/snowplow for delegated `*ApiRef` verbs.

## RDC environment variables / flags

From `go/rest-dynamic-controller/main.go`. The group/version/resource triple is passed as
container args by the rendered Deployment; the rest can be set via `rdc.env.*`:

| Env (flag) | Default | Description |
|---|---|---|
| `REST_CONTROLLER_DEBUG` (`--debug`) | `false` | Verbose output. |
| `REST_CONTROLLER_PRETTY_JSON_DEBUG` (`--pretty-json-debug`) | `false` | Pretty-print JSON bodies in HTTP debug output. |
| `REST_CONTROLLER_WORKERS` (`--workers`) | `5` | Worker threads. |
| `REST_CONTROLLER_RESYNC_INTERVAL` (`--resync-interval`) | `3m` | Interval between resyncs. |
| `REST_CONTROLLER_GROUP` / `_VERSION` / `_RESOURCE` (`--group`/`--version`/`--resource`) | — | The GVR this instance manages (set as args by the rendered Deployment). |
| `REST_CONTROLLER_NAMESPACE` (`--namespace`) | `""` (all namespaces) | Namespace to watch. |
| `REST_CONTROLLER_MAX_ERROR_RETRY_INTERVAL` (`--max-error-retry-interval`) | `90s` | Max backoff between retries; keep under half the resync interval. |
| `REST_CONTROLLER_MIN_ERROR_RETRY_INTERVAL` (`--min-error-retry-interval`) | `1s` | Min backoff. |
| `REST_CONTROLLER_MAX_ERROR_RETRIES` (`--max-error-retries`) | `5` | Retries before a resource is dropped. |
| `REST_CONTROLLER_METRICS_SERVER_PORT` (`--metrics-server-port`) | unset (disabled) | Metrics server port. |
| `REST_CONTROLLER_SERVICEACCOUNT_NAME` / `_NAMESPACE` | injected by the rendered ConfigMap | **Required** — RDC's own SA identity, used as the RoleBinding subject for self-provisioned `secretRef` RBAC and for delegated-verb auth. Startup fails loud without it. |
| `REST_CONTROLLER_SERVICEACCOUNT_TOKEN_PATH` (`--serviceaccount-token-path`) | in-pod default | Where the SA token is read from. |
| `URL_SNOWPLOW` (`--snowplow-url`) / `URL_AUTHN` (`--authn-url`) | `""` | Snowplow /call and authn endpoints, needed only when `observeApiRef`/`createApiRef`/`updateApiRef`/`deleteApiRef` are used. |
| `OTEL_ENABLED` / `OTEL_TRACING_ENABLED` (`--otel-enabled`/`--otel-tracing-enabled`) | `false` | OTLP metrics / trace export. The W3C propagator is installed **even when tracing is off**, so an inbound `traceparent` is still honored. |
| `OTEL_SERVICE_NAME` (`--otel-service-name`) | `rest-dynamic-controller` | `service.name` on exported telemetry. |
| `OTEL_EXPORT_INTERVAL` (`--otel-export-interval`) | `30s` | Metrics export interval. |
| `DEPLOYMENT_NAME` (`--deployment-name`) | `""` | Stable resource identification in metrics. |
| `SERVICE_VERSION` | `""` | Image version stamped as `service.version`. |
| `KUBECONFIG` (`--kubeconfig`) | `""` (in-cluster) | Out-of-cluster development only. |

The managed GVR is appended to `OTEL_RESOURCE_ATTRIBUTES` as `krateo.io/rest-gvr`, so
telemetry is attributable per dynamic CR type.

## Per-resource configuration: the `*Configuration` CRD

Runtime API parameters and credentials do not live in chart values at all: they live in
the generated `*Configuration` CR (authentication secret refs + the parameters declared
via `spec.resource.configurationFields`). That contract is documented in
[api.md](./api.md#generated-resources).

## Debugging a single resource

Add the annotation `krateo.io/connector-verbose: "true"` to a RestDefinition (provider
behaviour) or to a generated CR (RDC behaviour, including HTTP request/response logging)
to enable verbose logging for that object only.
