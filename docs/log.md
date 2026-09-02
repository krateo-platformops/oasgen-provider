---
type: Log
title: oasgen-provider — log
description: Curated chronological history of oasgen-provider — notable changes and decisions, newest first.
resource: oci://ghcr.io/krateo-platformops/charts/oasgen-provider
tags: [kog, history]
timestamp: 2026-09-02T00:00:00Z
---

# Log

Curated history (notable changes, decisions); release notes stay in GitHub Releases.
Both components ship from one tag at identical versions, so entries below cover the
provider and the rest-dynamic-controller together.

## 2026-09-02 — 0.22.2

Fixes a regression introduced in 0.22.1 that made resources **undeletable through
Kubernetes** on APIs that delete asynchronously. Upgrade from 0.22.1 promptly.

- **A 404 on DELETE is the success condition, not an error** (#98). 0.22.1 began holding
  the finalizer until the resource was verified gone (#77) — correct in itself — but the
  native delete path returned every `apiCall` error, including 404. So the first DELETE
  succeeded, the resource disappeared, and every retry thereafter 404'd, errored, and never
  released the finalizer: the CR hung in `Deleting` permanently and only a hand-edited
  finalizer cleared it. The external resource was removed correctly; the CR was not.

  The trade was bad in both directions: #77 fixed a rare silent orphan and 0.22.1 replaced
  it with a guaranteed hang. `Observe()` had always treated not-found as absence; the delete
  path now does the same, and skips the async block when the resource was already gone —
  there is no operation to poll, and the response carries the 404 rather than a handle.

  It shipped because the 0.22.1 test covered the resource-still-present path and never the
  already-absent one, which is the path every retry takes. The regression test now deletes
  **twice**, the second delete being precisely that retry.

Also in this release, though not user-visible: the org-shared image-existence check now sizes
its wait for a test-gated build. 0.22.1's charts failed to publish and needed a manual re-run
because that check budgeted ~30m for images to appear, assuming the build starts immediately
— an assumption the release gate added in #88 invalidated by putting the test suite first.

## 2026-09-01 — 0.22.1

A patch release whose only user-visible change is chart content; no Go source changed, so the
images are identical to 0.22.0 apart from their tag.

- Dead-org (`krateoplatformops`) references removed from shipped content (#87): the CRD chart's
  `icon` URL, and `app.kubernetes.io/part-of` in the RDC assets. The label matters more than the
  icon — `assets/rdc/` is mount-and-render, so it was stamped on **every generated controller in
  every cluster**. It now reads `krateo`, matching core-provider's equivalent `assets/cdc/`. Nothing
  selects pods by it (it is absent from the selector and the pod template), but anything selecting
  the Deployment or ConfigMap objects on the old value needs updating.

The rest of the release is CI and test infrastructure, which does not reach a cluster but is worth
recording because it changes what a release *guarantees*:

- **The release gate now gates** (#88). `build` had no `needs: [test]`, so images published whatever
  the suite did — observed on the 0.22.0 run, where all four jobs started within the same second.
  0.22.1 is the first release where a failing suite actually stops the images.
- **CRD generation writes every destination, and the guard diffs the whole tree** (#84). The
  generated `crds/` was checked while the versioned chart that ships them was not — the copy users
  install was the unguarded one, and it had already gone stale once during the #51 fix.
- The RDC integration suite is re-runnable after an interrupt (#85), and every workflow job now
  resolves to an org-shared reusable rather than a local re-implementation (#86, #94) — including
  `preflight-refs`, which was 292 lines of Python duplicated byte-for-byte in two repos.

## 2026-09-01 — 0.22.0

Four correctness fixes, two of which failed **silently** — reporting success while the
resource was diverged or gone.

- **Empty arrays are now an opinion** (#76). `compareSlices` only rejected a CR slice
  *longer* than the remote one, so an empty CR slice matched **any** remote array: the
  loop body never ran and the comparison returned equal. Emptying a list could never be
  enforced, and the controller reported `Ready=True` and "External resource is up to date"
  while diverged. The map subset rule (a key absent from the CR is an opinion not
  expressed) is unchanged and now pinned by its own test.
- **Delete verifies before releasing the finalizer** (#77). A 2xx on DELETE means the
  deletion was *requested*. An API deleting asynchronously with no pollable operation
  answers 204 immediately and keeps the resource, so the CR vanished while the resource
  lived on — and if that deletion later failed, nothing remained to retry. The RESTAction
  path already verified; the native path now does too.
- **Read-only resources get a usable spec** (#75). A resource exposing only `findby`/`get`
  derived its spec from a create body that does not exist, so its identifiers named fields
  present nowhere and it could never resolve — generated, admitted, non-functional.
  Identifiers are now materialised as **selectors**, typed from the observe response.
  Fixing it exposed a second defect: `getBaseSchemaForStatus` returned the first action's
  error, so a findby-only resource aborted on "action 'get' not defined" before findby was
  consulted, losing its status schema too.
- **`compareScope: updatable` is reachable** (#51). RDC has implemented it in full since
  0.20.0, but the CRD enum never listed the value, so the API server rejected it at
  admission — shipped and unusable. Adding it *is* the fix; the issue was half-shipped, not
  stale. A CEL guard now requires an update verb, since without one the comparison set is
  empty and the resource would silently never report drift.
- Dependencies: plumbing `v1.14.2`, whose crdgen fix translates `uniqueItems` instead of
  emitting a CRD the API server refuses — on our path, since every managed resource's CRD
  comes from `crdgen.Generate`. Also `x/crypto v0.55.0` for CVE-2026-56854 (CRITICAL), which
  had turned `main` red on its own as the vulnerability DB updated, and `x/mod v0.40.0`.
- `spec.oasPath` accepts hyphens in the ConfigMap key segment (#74): `[a-zA-Z0-9.-_]` is a
  character *range* spanning `/` and `:`, so it both rejected `my-oas.yaml` and wrongly
  accepted `we/ird`.

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
