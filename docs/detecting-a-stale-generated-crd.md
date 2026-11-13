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

> **This recovery has been reasoned from the code and not yet run.** It is stated here so that is known, rather than implied to be verified.

## What has actually been observed

| cluster | definitions checked | result |
|---|---|---|
| 45 days old, 0.52.0-era, provider at 0/0 replicas, no reconcile since the window | 34 | 34 agree, 0 mismatch |
| two long-lived clusters, one kind on one path each | 1 each | no latch |
| a throwaway cluster, window opened by a deliberate provider downgrade | 1 | latched |

**No latch has been found on any long-lived cluster.** That is the strongest available statement and it is not "this never happens": the measured population is small, and most of it was checked on one kind.
