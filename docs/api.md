---
type: API
title: oasgen-provider — api
description: The RestDefinition CRD contract — actions, the full spec surface (mappings, transforms, async, delegated verbs), immutability, generated resources and supported authentication.
resource: restdefinitions.ogen.krateo.io
tags: [kog, crd, restdefinition]
timestamp: 2026-10-20T00:00:00Z
---

# API

The controller exposes no HTTP API (the optional preview service, oasgen-render, is described
at the end of this page). Its contract is the **`RestDefinition` CRD**
(`ogen.krateo.io/v1alpha1`, namespaced, categories `krateo`/`restdefinition`/`core`) plus
the CRDs it generates from it. Sources of truth:

- CRD manifest: [`go/oasgen-provider/crds/ogen.krateo.io_restdefinitions.yaml`](../go/oasgen-provider/crds/ogen.krateo.io_restdefinitions.yaml)
  (shipped by the `oasgen-provider-crds` chart, byte-identical).
- Generated field-by-field reference: [restdefinition-crd-reference.md](./restdefinition-crd-reference.md).
- Go types: `go/oasgen-provider/apis/restdefinitions/v1alpha1/types.go`.

`kubectl get restdefinitions` shows `READY` and `AGE`; `-o wide` adds the generated
`API VERSION`, `KIND` and `OAS PATH` columns.

## Top level

| Field | Required | Immutable | Description |
|---|---|---|---|
| `spec.oasPath` | ✔︎ | ✖︎ | `configmap://<namespace>/<name>/<key>` or `http(s)://<url>` (pattern-validated). Mutable, and the provider also hashes the resolved document (`status.oasHash`) so an in-place edit of the referenced ConfigMap is picked up as drift. Avoid changing the `create` request body/parameters — the generated CRD schema derives from them. |
| `spec.resourceGroup` | ✔︎ | ✔︎ | API group of the generated resources (e.g. `github.ogen.krateo.io`). |
| `spec.resource` | ✔︎ | — | The resource mapping (below). |

## `spec.resource`

| Field | Required | Immutable | Description |
|---|---|---|---|
| `kind` | ✔︎ | ✔︎ | Kind of the generated CRD. |
| `verbsDescription[]` | ✔︎ | ✖︎ | The action → endpoint mappings (below). |
| `identifiers[]` | ✖︎ | ✔︎ | Human-friendly unique fields `findby` matches on; surfaced in status. Only meaningful with a `findby` verb. |
| `additionalStatusFields[]` | ✖︎ | append-only | Extra response fields surfaced in status (typically technical ids used by `get`). **Append-only, not immutable** — you may add entries at the end; existing ones cannot be removed, renamed, reordered or changed. |
| `excludedSpecFields[]` | ✖︎ | ✔︎ | Fields excluded from the generated spec (server-generated ids etc.). Dots inside a single field name are escaped bracket-style: `'["searchCriteria.title"]'`. |
| `includedSpecFields[]` | ✖︎ | append-only | The **allowlist** counterpart: when set, the generated spec keeps only these fields. Mutually exclusive with `excludedSpecFields` (CEL — an allowlist and a blocklist over the same fields is a contradiction, not a merge). Max 256 entries, dot-notation; naming a parent keeps its whole subtree, naming a child keeps the parent and only the listed children. A listed field that does **not** exist is fatal, where `excludedSpecFields` only warns — a typo in a blocklist leaves a field in, where it is visible; a typo in an allowlist leaves one out, and the author sees a CR rejected on a field they believed they had included. Four things are kept whether listed or not, because the resource cannot function without them: `configurationRef`, spec fields derived from **required** OAS parameters, and the identifier selectors materialised into a create-less resource's spec. |
| `configurationFields[]` | ✖︎ | ✔︎ | OAS parameters lifted into the generated `*Configuration` CRD: `fromOpenAPI.{name,in}` + `fromRestDefinition.actions` (`["*"]` = all actions, min 1 item). |
| `compareScope` | ✖︎ | mutable | Drift-comparison scope. `fullSpec` (default) compares every spec field against the observed response. `identifiersAndStatus` compares only identifiers + additionalStatusFields (requires at least one of them; CEL-enforced). `updatable` compares only the fields the **update verb's request body can express**, derived from the OAS rather than declared by hand — a field the update cannot send is one the controller cannot fix, so comparing it would loop forever; requires an update verb (CEL-enforced). |
| `observeApiRef` / `createApiRef` / `updateApiRef` / `deleteApiRef` | ✖︎ | mutable | Delegate a whole verb to a snowplow RESTAction (below). |

