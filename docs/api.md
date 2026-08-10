---
type: API
title: oasgen-provider — api
description: The RestDefinition CRD contract — actions, the full spec surface (mappings, transforms, async, delegated verbs), immutability, generated resources and supported authentication.
resource: restdefinitions.ogen.krateo.io
tags: [kog, crd, restdefinition]
timestamp: 2026-08-10T00:00:00Z
---

# API

oasgen-provider exposes no HTTP API. Its contract is the **`RestDefinition` CRD**
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
| `additionalStatusFields[]` | ✖︎ | ✔︎ | Extra response fields surfaced in status (typically technical ids used by `get`). |
| `excludedSpecFields[]` | ✖︎ | ✔︎ | Fields excluded from the generated spec (server-generated ids etc.). Dots inside a single field name are escaped bracket-style: `'["searchCriteria.title"]'`. |
| `configurationFields[]` | ✖︎ | ✔︎ | OAS parameters lifted into the generated `*Configuration` CRD: `fromOpenAPI.{name,in}` + `fromRestDefinition.actions` (`["*"]` = all actions, min 1 item). |
| `compareScope` | ✖︎ | mutable | Drift-comparison scope: `fullSpec` (default) compares every spec field against the observed response; `identifiersAndStatus` compares only identifiers + additionalStatusFields (requires at least one of them; CEL-enforced). |
| `observeApiRef` / `createApiRef` / `updateApiRef` / `deleteApiRef` | ✖︎ | mutable | Delegate a whole verb to a snowplow RESTAction (below). |

**Immutability** is CEL-enforced (`self == oldSelf`) on `resourceGroup`, `kind`,
`identifiers`, `additionalStatusFields`, `excludedSpecFields` and `configurationFields`:
changing them means deleting and recreating the RestDefinition.

## Actions

Five actions implement the four reconcile verbs (Observe ← `findby`/`get`, Create ←
`create`, Update ← `update`, Delete ← `delete`):

- **`findby`** — searches a **collection** endpoint using `identifiers`. Used on the
  first reconcile, before the external id exists. `identifiersMatchPolicy: OR` (default)
  matches on any identifier, `AND` requires all. Optional `pagination` (only
  `type: continuationToken`; request token in `query` only, response token in `header`
  only — both CEL-restricted). Not needed when a human-friendly key is the API's primary
  key (`GET /resources/{name}`).
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

The API endpoints must be **consistent** across actions (same field names/casing at the
same nesting); the declarative surface below absorbs much inconsistency, and a plugin
web service absorbs the rest (see [overview](./overview.md#when-a-plugin-wrapper-web-service-is-needed)).

## Per-verb tuning (`verbsDescription[]` items)

| Field | Description |
|---|---|
| `action` / `method` / `path` | Required. `action` ∈ create/update/get/delete/findby; `method` ∈ GET/POST/PUT/DELETE/PATCH; `path` must be an exact key of the OAS `paths` object. |
| `fieldMapping[]` | Unified request/response value relocation + optional transform. Exactly one API-side anchor per entry: `inPath`/`inQuery`/`inBody` (request direction) or `inResponse` (response direction, normalizing the observed body before status population and drift comparison). `inCustomResource` is the CR-side JSONPath; array elements can be addressed positionally (`[0]`) or by content predicate (`[?type=password]`, must match exactly one). Optional `valueMapping` (`alias`: bidirectional value pairs; `jq`: a one-directional gojq program), `resolver` (`secretRef`: substitute a Secret value, same-namespace only, request direction only), and `defaultIfAbsent` (response direction: inject a default when the API omits the field). |
| `requestFieldMapping[]` | **Deprecated** — the request-direction-only predecessor of `fieldMapping` (`inPath`/`inQuery`/`inBody` + `inCustomResource`, no transforms). Still accepted; each entry is treated as an equivalent request-direction `fieldMapping` entry at load time. |
| `requestTransform` / `responseTransform` | Whole-document gojq programs. `requestTransform` runs on the assembled request body after per-field mapping (create/update/delete; no-op on bodyless verbs; requires rest-dynamic-controller ≥ 0.17.0). `responseTransform` runs on the raw response body before per-field mapping, status population and drift comparison. jq programs are `inline` or `ref` (a `.jq` module at `configmap://`/`http(s)://`, optional `entrypoint`). |
| `identifiersMatchPolicy`, `pagination` | `findby` only (CEL-enforced). |
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

For `kind: Repo` in group `github.ogen.krateo.io`, the provider generates:

| Kind | When |
|---|---|
| `Repo` (`github.ogen.krateo.io/v1alpha1`) | Always — spec from the `create` request body (minus `excludedSpecFields`, plus path/query parameters), status from `identifiers` + `additionalStatusFields`. |
| `RepoConfiguration` (same group/version) | When the OAS declares security schemes and/or `configurationFields` are set. |

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
