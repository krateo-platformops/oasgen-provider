---
type: Log
title: oasgen-provider — log
description: Curated chronological history of oasgen-provider — notable changes and decisions, newest first.
resource: oci://ghcr.io/krateo-platformops/charts/oasgen-provider
tags: [kog, history]
timestamp: 2026-10-03T09:00:00Z
---

# Log

Curated history (notable changes, decisions); release notes stay in GitHub Releases.
Both components ship from one tag at identical versions, so entries below cover the
provider and the rest-dynamic-controller together.

## 2026-10-03 — 0.28.1

**Upgrade from 0.28.0.** 0.28.0 left every RestDefinition reporting `Ready=False` / `Creating`
indefinitely. Nothing was actually broken by it — generated CRDs were Established, every
rest-dynamic-controller was running, instances were correctly labelled and no object was being rewritten
— but the readiness signal never came good, so an umbrella install gating on it reads red forever.

`Observe` renders the controller deployment to work out what the digest *would* be, and compares it
against the digest `Create`/`Update` stored. In 0.28.0 those two paths rendered from different options:
the version-scoped label selector was set on the write paths and missing on the read path. The digests
could therefore never agree. Observe reported "not up to date" on every pass, Update re-rendered and
stored the value it already had, and the gate that marks a RestDefinition Available sits downstream of
that comparison.

The signature, if you are on 0.28.0: the provider logs "Rendered resources digest changed" every few
seconds while `status.digest` never moves and neither does the RestDefinition's `resourceVersion`. It is
a compute-only loop — it rewrites nothing, which is why it costs nothing but a false red.

The fix is one constructor for those options, shared by all three paths, with dry-run the only
difference permitted between the read path and the write paths. Adding the missing field back would have
fixed this instance and left the next field free to diverge the same way.

**No action needed beyond upgrading.** The RestDefinitions recover on their own; nothing has to be
deleted or edited. If any still require a nudge after this release, the digests disagree somewhere else
and that is worth reporting.

## 2026-10-02 — 0.28.0

Version coexistence becomes real: a rest-dynamic-controller now watches only the instances its own CRD
version owns. **Read the Kubernetes floor change before upgrading** — this release will refuse to install
on clusters the previous one supported.

### BREAKING: the chart floor moves 1.33 → 1.36

`oasgen-provider` and `oasgen-provider-crds` now require Kubernetes **1.36**. Unlike the previous 1.33
bound, this one excludes supported clusters: 1.33, 1.34 and 1.35 are all current upstream.

- **Why.** Instances carry `krateo.io/oas-version` to record which served CRD version owns them, and the
  controller is started with an *exact* label selector on it. The `MutatingAdmissionPolicy` that writes
  that label at admission is GA from 1.36. Below that nothing writes it, so the selector matches nothing
  — an install that comes up green and reconciles no instance at all. A refused install is the better
  failure, so the chart refuses.
- **Not a rounding.** CRD validation ratcheting (the old 1.33 reason) is still required; it is simply no
  longer the binding constraint.
- `controller-render-service` keeps no floor. It ships no CRDs and no controller, so neither requirement
  applies, and it stays installable on clusters the other two now refuse.

### Watching is scoped to a version

The controller for a version watches with exact equality on `krateo.io/oas-version`, the same construction
composition-dynamic-controller uses.

Exact, rather than "everything except the other versions". An exact selector is a constant function of the
controller's own version and cannot go stale. The set-based scheme it replaces did: a controller deployed
with `notin (v26)` starts claiming v28-labelled instances the moment a v28 appears, because the set it
excludes was fixed when it was rendered. That held under one snapshot and not across time.

### The label is now written in one place, and backfilled once

`rest-dynamic-controller` no longer stamps the label when it observes an unlabelled instance. That path
could never be reached under an exact selector — an unlabelled instance matches no watch, so it is never
observed — and a safety net that cannot catch the case it was written for is worse than none.

An absent `MutatingAdmissionPolicy` API is therefore an error now, where it used to be tolerated.

