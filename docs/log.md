---
type: Log
title: oasgen-provider — log
description: Curated chronological history of oasgen-provider — notable changes and decisions, newest first.
resource: oci://ghcr.io/krateo-platformops/charts/oasgen-provider
tags: [kog, history]
timestamp: 2026-10-27T17:30:00Z
---

# Log

Curated history (notable changes, decisions); release notes stay in GitHub Releases.
Both components ship from one tag at identical versions, so entries below cover the
provider and the rest-dynamic-controller together.

## 2026-10-27 — 0.35.0

One fix, for a class of document that could not be used at all.

### A `+` in a component name no longer fails the whole document

A document with a component whose NAME contains `+` did not generate a degraded CRD — it generated
nothing:

```
parser error [ModelBuildError]: failed to build V3 model: component
  `#/components/schemas/…ApiKeyQuotasDto+QuotaValue` does not exist in the specification
```

`.NET` emitters produce these names routinely: a nested CLR type `ApiKeyQuotasDto.QuotaValue`
serialises as `ApiKeyQuotasDto+QuotaValue`, because `+` is how the CLR spells a nested type. Any
vendor shipping an ASP.NET-generated document can hit this, and Aruba's CMP profile document did.

The `$ref` resolver unescaped fragments with `url.QueryUnescape`, which applies
x-www-form-urlencoded semantics and decodes `+` to a **space**. The lookup therefore asked for a
component whose name contained a space, which does not exist. Both spellings failed, by different
routes — a literal `+` was decoded once, while `%2B` was decoded twice, the second pass being guarded
on the presence of `%`. Either way the name ended with a space.

Fixed in the parser fork with `url.PathUnescape`, which leaves `+` alone and still decodes `%2B`
(`krateo-platformops/libopenapi` v0.21.9, four call sites). Both modules move to it.

**The pin also changes organisation**, from the pre-migration `krateoplatformops` to
`krateo-platformops`. That was checked rather than assumed: the new fork's parent is the old fork,
their `v0.21.8` tags are the same commit `0af9c359`, and that differs from upstream's `d3dcac4e` — so
the Krateo fork does carry its own patch, and `v0.21.9` sits directly on top of it. Nothing was
dropped in the move.

Verified against the real 45 KB vendor document through `/render`, from two binaries built from
identical source differing only in this pin: the old one returned `crds: []` and the error above, the
new one `crds: 1`, `configurationCrds: 1`, `errors: []`. Unit tests alone would not have established
that a document of that size builds a V3 model.

### Not fixed here

Generating from that document surfaced a second, unrelated defect, filed as #236. A resource whose
document has **no create endpoint** but whose RestDefinition declares `update` gets a spec built
read-only — identifiers become selectors — so the declared update sends a body missing the vendor's
own fields, and generation reports no diagnostic at all. For Aruba's AiKey that means an update would
`PUT {"metadata":{"name":"…"}}` and silently drop the `properties` object holding the key's quotas.
Until that is fixed, such a resource is safe to adopt and read, not to update.

> **Correction (2026-10-27).** "the `properties` object holding the key's quotas" is wrong, and was
> wrong when written. Tracing the document's reference graph while fixing #236:
> `ApiKeyQuotasDto` is referenced **0 times** out of its 99 `$ref`s and `ApiKeyQuotasDto+QuotaValue`
> only by `ApiKeyQuotasDto` itself, so the pair is an orphan island in `components` mentioned nowhere
> under `paths`. That is consistent with the fix above rather than contrary to it — libopenapi resolves
> every component, not only reachable ones, which is exactly why an unreachable schema could fail the
> whole model build — but it means the quotas are in no request or response shape. The request's own
> `AiKeyPropertiesDto` declares no fields at all. What the update actually omitted was
> `metadata.location` and `metadata.tags`. Same defect, different fields: the names in the original
> sentence were inferred from a component name that was never traced.

## 2026-10-27 — 0.34.0

Three fixes to paths that could not work, and the async engine driven end to end for the first time.
Two of the three were found by the integration coverage added here rather than reported by a user.

### Per-verb `oasPath` now reaches the controller

A resource whose verbs span two OAS documents generated a correct CRD and then could not execute the
verb that needed the second one: `retrieving requested params: operation not found: POST`. The
generator has resolved `oasPath` per verb since 0.25.0; the controller never read the field at all --
`VerbsDescription` had no `OASPath` on that side at any tag -- so the feature worked at **no released
version** (#220, fixed in #227).

Each verb is now executed against the document it declares, resolved by a per-reconcile client set
keyed by document path, so verbs sharing a document share one fetch and parse.

One client per reconcile *entry point* would not have been enough, and that is the part worth knowing
if you read the change. **Five uses execute an action their entry point never named**: the delete
verification runs GET from `Delete`; the async re-read runs GET from a create or update path;
`IsResourceKnown` runs GET from `Observe`, before the get-or-findby decision; the findby branch; and
`UpdatableBodyPaths`, which `compareScope: updatable` reads during `Observe` to ask what the UPDATE
verb's body can express. A client resolved once per entry point addresses the wrong document for
every one of those.

`UpdatableBodyPaths` also joins `UnstructuredClientInterface`, where `RequestedBody` and
`RequestedParams` already were. Being reachable only through the concrete type is what forced
`Observe` to hold one concrete client, which was the obstacle to resolving per verb.

### `postGet` can re-read a resource whose identifier the server assigns

An async create answers 202 with an operation handle and no resource body, so the identifier its get
verb needs exists nowhere on the CR. `postGet` built its request from the CR alone and failed with
`missing path parameter: id` -- **after the operation had already succeeded**, so the external
resource existed while the CR errored on every reconcile (#229, fixed in #231).

An ordering fix rather than a new mechanism: `BuildCallConfig` already resolves path parameters from
spec **or** status, and `additionalStatusFields` already declares which response fields reach status.
`postGet` simply ran before anything had projected the trigger response. It now projects first, in
memory, with the same two calls the caller uses.

Residual limitation, stated so the fix is not read as covering more than it does: a trigger response
carrying the handle **and nothing else**, with the identifier appearing nowhere until the resource is
fetched, still cannot be re-read by id -- you would need the id to fetch the thing that would tell
you the id. That shape needs `findby`.

### A correct async create no longer warns about a missing schema

`no suitable response schema found for action 'create'` fired on a perfectly correct async
definition: such a verb answers with an operation handle, not the resource, so there is no
resource-shaped schema to compare against the base action's. The CRD was already right; the warning
pointed authors at a problem they did not have (#228, fixed in #232).

The comparison already exempted a `dispatch` base for the same stated reason -- *"the dispatch's own
response is the 204-empty body that made this verb necessary"* -- and async was never given the same
treatment. Done through the verb model rather than as a special case: `Async` is now mirrored into the
generator's `Verb` alongside `Correlation`, carrying the document-facing fields only.

### The async engine is executed, both models

`driveAsync`, the `operationRef` extraction, the poll loop, `driveAsyncRequeue` and
`reReadStatusOnAsyncSuccess` had **no integration coverage**: no testdata declared `async:`, so all of
it was exercised only by unit tests over its helpers. That is the configuration that concealed the
whole `dispatch` verb being undriven before 0.33.0, and it concealed both fixes above (#230).

The mock vendor answers 202 with a handle and no body, then reports `running` until polled enough
times. The tests assert the **vendor's** poll count, not the CR's state, because the CR shows the
outcome either way and cannot say whether the controller actually polled. Model B additionally
asserts that the handle is recorded, that `Create` did **not** poll to completion inline -- the only
thing distinguishing the two models -- and that the handle is cleared on termination, without which
every later reconcile re-polls a finished operation forever.

## 2026-10-26 — 0.33.0

Two fixes to paths that could never succeed, both found by running the thing rather than reading it,
and both of which **change what the controller does** rather than only what it reports. Plus the
first end-to-end execution of the `dispatch` verb, which had shipped with no test that drove it.

### SECURITY-adjacent: `notFoundBody` was never evaluated when verifying a delete

A tombstone API — one that answers `GET` for a deleted resource with 200 and a record saying
`status: Deleted`, rather than 404 — could not have its deletion verified at all. The delete path
handed the `notFoundBody` predicate the whole `restclient.Response` **wrapper** instead of the
response body, and `jqengine.Program.Run` canonicalizes its output but not its input, so gojq was
given a Go struct it cannot index. The predicate errored on every call, landed in the deliberately
forgiving "unverifiable" branch, and the delete result governed.

Introduced by the commit that added the feature (#116, "a 200 tombstone is absence, not presence",
closes #111), so it never worked in any release carrying it — **0.23.0 through 0.32.0**. The comment
already sitting above the call site described its own bug: *"the same shape as every other bug on
this path: two code paths that must agree, and one was not updated."*

Three outcomes change:

| situation | before | now |
|---|---|---|
| tombstone, `DELETE` succeeded | unverifiable → delete trusted (right outcome, never verified) | verified gone |
| tombstone, `DELETE` failed | finalizer held **forever** | verified gone → released |
| 200 the predicate does *not* match | unverifiable → successful delete trusted → released | verifiably present → held and retried |

The second row is the wedge #111 was filed to remove, still fully in force until now. The third is
the one to weigh before upgrading: a predicate that has been dead since 0.23.0 becomes load-bearing,
so one that fails to match a real tombstone will now hang a delete that previously fell through. That
follows from #111's premise — a 2xx means the deletion was *requested*, not completed — and from the
priority table in `resolveDeleteOutcome`.

**Verified on live infrastructure** by the Aruba KOG side, against `security/Kmip`, the resource #111
was filed from. The `:164` debug line — *"Get returned 200 but notFoundBody reports the resource
absent"* — was emitted for the first time in the feature's life, exactly once, against 355 prior
holds, and the finalizer released by that path:

```
Get returned 200 but notFoundBody reports the resource absent; treating as deleted
Delete returned an error but the external resource is verifiably absent; releasing finalizer
                     deleteError: "unexpected status: 400: ..."
