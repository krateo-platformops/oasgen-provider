---
title: Detecting a generated CRD that is stale relative to its OAS document
description: A RestDefinition can store a content hash for a render that never happened, after which it compares new against new forever and never regenerates. This is how to tell whether a cluster is in that state -- and why the act of fixing it destroys the ability to measure it.
---

# Detecting a generated CRD that is stale relative to its OAS document

## Searchable symptoms

- a generated CRD whose `openAPIV3Schema` is missing a property the OAS document declares
- a field set on apply that is **absent from the object afterwards** (the apiserver prunes undeclared fields silently)
- `status.oasHash` that looks correct and a served schema that does not match the document
- a RestDefinition reporting `Ready=True` while its kind serves an outdated schema
- `Applying CRD` not appearing in the provider log for a definition whose document changed

## What the state is

`status.oasHash` can be stamped for a render that did not happen. Afterwards Observe compares the new document against a stored hash **of that same new document**, finds no drift, and never regenerates. Fixed in 0.63.1; reachable in every release from 0.45.0 through 0.63.0.

**A latch leaves the status internally consistent.** That is the whole difficulty: no comparison *among* definitions can find it — uniform `generatorBuild` with distinct per-definition hashes is exactly what a healthy fleet and a latched one both look like. Only a comparison against the **document** decides.

## Read this before measuring: the window closes when anything reconciles

**`generatorChanged` is the third drift input, combined with OR**, so a provider upgrade crossing a MAJOR.MINOR.PATCH boundary re-renders every definition once — clearing a latch **and the evidence of it together**. An empty `status.generatorBuild` (any install predating 0.53.0) forces the same re-render on the *first* reconcile under any current version.

So:

> **Exposure is only measurable on a cluster that has not reconciled since the latch could have formed.** The act of fixing destroys the ability to measure. A sweep that reports "no latches found" after the fleet has been upgraded is consistent with *there were none* and with *we erased them before looking*, and it must say which.

The clearest measurable case is a cluster whose provider is scaled to zero or otherwise quiet. If yours has already reconciled, record the result as **not measured** rather than clean.

## How to tell: compare the stored hash against the document

`status.oasHash` is a pure function of exactly two things — **the sorted set of (oasPath string, document bytes)**. No provider version, no plumbing version, no `spec.resource`. Measured independently: two clusters eleven releases apart compute the same digest for the same document.

```
sha256( for each path in sorted(paths): "<path>=<sha256(document bytes)>\n" )
```

So the same document under the same oasPath must produce the same `oasHash` on any cluster at any version. A mismatch means one side advanced its hash without rendering.

### Three traps that make this report the wrong answer

**1. Hash the ConfigMap's bytes, never the file you vendored.** The digest is over what the ConfigMap actually holds, and nothing guarantees that is byte-identical to your source file. A generator that writes compact JSON produces a ConfigMap a third the size of the pretty-printed original with a completely different sha256 — same document, different bytes. Measured on a real consumer: 178,675 bytes against 504,308, identical sha256 after minifying. Comparing against the file on disk reports a mismatch that means nothing.

**2. The oasPath STRING is hashed, not just the content.** `fmt.Fprintf(h, "%s=%s\n", path, digest)` — the path is part of the message, deliberately, because which document a verb reads is part of what generated the CRD. Two clusters whose definitions reference the same document under different namespaces, ConfigMap names or schemes will differ **legitimately**. Diff the `oasPath` values as strings before trusting a single mismatch, and remember the digest is composite: a definition referencing two documents hashes both, so the path *set* must match.

**3. An unreachable cluster must not share an exit code with "latch found".** `sys.exit("message")` exits 1. If 1 is also your code for LATCH, a cluster that is merely stopped reports a latch. Found by a consumer running their detector against a cluster that had just gone down.

### And a property worth building in

**Make agreement the success mode.** A detector that confirms N definitions agree cannot quietly be checking nothing: if the algorithm were reimplemented wrongly, *none* would agree. Report `AGREES / MISMATCH / NOT CHECKABLE` as three separate counts, and treat **zero agreements** as "suspect the script", not as "no latches".

## Recovery

An upgrade crossing a release boundary clears a latch on its own. Where that is not available — a cluster held at one version, or one running with `OASGEN_PROVIDER_REGENERATE_ON_UPGRADE=false` — clear it directly. This is non-destructive:

```bash
kubectl patch restdefinition <name> -n <ns> --subresource=status --type=merge \
  -p '{"status":{"oasHash":""}}'
```

Do it while the generated controller's Deployment is **Ready**: patching while it is absent routes the next pass through `Create` again.

**Measured once, on a deliberately latched definition on 0.62.0:** the probe appeared in the served CRD **17 seconds** after the patch. `crd_schema_sha` changed, `oasHash` was re-stamped to the new document's hash, and `resourceHash`, `generatorBuild` and the Deployment uid were all unchanged — so the patch caused a re-render and nothing else.

**Why it is seconds rather than up to a poll interval.** The RestDefinition is the controller's `For()` object and **no predicate filters status-only updates** — the builder chain is `Named → WithOptions → For → Build`, with no `WithEventFilter` and no `builder.WithPredicates`, and `ForControllerRuntime()` returns only `MaxConcurrentReconciles`, a rate limiter and optionally a queue constructor. So patching `status` generates a watch event and enqueues a reconcile immediately, rather than waiting for the 3-minute poll.

## What has actually been observed

| cluster | definitions checked | result |
|---|---|---|
| 45 days old, 0.52.0-era, provider at 0/0 replicas, no reconcile since the window | 34 | 34 agree, 0 mismatch |
| two long-lived clusters, one kind on one path each | 1 each | no latch |
| a throwaway cluster, window opened by a deliberate provider downgrade | 1 | latched |
| a disposable cluster on 0.62.0, window opened deliberately (Deployment **deleted**, then the document edited) | 2 | **both latched** |
| the same cluster, same edit, Deployment **present** (control) | 1 | regenerated correctly |

### The mechanism is reproduced, not inferred

The two-definition row above is a deliberate reproduction and it is worth stating in full, because it
is the difference between a code reading and a measurement:

```
baseline      Queue  probe=0  crd_schema=3d6af848  oasHash=f7352efb  deploy 1/1
after:  delete the Deployment, THEN edit the document, THEN let 2 polls run
result        Queue  probe=0  crd_schema=3d6af848  oasHash=57743d37  deploy=NEW uid
                             ^^^ UNCHANGED          ^^^ MOVED
control  Environment  probe=1  crd_schema=4bbbfd41  oasHash=b85b49f2  deploy unchanged
                              ^^^ the edit IS observable when the Deployment is present
```

The stored hash advanced to the new document while the served schema stayed byte-identical to the
old one — the latch, directly observed. `Create` recreated the Deployment and stamped the hash
without rendering. **The control is what makes this readable**: the same edit with the Deployment
present changes the served schema, so an unchanged schema in the subject arm cannot be explained by
the edit being invisible.

Note the Deployment must be **deleted**, not scaled to zero: `deployOk` means *exists*, and at
`replicas=0` the readiness test computes `0 == 0` and reports the Deployment as existing **and
ready**. Scaling to zero closes the window while looking like the healthiest possible state.

**No latch has been found on any long-lived cluster.** That is the strongest available statement and it is not "this never happens": the measured population is small, and most of it was checked on one kind.