**The upgrade stamps existing instances for you.** Anything written before the policy existed carries no
label, because the policy only stamps on create and update — on an established cluster that is most
instances, and under an exact selector all of them would have been orphaned by this release. The provider
now lists instances carrying no label and stamps the served version before deploying the controller that
selects on it. It fills an absence and never moves an existing value: rewriting one would be migrating the
instance onto another version, which is a deliberate act and not a side effect of an upgrade.

## 2026-10-02 — 0.27.1

Two fixes to the oas-version policy, both found by validating 0.27.0 on a live cluster rather than by
review. Neither changes an API; both change what the provider does with a policy that is already there.

- **A deleted policy was never restored.** `EnsureVersionPolicy` was reachable only from the CRD
  generation path, which runs when the CRD is absent or when the OAS/resource hash changed. On a stable
  install neither happens, so a policy removed out of band — by a cleanup script, by policy tooling, or
  by an operator trying to force a refresh — stayed gone until somebody edited the OAS document or the
  resource spec. Nothing reported its absence.

  It is now ensured on every reconcile. That also makes a stale policy fixable at last: **delete it, and
  the next reconcile recreates it from the running code.** There is still no in-place update, by design.

  Benign today, because rest-dynamic-controller stamps the label itself when it observes an instance
  without one. It would not be benign under version-scoped watching, where an unlabelled instance matches
  no controller's watch, is therefore never observed, and so never reaches that fallback at all.

- **Two API groups could collide on one policy.** The policy name slugifies the group with
  `[^a-z0-9]+` → `-`, so `github.krateo.io`, `github-krateo.io` and `github.krateo-io` all produce the
  same name. Accepting `AlreadyExists` without looking cannot tell two RestDefinitions converging on
  their shared group's policy — which is intended — from two different groups colliding, where the second
  group matches no policy and its instances are **never stamped**, with no error and no event.

  The existing policy is now read and its match constraints checked. A different group is refused with a
  message naming both. Two definitions in one group still converge exactly as before.

- **A stale policy can now be seen.** Policies carry `krateo.io/oas-version-policy-hash`, a content hash
  of the spec they were created from, so a policy that differs from what the running build would write is
  reported as a `VersionPolicyStale` event. It is a warning, not a failure: a stale policy still stamps.

  **Expect this event on every group immediately after upgrading from 0.27.0.** Every policy 0.27.0 wrote
  predates the annotation, so it reads as stale — correctly, since those are exactly the policies that
  cannot be corrected except by deleting them. Delete each one and let it be recreated; the replacement
  carries the annotation and goes quiet.

- Fixed in passing: the binding is ensured even when the policy already exists. Deleting the binding
  alone previously left the policy present and **inert** — stamping nothing while looking healthy.

No chart change, and no new permissions: the provider already had `create` and `get` on
`mutatingadmissionpolicies`, which is all of this needs.

## 2026-10-02 — 0.27.0

Per-version groundwork, and a validation rule relaxed so a KOG chart can grow a status field
without being deleted and recreated.

- **`additionalStatusFields` is append-only instead of immutable.** A KOG chart that gains a status
  field could not be upgraded in place: the apply was refused, and the only way through was deleting
  the RestDefinition and its CRs. Entries may now be added at the end; removing, renaming, reordering
  or changing an existing entry is still refused, because those break stored objects and printer
  columns.

  Two new bounds come with it, required to keep the validation rule inside Kubernetes' CEL cost
  budget: at most 64 entries, each at most 253 characters. A RestDefinition exceeding them would now
  fail validation.

- **Instances now record which served CRD version owns them** (`krateo.io/oas-version`), stamped by a
  per-group MutatingAdmissionPolicy and, below Kubernetes 1.36 where that API does not exist, by the
  controller. Groundwork for per-version coexistence; nothing changes behaviourally yet.

## 2026-10-01 — 0.26.1

A patch carrying a **security fix**. Read it before upgrading — and note that the fix is NOT in
0.26.0, which this corrects: 0.26.0 shipped the chart split only.

### SECURITY: the verbose request dump leaked credentials to pod logs

With `krateo.io/connector-verbose` on, `Authorization: Bearer <token>` was written in cleartext.
Anyone able to read the controller's logs could read the resource's credential.

