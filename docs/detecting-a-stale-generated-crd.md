---
type: Runbook
title: Detecting a generated CRD that is stale relative to its OAS document
description: A RestDefinition can store a content hash for a render that never happened, after which it compares new against new forever and never regenerates. How to tell whether a cluster is in that state, the three traps that make a detector report the wrong answer, and why the act of fixing it destroys the ability to measure it.
resource: oci://ghcr.io/krateo-platformops/charts/oasgen-provider
tags: [kog, crd, generation, detection, runbook]
timestamp: 2026-11-13T00:00:00Z
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

### Measured: 0.63.1 refuses to latch

The identical delete-and-edit that latched two kinds on 0.62.0 produced `probe=1` on 0.63.1 — the
served CRD carries the edit. That is the user-visible outcome, measured.

**The mechanism behind it was NOT measured on this run, and the distinction matters enough to state.**
Only the end state was sampled, two polls later, so the run does not show that `oasHash` stayed
unstamped while the Deployment was absent.

There is an elimination argument that would close that gap, and it is worth recording **with its
premise named**, because the premise is what a future run should capture. Render is reachable from
exactly two sites:

- `Create`'s CRD-exists path renders **only** `if generatorChanged`;
- `Observe` renders on `oasHash != stored || resourceHash != stored || generatorChanged`.

`resourceHash` was measured unchanged. **If** `generatorBuild` for that definition was already at the
new release when the edit landed, then `generatorChanged` is false, two of the three terms are
excluded, and the render can only have come from `oasHash != stored` — which requires the stored hash
to have still been the old document's, i.e. not stamped while the Deployment was absent.

**That premise was not measured for the definition under test.** The upgrade arm recorded
`generatorBuild` for two *sibling* definitions on the same cluster, both at the new release; the only
row for the subject definition predates the upgrade. So "it was already at the new release" is an
inference from siblings, not an observation of the subject, and the argument is plausible rather than
established.

> **To close it, record the subject definition's own status row immediately before the edit.** One
> extra read, and the end-state sample then proves the mechanism rather than only the outcome.

This is the same discipline the rest of this document asks for, applied to the document itself: an
argument whose premise is unmeasured is a hypothesis, however well it fits.

### Measured: an upgrade clears a latch, and only `generatorBuild` moves

The same deliberately latched definition, upgraded 0.62.0 → 0.63.1 with no patch applied:

```
before   probe=0  crd_schema=3d6af848  generatorBuild=0.62.0-8d6143fce  oasHash=57743d37  resourceHash=2b52eebb  deploy=f9f4f42b
after    probe=1  crd_schema=ab855bb8  generatorBuild=0.63.1-692a993ee  oasHash=57743d37  resourceHash=2b52eebb  deploy=f9f4f42b
                  ^^^ re-rendered       ^^^ THE ONLY INPUT THAT MOVED    ^^^ unchanged    ^^^ unchanged        ^^^ same uid
```

**Both content hashes stayed put and the schema re-rendered anyway**, which is the OR in
`oasHash != stored || resourceHash != stored || generatorChanged` observed directly. The Deployment
uid is unchanged, so this did not go through `Create`. (Sampled once, two polls after the upgrade, so
the probe appeared in the same interval `generatorBuild` moved — not provably the same instant.)

**And the unchanged `oasHash` is the clearest illustration of why status cannot detect a latch.**
`57743d37` is the *new* document's hash: the latch stamped it correctly while rendering nothing. The
status was never wrong **about the document** — it was wrong about *what had been rendered*, and no
field records that. After the re-render the same value is finally true. A status-only check sees an
unchanged, correct-looking hash in both states.

The control definition re-rendered too, with `generatorBuild` moving and its schema unchanged —
confirming the upgrade re-renders everything and only the latched one changes.

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
| the same cluster upgraded to **0.63.1**, same delete-and-edit | 1 | **did not latch** |
| 0.63.1, same edit, Deployment present (control) | 1 | regenerated correctly |

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