```

A 400 `DELETE` releasing the finalizer can only be the verified-absent branch; the fallback requires
either a successful delete or no get verb. **To be exact about the order: that run confirmed the fix
after it had merged, on green CI and unit evidence. The live evidence did not gate the merge**, and
is recorded here so nobody infers that it did.

That run also measured why the priority table is right. One Kmip went
`InCreation` → `CertificateAvailable` → `Deleting` → tombstone over 102 minutes, with `DELETE`
answering **400** in the wrong state and then **500** twice in the right one before the resource
proceeded to delete anyway; a sibling KMS took three `DELETE`s over 23 minutes, each answering 202
while staying `Active`. The delete status code carries almost no information about whether the delete
took, which is #111's premise from the other direction. Note also that a healthy Kmip's state is
`CertificateAvailable`, not `Active` — anything treating `Active` as a universal success value is
wrong. The predicate's `(.status | type) == "string"` guard is load-bearing by design, not
decoration: this API omits declared fields conditionally (#152).

Nothing in the suite distinguished the two shapes, because the existing `notFoundBody` tests all call
the helper directly with a map — they covered the helper and neither caller. Six tests now drive
`externalResourceStillExists` and `resolveDeleteOutcome` instead.

### Correction to the 0.23.0 entry

The 0.23.0 entry for #111 ends *"the delete path now consults the same predicate, so absence has one
definition instead of two."* **That was not true when written** and becomes true only with this
release. For 0.23.0 through 0.32.0, deletion of a tombstone resource was verified only by the
trusted-delete fallback, and the predicate was never consulted on that path whatever it said.

### A drift that cannot converge now says which field, and stops

A resource whose spec can never match the API's response sat in a permanent observe → update loop
reporting `Ready=False` with `reason=Creating` and no indication of which field was at fault. The
drift comparison computed a reason and discarded it, for two independent causes: nothing wrote status
on that branch, and the runtime re-fetches the object immediately after `Observe` returns, so an
in-memory condition is dropped. The only condition that survived came from the Update leg.

Now: every difference carries the field path it was found at (`nodePools[0].instance`, slice index as
its own segment), the condition names it, and after `maxDriftRepeats` observations of the *same*
difference the controller stops issuing the update that cannot fix it — `Ready=False` with
`DriftNotConverging`, the field named, and a pointer to `responseTransform`, which is the usual
remedy for a request/response shape divergence. The budget is keyed on generation, so editing the
spec restarts it. New condition reasons: `Updating` (replacing the runtime's `Creating` on the update
path — a resource that has existed for days is not being created) and `DriftNotConverging`.

Cost: one extra `PATCH` per poll on a drifting resource, to maintain the counter. That buys
termination after a fixed number of futile updates instead of an unbounded loop.

**The budget was very nearly not a bound.** `CompareExisting` returns on the first difference it
finds, so key order decides *which* difference is reported — and Go randomizes map iteration. A
resource drifting on two sibling keys reported a different field path per observe (measured on the
real Aruba Kaas shape: 2 distinct paths over 200 comparisons, split 173/27), the signature flipped,
the counter reset, and the halt fired only when the reported field happened to repeat five times
running. Keys are now walked in sorted order, which makes the reported difference a deterministic
function of the two documents without changing what counts as a difference. It was independently
wrong for the condition: `Ready` named a different field to each reader on each poll, for drift that
had not changed. Caught by the Aruba KOG side reading the branch before running it.

### The `dispatch` verb is now executed end to end

`dispatch` shipped in 0.32.0 with 14 unit tests over its helpers and **nothing driving the wiring** —
`grep -c dispatch` over the integration suite returned 0. There is now a mock vendor replicating
GitHub Actions `workflow_dispatch`: a bodiless 204 trigger that identifies nothing, and runs that do
**not** echo the dispatch inputs, so the correlation token arrives only in `display_title`. A mock
that echoed the inputs back would let a broken correlation implementation pass.

Three cases, none of which had ever run: correlation by a token that reached the run only through
`display_title`, with status projected from the matched item; a token present in the document but off
the configured `echoedAt` path **not** matching, so correlation cannot degrade into "find my token
anywhere"; and — the one that matters most — an invocation that never becomes observable exhausting
its budget, reporting `DispatchedButNotObserved` naming `correlation.echoedAt`, and leaving the
vendor on **exactly one** trigger. Re-POSTing a non-idempotent trigger to discover whether the first
one happened is the single thing this verb must never do.

Two notes for anyone extending that suite. `ResourceExists` cannot be asserted on: `observeDispatch`
returns `{exists: true, upToDate: true}` on **both** paths — on a match because the run was found, on
a miss so `Create` is not re-issued — so the distinction lives in the status projection, the
observe-miss annotation and the condition. And fire-once lives in `Observe`; `createDispatch` does
not check `alreadyDispatched`, so calling `Create` twice directly fires twice and is a sequence no
caller performs.

The dispatch verb still has **not** been driven against a live vendor API.

### An accepted spec change no longer silently does nothing

Appending to `additionalStatusFields` — the one mutation its append-only CEL rule permits — was
accepted, reported `Synced=True` and "External resource is up to date", and **never regenerated the
CRD**. The field never appeared, with no error, no warning and no event. The same was true of
`includedSpecFields`, confirmed with a captured before/after on a purpose-built probe (#224, fixed in
#225).

The mechanism to prevent it was already built and simply never reached: `status.resourceHash` exists
for exactly this case, `Create` stores it and `Update` compares it and regenerates — but `Observe`
never consulted it, and since the runtime calls `Update` only when `Observe` reports drift, that
comparison was unreachable for a spec-only change. `Observe` checked the OAS content hash, the
auth-secret digest, prunable versions and the *deployment* digest (RBAC, ConfigMap, Deployment —
never the CRD).

**Expect a one-time CRD regeneration per RestDefinition when you upgrade to this release.** That
regeneration is the fix working: it is what finally applies spec changes that were previously
accepted and dropped. An operator watching every CRD regenerate on a version bump whose notes
mention none of their resources would reasonably read it as a fault, so: it is expected, it happens
once, and the CRD it produces is the one the RestDefinition has been describing all along.

Two consequences worth stating plainly:

- **An appended `additionalStatusFields` entry now reaches an EXISTING CRD.** `resourceHash` covers
  the whole of `spec.resource`, so the append changes it, `Observe` reports drift and `Update`
  regenerates. No manual step, and no need to delete and recreate the RestDefinition — which was the
  only previous route and destroys the CRD and every CR of that kind.
- `status.resourceHash` shipped in 0.27.0. A RestDefinition created before that has the field empty,
  so its first reconcile after this release regenerates once and stores the hash: bounded and
  self-healing, and correct, since such a CRD may genuinely be stale.

### Tests that can fail

`DeleteRuleMatrix` asserted only that the finalizer was released — the controller's own report of
itself. Its `success/200-tombstone/verifiable` case therefore stayed green for ten releases while the
feature it is named after was dead code, with the gojq error printing inside the passing test. Two
columns now ask better questions (#226): `wantFetchable` asks the **vendor** whether a plain GET
still answers 200, which is the only way to catch a release that orphaned a live resource; and
`wantVerified` asks **how** the decision was reached, so a dead predicate reaching the right answer
by fallback cannot hide. Reverting the #219 fix now fails the tombstone case on `wantVerified` and
nothing else.

### Documentation

- **The two runnable examples' CRs had never resolved** (#211). A resource CRD's version comes from
  the OAS document's `info.version`, normalized — `1.1.4` → `v1-1-4`, `1.0` → `v1-0` — and only the
  `*Configuration` CRD is always `v1alpha1`; both examples wrote their CRs against `v1alpha1`. The
  sample also required `spec.id`, a response-only field that no author can supply before the resource
  exists. Rendering both at 0.28.0 produced the same output, so neither manifest had *ever* applied.
  A RestDefinition validates against a CRD that ships in the repo; a CR validates against one that
  only exists after generation runs, and the cheap check is the one that passes. Also ships the first
  `dispatch` example, including the vendor-side half that is easy to miss: GitHub does not echo
  `workflow_dispatch` inputs, so the workflow must put the token into its own `run-name`.
- **The verbose dump leaked credentials below 0.26.1, which is where we recommend it** (#214). Every
  page suggesting `krateo.io/connector-verbose` now states the floor, and that the annotation is
  per-object HTTP logging rather than a log level. The fix is in **0.26.1**, not 0.26.0.
- **`responseTransform` is what request/response shape divergence is for** (#215), with a worked
  example checked against a real divergent shape, and why `compareScope` is not the fix. Also: the
  generated per-controller ConfigMap is provider-owned, so a hand-added key is dropped silently on
  the next generation — set `rdc.env` in the chart values instead.

## 2026-10-19 — 0.32.0

Three features — an allowlist, Swagger 2.0 documents, and the first non-CRUD verb — plus a new
refusal and a new diagnostic. **One thing can break an existing install**, and it is the first entry
for that reason.

### Existing CRs can stop validating, if the vendor document declares path-item parameters

OpenAPI lets `parameters` sit on the path item rather than on the operation, and until this release
they were invisible: `PathItem` exposed only `GetOperations()`, so nothing ever read them. A path
parameter declared that way never became a spec field, which meant a generated kind could not bind its
own URL. Keycloak declares them on **273 of 274 paths**, so this is the common shape, not a corner.

They are now read and merged under the operation's own parameters, overridden by name **and** location.
Newly visible optional parameters are additive and benign. A path-item parameter with `required: true`
now marks its spec field **required**, and a CR that omits it stops validating.

What limits the damage: the chart's floor is 1.36, so CRD validation ratcheting is active and
`LockToDefault`, which means an update that does not touch the newly-invalid field is still accepted.
So stored CRs are not retroactively broken — it is creates, and updates that touch the field, that are
rejected. Check any resource whose document declares path-item parameters before upgrading.

The #108 cross-document conflict rule is **not** a broad risk alongside this: it is scoped to
cross-document disagreement, so a single-document RestDefinition cannot begin failing. Only a
multi-document one can, by gaining a name on both sides of the merge.

### Generation now refuses a CRD whose names would shadow a built-in resource

A generated plural comes from the vendor document's own resource name, and vendor APIs are full of
names Kubernetes already serves. GitHub's document contains "deployment", so generation minted
`deployments.github.krateo.io`; two groups then claimed the plural `deployments`, and `kubectl get
deployment` started refusing the bare two-argument form **for the built-in**.

The guard enumerates every non-CRD group from discovery — not just core, because `deployments` lives in
`apps/v1`, which is the trap — and checks plurals and singulars, subtracting CRD-backed resources so a
generated resource does not collide with itself on re-reconcile.

Two deliberate asymmetries. It **fails open** when discovery is unreachable, and partial results are
still used, because they can only miss a collision, never invent one. And it **refuses only while the
CRD does not exist yet, warning once it does**: refusing later cannot unbreak `kubectl`, and the only
remedy at that point destroys CRs. So this cannot break an install that already works.

### `includedSpecFields`: an allowlist, because a blocklist cannot narrow a 152-field schema

`excludedSpecFields` requires naming everything you do not want. Keycloak's `RealmRepresentation` has
152 fields, so the useful direction is the other one.

Mutually exclusive with `excludedSpecFields` (CEL), append-only rather than immutable, dot-notation,
bounded at 256 entries. Two decisions resolved toward refusing rather than guessing: a listed field
that does not exist is **fatal**, and machinery is **always kept** — `configurationRef`, spec fields
derived from required parameters, and identifier selectors on a create-less resource. Dropping a
required parameter's field would reintroduce the breakage in the first entry above, by configuration.

Validated on a cluster: the CRD Establishes at `maxItems: 256`, both lists together are rejected,
appends are accepted, removal and reordering are rejected, and 152 real entries are accepted.

### Swagger / OpenAPI 2.0 documents are accepted, by a second adapter

Selected by sniffing the document's spec type before a model is built. **Not** a 2.0 → 3.0 conversion:
converting would make `status.oasHash` hash a document the vendor never published, breaking the
guarantee that a generated CRD traces back to one vendor document — which this repository
checksum-enforces.

Cheap because a 2.0 parameter's and response's schema are the *same* object the 3.0 path already
consumes. Inline-typed non-body parameters synthesise a schema; responses synthesise a MIME map from
`produces`, which narrows rather than widens; the `in: body` parameter surfaces as the request body and
is excluded from the parameter list; and `type: basic` becomes `{http, basic}`, so a 2.0 document's
basic auth no longer generates no auth field at all.

### A `dispatch` verb, for endpoints that fit none of the five

ArgoCD sync, GitHub `workflow_dispatch`, Jenkins build. Two POSTs are two runs, so the call is not
idempotent, and the canonical case answers 204 with an empty body, so nothing comes back to key on —
which is also why `async` cannot reach it: `operationRef` extracts a handle *from the trigger
response*, and there is no handle.

What makes one safe is a **CR-supplied correlation token that the API echoes back** in an observable
collection. The token is the resource's identity, so observation is an ordinary lookup: found ⇒ Ready
and nothing fires; absent ⇒ the resource does not exist ⇒ the dispatch fires. Changing the token is a
new invocation, not drift.

Correlation is **mandatory** — an uncorrelated dispatch would report Ready while POSTing every resync —
and is checked at admission, against the OAS document, and against the generated spec. A dispatch
cannot be combined with `create`, `update`, `compareScope`, `async` or any `*ApiRef`; at most one is
allowed per kind; and `delete` is a no-op that releases the finalizer, because the invocation a token
names is immutable history rather than an object.

Fire-once is enforced, not merely intended: the controller records the token it successfully POSTed and
refuses to report not-exists for that token again, so a mis-declared echo path produces one dispatch and
then a diagnosis, instead of a duplicate run per reconcile. The annotation is written only after the
POST succeeds, so a dispatch that never actually happened is still retried.

**Not yet done: no dispatch verb has been driven against a live vendor API.**

### A delegated verb with no snowplow URL is now reported at generation

A RestDefinition that delegates a verb to a Snowplow RESTAction is inoperative, not degraded, when the
generated controller has no `URL_SNOWPLOW`: every reconcile of every instance fails. That was already a
clear runtime error — but it needed an instance to reconcile, so it was found by whoever first wrote a
CR rather than by whoever set the chart values. `rdc.env` defaults to `{}`, so the unconfigured case is
the default one.

Generation now says it once, as an event, naming the field and the RESTAction. A warning and never
fatal: the snowplow URL is platform configuration the RestDefinition author often does not own, and it
can arrive later by a `helm upgrade` that restarts the generated controller without the RestDefinition
changing at all.

### Documentation

Two notes corrected that said the opposite of the code. `docs/release.md` now records why the
`oasgen-provider-crds` release must never be renamed or replaced — and that `helm.sh/resource-policy:
keep` is **not** the remedy, because helm reads it from the release's stored manifest rather than the
live object; `krateo.io/deletion-policy=orphan` on the Composition is, and it lives in the installer.

Separately, the version-policy code claimed the chart floor was 1.33 and that rest-dynamic-controller
stamps the `krateo.io/oas-version` label itself when it meets an unlabelled instance. Both stopped being
true when version-scoped watching landed: the floor is 1.36 and that fallback was deleted, because the
watch selector is exact label equality so an unlabelled instance never reaches Observe. `Chart.yaml`
already said the opposite, so the repository contradicted itself — in the comment headed "if you are
here to judge whether this floor can be lowered".

## 2026-10-06 — 0.31.0

Makes per-verb `oasPath` real: a resource whose verbs legitimately span two OAS documents can now be
described natively. 0.30.0 refused the field; this release implements it and lifts the refusal.

**Two things can change under you** — a new way a RestDefinition can fail, and a change to generated
Configuration CRDs — both listed first for that reason.

### A RestDefinition can now be REFUSED for two documents that disagree

New failure mode, and it only exists once a verb overrides `spec.oasPath`.

Parameters are merged from every verb into one spec schema, deduplicated by name, first verb wins.
Within one document that is harmless: a path parameter repeated across verbs is the same parameter.
Across two documents it is a silent choice between definitions that disagree, decided by the order of
`verbsDescription`, after which the losing verb sends a value shaped for the other document's contract.

Generation now refuses that, naming the parameter and both verbs. The check is deliberately narrow: it
compares only where two documents demonstrably feed the same CRD field, and only across documents. Two
verbs sharing one document that declare a parameter differently is long-standing first-wins behaviour
and is untouched, so **this cannot fire for any RestDefinition that worked before this release**.

The refusal happens at generation, not admission. Admission cannot see the documents.

### Generated Configuration CRDs may change for split-document resources

A `configurationFields` entry scoped to several actions used to resolve its parameter once and apply
that one definition to every action. With one document that was correct by construction. With two it
meant whichever document answered first described every action.

Each action now takes the parameter as **its own** document declares it, so a `query.<action>.<param>`
entry's `default` and whether it is `required` can both change. Aruba's `CloudServer` is the live case:
`api-version` is declared by both compute documents, required with default `1.1` in the 1.1 document
that serves create and optional with default `1` in the 1.0 document that serves everything else. The
generated Configuration offered create the 1.0 description, so a Configuration omitting the field was
accepted and sent `api-version=1` to an endpoint that requires `1.1`.

If you have a split-document resource, re-read its generated Configuration CRD before upgrading a
Configuration CR against it. **Single-document resources are unaffected** — their generated output is
byte-identical.

An action whose own document does not declare the parameter keeps its entry, as before. Resolving
strictly per action would have deleted fields some Configuration may already set.

### A verb may name its own OAS document

`verbsDescription[].oasPath` overrides `spec.oasPath` for that verb only, using the same URI schemes.

This exists because a resource whose verbs span two documents was not merely degraded but impossible to
express. Aruba's `CloudServer` is the motivating case: `findby`/`get`/`delete` are published in
`compute-provider.json` (1.0.0) while `create` lives alone in `compute-provider_v1.1.json` (1.1.0),
where that document holds exactly one path. The workaround was to delegate the missing verb to a
RESTAction, which needs `URL_SNOWPLOW` — and no chart sets it, so that create had never executed.

Documents are read as published and never merged. Pre-merging would break the guarantee that a
generated CRD traces back to one vendor document, which this repository checksum-enforces.

Which document supplies what is unchanged, only now explicit: the create verb's document drives the
spec schema, the observe document drives status and identifiers, and the CRD version and security
schemes come from `spec.oasPath`'s document. So a 1.1 override does **not** move the generated CRD's
API version.

The `maxItems: 32` bound on `verbsDescription` is also gone. It was never a limit on verbs — it existed
only to keep 0.30.0's refusal rule inside the apiserver's CEL cost budget.

### Drift detection now covers every document a resource reads

`status.OASHash` was a hash of `spec.oasPath`'s bytes alone. With per-verb overrides that would have
been a silent hole: editing the second document would leave the stored hash unchanged, Observe would
see no drift, and the CRD would never regenerate — the resource staying `Ready` while serving a schema
that no longer matches its own document.

The hash is now a composite across every referenced document, keyed by path, so a document **moving**
between paths registers as a change even when its bytes did not.

### Validation status

Generation was verified on a cluster, not only in tests: the real published Aruba documents through the
controller's own path on Kubernetes 1.36.1, confirming the generated CRD's spec is built from the 1.1
document and that the existing definitions on that cluster were unaffected. The `api-version` defect
above was found that way, after the static suite had passed and the golden CRD was byte-identical.

Not yet done: no verb of a split-document resource has been driven against a live vendor API.

## 2026-10-05 — 0.30.0

Completes version coexistence, and closes a gap where a field was accepted and ignored. **Two things
will look like regressions if you are not expecting them** — both are listed first for that reason.

### Served versions are now retired, so a CRD's version list SHRINKS

A generated CRD used to accumulate every version hop's served endpoint forever. Versions nothing uses
are now removed, and `spec.versions` getting shorter is this working rather than something eating them.

A version is kept if it is the one currently served, if it is the vacuum storage version, if another
RestDefinition still serves it, or if any live instance still carries it in `krateo.io/oas-version`.
The decision is logged at INFO — naming what was pruned, what was kept and why — and a
`PrunedServedVersions` event is recorded on the RestDefinition, so a retirement can be reconstructed
afterwards rather than inferred.

Why it matters beyond tidiness: a retired-but-still-served endpoint is one a client can pick, and
writing through it silently drops any field added after that version. And since 0.28.0 each served
version is a rest-dynamic-controller Deployment, so dead versions are controllers reconciling nothing
for the lifetime of the install.

Nothing is pruned while an instance claims it. That is the one failure that would destroy data, and it
is the case the tests are built around.

### A per-verb `oasPath` is now REFUSED where it used to be accepted

`verbsDescription[].oasPath` has shipped in the CRD for several releases, pattern-validated and
documented, and **nothing has ever read it**. A RestDefinition setting it was admitted and the verb was
then resolved from `spec.oasPath` anyway — a CRD generated from the wrong document, with no error.

It is now rejected at admission, with a message saying why and pointing at
krateo-platformops/oasgen-provider#108. If you were setting it, nothing you had was working; the change
is that the failure is now visible instead of silent.

The feature itself — a resource whose verbs legitimately span two OAS documents, which is Aruba's
`CloudServer` — remains open in #108. This release only stops pretending it exists.

## 2026-10-05 — 0.29.0

Two changes an operator can see, both about making an existing silence audible. Nothing here changes
how a resource reconciles.

### Schema warnings are reachable

Schema generation and validation warnings were logged at DEBUG+3 and appeared nowhere else, so on a
default install nobody saw them. They are now logged at WARN and summarised in a `SchemaWarnings` event
on the RestDefinition, visible in `kubectl describe restdefinition`.

The failure they exist to catch is silent by construction: a RestDefinition whose identifier can never
match reports `Ready`, findby never matches, nothing errors, and the resource simply never converges —
indistinguishable from a healthy one to `kubectl get`. That is exactly the case where nobody has a
reason to go tailing debug logs, because everything visible says the system is fine. The warning had
been accurate since 0.22.x and unreachable the whole time.

One aggregated event per generation rather than one per warning, naming the first three and counting
the rest; the full list stays in the log. The message says the CRD *was* still generated — this is not
a failure — and that a field named in it may never resolve at runtime, which is the part worth acting
on.

Deliberately an event rather than a new status field. A status field brings staleness, and a warning
describing a problem that has since been fixed is worse than no warning: it teaches people to ignore
the field. If events prove insufficient, oasgen-provider#151 stays open for the status surface.

### Configuration instances are version-labelled too

The admission policy stamps `krateo.io/oas-version` on every kind in the group, including each
generated `…Configuration`. The backfill introduced in 0.28.0 stamped only the resource kind, so
instances predating the policy split permanently into a labelled half and a half that never would be.

Nothing was broken by that — a Configuration is resolved by reference, never by a version-scoped watch,
so an unlabelled one is not orphaned. It mattered for two reasons. `kubectl get <kind> -A -l
'!krateo.io/oas-version'` is how an operator checks a migration worked, and it returned a permanently
non-empty set. And future version pruning keeps versions that have labelled instances, while the
unlabelled set shrinks over time as objects are touched — so the same question would get a different
answer depending on when it was asked.

Configurations are stamped with the **Configuration CRD's own version**, which differs from the
resource's. That is what the policy writes for them, and writing anything else would have the backfill
disagree with admission on the same object.

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

  > **Correction (2026-10-26, #218).** That last sentence was **not true when written**. The delete
  > path passed the `restclient.Response` wrapper to the predicate instead of the response body, so
  > it errored on every call and the check degraded silently to "unverifiable". Through 0.32.0,
  > deletion of a tombstone resource was verified only by the trusted-delete fallback. Fixed in
  > 0.33.0 — see that entry.

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