**Immutability** is CEL-enforced, and the enforcement comes in **two kinds** — this page previously
listed `additionalStatusFields` under the strict kind, which was wrong in the direction that costs
someone work: it told readers they could not append a status field when appending is exactly what the
rule permits.

Strictly immutable (`self == oldSelf`) — changing one means deleting and recreating the RestDefinition:
`resourceGroup`, `kind`, `identifiers`, `excludedSpecFields`, `configurationFields`.

**Append-only** (`size(self) >= size(oldSelf) && oldSelf.all(i, v, self[i] == v)`) — you may add entries
at the end, but existing entries cannot be removed, renamed, reordered or changed:
`additionalStatusFields`, `includedSpecFields`.

So **five strict and two append-only**, seven rules in total. The authority is the marker on each field in
`apis/restdefinitions/v1alpha1/types.go`; counting them by grepping the rendered CRD for `oldSelf`
over-counts, because two of the matches are inside a `description` rather than a `rule`.
widening the allowlist only adds fields to the CRD, while narrowing it would remove fields
live CRs may be setting — which is what immutability actually protects against. Existing
entries cannot be removed, renamed or reordered.

## Actions

Six actions implement the four reconcile verbs (Observe ← `findby`/`get`, or a
`dispatch`'s correlation lookup; Create ← `create` or `dispatch`; Update ← `update`;
Delete ← `delete`):