The redactor only replaced values it had been TOLD were sensitive, and that list is populated solely
from secretRef-resolved fields. A bearer token supplied through the resource's own Configuration is
applied straight to the request and never registered, so it was never a candidate for replacement.
The redaction was working exactly as written and still leaking, which is the worst combination: a log
that looks sanitised.

Credential headers are now redacted BY NAME, whatever their value. **The leak is in logs already
written** — if you have run a verbose RDC, assume the token is in those logs and rotate it.

### Also in this patch

- **A path or query parameter can now be transformed on its way out** (`valueMapping: jq` on a
  request-direction entry). That was the one part of the request surface with no transformation hook,
  so "strip a prefix from this path parameter" needed a Go plugin to run one `strings.TrimPrefix`.

  It was not inert before, either: a declared request-direction jq was SKIPPED, producing no field at
  all. A missing path parameter still yields a URL that parses, so the API answers 404, and the
  reconciler creates on not-found. Anyone who declared one was silently creating duplicates.

- `controller-render-service`'s `global` no longer forbids extra properties, so the engine may inject
  its own keys.

## 2026-10-01 — 0.26.0

A minor carrying a **breaking chart change**. (The security fix described above is in 0.26.1, not
here — if you are on 0.26.0 with verbose logging enabled, upgrade.)

### `oasgen-render` becomes its own chart

A **breaking chart change**: `render.*` is removed from the provider chart's values, and
`render.enabled=true` is now rejected rather than ignored.

- **Why.** The render service was a conditional sub-deployment of the provider chart, so it was the
  only Krateo render service that needed install-time configuration: six `componentValues` keys set
  by hand (`render.enabled`, `fullnameOverride`, `service.port`, `resources`,
  `snowplowEndpoint.{enabled,name}`) or the portal's Controller Builder Preview and Publish were
  off. Its sibling `blueprint-render-service` is a first-class chart whose defaults are already
  correct, so it needs none. A fresh install therefore got a working Blueprint Builder and a dead
  Controller Builder, for no reason visible at the call site.
- **What.** New chart `helm/controller-render-service`, modelled on `blueprint-render-service`:
  Deployment, Service, and the snowplow endpoint Secret. Defaults are correct for composition use
  (`snowplowEndpoint.enabled: true`, name `controller-render-endpoint`, port 8080 on both the
  container and the Service, matching the sibling), so installing it as a Krateo component needs no
  configuration. Published from this repo, so it carries the SAME version as the provider and CRD
  charts — a co-versioned triple.
- **Deliberately not changed,** to keep this a move rather than a rewrite: `podSecurityContext` and
  `securityContext` remain values defaulting to `{}`, exactly as the sub-deployment had them.
  Hardening them to the sibling's distroless-nonroot profile needs the image verified against it
  first.
- **Migrating.** The new chart produces the same object names the sub-deployment produced once
  `render.fullnameOverride` was set, so the two cannot coexist: drop the provider chart's render
  objects first, then install the new component. Nothing in the controller changes.

### Also in this minor

- **A concurrent `findby` could lose its OAS download.** `BuildClient` used one fixed temp directory
  and deleted the whole thing on return, so with the default 5 workers one reconcile deleted
  another's file mid-flight. Its rate tracked concurrency rather than correctness — nearly invisible
  in steady state, and constant after a pod restart, where it fired 852 times in an hour on a live
  cluster and looked like a regression in a release that had not touched the module.

- The otel log stack moves to 0.22.0 **as a set** — dependabot cannot produce this change, because the
  modules only exist as a consistent group and bumping one alone does not build.

## 2026-10-01 — 0.25.1

A patch: chart knobs for placing `oasgen-render` behind a platform naming convention, and the
memory ceiling that asking for them exposed. Nothing in the controller changes.

