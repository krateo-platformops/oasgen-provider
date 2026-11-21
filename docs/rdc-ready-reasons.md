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
