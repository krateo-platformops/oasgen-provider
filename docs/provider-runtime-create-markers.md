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