- **`findby`** — searches a **collection** endpoint using `identifiers`. Used on the
  first reconcile, before the external id exists. `identifiersMatchPolicy: OR` (default)
  matches on any identifier, `AND` requires all. Not needed when a human-friendly key is
  the API's primary key (`GET /resources/{name}`).

  `pagination` is optional but matters more than it looks: a findby that stops before the
  end returns not-found, and the reconciler **creates** on not-found — so an unpaginated
  search over a multi-page collection does not merely fail to find the match, it creates a
  duplicate of it. Two strategies, mutually exclusive by CEL:

  - `type: pageNumber` — `?page=N&per_page=M`, the strategy behind the large majority of
    collection endpoints. `request` declares `pagePath`/`startPage` (required, no default:
    0- and 1-based APIs are both common and a wrong guess silently skips a page) and
    optionally `sizePath`/`pageSize`. `response` declares how the LAST page is recognised —
    `header` (a header whose content marks a next page, e.g. `Link` containing `rel="next"`)
    or `body` (`totalPagesPath` or `totalItemsPath`); omit `response` entirely for the
    short-page rule, which needs `request.pageSize` to compare against. `maxPages` is
    required and has no default.
  - `type: continuationToken` — request token in `query` or `header`, response token in
    `header` or `body`.

  Whenever a declared signal is missing or unreadable, the walk reports that it could not
  conclude. It never reports absence: "I could not tell" and "there is nothing there"
  reaching the same 404 is what created duplicates (#119). The same applies to exhausting
  `maxPages` — a bound on effort says nothing about existence.
- **`get`** — fetches a **single** resource, typically by a server-generated technical id
  stored in status via `additionalStatusFields`; used from the second reconcile on.
  Prefer defining both `findby` and `get`: `findby`-only works but pays the
  list-and-filter cost every reconcile.
- **`create`** — POSTs the resource; its request body **is** the source of the generated
  spec schema. A `202 Accepted` marks the CR Pending until observe stops returning 404
  (or use `async` below).
- **`update`** — request body must be a subset of the `create` body (the CRD schema comes
  from `create`).
- **`delete`** — must be idempotent (deleting an absent resource returns success); any
  2xx is accepted.
- **`dispatch`** — fires an **action** endpoint that is not a CRUD write: an ArgoCD sync, a
  GitHub `workflow_dispatch`, a Jenkins build, an Argo resubmit. It replaces `create`
  rather than joining it, and requires `correlation` (next section). Available from
  **0.32.0**.

The API endpoints must be **consistent** across actions (same field names/casing at the
same nesting); the declarative surface below absorbs much inconsistency, and a plugin
web service absorbs the rest (see [overview](./overview.md#when-a-plugin-wrapper-web-service-is-needed)).

### When the request and response shapes differ

A vendor that describes the same concept differently on the way in and the way out is
common, and it matters here because **the drift comparison compares the CR's spec against
the observed response directly**. Where the two shapes disagree, drift can be reported
that no update can fix — and the reconciler answers drift by issuing the update, so the
result is a write per poll interval, forever.

The comparison already tolerates one half of this: it iterates the **spec's** keys and
skips any key **absent** from the response (which is why `configurationRef` is not drift).
So a response that renames a field, spells it with different casing, or simply omits it
costs nothing.

What it cannot tolerate is a key present on **both** sides with a different *type* — the
request sends `instance: "K2A4"` and the response returns `instance: {name, cpu, ram}`.
That compares unequal on every reconcile.

`responseTransform` is the fix, and the reason it runs where it does: it is applied to the
raw response **before** per-field mapping, status population **and** the drift comparison,
so reshaping the response into the request's shape makes the comparison meaningful again.

```yaml
- action: get
  method: GET
  path: /kaas/{id}
  responseTransform:
    inline: |
      .nodePools |= map(.instance = .instance.name | .dataCenter = .dataCenter.name)
```

Per-field `fieldMapping` with `inResponse` does the same for one field at a time.
`compareScope` is **not** a fix for this: narrowing the scope helps only if the offending
field falls outside it, and the field a vendor reshapes is often exactly the one its update
verb accepts — in which case `updatable` still includes it.

## Dispatch and correlation

A `dispatch` verb fires an **action** endpoint rather than writing a resource: an ArgoCD
sync, a GitHub `workflow_dispatch`, a Jenkins build, an Argo resubmit. Two POSTs are two
runs, so the call is not idempotent, and the canonical case answers `204` with an empty
body, so there is nothing in the response to key on. In a level-based reconciler that is a
POST on every resync.

It is **not** the async case. `async`/`operationRef` extract a handle *from the trigger
response* and poll it; where such a handle exists, async already covers the endpoint and no
dispatch verb is needed. The correlation token travels the other way: the CR supplies it,
the API echoes it back, and the controller finds it again in a collection. That is the only
loop available when the trigger tells you nothing — which is why `async` and `dispatch` are
mutually exclusive.

**The token is the resource's identity**, and that is what answers "what does drift mean
for an action":

| | |
|---|---|
| token found in the observe collection | the invocation happened ⇒ `Ready`, nothing fires |
| token absent | the resource does not exist ⇒ the dispatch fires |

So changing the token is a *new invocation*, not drift, and firing for it happens exactly
once because its own run is what then stops it. Comparison is scoped to that one field and
never `fullSpec` — an unrelated spec edit must not read as drift on a resource whose only
available response to drift would be to POST again.

`correlation` is **required** on a dispatch and permitted nowhere else.

| Field | Description |
|---|---|
| `correlation.fromSpec` | Dot-path of the spec field the CR supplies as the token (e.g. `inputs.correlationId`). Must be unique per invocation — reusing a token means "that run already happened". |
| `correlation.observe.method` | `GET` or `POST` (some APIs expose search as a POST). |
| `correlation.observe.path` | The collection endpoint, spelled exactly as the OAS document spells it (paths are matched by exact string, so a differing parameter name is a miss). |
| `correlation.observe.itemsPath` | The property holding the array when the response is an **envelope** rather than a bare array (`workflow_runs` for `{"total_count": 3, "workflow_runs": [...]}`). Optional while the envelope has exactly one array property; **required** as soon as it has more, on both sides — guessing searches the wrong list, reports not-found, and here that means firing the dispatch a second time. |
| `correlation.echoedAt` | The path, **per item** of that collection, at which the token comes back (`.display_title`). Same path dialect as `itemsPath` and `async.poll.statusPath`. |

```yaml
verbsDescription:
  - action: dispatch
    method: POST
    path: /repos/{owner}/{repo}/actions/workflows/{workflow_id}/dispatches
    correlation:
      fromSpec: inputs.correlationId
      observe:
        method: GET
        path: /repos/{owner}/{repo}/actions/workflows/{workflow_id}/runs
        itemsPath: workflow_runs
      echoedAt: .display_title
```

Whether a given vendor really echoes the token is exactly what `echoedAt` forces the author
to establish before the RestDefinition is accepted. In the example above it does not echo
the input directly — the workflow has to put the token into its own `run-name`, which then
arrives as the run's `display_title`. Runnable, with that workflow:
[examples/github-workflow-dispatch](../examples/github-workflow-dispatch/README.md).

### Composition rules (CEL, at admission)

A dispatch cannot be combined with:

- **`create` or `update`** — the dispatch *is* the create, and there is nothing an update of
  a past invocation could mean.
- **another `dispatch`** — at most one per kind: two have no defined order and no way to
  tell which one a correlated invocation came from.
- **`compareScope`** — see above; any other scope makes an unrelated edit read as drift.
- **`async`** — see above; there is no handle to poll.
- **any `*ApiRef`** — delegating to a Snowplow RESTAction and declaring a correlated
  dispatch are two alternative answers to the same problem; declaring both would leave
  which one runs decided by the order of checks in the controller rather than by the author.

A `get`/`findby` may be declared, but **observe still goes through the correlation lookup**:
the token is what decides whether this invocation happened, which is the only question whose
answer can make the reconciler POST.

### What is checked, and where

Three layers, because a dispatch that cannot be correlated is the one failure mode that
causes duplicate runs rather than an error:

- **Admission (CEL)** — the composition rules above, and `correlation` present on a dispatch
  and absent everywhere else.
- **Generation** — `observe.path` must be a declared OAS path and `observe.method` an
  operation on it; `echoedAt`'s root segment must be a property the collection's items
  declare (skipped only when the item declares no properties, or allows additional ones);
  `fromSpec`'s root segment must exist in the generated spec schema. Each is reported
  against its own field, and each refuses rather than warns.
- **Runtime** — the backstop for what generation cannot see, e.g. a path into a free-form
  object.

### Runtime behaviour

- **No token in the spec** — the resource is **held**, not dispatched: firing an invocation
  that can never be found again is the one thing this verb exists to prevent. The same
  applies to a `fromSpec` whose nested segments name nothing (only the *root* segment is
  checked at generation), so a nested typo surfaces as a resource that never fires, not as
  one that fires repeatedly.
- **Fire-once is enforced, not merely intended** — `krateo.io/dispatched-token` records the
  token that was successfully POSTed, and observe then refuses to report not-exists for it
  again. Written only **after** the POST succeeds, so a dispatch that never actually
  happened is still retried: fire-once applies to invocations that occurred, not to attempts.
- **Waiting, then diagnosing** — `krateo.io/dispatch-observe-misses` counts reconciles since
  the dispatch fired in which the token has not appeared. Eventual consistency is normal and
  tolerated (`Creating`); after 5 checks the CR goes `Unavailable` with reason
  `DispatchedButNotObserved`, naming `echoedAt` as the likeliest cause. Nothing is
  re-POSTed — it is a diagnosis deadline, not a retry budget.
- **Ambiguity is a hard error** — two items echoing the same token means the token does not
  identify an invocation; the controller never picks the first.
- **A missing collection is an error, not absence** — a 404 on `observe.path` is not the
  same as the token being absent, and treating it as absence would dispatch against an
  endpoint that just said it is not there.
- **Status comes from the matched item**, not the collection — the same shape `findby`
  projects, so `identifiers` and `additionalStatusFields` resolve against the run that was
  correlated. This is also why a dispatch kind's status schema is generated from an item of
  that collection.
- **`delete` is a no-op that releases the finalizer** — the invocation a token names is
  immutable history, not an object, so there is nothing to tear down and a delete that tried
  would invent a semantic the vendor does not have.

## Per-verb tuning (`verbsDescription[]` items)

| Field | Description |
|---|---|
| `action` / `method` / `path` | Required. `action` ∈ create/update/get/delete/findby/dispatch; `method` ∈ GET/POST/PUT/DELETE/PATCH; `path` must be an exact key of the OAS `paths` object. |
| `fieldMapping[]` | Unified request/response value relocation + optional transform. Exactly one API-side anchor per entry: `inPath`/`inQuery`/`inBody` (request direction) or `inResponse` (response direction, normalizing the observed body before status population and drift comparison). `inCustomResource` is the CR-side JSONPath; array elements can be addressed positionally (`[0]`) or by content predicate (`[?type=password]`, must match exactly one). Optional `valueMapping` (`alias`: bidirectional value pairs; `jq`: a one-directional gojq program), `resolver` (`secretRef`: substitute a Secret value, same-namespace only, request direction only), and `defaultIfAbsent` (response direction: inject a default when the API omits the field). |
| `requestFieldMapping[]` | **Deprecated** — the request-direction-only predecessor of `fieldMapping` (`inPath`/`inQuery`/`inBody` + `inCustomResource`, no transforms). Still accepted; each entry is treated as an equivalent request-direction `fieldMapping` entry at load time. |
| `requestTransform` / `responseTransform` | Whole-document gojq programs. `requestTransform` runs on the assembled request body after per-field mapping (create/update/delete; no-op on bodyless verbs; requires rest-dynamic-controller ≥ 0.17.0). `responseTransform` runs on the raw response body before per-field mapping, status population and drift comparison. jq programs are `inline` or `ref` (a `.jq` module at `configmap://`/`http(s)://`, optional `entrypoint`). |
| `identifiersMatchPolicy`, `pagination` | `findby` only (CEL-enforced). |
| `correlation` | `dispatch` only, and **required** on it (CEL-enforced). How one invocation is identified afterwards — see [Dispatch and correlation](#dispatch-and-correlation). |
| `successCodes[]` | Extra HTTP codes treated as success, merged with the OAS-declared 2xx codes. |
| `tolerateCodes[]` | Codes treated as a successful **empty** response (e.g. 404 on an optional sub-collection) — beware masking real deletions. |
| `notFoundCodes[]` | Codes remapped to "resource does not exist" (e.g. 410, or 204 on an existence check). Intended for get/findby. |
| `notFoundBody` | gojq boolean predicate over the successful observe body meaning "does not exist" (200-with-tombstone APIs). Input is the whole GET body, or the single matched findby item. |
| `headers[]` / `queries[]` | Static name/value headers and query parameters injected verbatim on every request for this verb. |
| `async` | Long-running-operation handling for create/update/delete: `operationRef` (`in: body|header` + `path`, optional `jq`) extracts the operation handle from the trigger response; `poll` names the poll endpoint (`path` must contain the handle token and be an exact OAS path; `handleParam` names the path parameter, default `operationId`, requires RDC ≥ 0.18.0; `statusPath`, `successValues`, optional `failureValues`, `intervalSeconds` default 1, `maxAttempts` default 10, `timeoutSeconds`); `mode: blocking` (default, polls inline) or `requeue` (create/update only; delete always polls inline under its finalizer); `postGet` re-runs observe after terminal success. |

## Delegated verbs (`*ApiRef`)

`observeApiRef`/`createApiRef`/`updateApiRef`/`deleteApiRef` delegate a verb to a
**snowplow RESTAction** (`name` + `namespace`, optional static `extras`; on every
delegated verb the controller passes the CR's name/namespace/uid, its declared
identifier values and its whole spec as request extras, and projects the composed
`.status` back for observe/create/update). RESTActions must be idempotent. CEL-enforced
composition rules: `createApiRef` always requires a `get` or `findby` verb (so the
controller can verify the create converged — level-based convergence); additionally,
combining `createApiRef` with `observeApiRef` requires `observeApiRef.notFoundExpr` (so
the delegated observe can report absence and trigger the create);
`notFoundExpr`/`upToDateExpr` are
gojq boolean predicates over `{spec, status}` for observe delegation. Using these fields
requires the RDC's `URL_SNOWPLOW`/`URL_AUTHN` env to be set
(see [configuration.md](./configuration.md)).

## Generated resources

For `kind: Repo` in group `github.ogen.krateo.io`, from a document whose `info.version`
is `1.1.4`, the provider generates:

| Kind | When |
|---|---|
| `Repo` (`github.ogen.krateo.io/v1-1-4`) | Always — spec from the `create` request body (minus `excludedSpecFields`, plus path/query parameters, or kept by `includedSpecFields`), status from `identifiers` + `additionalStatusFields`. |
| `RepoConfiguration` (`github.ogen.krateo.io/v1alpha1`) | When the OAS declares security schemes and/or `configurationFields` are set. |

**The resource CRD's version comes from the OAS document, not from `v1alpha1`.**
`info.version` is normalized into a legal CRD version name — lowercased, every run of
non-alphanumerics replaced by `-`, a leading digit prefixed with `v` — so `1.1.4` becomes
`v1-1-4` and `1.0` becomes `v1-0`. A document that declares no version falls back to
`v1alpha1`, and the **`*Configuration` CRD is always `v1alpha1`** regardless. This is the
version CRs must be written against: `kubectl get restdefinition <name> -o wide` prints the
one in force (the `API VERSION` column), and writing a CR against `v1alpha1` out of habit
fails with `no matches for kind`.

Every generated CR carries a `configurationRef` (name/namespace) pointing at its
Configuration instance. The Configuration spec holds:

- `authentication` — one of the supported schemes (below), credentials referenced from
  Kubernetes Secrets (the provider grants the RDC per-namespace read RBAC on exactly the
  referenced Secrets, tracked in `status.authSecretDigest`/`authSecretRBACNamespaces`).
- `configuration` — the declared parameters, nested per location and action, e.g.
  `configuration.query.get.api-version: "7.2-preview.1"`.

### Status typing and string fallback

Status field types are derived from the `get` (or `findby`) response schema in the OAS.
When a declared `identifier`/`additionalStatusField` is not found there, the provider
logs a warning (`CodeStatusFieldNotFound`) and generates that field as `type: string` —
and the Kubernetes API server will then strictly validate what the controller writes, so
a type mismatch between CRD schema and actual API response surfaces as a
`ReconcileError` condition on the CR.

## Authentication

Declared via `securitySchemes` in the OAS document; three schemes are supported (a
document declaring only unsupported schemes still generates the CRDs, but the
Configuration CRD carries no `authentication` field; the provider warns and emits a
`NoAuthenticationGenerated` Warning event rather than failing):

1. **Bearer token** (`type: http`, `scheme: bearer`) → `authentication.bearer.tokenRef`.
2. **Basic auth** (`type: http`, `scheme: basic`) → `authentication.basic.usernameRef`/`passwordRef`.
3. **API key in header** (`type: apiKey`, `in: header`) → `authentication.apiKey` with
   `tokenRef` (the raw credential only), `header` (defaulted from the scheme when the
   document declares exactly one apiKey scheme) and optional `valuePrefix` prepended on
   the wire. `apiKey` in query/cookie is deliberately unsupported (credentials in URLs).

```yaml
apiVersion: github.ogen.krateo.io/v1alpha1
kind: RepoConfiguration
metadata:
  name: my-repo-config
  namespace: default
spec:
  authentication:
    bearer:
      tokenRef:
        name: gh-token
        namespace: default
        key: token
```

## RestDefinition status

`status` carries the standard Krateo conditions plus: `oasPath`, the generated
`resource`/`configuration` Kind+apiVersion pair, `digest` (managed-resources digest),
`oasHash` (content hash of the resolved OAS — an OAS edit is drift even when `oasPath` is
unchanged), `hasSecuritySchemes`, and the auth-RBAC bookkeeping fields
(`authSecretDigest`, `authSecretRBACNamespaces`).

## Runtime contract: what the RDC writes on managed CRs

The sections above describe what oasgen-provider *generates*. This one describes what the
generated controller *does* with those resources at runtime — RDC owns no CRDs of its own
and serves no HTTP API beyond an optional metrics endpoint, so this is its whole
outward-facing contract.

- **status** — the declared `identifiers` and `additionalStatusFields`, projected from the
  normalized API response. Cleared before repopulation on create/update, so a stale
  identifier can never deadlock reconciliation.
- **annotations** — an in-flight Model B (`requeue`) async operation is persisted on the
  CR as `krateo.io/async-operation-id`, `-action` and `-params`: the handle, the
  triggering verb, and the trigger's resolved path/query params, re-used verbatim at poll
  time. Authentication is deliberately re-applied fresh rather than replayed. The
  annotations are removed when the operation terminates.
- **conditions** — a single `Ready` condition with reasons `Available`, `Creating` (also
  used after updates), `Deleting`, `Unavailable` and the RDC-specific `Pending` (async
  operation in flight). On drift the `Unavailable` reason is rewritten to a
  `Resource is not up-to-date due to …` string naming the differing field and both values.
- **events** — `ResourceCreated`, `ResourceUpdated`, `ResourceDeleted` (Normal on success,
  Warning on failure), plus `AsyncOperationFailed` (Warning) when a Model B operation
  reaches a terminal failure status.

### Cluster side effects

For each CR instance that uses `secretRef` resolvers, RDC self-provisions a
namespace-scoped Role + RoleBinding named `<plural>-<version>-<fnv32a(ns/name)>-secrets`,
granting itself `get/list/watch` on exactly the referenced Secret names, and deletes the
pair when the CR is deleted.

### Metrics

With `REST_CONTROLLER_METRICS_SERVER_PORT` set, a Prometheus endpoint serves the
`unstructured-runtime` reconcile metrics. With `OTEL_ENABLED`, the same telemetry is
exported over OTLP (`provider_runtime.reconcile.*` / `controller_reconcile_*`),
resource-tagged with `krateo.io/rest-gvr=<group>/<version>/<resource>` so it is
attributable per dynamic CR type.

## Unsupported OAS features

- `number` type: converted to `integer` during CRD generation (RDC applies the matching
  conversion on response bodies).
- `nullable` (removed in OAS 3.1 in favor of `null` in the `type` array).
- `anyOf`, `oneOf`, `not` (`allOf` **is** merged, including enum deduplication).
- `format`: numeric formats (`int32`/`int64`/`float`/`double`) are emitted into the
  generated schema; every format is also appended to the field description, but
  non-numeric formats do not produce a more specific CRD type.
- Validation keywords `minItems`, `maxItems`, `minLength`, `maxLength`, `minimum`,
  `maximum`, `exclusiveMinimum`, `exclusiveMaximum`, `pattern`: silently dropped.
- `readOnly` / `writeOnly`.
- Arrays and objects in operation parameters (path/query/header/cookie).

`additionalProperties` is supported in **both** the boolean form and the object form
(typed free-form maps like `additionalProperties: {type: string}` are carried through to
the generated schema). This list may not be exhaustive.

## Preview: oasgen-render

`oasgen-render` renders RestDefinitions to the CRDs the controller would apply, **without
applying anything and without reading the cluster**. It runs the controller's own generation
code (`internal/tools/render`), ships in the provider image as `/bin/oasgen-render`, and is
deployed as a ClusterIP Service by its own chart, **`controller-render-service`** (published
from this repo at the same version as the provider chart). The pod mounts no ServiceAccount
token.

It used to be a conditional sub-deployment of the provider chart, behind `render.enabled`.
That made it the only Krateo render service needing install-time configuration — six values
had to be set by hand or the portal's Controller Builder Preview was simply off — while the
sibling `blueprint-render-service` is a first-class chart that works with none. The separate
chart removes that asymmetry; its defaults are correct for composition use, so it needs no
configuration at all. `render.*` no longer exists in the provider chart's values.

`POST /render`:

```json
{
  "restDefinitions": [ { "apiVersion": "ogen.krateo.io/v1alpha1", "kind": "RestDefinition", "metadata": {"name": "gh-repo", "namespace": "gh-system"}, "spec": { "oasPath": "configmap://gh-system/repo/repo.yaml", "...": "..." } } ],
  "oas": { "configmap://gh-system/repo/repo.yaml": "<the OAS document text>" }
}
```

`oas` is keyed by `spec.oasPath` spelled exactly as the RestDefinition spells it; nothing
is fetched. The reply (HTTP 200 whenever the request itself is well-formed):

```json
{
  "crds": [ <CustomResourceDefinition> ],
  "configurationCrds": [ <CustomResourceDefinition> ],
  "errors": [ { "restDefinition": "gh-system/gh-repo", "field": "spec.resource.verbsDescription[0].async.poll.path", "message": "...", "severity": "error" } ],
  "skippedSecuritySchemes": [ { "restDefinition": "gh-system/gh-repo", "scheme": "oauth (type: oauth2, in: )" } ]
}
```

- Each CRD is what the controller creates on a cluster where it does not exist yet (owner
  annotation and `VERSION` column included). Against an existing CRD the controller merges
  versions instead; the preview does not model that.
- `severity: "error"` means the controller would apply nothing for that RestDefinition
  (a missing or unparseable document, an async poll path violating the RDC contract, a
  mixed `*` wildcard, a fatal generation error, two RestDefinitions generating one CRD);
  it contributes no CRDs. `severity: "warning"` is what the controller logs and applies
  anyway: `ValidateSchemas` and generator findings, and a document whose only security
  schemes are unsupported.
- `restDefinition` is `<namespace>/<name>`, or `restDefinitions[<i>]` when unnamed.
- RestDefinition admission (the CRD's OpenAPI and CEL rules) is not evaluated.

`GET /healthz` returns `ok`. Limits: request body `OASGEN_RENDER_MAX_BODY_BYTES` (default
32 MiB, 413 beyond); listen address `OASGEN_RENDER_ADDR` (default `:8081`).
