---
type: API
title: rest-dynamic-controller condition reasons
description: Every condition rest-dynamic-controller sets on a generated kind's CR, with its wire value, its parent-composition verdict and whether it clears on its own. Shares core-provider's seven-column shape so one vendoring parser reads both, and records why a condition message never carries the vendor's response body.
resource: oci://ghcr.io/krateo-platformops/charts/oasgen-provider
tags: [kog, conditions, ready, synced, reference]
timestamp: 2026-11-21T00:00:00Z
---

# rest-dynamic-controller condition reasons

Every condition `rest-dynamic-controller` sets on a generated kind's CR, with what each one means for
a parent composition and whether it is expected to clear on its own.

The table's shape matches core-provider's `docs/ready-reasons.md` so the same vendoring parser reads
both. **The `reason` column is the WIRE VALUE** — the string that appears in
`.status.conditions[].reason` — not the Go identifier, and two of them differ:
`ReasonResolverDigestRecorded` is `DigestRecorded` and `ReasonResolverDigestFieldAbsent` is
`DigestFieldAbsent`.

`Ready` and `Synced` are `unstructured-runtime`'s types. `Observed` and `ResolverDigestRecorded` are
this controller's own, so a consumer that reads only `Ready` and `Synced` sees nothing from them.

**A condition message never carries the vendor's response body.** `StatusError.Error()` carries only
this controller's words plus the HTTP status code; the vendor's text lives in a `BodySnippet` field
that no caller passes into a condition. That is deliberate: `.status` is readable by every subject
with `get` on the CR, and a 4xx body routinely echoes the submitted request. **The body is logged at
Info** by the layer that holds a logger — deliberately not Debug, because a default install passes no
`-debug` — so an operator can read it while a namespace viewer cannot. A tooltip built from a
condition message can show the status code and this controller's wording, and must not promise the
vendor's explanation.

| condition | reason | status | writer | parent verdict | expected to clear | written by |
|---|---|---|---|---|---|---|
| Ready | `Pending` | False | rest-dynamic-controller | converging | `next pass`: a referent the spec names has not resolved yet | `Pending`, `PendingReferent` |
| Ready | `Updating` | False | rest-dynamic-controller | converging | `next pass`: an update was issued; the next observe decides whether it took | `Updating` |
| Ready | `CreateRefusedPersistently` | False | rest-dynamic-controller | converging | `indefinite`: re-sent on every resync until the vendor accepts the create; `Synced=False/ReconcileError` carries the vendor's error | `CreateRefusedPersistently` |
| Ready | `DriftNotConverging` | False | rest-dynamic-controller | converging | `indefinite`: the same difference has been reported and the same update issued repeatedly; it will not clear without a spec or vendor change | `DriftNotConverging` |
| Ready | `CreateUnreachable` | False | rest-dynamic-controller | failed | `never`: create is the resource's only write verb and its findby cannot resolve, so no pass can reach the create | `CreateUnreachable` |
| Ready | `AuthorizationDenied` | False | rest-dynamic-controller | failed | `never`, without a credential change: the API answered 403 and the response carried no retryable signal | `AuthorizationDenied` |
| Ready | `CreateFailed` | False | rest-dynamic-controller | failed | `never`: the create was attempted and cannot succeed as declared, so it is not retried | `CreateFailed` via `permanentVerbFailure` |
| Ready | `UpdateFailed` | False | rest-dynamic-controller | failed | `never`: the update was attempted and cannot succeed as declared, so it is not retried | `UpdateFailed` via `permanentVerbFailure` |
| Ready | `NoCreateVerbDeclared` | False | rest-dynamic-controller | failed | `never`: the OAS declares no create verb for this kind, so an absent resource cannot be created | `NoCreateVerbDeclaredConditions` |
| Ready | `NoIdentifierRecorded` | False | rest-dynamic-controller | failed | `never`, without a spec change: the create reported success but the response carried none of the identifiers the kind needs to find it again | `NoIdentifierRecorded` |
| Ready | `AssumedUpToDate` | True | rest-dynamic-controller | healthy | `steady` | `AssumedUpToDate` |
| Synced | `NoCreateVerbDeclared` | False | rest-dynamic-controller | — | `never`: set alongside the `Ready` row above, so a consumer reading only `Synced` still sees the fault | `NoCreateVerbDeclaredConditions` |
| Observed | `ResponseObtained` | True | rest-dynamic-controller | — | `steady`: the last observe got a response body | `Observed` |
| Observed | `NoResponseBody` | False | rest-dynamic-controller | — | `next pass`: the last observe got no body to compare against | `NotObserved` |
| ResolverDigestRecorded | `DigestRecorded` | True | rest-dynamic-controller | — | `steady`: a resolver digest was recorded for this pass | `ResolverDigestRecorded` |
| ResolverDigestRecorded | `DigestFieldAbsent` | False | rest-dynamic-controller | — | `next pass`: the field a digest would be taken from is absent | `ResolverDigestFieldAbsent` |

## How `Ready=False/CreateRefusedPersistently` reaches the cluster, and why it has never arrived yet

**IT DOES NOT WORK IN ANY RELEASED VERSION, INCLUDING 0.86.0.** An earlier revision of this section
said "it works from 0.86.0". That was wrong, and the way it was wrong is the point of keeping it:
**the fix has two halves, and 0.86.0 shipped only the half that was not ours.**

