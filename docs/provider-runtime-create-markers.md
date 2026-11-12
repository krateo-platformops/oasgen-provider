---
type: Runbook
title: The external-create markers, their precision, and what oasgen-provider pins
description: provider-runtime decides whether a create is in flight by ORDERING three timestamp annotations. oasgen-provider pins v1.8.2, which writes them at second precision, so two markers written in the same second are indistinguishable. v1.18.0 writes RFC3339Nano. Measured: parsing is compatible both ways, the writer's truncation is not.
resource: oci://ghcr.io/krateo-platformops/charts/oasgen-provider
tags: [kog, upgrade, provider-runtime, dependencies]
timestamp: 2026-11-12T00:00:00Z
---

# The external-create markers, their precision, and what oasgen-provider pins

provider-runtime tracks an in-flight create with three annotations on the managed resource:

```
krateo.io/external-create-pending
krateo.io/external-create-succeeded
krateo.io/external-create-failed
```

**The decision is an ordering, not a presence check.** `meta.ExternalCreateIncomplete` (v1.8.2
`pkg/meta/meta.go:250-262`) reports a create incomplete when *"the 'external create pending' annotation is
the newest of all tracking annotations that are set"*. So what matters is which timestamp is later, and
therefore how much precision the timestamps carry.

## What each version writes

| | formats with | precision |
|---|---|---|
| **v1.8.2** — what oasgen-provider pins today | `t.Format(time.RFC3339)` | **seconds** |
| v1.18.0 | `meta.MarkerTimeLayout` = `time.RFC3339Nano` | nanoseconds |

At v1.8.2 all three markers are written through `time.RFC3339` (`meta.go:213`, `:230`, `:247`), which
truncates sub-second digits. **Two markers written in the same second produce the same string**, so an
ordering between them cannot be resolved. v1.18.0 makes that ordering resolvable.

## Measured: parsing is compatible in both directions, the writer is not

Measured with Go's `time` package, each arm with a control:

```
time.RFC3339     parsing a NANO value:  err=<nil>  -> 2026-11-12T18:30:00.123456789Z
time.RFC3339     parsing a SEC  value:  err=<nil>  -> 2026-11-12T18:30:00Z          (control: must succeed)
time.RFC3339     parsing GARBAGE:       err=true                                     (control: must fail)
time.RFC3339Nano parsing a SEC  value:  err=<nil>  -> 2026-11-12T18:30:00Z
```

So a **reader** on either version reads a marker written by either version, at full precision. There is no
parse-compatibility barrier to upgrading, and none to running mixed versions against the same objects.

The **writer** is a different matter. The same instant, written by each version:

```
v1.8.2  writer:  "2026-11-12T18:30:00Z"
v1.18.0 writer:  "2026-11-12T18:30:00.123456789Z"

strings equal?       false
parsed times equal?  false     <- the truncation is lossy, so even comparing PARSED times is unsafe
                                  across a writer upgrade
```

**"Compare parsed times, not strings" is necessary and not sufficient.** Parsing fixes the format
difference; it cannot recover digits the v1.8.2 writer discarded. Two markers for the same real instant,
one written before an upgrade and one after, are not `Equal`.

## What this does and does not explain

**It does not explain the wedge this repository recorded.** `restdefinition.go` carries a measured
incident — `external-create-pending` re-set **75 seconds after** `external-create-succeeded`, on two
separate clusters — and 75 seconds is not a same-second tie. Second-precision truncation cannot produce
it, and the ordering hazard described here is a *different*, latent problem. They are easy to conflate
because both are about these annotations; they are not the same bug.

**What it does mean** is that on v1.8.2 a create whose pending and succeeded markers land in the same
second has an unresolvable ordering, and that is a real reason to want v1.18.0 — independently of the
recorded incident.

### The wedge IS diagnosed, in this file's own source

Not an open question, and worth stating here so nobody re-investigates it. Two instances, both recorded in
`internal/controllers/restdefinition/restdefinition.go` with measurements, and both the same shape: **our
Observe reported `ResourceExists: false` for a resource that existed**, so the runtime re-entered the
create handshake on an already-created resource.

| instance | our error | gap measured |
|---|---|---|
| comment at `:577-600` | `ResourceExists: false` while the generated controller's Deployment existed but was still rolling out — *"EXISTS BUT NOT READY IS NOT 'DOES NOT EXIST'"* | pending re-set **75 s** after succeeded, on a 34-RestDefinition install where exactly one wedged |
| comment at `:2119-2131` | a status update conflicted 70 ms after the CRDs were applied, `observedVersion` fell back to `resourceVersion`, and the next Observe looked the CRD up at the **wrong version** | pending re-set **97 s** after succeeded, on a second cluster |

The runtime's side is ordinary and documented: after the creation grace period (`defaultGracePeriod = 30s`,
provider-runtime v1.8.2 `pkg/reconciler/reconciler.go:33`) a pass that still sees `ResourceExists: false`
takes the create branch and calls `SetExternalCreatePending(now)` (`:1120-1128`). That is why the gap is
always *a bit over* 30 seconds plus backoff.

**So the cause was ours both times, and the fix was ours both times** — `resourceExistsForController(deployOk,
deployReady)` for the first, recording the applied version for the second. A provider-runtime bump would not
have changed either. Readiness belongs in a condition; `ResourceExists` answers only whether the thing is
there.

Still open and in the same family: **oasgen-provider#446**, *"RestDefinition Ready=True does not mean the
apiserver is serving the new CRD schema"* — readiness signals that describe the writer rather than whether
the apiserver serves the schema.

## Practical guidance

1. **Never compare these annotations as strings.** Parse them.
2. **Do not compare a marker written before a provider-runtime upgrade with one written after**, even as
   parsed times. The older writer's value is truncated, so the comparison is between a rounded instant and
   an exact one.
3. **Nothing in this repository reads these markers in code.** Every reference in
   `internal/controllers/restdefinition/restdefinition.go` is a comment describing the recorded incident.
   The runtime reads them on our behalf, which is why the pinned version decides the behaviour rather than
   anything we write.
4. `provider-runtime#57` (cost model) and `provider-runtime#59` (credentialuse) are in progress and **not** ready to
   adopt, as of this note.
