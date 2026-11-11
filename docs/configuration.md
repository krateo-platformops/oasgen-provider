---
type: Configuration
title: oasgen-provider — configuration
description: The whole config surface — both charts' values, the provider's env/flags, the hand-maintained RDC image pin, and the env contract of every generated RDC instance.
resource: oci://ghcr.io/krateo-platformops/charts/oasgen-provider
tags: [kog, helm, values, env]
timestamp: 2026-10-20T00:00:00Z
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
| _(removed)_ | — | The `render.*` block is gone. `oasgen-render` now ships as its own chart, **`controller-render-service`**, published from this repo at the same version; it needs no configuration. See [api.md](./api.md#preview-oasgen-render). |

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

> **Below 0.26.1 this writes credentials to the pod log.** The verbose dump printed
> `Authorization: Bearer <token>` in cleartext: the redactor only replaced values it had
> been told were sensitive, and a bearer token applied from the resource's own
> Configuration was never registered, so a log that looked sanitised was not. Fixed in
> **0.26.1** (#166) by redacting credential headers *by name* — the fix is **not** in
> 0.26.0. On an older provider, treat any log written with this annotation on as
> containing the credential, and rotate it.

It is **per-object HTTP logging, not a log level.** It does not change what the controller
itself logs, so it does not surface the controller's own `Debug` lines (the drift-comparison
reason, for one) — those need `REST_CONTROLLER_DEBUG` on the generated controller, set
through the chart's `rdc.env`, which restarts every RDC instance.

> **Set it in the provider chart's values, not on the generated ConfigMap.** The
> `<name>-configmap` next to each generated controller is **rendered and owned by the
> provider** from the template mounted into its pod, so a key added to it by hand is
> dropped the next time that RestDefinition reconciles — silently, and with the controller
> still reporting `"debug": false` at startup. The supported path is
> `rdc.env.REST_CONTROLLER_DEBUG: "true"` in the oasgen-provider chart's values followed by
> `helm upgrade`: the provider re-renders every generated ConfigMap, and because it hashes
> the result it restarts each generated Deployment so the new environment is actually
> picked up.

## Checking a RestDefinition's declarations — `status.schemaWarnings`

A RestDefinition can be `Ready=True` and `Synced=True` while its own declarations contain errors. Naming a
field that the OpenAPI document does not have is **deliberately non-fatal** — failing generation over it
would break working installs — so the generator records the problem and carries on.

`status.schemaWarnings` lists what the **current** render found:

```console
$ kubectl get restdefinition baremetalhost -n krateo-system \
    -o jsonpath='{range .status.schemaWarnings[*]}{@}{"\n"}{end}'
generation error at identifiers.node_id: status field 'node_id' not found in response, defaulting to string
generation error: field 'uuid' set in excludedSpecFields not found in schema
```

**Absent means clean.** The field is rewritten on every render, including to nothing, so a fixed
declaration clears it rather than leaving a stale complaint.

### Sweeping a fleet

One command, no tooling:

```console
$ kubectl get restdefinition -A \
    -o jsonpath='{range .items[?(@.status.schemaWarnings)]}{.metadata.namespace}{"/"}{.metadata.name}{"\t"}{.status.schemaWarnings}{"\n"}{end}'
```

That matters because the alternative was rendering every definition by hand. On one fleet, four
definitions carried eight generation errors between them and all eight were found that way.

### Why the Event is not enough

The same warnings are also emitted as a `SchemaWarnings` **Warning Event**, which is still the right
channel for "this just happened" — but it is not a record. The apiserver's default `--event-ttl` is **one
hour**, while a declaration naming a missing field stays wrong until someone edits it. A definition
installed yesterday was therefore indistinguishable from a correct one through every cluster-visible
channel.

```console
$ kubectl describe restdefinition baremetalhost -n krateo-system | grep -A3 SchemaWarnings   # only if recent
$ kubectl get restdefinition baremetalhost -n krateo-system -o jsonpath='{.status.schemaWarnings}'  # always
```

### When it refreshes

On create, and whenever the OAS document or `spec.resource` changes — which is exactly when the answer can
have changed. From 0.53.0 a provider **upgrade** also re-renders every definition once
(`status.generatorBuild`), so an upgrade refreshes this field across the fleet. If you have set
`OASGEN_PROVIDER_REGENERATE_ON_UPGRADE=false`, it refreshes only when a definition itself changes.

## Forcing a reconcile, when you are measuring something

A provider behaviour you are trying to observe may simply not have happened yet. **An individual resource
is checked for drift every 3 minutes by default**, so a probe that waits less than that and then reports
"the provider never did X" is measuring your patience rather than the provider.

```
poll interval   default 3m    -poll   /  OASGEN_PROVIDER_POLL_INTERVAL
sync period     default 1h    -sync   /  OASGEN_PROVIDER_SYNC
```

The `-poll` value is what governs per-resource drift checks; `-sync` is the controller manager's own
resync and is much longer. A measurement window has to exceed the poll interval, or exceed it after
forcing a reconcile.

**To force one, write to the RestDefinition itself — not to its status.**

```bash
kubectl annotate restdefinition <name> -n <ns> \
  example.com/force-reconcile="$(date +%s)" --overwrite
```

A **main-resource** write triggers a reconcile; a `/status` patch does not, because the controller does not
watch its own status writes (which is also what stops a status update from looping). So annotating is the
reliable trigger and patching status is not — and the failure mode of getting that wrong is silence rather
than an error.

**Why this is safe for a measurement rather than a way of faking one.** The annotation only causes a
reconcile; it does not supply any value the reconcile reads. The status fields such a probe inspects —
`status.authSecretRBACNamespaces`, `status.authSecretDigest`, `status.schemaWarnings`,
`status.generatorBuild` — are written **only** by the provider, from the document and the cluster, so the
trigger cannot influence the outcome it is used to observe. That is the property that makes it an
accelerator and not an input.

**If the result is a non-event, you need an activity witness.** A probe asserting "X did not happen" must
establish that the provider ran at all during the window, or *"it correctly did nothing"* and *"it never
ran"* are the same reading. Count the provider's own reconcile lines for the definition
(`managed/restdefinition.ogen.krateo.io`) and treat a window with no increase as **void** rather than as a
pass.

**And beware a second trigger you did not intend.** Any change to the set of Secret names a kind's
`secretRef` resolvers reference currently causes that kind's controller to be restarted, so a probe that
plants and removes Configurations is also rolling the controller whose logs it is counting. Order such
probes so the log-counting one runs first.

Credit for the method and the activity witness to the openstack-nova-operator-kog session, which found the
poll-interval trap by reporting a provider defect that turned out to be a 120-second wait against a
3-minute period.

## Before trusting any timed measurement, check the control plane

This applies to **every** duration measured against this provider, not only to the forced reconcile above,
and it is the control most likely to be missing.

On a loaded machine — several `kind` clusters sharing one Docker VM, for instance — `kube-controller-manager`
and `kube-scheduler` can be CPU-starved while `etcd` and the apiserver are fine. **Object reads stay
correct under that, so nothing fails.** What degrades is *timing* and *garbage collection*, which is exactly
what a timed window depends on. One session measured 143 kcm and 141 scheduler restarts against 3 for etcd
and the apiserver on the same VM.

So a structural result — a field's presence, a `required` set, a schema comparison, an admission verdict —
is unaffected and can be trusted. A duration cannot, and should either be re-run or reported without
timings.

```bash
# restart counts; anything much above zero on kcm or the scheduler means durations are unsafe
kubectl get pods -n kube-system -o \
  custom-columns=NAME:.metadata.name,RESTARTS:.status.containerStatuses[0].restartCount

# and a LIVE control, because a fresh cluster has zero restarts by construction and so proves nothing:
# delete a one-replica Deployment and time how long its ReplicaSet survives. Garbage collection runs
# inside kube-controller-manager, so a healthy cluster collects it in about a second.
```

The live control is the half worth keeping: a restart count of zero is equally consistent with "healthy"
and with "this cluster is four minutes old", and only one of those tells you a duration is safe to quote.

## Where a credentialed request may go — `rdc.endpointAllowlist`

This is the config surface's one security control and it was missing from this page. The values carry
thorough comments; what follows is the **matching rule**, which they do not state and which decides
whether a given redirect is refused.

```yaml
rdc:
  endpointAllowlist: []       # default: only the hosts the OAS document itself names
  allowAnyHost: false         # default: matching is enforced
```

They render into `REST_CONTROLLER_ENDPOINT_ALLOWLIST` and `REST_CONTROLLER_ALLOW_ANY_HOST`. Set them
through these **dedicated keys, not through `rdc.env`** — a misspelled key in a free-form env map
silently does nothing, so an operator would believe the allowlist is set when it is not. The schema
rejects a typo in these.

### The matching rule: `(host, port, scheme)`, with the port defaulted first

An entry and a request are compared on all three, **exactly** — but an omitted port is filled in from the
scheme first, **on both sides, by the same rule**:

```
no port + https  ->  443
no port + http   ->  80
any other scheme ->  refused outright
```

The symmetry is what makes the exact comparison usable at all: an entry written `api.example.com` becomes
`(api.example.com, 443, https)`, so it matches a request to `https://api.example.com` *and* one to
`https://api.example.com:443`. Without defaulting the entry too, a port-less entry would match nothing.

That defaulting is the part worth knowing, because it makes two superficially similar redirects go
opposite ways:

| redirect | outcome | why |
|---|---|---|
| `https://api.example.com:8443` → `https://api.example.com:9443` | **refused** | 9443 ≠ 8443 |
| `http://api.example.com:80` → `http://api.example.com` | **permitted** | both resolve to port 80 |
| `https://api.example.com` → `https://api.example.com:443` | **permitted** | both resolve to port 443 |

So "a redirect that changes only the port is refused" is **too strong**. A redirect that only adds or
drops the *scheme's default* port changes nothing the policy can see, and is allowed. One that moves to
any other port is refused.

Entry syntax, from the values' own comment: `host[:port]`, optionally prefixed `http://` — **no prefix
permits https only**, cleartext has to be asked for. A single leading `*.` wildcard matches one or more
leading labels and **not** the bare suffix. A bare `*` is rejected at startup; use `allowAnyHost`. A
**path is refused at startup** with a message saying what to write instead, so pasting a document's
`servers[0]` verbatim fails loudly rather than silently never matching.

### `allowAnyHost: true` short-circuits all of it

It is the first thing checked, so nothing below it runs. It is a separate key rather than a `*` entry so
that "permit any host" shows up in a values diff instead of hiding as punctuation, and the controller
logs it loudly once at startup.

**If you are testing whether the policy refuses something, assert `allowAnyHost` is off first.** A
permitted request under allow-any mode looks exactly like a policy that failed to refuse.

### Two refusals with different causes

A refused redirect names the endpoint:

```
refusing to follow a redirect: refusing to send a credentialed request to "<host>:<port>":
it is not the document's server and is not in the operator's endpoint allowlist
```

A **different** refusal, `stopped after 10 redirects`, is Go's hop limit and says nothing about the
allowlist. Match on the endpoint text, not on "a redirect was refused".

### Verbose debugging does not bypass the check

Worth stating because the opposite was once true: the debug round-tripper used to wrap the *caller's*
transport and assign it back, which made that transport non-nil and sent the request on it instead of on
the policy's — so enabling verbose logging silently skipped the resolved-address and per-hop redirect
checks, in the one configuration an operator turns on when something is already wrong.

That is fixed and guarded in two independent ways, which is why this section can state it rather than
hedge it:

- a source-level guard asserting **no production site supplies an `http.Transport`**, scoped to the
  package where the client construction sites live, with a control count
- behavioural tests in **both** directions — the guard still applies with debug enabled, *and* a
  permitted host still works with debug enabled

## Finding out who last wrote a field

When a value on a generated object is not what you expect, the first question is whether this provider
wrote it at all. `managedFields` answers that per field, and it is usually faster than reading any
controller's logs.

```bash
kubectl get cm <kind>-controller-configmap -n <namespace> \
  -o jsonpath='{range .metadata.managedFields[*]}{.manager}{"  "}{.operation}{"  "}{.time}{"\n"}{end}'
```

A generated ConfigMap's `data` is written by the provider's own field manager. The provider's *template
source* — `oasgen-provider-rdc-configmap`, which the chart renders `rdc.env` into — is written by `helm`.
Seeing the two is how one session established that no third party was rewriting a generated ConfigMap,
which eliminated a whole class of explanation in a single call.

> **`kubectl` strips `managedFields` unless you ask for them.** With `-o json` or `-o yaml` and no
> `--show-managed-fields`, the field is removed from the output. A probe that omits the flag sees no
> managers and concludes the fields were *cleared* — a wrong answer that looks like a finding. Measured by
> `azuredevops-provider-kog`, 2026-11-11, while diagnosing issue 599.
>
> It is the same shape as every other instrument trap in this file: the query was wrong, the output was
> empty, and the emptiness read as a fact about the world.

## Did the generated controller actually restart?

Three of the obvious signals do not answer this, and two of them are actively misleading.

| signal | what it actually tells you |
|---|---|
| `metadata.generation` | **not a rollout count.** It increments on any `spec` change — `replicas`, `strategy`, `revisionHistoryLimit` — and a no-op update does not bump it at all |
| `kubectl.kubernetes.io/restartedAt` | what something *wrote*. Before 0.60.0 the apply strips it, so it can be absent even when a roll did happen |
| `status.observedGeneration` | that the Deployment controller has seen the spec, not that any pod changed |

Measured on 25 generated controllers across two ConfigMap changes: `generation` went `1 → 2 → 3` while
`deployment.kubernetes.io/revision` stayed at `1`, there was exactly one ReplicaSet per Deployment created
at install time, and **no pod was created after either change**. Generation moved twice and nothing rolled.
One session had to withdraw a 25-of-25 "pass" for counting a generation bump as a roll.

**Ask the two ground-truth channels, and require both to agree:**

```bash
# 1. a new ReplicaSet — each distinct pod template gets one; the annotation orders them
kubectl get rs -n <namespace> -l app.kubernetes.io/name=<kind>-controller \
  -o custom-columns=NAME:.metadata.name,REV:.metadata.annotations.'deployment\.kubernetes\.io/revision',CREATED:.metadata.creationTimestamp

# 2. pod identity and age — what a user actually experiences
kubectl get pods -n <namespace> -l app.kubernetes.io/name=<kind>-controller \
  -o custom-columns=NAME:.metadata.name,CREATED:.metadata.creationTimestamp,RESTARTS:.status.containerStatuses[0].restartCount
```

A new ReplicaSet with no change in pod identity is not a roll anyone experiences. A change in pod identity
with no new ReplicaSet is an eviction or a node event, not a roll caused by whatever you changed. Only both
together establish that your change rolled the controller.

`restartCount` is worth reading separately: it counts container restarts *within* a pod, so
`restartCount 0` on a pod whose `creationTimestamp` falls inside your change window means the pod was
**recreated** rather than restarted — which is Kubernetes rolling the Deployment itself, for instance
after an `rdc.image.tag` change alters the pod template.

## Which changes reach a running controller, and which need a restart

The generated controller receives its configuration through `envFrom: configMapRef`, which is read **once
at container start** and referenced by name. So a change to the ConfigMap's *data* leaves the pod template
untouched and cannot reach a running process by itself.

| what you change | how it reaches the controller |
|---|---|
| `rdc.image.tag` | changes the pod template, so **Kubernetes** rolls the Deployment. Nothing provider-specific is involved |
| `rdc.env`, and anything else in the generated ConfigMap | nothing changes the pod template, so **the provider must restart the Deployment explicitly** |
| a `secretRef` Secret's *name set* | RBAC only. Authorization is evaluated by the apiserver per request and the controller caches no decision, so this needs **no** restart and must not cause one |

> **On 0.59.0 the second row does not work.** The restart never fires, so an `rdc.env` change updates the
> ConfigMap, reports success through `helm upgrade`, and never reaches the running controller — with no
> error, event or condition anywhere (issue 599). The workaround on that release is to delete the generated
> controller's pods yourself; they read the updated ConfigMap at start. Fixed in 0.60.0. The first row is
> unaffected on every version, so an image upgrade on 0.59.0 did take effect.

## Inspecting a running controller — there is no shell in either image

Both images run on `gcr.io/distroless/static:nonroot`: `rest-dynamic-controller` since **0.20.0**, and
`oasgen-provider` since **0.41.0**, which brought it into line (#271). Checked against both
Dockerfiles rather than inferred from the release note, which recorded only what the change *saved* —
338.4 MB compressed down to 33.9 — and nothing about what it removed.

What it removed is every in-container diagnostic. There is no shell, no `env`, no `cat`, no package
manager, so these no longer work **on either controller**:

```console
kubectl exec -n krateo-system deploy/oasgen-provider -- env       # no shell, no env binary
kubectl exec -n krateo-system deploy/oasgen-provider -- /bin/sh   # no shell
```

This is worth knowing **before** you need it. The people who reach for `exec` are debugging something
at the time, and discovering the shell is gone mid-incident is the worst moment to learn it — "exec
into the pod" sits in enough runbooks and enough habits that nobody checks a release note first. It
was found exactly that way, by a consumer reaching for `kubectl exec ... -- env` to read a
controller's environment.

### The substitutes, which cover nearly every real use

Reading the environment — the most common reason to exec, and the pod spec is the better source
anyway, because it shows what was *configured* rather than what a shell happens to resolve:

```console
kubectl get deploy/oasgen-provider -n krateo-system -o jsonpath='{.spec.template.spec.containers[0].env}' | jq .
kubectl set env deploy/oasgen-provider --list -n krateo-system
```

For a generated controller, the same two commands against its own Deployment (`<restdefinition-name>-controller`).

| instead of | use |
|---|---|
| `exec ... -- env` | `kubectl set env <deploy> --list`, or the `jsonpath` above |
| `exec ... -- cat /etc/...` | `kubectl describe pod` for the resolved spec, mounts and events |
| `exec ... -- ls /mnt` | `kubectl get pod -o jsonpath='{.spec.volumes}'` plus the container's `volumeMounts` |
| `exec ... -- ps` | `kubectl logs`, and `kubectl describe pod` for restarts and exit codes |

### The escape hatch, for the cases that genuinely need a shell

`kubectl debug` restores the old workflow without reverting the image, by attaching an **ephemeral
container** that shares the target's namespaces:

```console
kubectl debug -it <pod> -n krateo-system --image=busybox --target=<container-name>
```

`--target` is what makes it useful: without it the debug container shares the pod's network but not
its process namespace, so you cannot see the controller process or its `/proc`. Ephemeral containers
are on by default since Kubernetes 1.25, and these charts floor at 1.36, so the feature is always
available where the charts install at all.

The filesystem you land in is **busybox's**, not the controller's. To read the controller's files, go
through `/proc/1/root/` from the debug container.