| Half | What it does | Where it is |
|---|---|---|
| The runtime applies a handler's conditions on the **failed**-create path | `unstructured-runtime` #124 | **Released**, v1.32.1, pinned by 0.86.0 |
| This handler **returns** the condition instead of writing it | RDC's create paths | **Merged, unreleased** — see the PR that carries this line |

**So on 0.86.0 the runtime dutifully applies `created.Conditions`, which this handler left empty, and
the report still does not arrive.** A consumer pre-registered the row INVERTED against 0.86.0 for
exactly that reason. **Do not read "v1.32.1 is pinned" as "the report works"** — that is the whole
mistake this table exists to prevent.

**Measured on a live cluster** by the `azuredevops-provider-kog` session, run 0852 at **0.85.2**: a CR
whose create the vendor refused with a 400 sat **198 minutes** against a **1-hour** deadline, and the
reason never changed. It read `Ready=False/Creating` with the runtime's own message throughout.
**Exactly one Ready condition, and its `lastTransitionTime` held** — so the anchor the report depends
on worked; the report did not arrive.

**THE RULE, which is a boundary rather than a bug:** *conditions reach the cluster only through a
returned result.* A handler that calls `SetConditions` on the object it was handed is writing to a
copy the runtime then discards — it re-fetches so that it owns the single status write, and a write
from inside the call would leave the runtime's copy at a stale resourceVersion. The channel is the
**returned** `ExternalObservation.Conditions` or `CreateResult.Conditions`, never the argument.

**What was missing was narrower than that:** `CreateResult.Conditions` were applied only **on a nil
error**, and both refusals its contract named were `Requested=false`. A *failed* create had no
channel at all — neither the argument (discarded) nor the result (not applied on that path).

**`unstructured-runtime` v1.32.1 extends it.** On a failed create the handler's
`CreateResult.Conditions` are upserted by Type onto the object that gets written, **after** the
runtime's own, so a handler `Ready` **replaces** `Ready=False/Creating` and any other type is
**added**. **A handler `Synced` is ignored on that path by design**, because it would hide
`Synced=False/DeletionPolicyInvalid` — which *can* coexist with a create. (**Not**
`ManagementPolicyInvalid`: an invalid management policy is observe-only, so `worker.go`
short-circuits the Create event at `!ManagementDecision(el).May(managementpolicy.Create)` and
`handleCreate` is never reached at all.)

**AND THAT ALONE WAS NOT ENOUGH.** The runtime can only apply what a handler RETURNS. This handler
wrote the condition onto its argument until the PR named in the table above, so the extended contract
had nothing to act on. **Bumping a dependency is not the same as using it.**

**So from 0.86.0 the row above is a plain row.** On 0.85.2 and earlier it never fired, and a consumer
whose pill never lit was reading a correct integration against a release that could not deliver.

## Where there is deliberately NO condition

**Deletion of a CR whose existence is permanently unanswerable reports through an `unstructured-runtime`
Warning EVENT, and sets no condition.**

This is stated rather than left as a gap, because a reader who finds no row for it will assume one is
missing. **The reason it cannot be a condition is structural:** that path releases the object's
finalizer, so a condition written there disappears with the object it was written on. An event
outlives the CR; a condition does not.

**The event's reason literal is `ExternalResourceKeptExistenceUnanswerable`**, with action
`DeleteExternalResource`, written by `unstructured-runtime`.

**NOT YET SHIPPED, and that distinction is the point:** it comes from `unstructured-runtime` PR 121,
which is **open and unmerged**, and it ships in **v1.32.0**, which is **not tagged** — this module
pins v1.30.0. So the literal is recorded here as what the PR declares, not as something observable
in any released version. **Do not map it in a consumer until that tag exists**; if the PR renames it
before merging, this line is wrong and the PR is right.

**The NOT-being-deleted path is different and DOES carry a condition:** the existing
`Ready=False/CreateOutcomeUnknown`, with no new reason, whose message names why the existence check
can never be answered and the two ways out (write the id into status if the external resource exists;
clear `krateo.io/external-create-pending` if it does not). **The marker is deliberately kept — there
is no duplicate risk in staying stopped**, and skipping it would trade a definite strand for a silent
duplicate on exactly the kinds that cannot detect one.

See `krateo-platformops/unstructured-runtime` issue 119 for the measurements and the decision.

## Reasons written by the runtime, not by this controller

`Ready=False/Creating`, `Ready=True/Available`, `Synced=True/ReconcileSuccess` and
`Synced=False/ReconcileError` come from `unstructured-runtime` and `provider-runtime`, so they are
documented in those repositories. **Two are worth stating here because a consumer will see them
beside the rows above:**

- **`Synced=False/ReconcileError` is what marks a CR as failing.** A `Ready` verdict of `converging`
  on a long-running retry does not become `failed` with time — `CreateRefusedPersistently` is still
  retrying — so a consumer deciding whether to alert should read `Synced`, not the age of `Ready`.
- **`Ready=False/Creating` does not mean a create is in flight.** `handleCreate` returns without
  enqueueing an observe, so a healthy resource can read `Creating` until the next resync.

## How to regenerate the wire values

```
git show origin/main:go/rest-dynamic-controller/internal/controllers/condition/condition.go
```

The reason constants are a single `const` block near the top; `ReasonNoCreateVerbDeclared`,
`TypeObserved` and `TypeResolverDigestRecorded` are standalone `const` declarations elsewhere in the
package. **Read the string literal, not the identifier** — the two Digest reasons are why.