- **The render service had no memory ceiling and accepted 32 MiB of untrusted OpenAPI** — a
  combination that could not work. Asked to size a limit for a 32 MiB document, we measured what
  rendering costs rather than guessing: parsing and rendering holds roughly **29x the document size
  as live heap** at peak (35 KiB → 8 MiB, 1.3 MiB → 46 MiB, 3.1 MiB → 90 MiB).

  At 32 MiB that is ~930 MiB for a single request. So the cap and any sane container limit were
  mutually inconsistent: the service accepted bodies it could not render without being OOM-killed,
  and on a shared node a pathological document could take neighbours with it. Adding a limit alone
  would have converted unbounded growth into a hard kill rather than fixing anything.

  Both ends moved. `OASGEN_RENDER_MAX_BODY_BYTES` defaults to **4 MiB** instead of 32 MiB, and
  `render.resources` now ships 100m/128Mi requests and 500m/512Mi limits. 4 MiB covers every real
  document we have seen and sits at ~116 MiB live heap; the one known consumer sends ~0.6 MiB. The
  two numbers are documented as coupled, because raising the cap alone restores the inconsistency.

  The asymmetry with the manager's `resources: {}` is deliberate: the manager parses documents the
  platform put in a ConfigMap, while `/render` parses whatever any caller in the cluster sends it.

- **`render.fullnameOverride`** names the render Deployment, Service and selector labels, so a
  platform can impose its own naming without forking the chart. Unset, every existing name is
  unchanged. Note that a Deployment's `spec.selector` is immutable: setting or changing this on a
  release that is ALREADY running the render pod fails the upgrade and the Deployment must be
  deleted first.

- **`render.snowplowEndpoint.{enabled,name}`** creates a Secret whose `server-url` points at the
  render Service, so a consumer reads the address from one place rather than having the same host
  spelled out in two charts. The URL is built from the same helpers the Service uses, so a rename
  or a port change moves both together.

- **`render.CRDs` is now pinned by a golden file.** It had no guard: the existing parity test
  compares the `/render` surface against the controller's apply path, but both call `render.CRDs`,
  so a change to it moved both sides identically and the test stayed green. That function is the
  generation half of every RestDefinition reconcile, so a silent change to it changes the CRDs
  served to users.

## 2026-09-30 — 0.25.0

A minor: one new service, one findby correctness fix that changes generated status types, and a
**Kubernetes floor that will refuse the upgrade below 1.33**. Read that last one before rolling.

