---
type: Log
title: oasgen-provider — log
description: Curated chronological history of oasgen-provider — notable changes and decisions, newest first.
resource: oci://ghcr.io/krateo-platformops/charts/oasgen-provider
tags: [kog, history]
timestamp: 2026-08-10T00:00:00Z
---

# Log

Curated history (notable changes, decisions); release notes stay in GitHub Releases.
Both components ship from one tag at identical versions, so entries below cover the
provider and the rest-dynamic-controller together.

## 2026-08-10 — 0.21.0
- The **rest-dynamic-controller image now builds from this monorepo** (#64), so one tag
  publishes both images. The standalone repo stops being a release source.
- `rdc.image.tag` emptied: the RDC tag now derives from the chart `appVersion` instead of
  a hand-maintained pin, which is what made the pin drift in #62 possible.
- Release guards, all org-wide reusables: charts are refused if any image they reference
  does not exist; tags must be contained in `main`; the image matrix runs
  `fail-fast: false` so one module's failure no longer cancels the other's build.
- The rest-dynamic-controller doc bundle was folded into this one — the repo's docs are
  now the single documentation set for both components.

## 2026-08-07
- Adopted the Krateo Documentation Standard (this bundle): thin README, the invariant
  docs/ nine, `examples/github-repo`, regenerated CRD reference, dead-org purge.

## 2026-08-04 — 0.20.0
- `global.imageRegistry` chart value: one registry override for the provider and RDC
  images (mirror / air-gapped installs) (#56).
- CI consolidation: canonical `release-oci.yaml` publishes both charts on tag (#57),
  shared reusable multi-platform image build (#58, #59), obsolete crds→chart-repo
  publish job dropped (#60) — the charts live in this repo now.

## 2026-08-02 — 0.19.0
- apiKey-in-header authentication: the generated Configuration CRD gains
  `authentication.apiKey` (`tokenRef` + `header` + optional `valuePrefix`); unsupported
  security schemes are no longer skipped silently — generation still proceeds, but the
  provider warns and emits a Warning event (`NoAuthenticationGenerated` when no scheme
  could be generated at all) (#49).
  Joint contract with rest-dynamic-controller 0.19.0 (the chart pin).

## 2026-08-01 — 0.18.0
- `async.poll.handleParam`: bind the extracted operation handle to a vendor-named path
  parameter (e.g. Aruba's `.../monitor/{id}`) without patching the OAS; poll `path` is
  validated up front (#48). Requires RDC ≥ 0.18.0.
- Object-form `additionalProperties` (typed free-form maps) carried through to the
  generated schema (#45/#47).

## 2026-07-30 — 0.15.x–0.17.0
- 0.17.0: `requestTransform` accepted again now that RDC executes it (#44) — it had been
  deliberately rejected at admission while unimplemented (#42).
- Engine fold: oasgen-provider became a monorepo — `go/oasgen-provider` +
  `go/rest-dynamic-controller` + `helm/` charts, matrix build/test (#54); Go module
  identity migrated to `github.com/krateo-platformops/*` (#53).
- Tests now gate releases and run on pushes to main (#40); the crds-subchart
  `CHART_VERSION` placeholder is preserved by the CRD-sync job (#39).
- 0.15.0: the never-used `apiLookup` FieldResolver kind removed (see the design notes in
  `types.go`); `[?key=value]` array predicates documented in fieldMapping paths;
  `manifests/` (a non-shipping second copy of the RDC templates) deleted — the chart's
  `assets/rdc/` is the single copy.

## 2026-07 — 0.12.0–0.14.x
- `FieldResolver` (secretRef) on fieldMapping entries; dynamic watch on the generated
  Configuration Kind; auth-secret access migrated off a standing secrets grant onto
  per-namespace, per-Secret RBAC tracked in RestDefinition status.
- Superseded served CRD versions pruned (migration-free version derivation).

## Earlier
- The core KOG design stabilized: RestDefinition → generated resource +
  Configuration CRDs → one rest-dynamic-controller Deployment per RestDefinition,
  rendered from chart-owned templates (mount-and-render, the same mechanism
  core-provider uses for its CDCs).

## Component history: rest-dynamic-controller before the fold

Changes on the RDC side of the contract, carried over from its own bundle. Versions match
the shared line; entries already covered above are not repeated.

- **0.20.0** — `compareScope: updatable`: drift restricted to fields the update verb's
  request body can express, ending unfixable update loops on server-assigned/create-only
  fields. Secret-sourced credentials are whitespace-trimmed — a trailing newline used to
  surface as an opaque `net/http: invalid header field value`.
- **0.18.0** — RESTAction delegation forwards the CR spec in **every** direction, not just
  create/update; without it a delegated delete saw nulls, reported success, and left the
  finalizer unreleased forever.
- **0.17.0** — `requestTransform` actually executes on the outgoing body (it had been
  materialized but never run); the real `valueMapping` support matrix documented — `jq` is
  response-direction only.
- **0.16.x** — content-predicate array paths `[?key=value]` in fieldMapping/secretRef
  paths, addressing an array element by content instead of position; a CR whose create
  failed is no longer undeletable.
- **0.15.0** — **breaking**: the `apiLookup` resolver removed; `secretRef` is the only
  field resolver kind.
- **0.14.0** — a `fieldMapping` into a body field no longer drops that field's unmapped
  siblings.
- **0.13.0** — array-index paths in mappings; the `<Kind>Configuration` GVK no longer
  inherits the managed resource's version — always `v1alpha1`, matching what oasgen
  generates.
- **0.12.0** — `secretRef` field resolvers with per-CR-instance **self-provisioned RBAC**:
  RDC grants its own ServiceAccount read access to exactly the referenced Secrets, which
  made `REST_CONTROLLER_SERVICEACCOUNT_NAME/_NAMESPACE` hard-required at startup.
- **0.11.0** — `compareScope: identifiersAndStatus` drift mode.
- **0.10.0** — the delegation + async wave: `observeApiRef`/`createApiRef`/`updateApiRef`/
  `deleteApiRef` via snowplow `/call` under an authn-issued identity; the async engine
  (Model A `blocking` inline polling, opt-in Model B `requeue` with header-based handles);
  per-verb `successCodes`/`tolerateCodes`/`notFoundCodes` and static headers/queries;
  body-based absence via `notFoundBody`; the `ref:` jq module loader; delete holds the
  finalizer on transient definition-lookup failures.
- **≤0.9.x** — the foundation: the dynamic GVR controller over unstructured-runtime, the
  OAS-driven client with request validation, `get`/`findby` observe with
  `identifiersMatchPolicy` and `continuationToken` pagination, basic/bearer auth from
  `<Kind>Configuration`.