- **The chart now requires Kubernetes >= 1.33** (#152). This is the change most likely to affect an
  upgrade, and it is unrelated to everything else here: `helm upgrade` will now be REFUSED on an older
  cluster rather than proceeding.

  It is a correctness requirement, not a compatibility bound. The Observe path merges into whatever
  status is already stored and never clears it, so a stored value that violates a tightened CRD schema
  is rewritten unchanged on every reconcile. That is harmless only because CRD validation ratcheting
  makes the API server skip validating a field a write leaves unchanged. 1.33 is where that gate became
  GA *and* `LockToDefault: true` — the first version where a cluster operator cannot turn it off. Below
  it, a tightened status schema can wedge existing resources rather than rewrite them.

  Costs nothing real: the modules build against client-go 1.35, so the supported skew already implied
  ~1.34-1.36, and everything excluded is end-of-life upstream.

- **`findby` returned the wrong schema for an envelope response** (#110). `ExtractSchemaForAction`
  unwrapped only when the 200 schema was itself an array, so for `{total, values:[...]}` — what most real
  APIs return — the base schema was the envelope. Identifiers were looked for a level above where they
  live: their types degraded to `string`, and the not-resolvable warning fired identically for a valid
  identifier and a genuinely absent one.

  **This changes generated status types**, but far more narrowly than it sounds: only for a resource whose
  status is built from the findby envelope, i.e. one with **no `get` verb**. Anything with a `get` was
  already correct. Verified on a 34-resource live provider where 33 had a `get` and were unaffected.

  Found while fixing it: the runtime picked the collection with "the first value that is a list" over a Go
  map, and map iteration order is randomised — so an envelope with two arrays searched a different one on
  different reconciles. Both sides now follow one rule and **refuse** when two or more arrays leave it
  ambiguous, rather than guessing. A findby that guesses wrong reports not-found, and the reconciler
  creates on not-found, so the cost of a wrong guess was a duplicated external resource.

- **`findby` matched raw list items, so it could not key on a normalized field** (#145). The match compares
  against the CR with `DeepEqual` — API-domain data against CR-domain data — while `responseTransform` and
  response `fieldMapping`, the things that bridge those domains, ran only afterwards. A kind therefore could
  not key a findby on any field whose shape differs between the list response and the spec, even when the
  same field normalized correctly for `get`. Items are now normalized before matching.

- **A deleted RestDefinition could block CRD regeneration forever** (#137). The ownership annotation names a
  RestDefinition; the check compared that name and never asked whether it still resolved to anything. A
  composition recreation leaves such annotations behind, and they sit inert until some unrelated OAS change
  needs a regeneration — which is then refused permanently, arbitrarily far from the event that armed it.
  On a live cluster: a 1h40m outage from an ordinary chart version bump, silent four levels up. A confirmed
  dangling owner is now adopted and the displaced husk reported as an Event; an *unverifiable* one is
  neither adopted nor rejected.

- **`oasgen-render`, a preview service** (#157). `POST /render` turns RestDefinitions plus inline OAS
  documents into the CRDs they would generate, without fetching or applying anything. Second binary in the
  same image; chart Deployment and Service behind `render.enabled`, **off by default**, ClusterIP, no
  service-account token. The generation half of the controller's path was lifted into a shared package so
  the preview runs the same code — verified byte-identical against the pre-refactor controller output.

  One behaviour change on an error path: if the Configuration CRD fails to generate, nothing is applied.
  Previously the resource CRD was applied and then the reconcile errored, leaving a half-installed pair.

- **Published images now carry a real `service.version`** (#127). Both were built with no `build_args`, so
  the stamp was empty and the collector fell back to the chart label — which cannot distinguish two images
  built from different commits at the same release.

- **Generation warnings no longer render `generation error at :`** with an empty location. Most emitters set
  no path, so most warnings read as a broken format string — a poor way to introduce a warning whose job is
  to be believed.

## 2026-09-25 — 0.24.0

A minor rather than a patch: `findby` gains a second pagination strategy, and the CRD grows the
fields to declare it. Nothing existing changes shape — a RestDefinition written for 0.23.x is
admitted unchanged.

- **`findby` could not paginate the strategy most APIs actually use** (#119). Only
  `continuationToken` existed. Page-number pagination — `?page=N&per_page=M` — covers the
  large majority of collection endpoints (162 of the 219 array-returning GETs in GitHub's own
  OpenAPI document, none of which use a continuation token), and against every one of them a
  `findby` read page one and stopped.

  The consequence was not a slow search. A search that stops early returns not-found, and the
  reconciler **creates** on not-found — so a resource sitting on page two was not merely missed,
  it was duplicated.

  `type: pageNumber` is now a first-class strategy. Nothing in it names a vendor: 0- vs 1-based
  numbering, the parameter names, and how the last page is recognised are all declared, because
  an engine that knew one API's spelling would simply relocate the bug to the next one. Three
  end-of-collection mechanisms, exactly one per declaration — a header that marks a next page, a
  total read from the body, or the short-page rule when nothing is declared.

  `startPage` and `maxPages` are required with no defaults, deliberately. A defaulted
  `startPage` guesses at 0- vs 1-based and silently skips a page; a defaulted `maxPages` is a
  bound nobody chose that ends searches at a number the author never considered.

  Preceded by a refactor (PR #141) that made the underlying bug class unrepresentable: a
  paginator's verdict is now three-valued — more pages, genuinely exhausted, or could-not-tell —
  where it used to be a bool whose `false` meant both "the collection ended" and "I have nothing
  left to go on", with the caller turning either into a 404. Only *exhausted* may become absence.
  Exhausting `maxPages`, a missing declared header, an unreadable body path: each reports that
  the walk could not conclude, and the reconcile fails loudly rather than creating a duplicate.

  That refactor also completed a stub. `continuationToken`'s response token could be documented
  as `header` or `body` while the body branch was a `// Not implemented yet` comment that fell
  through to "no token" — so a body token ended the walk after page one, silently. It works now,
  and the CRD enum admits it.

- **Every Kubernetes Event the provider emitted was silently dropped** (#126). The chart's
  ClusterRole granted `events` only in the legacy core group (`""`). Modern client-go event
  broadcasters write to `events.k8s.io`, so the broadcaster 403'd on every emit.

  Nothing failed loudly, because event emission is best-effort by design: the reconcile carried on
  and the provider looked healthy. What was lost was the diagnostic channel — `kubectl describe` on
  a struggling resource showed nothing, so the one place an operator looks first was empty for
  reasons unrelated to the resource. Both API groups are granted now.

## 2026-09-18 — 0.23.2

Two field-reported defects, one of which could destroy production infrastructure.

- **Deleting a RestDefinition could delete real external resources** (#125). Its generated CRD was
  uninstalled unconditionally, which cascade-deletes every CR of that kind — and those CRs carry
  finalizers that delete the *external* resource. So a routine lifecycle operation could remove live
  GitHub repositories, with no per-CR opt-in and nothing to undo it.

  A guard existed (the `restresources-still-exist` finalizer) but was consulted only for the
  Deployment teardown, never for the CRD, so the ordering was: uninstall the CRD, *then* report that
  resources still exist. It also reflected a previous reconcile's observation rather than the cluster's
  state at the moment of deletion.

  `Undeploy` now lists instances immediately before `crd.Uninstall` and refuses if any exist — covering
  every caller, not just this path. A listing **failure** also refuses: "I could not count" must never
  read as "there were none", and here that conflation is unrecoverable rather than merely wrong. A
  missing CRD is the one exception, since the kind is already gone.

  There is deliberately no force flag. Refusing is recoverable — delete the instances, then the
  RestDefinition; the alternative is not.

- **A renamed resource never reconciled** (#132). `http.Request` built as a struct literal leaves
  `GetBody` nil, and Go will not follow a method-preserving redirect whose body it cannot replay — it
  returns the 3xx to the caller, where it fails the OAS status gate. GitHub serves a renamed
  repository's old name with a 307, so the CR sat `ReconcileError` on `unexpected status: 307` forever.

  Fixed on the transport, not by accepting 307 as a success code: treating "this resource lives
  somewhere else now" as success would silently stop reconciling the real object.

Also: OpenTelemetry dependency bumps across both modules.

## 2026-09-10 — 0.23.1

Two field-reported failures, both of which presented as a resource being permanently stuck rather
than as an error.

- **A RestDefinition could wedge forever while its controller was still starting** (#122). `Observe`
  reported `ResourceExists: false` when the dynamic controller Deployment existed but had not
  finished rolling out. provider-runtime reads that as "the external resource is gone" and re-enters
  the create handshake, so a reconcile landing mid-rollout re-set
  `krateo.io/external-create-pending` on a resource that had **already** been created and then wedged
  on `errCreateIncomplete` once the creation grace period lapsed. The RestDefinition sat `Ready=False`
  permanently with a CRD that was in fact served and functional, and took its owning composition down
  with it.

  Reported from a 34-RestDefinition install where exactly one wedged — the slowest controller to
  become Ready — with `external-create-succeeded` at 20:34:44 and `external-create-pending` re-set at
  20:35:59, 75 seconds later. It is timing-dependent, so it recurs intermittently on large multi-kind
  installs and never on small ones.

  Existence and readiness are now separate axes: readiness is surfaced through the condition, and
  only a genuinely absent Deployment reports non-existence. That asymmetry is deliberate — where the
  Deployment is missing, `Create` is what deploys it, so reporting it as existing would mean the
  controller is never created at all.

- **A paginated `findby` could never terminate** (#119, in part). The pagination loop had no page
  cap: a server that always advertises a next page walked forever, holding a reconcile worker and
  producing no diagnosis. It is now bounded.

  What the bound *returns* matters more than the bound itself. It is deliberately not a 404, because
  the reconciler acts on not-found by **creating** — so reporting the cap as absence would say "this
  does not exist" when the truth is "I stopped looking", and create a duplicate of the object it never
  finished searching for. "I scanned N pages and did not conclude" stays a distinct answer.

  This is the safety half of #119. The `pageNumber` strategy itself is still open.

## 2026-09-09 — 0.23.0

A minor rather than a patch: the generated CRD shape changes for read-only resources, and the
delete path was restructured. Everything here was found by running the provider against a live
vendor API, not by inspection.

- **Query parameter values were escaped twice** (#113). `buildPath` called `url.QueryEscape` and
  then handed the escaped string to `url.Values`, whose `Encode()` escaped the `%` of the first
  pass. A git ref `builder/sock-shop` left as `builder%252Fsock-shop`; the API decoded once, looked
  up a ref literally named `builder%2Fsock-shop`, and returned 404 — so observe never saw the file
  it had just created and the CR looped `Creating` forever. Path parameters were never affected and
  are now pinned by a test so they cannot become so.

- **Dotted identifiers emitted a flat key** (#106). A nested identifier such as `metadata.name` was
  generated as a property literally named `"metadata.name"`, while RDC reads identifiers as nested
  paths. The two could never agree, so `findby` matched nothing for any read-only resource with a
  nested identifier — silently, on a CR reporting `Ready`. A regression in the #75 selector work;
  `composeStatusSchema` had always handled the same class of value correctly, and the selector
  builder now uses the same helpers.

- **A 200 tombstone is absence, not presence** (#111). Some APIs answer `GET` for a deleted resource
  with 200 and a `status: Deleted` record rather than 404. The delete verification read that as
  "still there" and never released the finalizer. `notFoundBody` already expressed exactly this and
  Observe already honoured it; the delete path now consults the same predicate, so absence has one
  definition instead of two.

- **One arbiter decides the finalizer** (#103). Three delete bugs in three releases (#77, #98, #101)
  were a single defect: the authoritative existence check sat at the *end* of `Delete()`, so any
  branch returning earlier decided without it, and each fix moved the check one branch earlier. It is
  now the sole arbiter, and the delete call is best effort — its status code no longer decides
  anything by itself. The rule: **release when the resource is observably gone, whatever the delete
  call said**, except where absence cannot be established, in which case the delete result governs
  and "could not check" means retry rather than "assume gone".

  The delete contract is now tested as a matrix over {delete outcome} × {resource present} ×
  {verifiable} rather than one case per past incident. Four tests in this codebase have passed while
  testing nothing; two of them shipped, as #98 and #101.

Also: grpc bumped in both modules.

Not in this release, and tracked: per-verb `oasPath` (#108), envelope findby unwrapping (#110), and
request-direction value transformation on path parameters (#117). Each changes API surface or CRD
compatibility and wants a deliberate decision rather than inclusion in a bugfix cut.

## 2026-09-03 — 0.22.3

Third fix on the delete path in as many releases, and the last of one root cause.

- **The observe verb decides, not the delete status code** (#101). 0.22.2 released the
  finalizer when DELETE returned 404, but any *other* error still returned before the
  existence check could run — so an API that answers a delete with an error for a resource
  it has already removed hung the CR forever. Seen on Aruba `security/Kms`: `DELETE` → 400
  "Some kms keys are not deleted", while a direct `GET` on the same id → 404.

  The rule that finally holds is the one the reporter stated: **the finalizer releases when
  the resource is observably gone, whatever the delete call said.** The delete status code is
  a proxy; the observe verb is the ground truth.

  One safeguard is load-bearing. `externalResourceStillExists` previously answered "not
  present" both when the resource was verifiably absent and when it could not check at all —
  no get verb, no buildable request. Releasing on the latter would orphan the resource, which
  is the failure #77 exists to prevent, so it now reports whether it actually verified, and
  only an affirmative absence releases. Where deletion cannot be verified the delete error
  stands and the operation is retried.

This is the third bug of identical shape: #77, #98 and #101 were each a branch that decided
the finalizer's fate before reaching the existence check, and each fix moved that check one
branch earlier. The structural remedy — making Delete level-triggered, so the check leads
rather than trails, and expressing "in progress" as `Pending` instead of an error — is
tracked in #103 rather than attempted in a hotfix.

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
