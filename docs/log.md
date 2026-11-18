---
type: Log
title: oasgen-provider — log
description: Curated chronological history of oasgen-provider — notable changes and decisions, newest first.
resource: oci://ghcr.io/krateo-platformops/charts/oasgen-provider
tags: [kog, history]
timestamp: 2026-11-11T00:00:00Z
---

# Log

Curated history (notable changes, decisions); release notes stay in GitHub Releases.
Both components ship from one tag at identical versions, so entries below cover the
provider and the rest-dynamic-controller together.

**Annotations.** Indented blockquotes inside a release's section are labelled, and the label says what
kind of claim it is. The distinction is load-bearing: only the first means the text above it is wrong.

| label | means |
|---|---|
| `Correction (date)` | **the text above was wrong.** A retraction of this file's own claim |
| `Known issue (date, issue N)` | later news: a defect found after that release shipped. The text above stands |
| `Runtime verification (date)` | someone measured it on a cluster afterwards, and this is what they found |
| `Note (date)` | context that is neither a retraction nor a defect |

An unlabelled blockquote is a **quotation** — of a previous section, a code comment, or a peer — not an
annotation about the release.

## 2026-11-17 — 0.82.0

**Five fixes that consumers found, four of them in code I had described as working.** A chart that bound
cluster-wide grants to the namespace's `default` ServiceAccount; cluster-scoped resources with no labels;
~15 seconds with no leader on every rolling update; and a 401 that stranded a resource forever.

### The chart refuses `serviceAccount.create=false` with no name (issue 785)

With that combination **all three of this chart's bindings attached to the namespace's `default`
ServiceAccount** — the one every pod gets when it names none — and between them they grant `secrets`,
`roles`/`rolebindings` and `customresourcedefinitions`. **Any workload scheduled there without an explicit
`serviceAccountName` inherited the lot.**

**Why it survived an investigation that looked straight at it.** `clusterrolebinding.yaml` documents a
*different* problem on the same values key (#717): the binding subject and the Deployment's
`serviceAccountName` could **diverge**, 403ing every call. That fails loudly, and #717 was hunting a
mismatch. **This is a match** — the Deployment also runs as `default` — so the provider works perfectly and
nothing surfaces. **A defect that makes the system work is invisible to every check aimed at a failure.**

A refusal rather than a warning, because there is no error to read and no condition to see: a template that
renders is a template nobody re-reads.

> `Note (2026-11-17)` — **this exposure is not new in 0.81.0.** A consumer rendered published **0.74.0 and
> 0.80.0** and found the same `default` binding in both: it has been there since #717 made the
> ClusterRoleBindings follow the helper. **0.81.0 only added the Lease RoleBinding to the set.** Without that
> check this entry would have sat beside the leader-election work and implied a regression.
>
> The installer's schema exposes `componentValues.oasgen-provider.serviceAccount` with `create` and `name`
> both optional, so the case is expressible through an Installer CR today. **This refusal is the only
> control.**

### Every chart resource carries labels (issue from a consumer's harness)

**Five of twelve rendered resources had no `metadata.labels`:** both ClusterRoles, both ClusterRoleBindings,
and the rbac ConfigMap. Everything namespaced already had them.

```
kubectl get clusterrole -l app.kubernetes.io/instance=<release>   ->   nothing
```

**So a label-driven audit or cleanup silently skipped exactly the objects that grant the most** — and
cluster-scoped objects survive a namespace deletion. `helm uninstall` works from release metadata rather
than labels, so the chart behaved correctly and **the absence stayed invisible until something tried to
SELECT them.** Which is what a consumer's validation harness did, falling back to helm's release-name
annotation.

The leader-election Role and RoleBinding added in 0.81.0 already had labels, written fresh against the
standard helper, so the gap was in the older cluster-scoped templates only.

> `Note (2026-11-17)` — **my first audit of this measured the wrong revision.** I rendered the working tree,
> detached at an older commit, and reported 8 resources with 4 unlabelled; the shipped chart renders **12
> with 5**. The tree predated 0.81.0's Role, so I was auditing a chart no consumer has, and had I stopped
> there I would have told them their live observation was impossible. **A render of the wrong revision looks
> exactly like a render of the right one.**

### A rolling update no longer runs ~15 seconds with no leader

> `Runtime verification (2026-11-17)` -- **MEASURED ON 0.82.0, and the gap is 3.9 SECONDS rather than the
> milliseconds this entry implies.** github-provider-kog rolled published 0.82.0 to itself:
>
> ```
> new pod  "Attempting to acquire"  22:20:16.490
>          "Successfully acquired"  22:20:20.399     -> 3.909 s   (0.81.0: 17.5 s)
> lease    22:20:15 old holder | 22:20:18 holder EMPTY, transitions 0, renewTime 22:20:16.494
>          22:20:22 new holder, transitions 1
> ```
>
> **The fix works and the claim was too strong.** The old pod DID release -- the holder went empty at
> renewTime 22:20:16.49 -- so `ReleaseOnCancel` does what the entry says. What remains is about two
> controller-runtime `RetryPeriod`s: the new pod's first acquisition attempt found the lease still held
> and then waited for retry ticks, not for the 15 s LeaseDuration. **That is an inference from the
> timestamps rather than a log read of the old pod**, which is how the reporter put it.
>
> `Second sample (2026-11-18)` -- **AND MY OWN CORRECTION ABOVE WAS ITSELF TOO STRONG. The gap is not
> 3.9 seconds; it is 0.009-3.9 seconds across two samples, and the mechanism I gave predicts exactly
> that spread.** The same roll on 0.83.0-rc1, same code for the provider's own Lease:
>
> ```
> new pod  "Attempting to acquire"  00:34:21.747
>          "Successfully acquired"  00:34:21.756     -> 0.009 s
> ```
>
> **So "3.9 SECONDS rather than the milliseconds this entry implies" presented one sample as the
> characteristic value, when the quantity is bimodal.** If the new pod's first attempt lands AFTER the old
> pod's release there are no retry ticks and the gap is microseconds; if it lands before, it waits for
> ticks. **My own stated mechanism -- "found the lease still held and then waited for retry ticks" --
> already implied the spread, and I drew a single number from it anyway.**
>
> **What a reader should take:** the roll no longer costs ~15 s, which is the entry's actual claim and is
> confirmed by both samples. **The remaining gap is a race, not a constant**, so neither 3.9 s nor 9 ms
> should be quoted as the figure.
>
> **17.5 s -> 3.9 s, not 17.5 s -> 0.005 s.** The 5-millisecond number above is a FRESH INSTALL, where no
> lease exists to contend for, and reading it as the post-fix roll figure is the mistake this annotation
> exists to stop. A shorter `RetryPeriod` is the lever if 3.9 s ever matters; it is not being pulled,
> because it costs API traffic on every manager of every consumer to save two seconds on an upgrade.

```
new pod  "Attempting to acquire leader lease"   15:25:04.415
new pod  "Successfully acquired lease"          15:25:21.921    17.5 s, one LeaseDuration
fresh install, same chart, same image                            5 MILLISECONDS
```

**The two numbers together are the diagnosis:** acquiring is instant when no stale lease exists, so the
17.5 seconds was entirely the wait for an abandoned lease to expire. `LeaderElectionReleaseOnCancel` was
unset, so a terminating manager kept its Lease until it timed out.

**Nothing was wrong with the old behaviour** — a single writer held the Lease throughout, which is what
issue 771 asks for. It was availability: 15 seconds of no reconciliation on each upgrade.

**The precondition is why this is not merely a flag flip.** With `ReleaseOnCancel` the binary must not keep
writing after the manager stops. `mgr.Start` is the last statement in `main` and returns only once the
controllers have stopped, so when the Lease is released no reconcilers are running. What remains is the
deferred telemetry flush, which writes to OTLP and never to the Kubernetes API — **and it is now bounded at
5 s instead of `context.Background()`, because releasing the Lease makes "alive but not writing" a
load-bearing claim and it should not rest on a remote collector's responsiveness.**

Measured by a consumer rolling 0.81.0 to itself; they inferred the cause from the timing and said so, which
is why it was read in the code before being fixed.

### A 401 means nothing was created, so the resource retries instead of stranding (issue 783)

A 401 held the resource at `CreateOutcomeUnknown`. **For a kind whose existence cannot be answered that is
permanent AND undeletable**, so every create issued during a token expiry became a stuck resource needing a
human — on an ordinary operational event that clears by itself.

```
before   401 unclassified -> the existence-confirm gate -> never answers for a no-findby kind
after    401 -> CreateNotCommitted -> the runtime re-sends; the pass after the rotation files it
```

**`IsAuthorizationDenied` stays 403-only, deliberately.** `createAuthorizationDeniedResult` returns
`Requested: false`, no error, and a condition telling an operator to fix the credential — right for a 403,
which retrying will not change. **A 401 is the opposite: it clears when the secret manager rotates**, so it
needs a retry, which is what `CreateNotCommitted` gives.

> `Correction (2026-11-17)` — **0.81.0's entry claims more than 0.81.0 did.** The dispatch block added there
> says the missing branch meant *"a **401 or 403** landed there and held the CR at CreateOutcomeUnknown"* —
> and the branch it added keys on `IsAuthorizationDenied`, which is **403-only**. So a sentence asserting
> both halves sat above a fix for one, **and the 401 half stayed broken behind a comment saying it was
> fixed.** That is why it survived review: the reasoning had already been written down, so nobody checked
> whether the code matched it.
>
> **The existing test's own rationale demanded the change it forbade.** It listed 401 as must-not-classify
> because *"a request failing against a token being replaced must be retried"* — but leaving a code
> unclassified does not retry the create, it routes to an existence check that for a no-findby kind never
> answers. **`CreateNotCommitted` is the mechanism that does retry.**
>
> **The residual risk, stated:** a gateway that authenticates *after* forwarding could answer 401 having
> already written, which would now retry into a duplicate. Same risk class the list already accepts for 400
> and 422, and not the shape any measured case takes.

### The authz predicate is now tested for matching, not just for ordering

#783's guards asserted the **order** of the dispatch caller's branches, by source offset. **They cannot fail
when the predicate does not match the error the dispatch path produces** — the branch is simply never taken,
and an ordering assertion is still true. That is the shape the 401 report arrived in.

The new test builds the error the dispatch path produces, wrapped exactly as `dispatch.go` wraps it, and
asserts what each predicate says about it. **The load-bearing row is 403:** it proves `fmt.Errorf` with two
`%w` verbs keeps the `*StatusError` reachable by `errors.As`. Had that not held, #783 would have been dead
code and every guard shipped with it would still have passed.

It carries a negative control for the wrap itself — an error with no status must also return false —
because the 403 row alone cannot distinguish *"the wrap preserves the status"* from *"the predicate says yes
to everything"*.

### main went red from two of my own green PRs, and the fix is in this release

**#792 added a table row asserting `createRefusedWithoutCreating(401) == false`. #793 made it `true`.**
Both were open at once, each passed CI against a main that did not contain the other, and merging both
left main red on exactly that row. **A docs-only PR surfaced it minutes later**, which is luck rather than
a procedure.

**Neither review could have caught it.** #792 touches only a new test file; #793 touches the classification
and its own tests. The diffs do not mention each other, and the conflict lives in a value one asserts and
the other changes -- so no reviewer had both halves in front of them. **CI on a PR measures the PR against
main as it stands, never against another open PR.**

The row now carries why it changed, so the next reader does not restore the old value from the old
reasoning. **The surviving behaviour is #793's**, which is why the test was corrected and not the code.

> `Note (2026-11-17)` -- **the check belongs at the merge boundary**, not in either PR: after merging
> anything that changes a predicate, a status list, a default or a classification, re-run CI on every
> other open PR that asserts anything about it. And when main goes red, run the failing test against a
> clean checkout of `origin/main` with your own change absent before assuming you caused it -- a runtime
> bump in progress looked guilty here and was innocent.

### Known issues opened against this release's area

> `Known issue (2026-11-17, issue 444)` — **findby adopts the first item matching the declared identifiers
> and never counts.** Measured on a cluster: a fresh CR adopted a pre-existing closed pull request within 5
> seconds, 0 POSTs, and opened nothing; a CR whose own create never landed had the **existence check** adopt
> a stranger's object and stamp `external-create-succeeded`. The controller then sent **12 PATCHes** to
> those foreign objects — **all refused by the vendor, which is the vendor's protection and not ours.**
> PR #789 counts matches and refuses on two or more; it is **held as a draft** because for one real kind the
> refusal converts a silent wrong adoption into a permanent, undeletable block.
>
> `Known issue (2026-11-17, issue 790)` — #789's counting is **per page**, so for a paginated findby two
> matches on different pages still adopt the first.
>
> `Known issue (2026-11-17, issue 794)` — **`Ready=True` over a known unreconciled difference.** The drift
> comparison's input is normalised by whichever verb Observe used, and Observe switches from findby to get
> once an identifier exists; the two transforms are declared per verb and need not agree, so a field can
> stop being compared. Confirmed by a consumer's request-path correlation. **Narrower than first filed:** an
> ordinary create never crosses that boundary, because a findby pass that finds nothing produces no
> comparison at all — so this is downstream of issues 444 and 740.
>
> `Known issue (2026-11-17, issue 740)` — a kind that cannot answer "does it exist" blocks **deletion** as
> well as reconciliation. `kubectl delete` hangs with the finalizer held, measured at 58 minutes.
> `krateo.io/deletion-policy: orphan` is taken before the gate and deletes cleanly.

## 2026-11-17 — 0.81.0

**The provider elects a leader**, a dispatch refused for a bad credential says so instead of waiting for a
human, the generated controllers' metrics can be scraped at all, and **0.77.0 is retracted.**

### The provider manager elects a leader, and gets the Lease grant to do it (issue 771, half one)

Without leader election every replica reconciles every RestDefinition — and a RestDefinition reconcile
**creates CRDs, Deployments, ClusterRoles and ConfigMaps.** Two writers means concurrent CRD applies on the
same group-resource, against an ownership annotation that assumes a single writer. **A rolling update is two
writers by construction**, so the default has to be true.

**The flag alone would have been an outage**, which is why the grant is in the same change. This chart had
**no namespaced Role at all** — only a ClusterRole, because every grant the provider needed was
cluster-scoped — and controller-runtime's manager **exits** when it cannot acquire its Lease.

> `Note (2026-11-17)` — **this covers the PROVIDER only, and a consumer had to correct a claim that it
> covered more.** I told them "every generated controller now acquires a Lease"; the generated controllers
> start exactly as they did on 0.80.0. `builder.WithOwnership` on those is **issue 771's other half and is
> not started.**
>
> **The issue's own text listed the two halves.** I built one and described it as both — the third time in a
> day that a peer corrected a claim of mine about this repository, each time because I described what a
> change MEANT at a level above what it DID.
>
> **It also corrupted the removal order for `rdc.autoscaling`.** Believing both halves had shipped, I had it
> as "the installer narrows its schema, then the keys go". It is **half two, then the installer, then the
> keys** — because `rdc.autoscaling` is the knob whose hazard half two fixes, and removing it first takes
> away the setting while leaving the hazard.
>
> **The fix cannot protect its own rollout:** during the upgrade that introduces it the old pod has no
> leader election and the new one does. The protection starts from the next rolling update.

### A dispatch refused for a bad credential says so (issue 783)

After 0.80.0 gave the dispatch block a `Requested: true` path, a **401 or 403 landed there** and held the CR
at `CreateOutcomeUnknown` — waiting for a human on an ordinary operational event, a rotated or expired
credential. **The ordinary create path has handled authorization denial ahead of any classification all
along; the dispatch block had no such branch.**

> `Note (2026-11-17)` — **the fix is not adding 401/403 to `createRefusedWithoutCreating`**, which a consumer
> offered as a one-liner. That list is deliberately short and its own comment says 403 would be **dead code**
> there, because the ordinary path never reaches it with one — precisely because the earlier branch catches
> it. Adding the entry would have implied the opposite of what the code does **and still left 401 broken on
> the dispatch path.** The gap was the missing earlier branch.
>
> **Found by a consumer reasoning about their own documentation**, not from a failure: for them this is the
> expired end-user token case, and treating it as "the create may have landed" turns every token expiry into
> a manual recovery per CR.

### The generated controllers' metrics port is reachable (issue 772)

The runtime **counts** `IncReconcileRequeueAfter` and `IncReconcileFailure` on every pass, and no deployment
this chart produced could be scraped: the flag defaults to 0 and nothing set it. **The counters were
incremented and discarded in every generated pod** — and those two are exactly what separates a pass that
DEFERRED from one that FAILED, a distinction that took sub-second log arithmetic across four bursts to
recover during a real investigation and would have been one scrape.

**The default is still 0.** A port on every generated controller is a surface change, and on the one cluster
where this was measured **nothing scrapes them** — the collector declares a `prometheus` receiver that sits
in no pipeline, so it never starts. What changed is that it is reachable, as a dedicated key the schema
validates rather than a free-form env entry where a typo is silent.

### The release sweep stops reporting a false MISMATCH mid-publish

`hack/release-sweep.sh` landed in 0.80.0 and produced a believable false alarm on its first real use, four
minutes after that tag:

```
charts/oasgen-provider            appVersion=<none>  MISMATCH, want 0.80.0
charts/controller-render-service  appVersion=<none>  MISMATCH, want 0.80.0
VERDICT: a chart points at the WRONG image version
```

**Nothing was wrong.** The charts were not published yet, so `helm show chart` returned nothing and the
appVersion came back empty — which the comparison read as a mismatch. **"Not published" and "published
pointing at the wrong image" were reported identically**, and the second is an incident while the first is a
normal four-minutes-after-tagging state. **Its own control held throughout**, which is what made the alarm
believable rather than obviously broken.

**Third time in that one file that an absence with a legitimate cause needed its own branch rather than a
louder alarm:** an imageless chart has no appVersion to set, a chart pinning `image.tag` explicitly has no
use for one, and an unpublished chart has nothing to read at all. **The check now VOIDS in all three and
says which.**

### 0.77.0 is retracted, with `use: 0.78.0`

On a findby-addressed kind, a create whose outcome cannot be determined stays `Ready=False/Creating`
indefinitely instead of reaching `CreateOutcomeUnknown` — and the CDC rollup reads `Creating` as
**converging**, so a parent composition reports healthy over a resource that will never exist.

> `Note (2026-11-17)` — **it sits under a different standard than the 0.67.0/0.68.0 decision in the same
> file, and that file now says so.** Those were refused because the route was a **code reading with no
> measurement.** This has the measurement — two vendors, two consumer sessions, independently — and what it
> measured is **not damage**: no duplicates, no data loss, both recovery arms work.
>
> **The harm is that the signal is gone.** `CreateOutcomeUnknown` exists for no other purpose than to prompt
> a human, and 0.77.0 removed that prompt for exactly the kinds where a lost create answer is unrecoverable
> without one. **A failure nobody is told about is worse than a visible one.**
>
> **And it is not a push, which the rule warns against:** 0.77.0 was superseded within hours, two consumers
> already refused it on their own measurements, and `exists.go` is byte-identical from 0.78.0 onward.
> **All three charts are named**, because the version-line gate would otherwise confirm a bad set as "in
> step".

## 2026-11-17 — 0.80.0

**A dispatch that reached the vendor is no longer reported as "nothing was sent"**, the duplicate-retry-budget
class is closed on both of its sites, and two checks that were methods in a message thread are now scripts
in the repository.

### A dispatch that reached the vendor is a REQUESTED create (issue 776)

Every error return in the dispatch block returned the **zero** `CreateResult`, so `Requested` was false even
when the request had gone out and the vendor had stored the object. The runtime's gate is
`createCommittedNot(actionErr) || !created.Requested`, so it read *"nothing was sent"*, skipped the
existence-confirm gate and re-entered Create.

```
MEASURED on a real cluster against 0.78.0, with a fake that stores the object then answers 500:
  t+10s  posts=4   external-create-failed == external-create-pending == 09:52:49Z
  t+20s  posts=9   ObserveFailed
  t+50s  posts=12  "correlation is ambiguous: 6 items in the collection echo the token"
  four dispatches in 7.5 s, before any observe
```

`external-create-failed` stamped at the same instant as `external-create-pending` on every attempt is the
issue-746 signature.

> `Note (2026-11-17)` — **a status-code check would not have fixed it**, which is why this needed a sentinel.
> A pre-send failure and a connection dropped mid-flight both arrive with **no status code**, and
> `externalStatusCode` returns 0 for each — so classifying by code reports a lost answer as "nothing was
> sent", the defect itself. **The discriminator has to be whether the request went out**, which only
> `createDispatch` knows, so it marks its two post-send returns and the caller tests `errors.Is`. The same
> shape as #718's wait: the discriminator is the error's IDENTITY.
>
> **And #747's guard was blind to this by construction.** It anchors on `CreateWithResult`'s own `apiCall`
> line and checks every zero-value return PAST it — but the dispatch block sits *before* that anchor, and
> the dispatch's send happens inside `createDispatch`, in another function no positional check on
> `CreateWithResult` can reach. **A positional guard needs its SCOPE stated as carefully as its assertion.**
>
> **The replacement guard failed on correct code on its first run**, reporting "1 of 2 marked": gofmt had
> wrapped the long return and the sentinel sat on the **continuation line**, which a line-based scan cannot
> see — the same defect class the guard exists to catch. It scans by statement now.

**Creates resuming after an observe fails as ambiguous is a SEPARATE defect** and survives this fix. Filed
with its measurement and three unverified candidates rather than a mechanism.

### unstructured-runtime v1.24.0 → v1.27.0: both sites of a duplicate-retry-budget class (issue 769)

A worker-side follow-up enqueue built a fresh event with `QueuedAt: time.Now()` and called `AddWithOpts`
**with no `items.LoadOrStore`**, while the informer paths had four of them. The queue key is the Event with
`QueuedAt` included, so a Create waiting out its backoff got a **second key with its own retry budget**.

```
MEASURED: 12 vendor requests where 1 + MAX_ERROR_RETRIES(5) = 6 is configured, as two keys of 6 --
one climbing at ~2/3/5/9/17 s, one flat at ~1 s (the second key's first pass, RateLimited: false)
```

> `Note (2026-11-17)` — **FIVE mechanisms were proposed and excluded before anyone counted the queue keys**,
> and the instrument that settled it used data already on disk: group the refused-create log lines by
> `queuedAt`, with the clean per-resync bursts as its own control. **One grep answered what five rounds of
> code-reading had not.**
>
> **The lesson is not "exclusions are durable".** When mechanisms keep dying, the question is not *what else
> could cause this* but **what quantity are all these stories disagreeing about, and can I measure it
> directly?** Here that was the number of queue keys, measurable from the first report.
>
> **The second site was found by asking the runtime's owner whether their TEST covered a precondition**, not
> whether something was a bug. Their answer enumerated their own paths and turned up an unfixed enqueue in
> the incomplete-create recovery — open for the whole two-minute grace, which is this provider's issue-758
> territory, and reachable with no operator action at all.

### Two checks are now scripts rather than methods (`hack/`)

**`not-walk.py`** detects the 0.79.0 crdgen tightening: it walks parsed structure for a schema key named
`not`, so it is immune to the three spellings a grep must enumerate, and **gates its own output on
controls** — exit 2 means *the result means nothing*, distinct from exit 0 *clear*. An unparseable file in
scope fails the controls; a Helm template is skipped as out of scope; an external `$ref` is reported as a
caveat, because its target is not walked and a `not` can hide there.

**`release-sweep.sh`** checks that a release's five artifacts are published, with **one instrument per path
shape and a control pair for each**. A raw registry manifest probe returns 404 for `charts/*` at every
version; `helm show chart oci://...` asks through the client that actually pulls.

> `Note (2026-11-17)` — **three instrument failures on one sweep in one day**, and only two were caught by
> controls. `crane` was not installed, so every row *and* both controls returned 0 — caught, because they
> went to zero together. The registry API returned 403 for everything including the negative control, which
> is not a control but a second copy of the same reading — also caught. **The chart-path blindness was not
> caught**, because the controls were on image paths: a control validates the instrument **for the shape it
> tests**, and a sweep mixing images and charts needs one pair of each.
>
> **It also checks the LINK the rows cannot see.** Both templates resolve through
> `defaultTag .Chart.AppVersion` with `image.tag: ""` shipped, so appVersion is what a deployed pod actually
> pulls — and a chart published at the new version whose appVersion is the previous one passes every row and
> then deploys the old image. **That rule is NOT general**, so its precondition is asserted rather than
> assumed: a consumer ran it over 20 pinned components and found 3 benign mismatches — two wrapper charts
> whose appVersion is the upstream app's version, and one pinning `image.tag` explicitly with a digest.
> **A check that flagged all three would be noise, and noise is how a gate gets ignored.**

## 2026-11-17 — 0.79.0

**Two declarations that were accepted and silently ignored now do something**, a delete that could take away
someone else's CRD no longer can, a warning stops asserting what it cannot observe, and the plumbing bump
carries a change that can refuse CRs that already exist.

### A request-direction fieldMapping on a dispatch verb now runs (issue 718)

A `fieldMapping` on an `action: dispatch` verb — **`secretRef` among them** — was parsed, validated,
admitted, plumbed to the call and **never applied**. The resolved map was a literal `nil`. So a dispatch
could file a vendor record missing the value its own declaration named, with nothing on the CR and nothing
in the logs to say so.

> `Note (2026-11-17)` — **the comment that stood over this misdiagnosed its own problem**, and the
> correction is the design. It said `(bool, error)` *"has no way to express 'wait, do not treat this as
> failed'"* and called that a signature question.
>
> `handled == false` means *"this resource declares no dispatch verb"*, so returning it to mean "wait" makes
> the caller **fall through to the ordinary create path** — dangerous, not merely wrong. And `(true, nil)` is
> handled-and-succeeded. **It is true that no pair says "mine, nothing sent".**
>
> **But the discriminator does not have to be a return VALUE. It is the error's IDENTITY**, and
> `isLookupReferentMissing` already existed and is already what the Create path tests. No signature change
> for the wait; only a `clientSet` parameter so the resolvers have something to resolve with.
>
> **The audit is why ORDER matters, and reading the dispatch function alone would have missed it.** The
> caller audits unconditionally on `handled`, because a dispatch IS this resource's external write. The
> resolvers run strictly before the request is built, so a referent that does not exist yet means **nothing
> was sent** — and auditing it writes a row asserting an external write that never happened, into the trail
> that block exists to keep complete. **Worse than the gap the audit was added to close.**

### A RestDefinition that does not own a generated CRD no longer uninstalls it (issue 763)

Ownership was enforced on apply and **not on delete**. A RestDefinition that was *refused* the CRD at apply
time — told so explicitly, by owner name — could still uninstall it when deleted, because `Undeploy` drops
the CRD by group-resource and nothing on the delete path read the annotation.

```
owner has instances    the non-owner wedges on ErrLiveInstances, forever, for a delete that
                       should have been a no-op
owner has none         the owner's CRD goes, and DeleteBackfillRBAC is keyed by GROUP AND RESOURCE
                       rather than by this RestDefinition's name, so the owner's ClusterRole goes too
```

**`SkipCRD` is reused rather than a new code path**, so a non-owner's delete becomes the same shape as a
delete that must not touch the CRD — **and the live-instance guard is never reached, which is what unwedges
the first case.** A failed owner read is **not** treated as "unowned": anything but `NotFound` skips the
shared teardown, because refusing to drop a CRD we cannot prove we own is the safe direction. The finalizer
is removed either way, so the delete always completes.

**The test asserts the CRD's `uid`, not its existence** — a recovery path that recreated the CRD would
satisfy "a CRD is present" while having cascade-deleted every CR of the kind. Reported by
`keycloak-provider-kog`, found by accident: re-applying `helm get manifest` output without a namespace, which
is what an operator doing the same thing would hit.

### The unmodelled-key warning stops asserting what the vendor will do (issue 757)

It said an update **"would DELETE"** the keys the vendor holds and the CR cannot express, and the event reason
said so too.

```
MEASURED on Azure DevOps, 7 kinds, 16 key rows:  10 KEPT, 5 RESET, 1 where a reset would be invisible
every server-filled key was KEPT, 16 rows, no exceptions
five of SIX author-set settings were RESET
```

**So the assertion was wrong about most of the cases that produced it**, and it sent operators to model keys
the endpoint was never going to drop. What this controller can observe is its own behaviour — it sends each
field whole. **What happens to a key it did not send is the endpoint's semantics, and nothing here can see
that.**

> `Note (2026-11-17)` — **the event reason is RENAMED**, `UnmodelledKeysWouldBeRemoved` →
> `UnmodelledKeysPresent`, and **a filter keyed on the old name stops matching.** The reason is the channel
> most often read alone, in a `REASON` column with no message beside it, so a name that predicts the vendor's
> behaviour is exactly the assertion this fixes. The one consumer who might have filtered on it checked and
> does not.
>
> **No classifier, deliberately.** Every server-filled key measured survived, so "suppress the
> server-computed class" is the obvious next step — and the controller would have to GUESS which keys those
> are, where **a wrong guess suppresses a real warning.** `settings.creatorVoteCounts` is the counterexample
> that rules the heuristic out: author-set, and kept.

### plumbing v1.60.0 → v1.93.0, and it can refuse CRs that already exist (issue 767)

Audited **per package** against the version gap rather than from the release announcement, which describes
the announcing PR:

```
crdgen     9 commits   generates every CRD this provider ships
fieldpath  1 commit    Set/Remove added; Get and Parse unchanged
```

**MVS dragged more than plumbing**, and these are not cosmetic: `krateo-platformops/audit` v0.6.0 → v0.13.0
(a **direct** dependency, v0.x, seven minors — for a v0.x module MVS picking the higher minor is a *breaking*
upgrade), `go-openapi/jsonpointer` and `jsonreference` each to **v1.0.0**, `go-openapi/swag` plus twelve new
sub-modules.

> `Note (2026-11-17)` — **crdgen's #209 is a TIGHTENING of a shipped CRD, and "carried natively"
> understates it.** Measured with one variable, the same probe source against both versions:
>
> ```
>                  v1.60.0        v1.93.0
> not: in the CRD  0              3
> warnings         3x "not on non-scalar node unsupported -> dropped"   0
> ```
>
> **`not: {required: ["x"]}` means x must be ABSENT.** While the constraint was dropped a CR setting `x` was
> admitted; now the apiserver **rejects** it. crdgen is correct — the OAS author wrote "must not be present"
> and meant it — and the exposure is to CRs created while it was silently gone.
>
> **THE DECISIVE CHECK IS STATIC: walk every key of your OAS assets and look for a schema key named
> `not`.** No `not` in the input means crdgen has nothing to enforce, whatever any version does. It needs a
> positive control — plant `a: {not: {required: [x]}}` and confirm the walker finds it — and it requires the
> assets to be self-contained, since an external `$ref` can carry one in.
>
> **The old version's warning is a SUPPORTING check, not a decisive one.** Grepping a render for
> `not on non-scalar node unsupported -> dropped` is appealing because a deprecation-shaped warning is a
> forward-looking inventory of what a later version will enforce — **but a grep that finds nothing is
> evidence only if that warning is emitted at the configured log level, and the level comes from the
> chart.** An absence found that way is indistinguishable from a warning that was never printed.
>
> **This correction is a consumer's.** Asked for the grep, they did the static walk instead, reported the log
> grep as "supporting only" and said why: they had not confirmed the provider logs that warning at its
> configured level. **The weaker check was the one in the original draft of this note.**
>
> **Validated on a 32-kind consumer corpus**: 64 objects, schemas identical, no warning appeared or
> disappeared, with a `maxLength` control in both renders and both images built the same way. **That corpus
> contains no such `not`, so it clears the bump without exercising #209** — which is why #209 was then
> exercised with a crafted input instead.

## 2026-11-16 — 0.78.0

**A marking from 0.77.0 was wrong and is reverted**, and a dispatch kind can finally send the query
parameter its vendor needs to paginate at all.

### "A list matched nothing" is no longer transient (issue 758)

0.77.0 marked five existence refusals transient. **One of them covers a permanent cause as well**, and it
is the one its own comment argued hardest for:

```
transient reading   the create LANDED and the vendor has not listed it yet   (Ironic)
permanent reading   the create NEVER HAPPENED, so there is nothing to list   (a GitHub PullRequest)
```

**Both produce the identical observation and a single pass cannot tell them apart.** 0.77.0's comment said
*"so 'no match' is a state the vendor leaves"* — **true only if the create landed.**

Found by `github-provider-kog` on a findby-addressed kind: marked transient, a never-created resource
reports `Ready=False/Creating` and requeues forever, **and core-provider's rollup reads `Creating` as
converging** — so a parent composition looks healthy indefinitely over a resource that will never exist.
That is the defect `CreateOutcomeUnknown` was added to fix, reintroduced in the release that adopted the
fix for it.

**It is the same shape as the seven setup failures 0.77.0 deliberately held back**, and the hold's own
reasoning was applied to those and then not to this one.

**Un-marking costs one resync.** An unmarked refusal still clears itself, because the digest dedupe is
released at the top of `processItem` and the periodic resync runs the check again — so Ironic's
late-listing resource shows `CreateOutcomeUnknown` for about one interval and then resolves when the list
catches up. **A wrong condition for three minutes beats a silent permanent one.**

> `Note (2026-11-16)` — **time-bounding the mark was rejected, and it is the option that is right for both
> causes.** Keeping it transient only while the create-pending marker is young would spare the Ironic case
> even one resync of a wrong condition. It buys three minutes of accuracy for a duration constant nobody
> can justify — the runtime's two-minute grace does not apply, since this path returns an error and never
> reaches it — plus a branch in a function whose value is that each refusal returns one honest sentence.
> **Recorded rather than dismissed: if a consumer finds those three minutes painful, that is the fix.**

**Anyone on 0.77.0 is exposed for any findby-addressed kind.** Consumers counted **15** such kinds in one
chart and **11** in another, which is why both skipped that release for this path.

### The chart documents all three OTEL switches, not one of three (issue 729)

`values.yaml` and `values.schema.json` now name **all three** export switches with what each one gates,
where they documented one. **The endpoint says WHERE and nothing about WHETHER**, and all three default to
false — 34 generated controllers on krateo-057 had the endpoint set, every channel off, and exported
nothing.

**They are independent, not a master and sub-switches, and that is measured rather than inferred:** on that
cluster all 34 rest-dynamic-controller ConfigMaps carried `OTEL_LOGS_ENABLED=true` with **no `OTEL_ENABLED`
key at all** and exported audit rows, while 76 core-provider ConfigMaps carried the inverse —
`OTEL_LOGS_ENABLED=false` with metrics and traces on. **So setting `OTEL_ENABLED=true` expecting "all
telemetry" gets metrics and traces and no audit trail.**

The schema text matters more than the yaml comment: an installer-schema description is what a UI shows, and
`helm package` strips comments while schema text survives. It also states that `rdc.env` is **fleet-wide**
and that there is no per-kind setting, because that is the question an operator asks next and the answer is
not visible from the field's shape.

**The startup-line half of that issue shipped in 0.76.0**; this is the chart half. Measurement by the
`observability` session.

### A correlation walk can send the query parameter its vendor needs (issue 719)

`correlation.observe` now accepts static `queries`, **sent on every page, including after the pagination
token is appended.**

**A vendor can make its pagination MODE depend on a query parameter.** Zendesk switches to cursor
pagination only when `page[size]` is sent: without it the response carries no `meta.has_more` or
`meta.after_cursor`, so a declared `continuationToken` pagination has nothing to follow and the walk sees
one page. **A dispatch kind whose correlation token is not on that page never reaches Ready, and files
nothing either, with no error on the CR.**

There was no way around it from the author's side: `queries` existed on a verb, but the correlation observe
builds its own call info and never set the field — so a `queries` entry there was pruned at admission with
only a helm warning.

> `Note (2026-11-16)` — **"on every page" is the load-bearing half.** The obvious implementation threads
> the queries into the first request and lets pagination build the rest, which drops them from page two
> onward — and on such a vendor the walk then silently reverts to offset mode mid-walk, the token path
> disappears, and it ends after one page with no error. **A one-page test cannot see that.**
>
> It works because the walk reuses one call configuration for every page and the paginator only ADDS its
> token to the existing query rather than replacing it. **Those two facts are asserted separately rather
> than their composition**, because the second is the fragile one: a paginator that built a fresh query
> would break this silently.
>
> **A misspelled key is refused rather than pruned**, and that comes free from reusing the existing
> `QueryParam` rather than an open map: `name` and `value` are both required, so a typo prunes to `{}` and
> then fails the required check. An open map would have accepted it and dropped it silently — the shape
> that made the field necessary.

Requirements and the motivating case are the `acmp` session's. **A query parameter whose absence changes
the vendor's pagination mode is a better example than anything invented here: it makes the feature
necessary rather than convenient.**

## 2026-11-16 — 0.77.0

**A transient refusal stops reporting a state a human must act on.** Four commits, and the first is the
one a consumer release is floored on.

### Five of sixteen existence refusals are now marked transient (issue 740)

`Exists` refuses for **sixteen** distinct reasons and the runtime could not tell them apart: every one
arrived at the incomplete-create branch as a plain error and got `Ready=False/CreateOutcomeUnknown`, which
names a state a human must act on. **Five resolve themselves.**

unstructured-runtime **v1.24.0** adds the contract (`ExistenceUndetermined`, a refinement of its #93) and
this release adopts it. A marked refusal gets `Ready=False/Creating` — the existing non-sticky reason, which
core-provider's rollup reads as converging, which is true because a create really is pending — plus
`Synced=ReconcileError` naming the cause, and the error is returned so the queue backs off.

```
MARKED    :92   observation delegated to a RESTAction
          :96   an async operation still in flight
          :99   the resource is Pending
          :177  a list succeeded and matched nothing
          :191  the read itself failed (5xx, timeout)

UNMARKED  :137  no verb to read with            :149  a status-sourced path parameter
          :142  the call config would not build :145  a jq program that did not compile
          plus seven setup/construction failures
```

**`:92` is the one a consumer was waiting for:** a kind whose observation is delegated to a RESTAction was
stuck permanently after a lost create response, and `keycloak-provider-kog`'s
`KeycloakServiceAccountRoleMapping` is rendered whenever `identityLifecycle` is enabled. Their KOG release
floors on this version for that reason.

> `Correction (2026-11-16)` — **this names the wrong one of that chart's two delegated kinds.** The
> reporting session measured it afterwards: `KeycloakServiceAccountRoleMapping` **as deployed never takes
> the create path** — the target service-account user already exists when the mapping starts, so the
> delegated observe reports it found and the controller updates. No `external-create-*` annotations
> appeared at all.
>
> **`KeycloakAuthenticationExecution` is the kind that creates through a delegated observe**, and it is
> the one this marking protects. So the fix has a real beneficiary in that chart — just not the kind
> named above, which was the kind the discussion started from.
>
> Their release log carries the same distinction and says explicitly that the role mapping was not
> exposed. **A release note naming a kind that does not take the path is worse than naming none**, because
> it invites an operator to look for a symptom that cannot occur there.

**`:177` is a deliberate choice rather than an obvious reading.** An eventually-consistent vendor can accept
a create and not yet list it — Ironic demonstrated exactly that, a node `active` on disk that a list did not
show. It stays an **error** rather than becoming "definitely absent", because absence would license a
re-create.

**`:191` is the measured one.** The observability session found **321** of these on krateo-057 from a vendor
503, across 17 resources of one kind, all clearing within the same second when the vendor recovered.

> `Note (2026-11-16)` — **the seven setup failures are a deliberate hold, not an oversight.** Each covers
> both causes in one return: *"building the REST client for get"* fails for a missing Secret (transient)
> **and** for a Configuration naming a scheme that does not exist (permanent), and the error does not
> distinguish them — so one marking must be wrong for one of the two.
>
> The asymmetry decides it. **Marked-but-permanent requeues forever reporting `Creating`, which the cdc
> rolls up as converging**, so a parent sits in `childConverging` with nothing saying why — the defect
> `CreateOutcomeUnknown` was added to fix, reintroduced through its own fix. **Unmarked-but-transient shows
> `CreateOutcomeUnknown` for one resync and clears.** A wrong condition for three minutes beats an invisible
> wedge. To be revisited per-site when a consumer reports one, with their error in hand.

### The audit instrumentation scope is the module path (issue 744)

Every other writer in the platform passes `github.com/krateo-platformops/<component>/audit`; this controller
passed a bare `rest-dynamic-controller/audit`, so its rows looked like they came from somewhere else.
**Routing never depended on it** — that keys on `event.name=audit` — which is why the rows arrived correctly
and the mismatch survived several releases unnoticed.

> `Correction (2026-11-16)` — **the other half of that issue was not a defect**, and its own reporter
> established that. `krateo.audit.schema_version` has been stamped by the audit library since **v0.4.0**
> and this controller pins v0.6.0, so it was already carried. The 24 rows that prompted the issue came from
> `service.version` **0.48.0 and 0.56.0** builds, and no build had written an audit row on that cluster
> since — so the **current** builds were *unobserved*, not defective. Now pinned in CI rather than needing a
> cluster.

### A caveat on partialFields that is a precondition of the feature (issue 739)

**`partialFields` assumes the GET returns, faithfully, every key the PUT can set.** That is the precondition,
and an earlier draft of this entry stated it wrongly — see the Correction below.

`github-provider-kog` measured GitHub's webhook `config` and it does not: an omitted `secret` is a
**removal**. They also found that a PATCH carrying the mask `"********"` makes GitHub sign with the
**literal mask** — verified by checking `X-Hub-Signature-256` against HMAC-SHA256 of the recorded payload.
So the merge there would replace a real HMAC secret with a guessable one, which is worse than clearing it.

**Neither skipping mask-shaped values nor excluding the field would help**: both end with the key absent,
and absent means removed. **The only safe shape is the CR owning the value**, through a `secretRef`
resolver that injects before the overlay.

> `Correction (2026-11-16)` — **an earlier draft of this entry, and the caveat it added to 0.76.0, stated
> the precondition as "the vendor treats an omitted key as UNCHANGED". That is wrong**, and
> keycloak-provider-kog caught it by applying the test to their own vendor.
>
> **Keycloak already treats an omitted key as removed — that is why `partialFields` exists for them.** A
> whole-map PUT deletes the keys the CR does not carry precisely *because* Keycloak drops what is omitted.
> So a test of "PUT without one key, then GET" reproduces the original defect on every field and says
> nothing about this feature.
>
> **The merge does not omit anything. It replays what the GET returned.** So the precondition is a property
> of the READ, not of the write:
>
> ```
> partialFields needs:   the GET returns, faithfully, every key the PUT can set
>
> FIDELITY failure       the key comes back WRONG -- GitHub's secret as "********" (#739)
>                        -> replaying it writes a guessable value
> COMPLETENESS failure   the key does not come back AT ALL -- a write-only key
>                        -> the overlay cannot replay it, and the vendor drops it
> ```
>
> **GitHub webhooks fail both**, which is why nothing rescues them: the read is unfaithful *and* omission is
> destructive, so neither replaying nor dropping the key is safe. **A vendor that fails only the read
> condition while honouring omission would be fixable** — drop the key from the overlay and it survives.
>
> So the per-field test is: **seed a key through the vendor's own API, then confirm the GET echoes it with
> the same value.** A key that is stored but not returned, or returned altered, disqualifies that field.

## 2026-11-16 — 0.76.0

**0.75.0's headline fix did nothing. This release is what makes it work.** Four defects that were
shipping in 0.75.0, and the first of them is a correction to that release's own central claim.

### The duplicate-create recovery now engages (issue 746)

`CreateWithResult` told the runtime that **nothing had been sent**, on every error path after the POST.
The runtime's gate is `createCommittedNot(actionErr) || !created.Requested`, and `CreateResult{}` is a zero
value — so a 5xx, a transport failure, a timeout and a lost response all read as "no request was made":
`external-create-failed` was stamped, `ExternalCreateIncomplete` went false, **the existence-confirm
recovery never ran, and the next pass POSTed again.** The 400/422 classification shipped in the same
release was irrelevant, because everything was already being treated as not-committed.

**Twelve sites now report `Requested: true`** — every error return at or after the point the request leaves
the process. Seven of them are *after a successful create*: a response-body parse failure or a lost status
write was reporting that nothing had been created, **which is a duplicate route with no network failure in
it at all.** Eight sites deliberately still report false: the setup failures before the request, where
nothing reached the vendor.

> `Note (2026-11-16)` — **the comment at the defect site had the mechanism right and the value wrong, in
> adjacent lines.** It reads: *"A ResultCreator reporting Requested=false already counts as not committed
> in the runtime, so the 'nothing was sent' cases … are not wrapped here either."* An accurate description
> of the gate, written immediately above a return of exactly that value on the path where something HAD
> been sent. Understanding the mechanism was not enough, and nothing checked that the value matched the
> sentence beside it.

**Found by github-provider-kog, who read it in source and then measured it on the released artifact**, and
confirmed independently from source by azuredevops-provider-kog. The guard is source-level
(`TestNoCreateResultAfterTheRequestClaimsNothingWasSent`), because every individual return is
syntactically fine and returns a correct error — what was wrong is one bool, on one side of a gate in
another repository.

### Every backfill ClusterRole was leaking (issue 742)

`DeleteBackfillRBAC` shipped in 0.74.0 with a doc comment, **zero call sites and zero tests**. So every
RestDefinition ever deleted left its labelled ClusterRole behind, aggregated into the provider's role,
granting `patch` on a resource whose CRD had just been dropped. The rule names a **group and a resource,
not a UID** — so a later CRD at the same group/resource would silently inherit the grant.

**The fix does not clean up what already leaked.** On a cluster that deleted a RestDefinition while running
0.74.0 or 0.75.0, the audit is in issue 742. A one-time sweep is deliberately absent: *"no current
RestDefinition"* races one still being created, and revoking a grant mid-creation is worse than leaking one
for a kind that is gone.

### A partial spec field is merged instead of replayed (issue 400)

A verb may declare `partialFields`, naming top-level body fields the CR carries as a **partial view**. The
controller reads the resource before the update and overlays the CR's keys onto the current value, so keys
the CR does not model survive. If that read fails the field is **omitted** rather than sent whole, so the
vendor replaces nothing.

Three measured losses it prevents, on three vendors: Ironic's `spec.extra` (82 seconds without a
control-plane VIP), a github webhook's `config.secret` (the HMAC secret, silently), and — the first found
*before* any loss — keycloak's `spec.attributes` on a realm and two clients, 14 keys named by the warning
0.74.0 shipped.

> `Known issue (2026-11-16, issue 739)` — **`partialFields` cannot protect a field whose omission the
> vendor treats as a DELETION, and GitHub's webhook `config` is one. Do not declare it there.**
>
> Measured by the github-provider-kog session on a throwaway repo, by reading each ping's
> `X-Hub-Signature-256` from the deliveries API and verifying HMAC-SHA256 over the recorded payload:
>
> ```
> control   hook created with secret S       -> signed with S
> MASK      PATCH with secret "********"     -> signed with the LITERAL "********"
> omit      PATCH without secret             -> NO signature header at all
> ```
>
> **So the merge would replace a real HMAC secret with a value anyone can guess** — worse than clearing
> it, because a cleared secret fails verification loudly while a guessable one successfully verifies
> attacker-forged payloads.
>
> **And the omit arm is the one that generalises.** An omitted `secret` is a *removal*, not "leave it
> alone" — so neither skipping mask-shaped values nor excluding the field from the overlay would help:
> both end with the key absent, and absent means removed on that endpoint.
>
> **The precondition this exposes, which the section above does not state:** `partialFields` assumes the
> vendor treats an omitted key as UNCHANGED. Where it does not, no merging logic can preserve a value the
> CR does not carry, because the controller never has it. **The test to apply before declaring a partial
> field that contains a secret is: does this vendor treat an omitted key as unchanged, or as removed?**
>
> **The safe shape is unchanged from the section above:** a key the CR expresses wins, and a `secretRef`
> resolver injects before the overlay runs. keycloak-provider-kog held `kog-identityprovider` out of
> their declaration for this reason before any of it was measured.

### The OTLP endpoint is not the switch (issue 729)

A startup line now says whether the endpoint and the three export channels agree. `OTEL_EXPORTER_OTLP_ENDPOINT`
says *where* and nothing about *whether*: 34 generated controllers on krateo-057 had the endpoint set,
every channel defaulting false, and exported nothing. Both directions warn, and the worse one is a channel
enabled with **no** endpoint — telemetry produced and discarded, which a consumer cannot distinguish from
no activity.

Measured on 057 by the observability session: all 34 RDC ConfigMaps carry `OTEL_LOGS_ENABLED=true` with
**no `OTEL_ENABLED` key at all**, against 76 cdc ConfigMaps at logs=false and traces=on. **So the switches
are independent, not a master and sub-switches** — an operator setting `OTEL_ENABLED=true` expecting
everything gets traces and metrics and no audit trail.

## 2026-11-16 — 0.75.0

> `Correction (2026-11-16, issue 746)` — **this release's central claim is false. The fix did not engage
> in rest-dynamic-controller at all.**
>
> `CreateWithResult` returned a zero-value `CreateResult` on every error path after the POST, and its
> `Requested` field is therefore false. The runtime's gate is `createCommittedNot(actionErr) ||
> !created.Requested`, so every unclassified create error — including the lost response this release
> exists to fix — read as "nothing was sent": `external-create-failed` was stamped,
> `ExternalCreateIncomplete` went false, and the next pass POSTed again. **The existence-confirm branch
> below was unreachable from this controller.**
>
> Measured on the released artifact by github-provider-kog: `external-create-failed` stamped in the same
> second as `external-create-pending` on every attempt, never `CreateOutcomeUnknown`. And measured on a
> second vendor by azuredevops-provider-kog: **3 of 4 triggered trials re-fired, leaving 4 runs at the
> vendor that no CR references** — every CR reported `Ready=True` tracking only its last attempt.
>
> **So everything below about `CreateOutcomeUnknown` describes a path 0.75.0 could not reach**, including
> the `Known issue` about a kind with no findby. Fixed in **0.76.0**; the condition becomes reachable
> there, which means the stuck state that `Known issue` describes arrives with the fix rather than
> predating it.

**A create whose response was lost stops re-creating the resource.** The runtime bump to
unstructured-runtime **v1.22.0** is the whole of this release's behaviour change, and the defect it fixes
is a duplicate: a create whose response never arrived was indistinguishable from one that never happened,
so the same event was re-delivered and the POST re-sent — bounded at `1 + REST_CONTROLLER_MAX_ERROR_RETRIES`,
which is **six POSTs** at the default. On an API whose names are not unique, that is six resources.

**And rest-dynamic-controller now tells the runtime when the vendor definitely did NOT create anything.**
A create the vendor *refused* — `400 Bad Request` or `422 Unprocessable Entity` — is reported as a
committed non-creation rather than an unknown outcome, so the runtime stops guessing about it. Not 409,
because some vendors answer it precisely *because* the resource already exists; not 404; not 5xx; and not
403, which never reaches the classification site at all because authorization denial is handled earlier
and returns a nil error.

### The one thing to know before upgrading: some resources will show red that previously showed converging

`CreateOutcomeUnknown` is a reason core-provider maps to **`childFailed`**, where the same situation
previously fell through to `default: childConverging`. Operators will see new red. Three facts bound it,
and the second is narrower than this entry's first draft claimed:

**It needs BOTH halves.** A lost create response does *not* by itself produce this. After an unclassified
create failure the runtime runs an existence check — rest-dynamic-controller's Observe — and:

```
exists        -> stamped succeeded, routed to Observe.          NO red.
absent        -> waits out a 2-minute recovery grace, then re-creates.
check ERRORS  -> Ready=False / CreateOutcomeUnknown, plus Synced ReconcileError.
```

So new red means *"a create's outcome was unknown **and** the existence check could not answer"*, which is
materially rarer than a lost response. Verified in `pkg/controller/worker.go:617-654` at v1.22.0: the
condition is written only inside `if obsErr != nil`. A status-write conflict requeues before reaching it.

**The consumer floor is 2.13.103.** Below it the reason is unmapped and falls to the converging default, so
an operator on an older core-provider sees no change whatever. Confirmed against the files rather than the
PR order: `git show <tag>:…/childhealth.go` carries the arm at 2.13.103 and not at 2.13.102.

**It clears by itself.** A later existence check that succeeds ends it, which the ordinary resync provides;
a human can also clear `krateo.io/external-create-pending`.

> `Known issue (2026-11-16, issue 740)` — **"it clears by itself" does NOT hold for one shape of kind, and
> that shape is common.** A kind whose `get` is keyed on a SERVER-ASSIGNED identifier and which declares
> **no findby** can never answer the existence check after a lost create response: the identifier was never
> recorded, so the resource is not addressable, and there is no findby to re-find it with. The check fails
> identically on every pass, forever.
>
> ```
> exists.go:109   IsResourceKnown  -> false      (status.<id> is empty)
> exists.go:115   action = FindBy                 <- the only way left to answer
> exists.go:137   no findby declared -> error     "existence cannot be read"
> worker.go:654   Ready=False/CreateOutcomeUnknown + Synced=ReconcileError, every pass
> ```
>
> **Only a human clears it**, by recording the identifier in status — and **deleting the CR orphans the
> external resource**, which is the action an operator reaches for first. Once the create marker is cleared,
> Observe finally runs and replaces the condition with `NoIdentifierRecorded`, whose message carries the
> do-not-delete warning. Both conditions appear, in that order.
>
> **This is the fix working, and the trade is not free.** Before v1.22.0 these kinds were RE-CREATED, so
> 0.75.0 trades a silent duplicate for a loud stuck resource. `exists.go:137` chooses that deliberately:
> *"NOT false: a kind with no get and no findby is unobservable, and reporting 'definitely absent' for it
> would let recovery recreate on every grace-period expiry."*
>
> Found by **github-provider-kog**, who asked what their Issue, RepoDeployment and Release kinds would do
> before documenting the reason — all three are exactly this shape.
>
> **A findby makes such a kind self-healing ONLY IF its key is one the vendor keeps UNIQUE**, and this
> correction is theirs: an earlier draft of this entry said "any list endpoint plus `identifiers` the vendor
> echoes". That is wrong, and wrong in the dangerous direction. GitHub does not make issue titles unique,
> nor a deployment's `ref`/`environment`, nor a draft release's `tag_name` — so a findby keyed on any of
> them lets recovery **adopt someone else's object**. That trades a stuck CR for a CR confidently managing
> the wrong external resource, which is worse than the state it was trying to fix. `identifiersMatchPolicy:
> AND` does not rescue it either: an AND over non-unique fields is still non-unique.
>
> So the remedy has a precondition, and without it the remedy is a worse defect than the problem.

> `Note (2026-11-16)` — **what a failed child does beyond the status, as far as its owner can see.**
> core-provider reports the verdict feeding only the parent's own `Ready` write — `Ready=False`/
> `Unavailable`, with the message naming the child — and its coverage counts: no requeue, retry policy,
> event or helm action is keyed on it. `Unavailable` is itself `failed`, so it propagates up a composition
> tree as status.
>
> **What nobody has verified, and it is where new red could do more than display:** consumers of
> `Ready=False` *outside* core-provider — the installer's gates, a blueprint's `deps:` ordering, the
> portal. The installer has classified `CreateOutcomeUnknown` as failed since 2.13.103.

### What the conditions look like, on BOTH objects, because they are opposite

An operator looking at this will see one pair on the resource and the inverse on its composition. Named
explicitly here because two sessions crossed this exact object boundary while establishing it:

```
the CHILD        (the generated kind's own CR, written by unstructured-runtime)
  Ready   False  CreateOutcomeUnknown
  Synced  False  ReconcileError

the COMPOSITION  (written by core-provider's child-health machinery)
  Ready   False  Unavailable           <- names the child in its message
  Synced  True   ReconcileSuccess
```

**`Synced=True` with `Ready=False` on the composition is the EXPECTED shape here, not a contradiction to
chase.** A child failing does not fail the pass: the composition still completes its reconcile, so it
reports success for the pass and unavailability for the child. core-provider establishes that the cdc
writes `Synced` in exactly two places, both the graceful pause, and that all seven arms of its ready
derivation go through one writer typed `TypeReady` — so **no child-health verdict can reach a
composition's `Synced` at all**.

The installer session said they would have treated that pair as suspicious and gone looking for a stale
write. That is the cost of not writing it down.

**The reason string is exactly `CreateOutcomeUnknown`**, and it is unstructured-runtime's own constant —
core-provider imports it rather than re-typing it, so the two cannot drift apart.

### What this release does NOT change, asked for by name

**Nothing about the oas-version backfill.** Asked explicitly by the installer session, because 057 now has
a verified 0.74.0 baseline for it — 28 unlabelled Configuration objects to 0, with three independent
non-github controls holding their labels — and a release that moved it would invalidate that baseline.

Measured rather than asserted, with a positive control so a zero reads as absence and not as a bad path:

```
git diff --name-only 0.74.0..origin/main          6 files, ALL in rest-dynamic-controller
                                                  plus docs/log.md
backfillrbac.go, clusterrole-backfill.yaml        present at BOTH refs (git cat-file -e)
git diff 0.74.0..origin/main -- those paths       empty
```

**And the first-pass backfill Warnings do NOT recur on an 0.74.0 → 0.75.0 upgrade.** They come from the
apiserver authorizer's RBAC cache lagging a *newly created* grant — `SyncBackfillRBAC` says so in its own
comment — and on this upgrade the ClusterRoles already exist with byte-identical content, so `kubecli.Apply`
changes nothing and there is no new grant for the authorizer to lag on. **Stated as a prediction with its
mechanism rather than as a measurement**, because nobody has rolled it yet: it is falsified by a single
`forbidden … cannot patch` event on a cluster already running 0.74.0, and the installer session is the one
who would see it.

**In fact no code in the oasgen-provider module changed at all.** Every change in this release is in
rest-dynamic-controller; the provider image differs from 0.74.0's only by its version stamp. The generated
CRDs, the aggregated-ClusterRole grant, the MutatingAdmissionPolicy and the render service are byte-identical.

**A correction to 0.74.0's entry travels in this release**, not a code change: the unmodelled-key warning
is reported from resyncs rather than on the write it warns about, and compares one level deep. Both are
written into that section.

## 2026-11-16 — 0.74.0

**This release is not what it was scoped to be, and the reason is worth stating first.** 0.74.0 was
scoped around #400 — a spec field holding a map or array is replayed to the vendor whole, deleting
server-held keys the CR cannot express. It ships **#400's detection half and not its merge**: a live
failure on krateo-057 (#717) took priority, and two peer-found defects arrived with measurements
attached. The merge — a declaration, a fresh read before the update, a per-key merge — is next.

`git log 0.73.0..0.74.0` holds **nine** commits: **#721** (the live one), **#725** (#400's first half),
**#722** (issue 718), **#727** (an instrument), **#720**, **#726** and **#728** (contracts and targets),
**#716**, which belongs to this range rather than 0.73.0's — and the PR carrying this section.

> `Note (2026-11-16)` — that count was written before the last two merged and **verified against
> `git log` immediately before this section was cut**, which is the check that found the error corrected
> in 0.73.0's entry below. Writing the list from memory is how the wrong number got there; writing it
> from memory and then not re-reading it is how it shipped.

### oasgen-provider could read every generated kind and write none of them (#717, fixed by #721)

The provider's own ClusterRole ends with `apiGroups: ["*"], resources: ['*'], verbs: [get, list, watch]`
— read-only on purpose, because a generated kind's API group comes from each RestDefinition's
`spec.resourceGroup` and cannot be enumerated in a release. Every other rule names a fixed group. **So
the oas-version backfill, whose whole job is a PATCH, was refused on every generated kind.**

Measured on krateo-057: **28 Configuration objects with no `krateo.io/oas-version` label**, failing every
pass since **2026-11-10 08:13Z**. Zero bare resource kinds affected — the backfill selects on label
ABSENCE and the admission policy stamps on WRITE, so a resource instance is written often and already
labelled, while a Configuration written once at install is permanently selected and permanently refused.
**The missing `patch` was cluster-wide; the unlabelled Configuration is only what exposed it.**

The grant now arrives at runtime by **ClusterRole aggregation**. The provider publishes a labelled
ClusterRole carrying `patch` on exactly the two resources one RestDefinition generates; the chart ships
the aggregated role and **the only binding**. Three properties decided it: the provider never names a
SUBJECT, so no defect here can grant permissions to another ServiceAccount; it needs no way to discover
its own identity, which would otherwise mean a chart-set env var or a `SelfSubjectReview`; and the binding
stays auditable in the release.

**Provisioned on the pass that uses it**, which is the actual lesson. #180 widened this backfill from one
kind to two so it would agree with the admission policy's `resources: ["*"]`, and **the grant was not
widened with it** — two files, one invariant, and no test that could see the disagreement.

> `Runtime verification (2026-11-16)` — **expect UP TO one Warning per generated kind on the first pass
> after upgrading into this release, and expect them to clear themselves.** Measured on 057 by the
> installer session across 28 kinds:
>
> ```
> 21 new `forbidden ... cannot patch` events, each within 2 minutes of its own
>    oasgen-backfill-<kind> ClusterRole being created
> every one self-clearing; nothing emitted after T+2m
> 7 of 28 kinds did not race at all
> ```
>
> The paragraph above already says a first-pass refusal is expected, which is why nobody on 057 treated
> these as a fault. **The COUNT is the new information:** someone upgrading a cluster with 32 generated
> kinds should expect a number of this ORDER, not one. A warning per kind looks like a systemic failure if
> you are not told to expect it.
>
> **"Up to", not "one each", and the bound matters in the reassuring direction.** The measured ratio is
> 21 of 28 — a quarter of the kinds did not race at all, because the grant can land before that kind's
> first backfill attempt. **A cluster seeing fewer warnings than it has kinds has lost nothing**, and
> someone told to expect exactly one each would go looking for the missing ones. Corrected on the
> installer session's reading: this entry's own data block said 21 of 28 while its prose said one per
> kind, and nobody compares a number to the sentence above it.

> `Note (2026-11-16)` — the grant is `patch` only. Not `update`: the backfill issues a merge patch carrying
> one label, and `update` would authorise a whole-object replacement this code never performs. Not `get` or
> `list` either, though it lists — those are already covered cluster-wide, and restating them would make
> this role look like the authority for a read it does not gate. **A grant that duplicates a wider one
> reads as a control while restricting nothing**, which is the mistake 0.54.0's blanket `secrets` rule made.

> `Note (2026-11-16)` — a **latent** chart defect was fixed on the way: `clusterrolebinding.yaml` named
> `fullname` as its subject where the Deployment uses the `serviceAccountName` helper. Equal with the
> shipped values, so nothing was broken — but the installer session confirmed their schema **can** set
> `serviceAccount.name` and `serviceAccount.create`, either of which would have the pod running as one
> ServiceAccount with every grant bound to another, and **every operation would 403**. Found because the
> new binding had to pick a subject and could not reconcile the two spellings.

> `Correction (2026-11-16)` — this entry first carried the figure **8,768 events in 24h**, then **52,375
> over six days**, and described the latter as a denial of the event stream. **Both were records, not
> Events.** Measured: **28 live Event objects** with `count` 314–408, ~196 Event generations over six days
> as the apiserver's TTL deleted and recreated them, and ~52,250 recorder emissions folded into 28 objects
> at a time. Aggregation works exactly as this repo's own comment claims. The honest harm is "an unreadable
> diagnostic whose count climbs ~6/min until TTL", and the cost lands in a telemetry pipeline rather than
> in etcd. `otel_logs k8s-events` is a **view** of Event objects; the record count was read as a claim about
> the apiserver.

**The second half of #717 is not in this release.** A permanent 403 that cannot clear without a human is
still re-reported every pass. It belongs in a condition an operator reads once, with the Event on
transition.

### Warn before an update deletes vendor keys the CR cannot express (#400, half of it, #725)

`processFields` assembles the request body by top-level spec key — `mapBody[field] = value` — so a CR
modelling PART of a map enforces that partial view as the WHOLE object. #400's own framing is why this
half exists on its own: *"nothing distinguishes 'this map is the complete desired state' from 'this map is
a partial view', and **nothing says anything when a key present on the server is about to be removed**."*
The merge fixes the first clause for an author who declares it. **This fixes the second for everyone,
including the author who does not yet know the problem exists — which is every author today.**

Measured by `openstack-ironic-operator-kog` on a live cluster: a sidecar mirrored two of the four keys a
bare-metal node publishes into Ironic's `node.extra`, RDC sent those two as the whole `extra`, and
`etcd_ip` and `cp_endpoint` were deleted. **82 seconds, across 6 PATCHes** — an enforcement loop, not a
race. `cp_endpoint` was a control-plane VIP, so every replica rendered `curl -k https:///healthz`, a URL
with no host, polled it 120 times and exited 1, while Ironic reported `active` for all six machines. The
controlled arm is what makes it attributable: blade01 had a non-empty `spec.extra` and lost keys; blades
02 and 03 had none, were sent no `extra`, and kept theirs.

**Detected at Observe**, where the vendor's body is already in hand — the response the drift comparison
just read — so it costs no extra request on any kind. **Reported whether or not the comparison found
drift**, because the deletion happens on ANY update and the informer's spec-change path never reaches that
comparison: gating on drift would be silent for exactly the author who edits a partial map and triggers
the write themselves. **Scoped to the update verb's body**, because a field the update does not send cannot
be deleted by it, and a false positive in a warning about data loss teaches the reader to ignore the real
one.

> `Correction (2026-11-16)` — **this entry describes the warning as stronger than it is, in two ways, both
> measured on this release within hours of it shipping.**
>
> **It never fires on the update an author's own edit triggers (#734).** The report runs in Observe, and a
> spec edit reaches `handleUpdate` without one: `cmp.Diff(newSpec, oldSpec)` non-empty enqueues `CRUpdated`,
> and that handler calls the external Update directly. Measured by `github-provider-kog` on released
> 0.74.0: their `events` edit dispatched `update` with the PATCH and **no GET before it**, so no report ran
> for that generation and **the PATCH that removed the secret went out unwarned**. The warning they DID see
> existed only because a resync landed between the out-of-band change and the edit — so with the default
> 180s resync there is a window up to three minutes wide, and it is exactly the window an author works in.
> **The honest statement is that this warns from RESYNCS, not on the write it is warning about.**
>
> **And it compares only one level deep (#732).** A key present in the vendor's object and absent from the
> CR's is reported only when both sit directly under the named top-level field. Both measured losses behind
> issue 400 are depth 1 — Ironic's `spec.extra` keys and github's `spec.config.secret` — which is why the
> shallow comparison covered them and the limit was invisible. **No consumer has yet demonstrated a depth-2
> loss**, so this is a coverage gap with no known instance rather than a reported failure; an earlier
> write-up of mine invented an example for it and attributed it to the session that had only asked a
> question, and that is corrected on #732.
>
> **Why both corrections are here rather than only in the issues:** the text above is what an operator
> reads, and a silence from this warning is exactly what they would otherwise take as "nothing is being
> lost". The property that IS measured and does hold is the one in the paragraph above — reported whether or
> not the comparison found drift, with **zero requests on the wire** while reporting, confirmed live.

**An Event, deliberately not a `Ready` condition.** A reason a consumer does not map falls to
core-provider's `default: childConverging` and holds every parent at `Ready=False/Creating` for as long as
the state lasts — and this state lasts until a human acts. `AuthorizationDenied` and `DriftNotConverging`
each wedged parents that way, and those were states that at least WERE unhealthy. This one is not: the
resource reconciles correctly and the DECLARATION is incomplete. **Once per (generation, finding)**, which
is #717's lesson applied from the start rather than after a storm.

> `Note (2026-11-16)` — **maps only, and the omission is a decision.** An array is replayed whole too and
> can lose elements, but naming WHICH element needs a notion of element identity, and this repo has three
> in flight: positional comparison, set-typed arrays, and map-list keys. Choosing one inside a warning
> would quietly establish it for the merge as well. Arrays get their own decision, and #725 must not be
> read as covering them. `github-provider-kog` asked for that to be said in their words: the live array gap
> is `dismissal_restriction` and `required_reviewers` inside a `pull_request` rule's `parameters` (nested
> actor unions), **not** the "14 of 23 rule types" this issue's body still records — since 2026-11-04 every
> ruleset rule type GitHub accepts is modelled, 25 repo-level and 21 org-level.

> `Runtime verification (2026-11-16)` — a **second vendor, and a worse outcome than the Ironic one**,
> measured live by `github-provider-kog`. A `PATCH /repos/{o}/{r}/hooks/{id}` whose `config` carried
> `{url, content_type, insecure_ssl}` and no `secret` **cleared the HMAC secret** — absent from both the
> response and a fresh GET — while the control, a PATCH of `active` only, kept it as `********`. Ironic lost
> availability, loudly and traceably. **This loses integrity, silently: webhook signature verification
> turns off and there is no observable difference until something downstream accepts a forged payload.**
> #725 does report it: the scope check applies to the top-level key (`config`), and the inner keys come from
> the vendor's response versus the CR, so an update schema that deliberately omits `secret` does not hide
> it. **The warning disappears once the secret is already gone**, which is correct — it warns before the
> deletion — but it means #725 cannot detect a loss that has already happened.

### A dispatch verb's `requestTransform` was accepted, plumbed to the call, and dropped (#718, fixed by #722)

`callInfo.RequestTransform` is populated for EVERY action, and Create, Update and Delete each applied it in
place. `dispatch` — a hand-built call path next to those three — did only the first half. So a
`requestTransform` on an `action: dispatch` verb was parsed, validated, admitted, carried all the way to
the call, and then nobody read it. No dispatch test mentioned it.

**The failure is silent, which is why it is worse than a 422.** A dispatch's correlation token is the only
handle on what the invocation created. Where the token is injected BY the transform, an untransformed body
can still be ACCEPTED: the vendor record exists, carries no token, can never be correlated, and the CR
reports `DispatchedButNotObserved` with no error at any layer. **A real record is stranded and nothing
says so.**

Build-and-transform are now ONE step in one helper, used by all four body-sending callers. Delete's single
standalone transform after both its build and its rebuild is gone: both build points transform now.

> `Note (2026-11-16)` — **the enumeration found the fix complete, which was measured rather than assumed.**
> Every `builder.BuildCallConfig` call site in the package was listed against the contract rather than
> against the symptom: ten in all, and the nine others are `apiaction.Get` or `apiaction.FindBy`, every one
> passing `nil` for `resolved`. A read assembles no body, so dispatch was genuinely the only body-sending
> path that bypassed the transform. A shape test now requires every non-helper call site to be enumerated
> with a reason, so a new one fails the build until someone justifies it.

**Not fixed, and deliberately not papered over:** request-direction `fieldMapping` resolvers — `secretRef`
among them — still do not run on a dispatch. It is not a line of wiring: Create answers an unresolvable
resolver with a `PendingReferent` CreateResult, and `createDispatch` returns `(bool, error)` with no way to
express "wait, this is not failed". **#718 stays open for it, and it must end in either resolvers working
there or admission REFUSING a request-direction resolver on `action: dispatch`** — never in a declaration
that is accepted and ignored.

### The external request timeout is settable (#727)

Every CRUD and dispatch call site constructs a bare `&http.Client{}`, so all of them took a hardcoded 30s
inside `restclient.do()`. There was no flag, no env var and no field.

That mattered for a reason that is not tuning: **a consumer wanting to measure what happens when a vendor
COMMITS a create and the response never arrives had no way to produce that state** without inventing a
proxy or a sidecar. `REST_CONTROLLER_REQUEST_TIMEOUT` (or `--request-timeout`) now sets the default, which
remains 30s. **A caller's own `Timeout` still wins**, which is why it is a default and not a cap:
`async.RequestTimeout` and `lookupRequestTimeout` are set deliberately by their call sites.

> `Note (2026-11-16)` — **lowering it is a test instrument, not a tuning knob**, and the chart's `rdc.env`
> entry says so. A value below a vendor's write latency makes the controller abandon a request the vendor
> may already have committed, which is precisely how a duplicate external resource is made. The installer
> session confirmed their schema materialises **no default** for anything under `rdc.env`, so it arrives
> settable-but-unset.

### Every reason we publish has a consumer version floor (#720)

`AssumedUpToDate`'s doc comment explained why its status must stay `True` but not that the behaviour
requires a consumer version. Bisected over core-provider's tags by `openstack-ironic-operator-kog`, with
`childHealthy` present at every tag as a control, and confirmed by core-provider from their own code:

```
TerminalByDesign   mapped from core-provider 2.13.99     (not yet produced here)
AssumedUpToDate    mapped from core-provider 2.13.104    (produced here since 0.73.0)
```

Below a floor the reason is unmatched and falls to `default: childConverging`. For `AssumedUpToDate` that
means a cluster under 2.13.104 reads healthy through the generic `status == "True"` arm — **exactly as the
`condition.Available()` that 0.73.0's #712 replaced.** So #712 changes what the CR SAYS and nothing about
what any parent CONCLUDES there.

> `Note (2026-11-16)` — how that stayed invisible is the reusable part, and it is the reporting session's
> framing: every repo verified its own half at its own version, and core-provider's arm reached a third repo
> as a **line number**. **A line number carries no version.** Three repos agreed on a contract none of them
> had tested together, and each one's evidence was sound in isolation. For `TerminalByDesign` specifically,
> below 2.13.99 the verdict is indistinguishable from the `Pending` it would replace — so, in their words,
> *"the regression is not in the conditions, it is in the confidence"*.

### `use:` moves to 0.73.0, and what the gate does and does not read (#726, #728)

The previous entry set the bar and refused to name 0.73.0 until someone had run it. Four independent arms
did: `azuredevops-provider-kog`'s 194/194 12-suite regression (the comparable arm — same suite and cluster
shape that gave 0.72.0 its 194/194), `github-provider-kog`'s live `pageNumber` walk plus a 32-kind render
diff at zero with a planted positive control, `openstack-ironic-operator-kog`'s arm64 run where pods
started and did work, and `acmp`'s GKE e2e. **What they do not establish is recorded beside them**, because
"exercised" is not one claim.

#728 records the enforcement mechanism's measured reach and, in the `azuredevops-provider-kog` session's
words, **when a consumer gate actually reads this file**: when it RUNS, not when the consumer RELEASES. A
version retracted after a commit's lint can still be released from that commit — observed today, with a
five-minute gap. And a passing pin never reads `use:` at all; only a refused version's message shows it.

> `Correction (2026-11-16)` — recorded in `retracted-versions.yaml` as well, because that is where it would
> have become fact: I told that session their chart 0.11.0 was "the first pin anywhere to go through a
> consumer-side gate at a `use:` target that had just moved". **It was not** — their gate ran five minutes
> before the repoint merged, and a passing pin never evaluates `use:`, so the claim was impossible rather
> than early. It was flattering to this repo and arrived in writing from this repo's own author, which is
> the kind nobody checks.

### Known at ship time, and not fixed here

> `Known issue (2026-11-16)` — **a create whose response is lost is re-issued, up to six times, with no
> Observe between.** Measured by `acmp` against a fake that stores the request and then answers 500: six
> POSTs, then a CR reading `correlation is ambiguous: 6 items … echo the token`. The cause is in
> unstructured-runtime v1.18.0, which this release pins: a failed create re-enters as the same `Create`
> event, and the incomplete-create recovery in `processItem` is gated by `meta.ExternalCreateIncomplete`,
> which is true only while `pending` is the NEWEST tracking marker — so stamping `failed` turns it off **by
> recency**. core-provider has confirmed it is **not** closed at v1.21.1 and is fixing it; the shape Diego
> decided is that a handler may classify an error as definitely-not-committed, and everything else leaves
> `pending` newest so an existence check runs before any second POST. **Any non-idempotent create has this
> exposure on any 5xx, timeout or reset after the vendor committed** — `azuredevops-provider-kog` confirmed
> three of their kinds accept an identical POST twice. The RDC-side classification is ours and is not in
> this release.

> `Known issue (2026-11-16, issue 724)` — **the stuck-latch Warning fires forever, by design, on a kind
> whose findby can never match**, and its remedy advice is wrong for exactly that population: it names
> `responseTransform` and `spec.resource.identifiers`, both of which are already correct for such a kind.
> Measured on arm64 at 0.73.0 over 6.4 resync periods. It recurs per generation, not once per CR, because
> the counter's signature is `gen=%d;<kind>`. The fix is for `restsAtNotReady` to suppress it — keyed on the
> DECLARATION, never inferred from the latch, since inferring it would delete the detector for the
> population it exists for.

## 2026-11-16 — 0.73.0

**A dispatch kind's correlation observe read exactly one page.** That was a correctness problem rather
than a scale one: an invocation whose echo had fallen past page one was reported ABSENT, and on a
dispatch kind not-found is what makes the controller fire the invocation again. A collection longer
than one page could re-run something that had already happened.

Scoped ahead of #702 and #690 because it unblocks a plugin removal already decided: `zendesk-kog`'s
`ZendeskRequest` was inexpressible without it.

`git log 0.72.0..0.73.0` holds seven commits: **#704** and **#710** (the work), **#712** (issue 709),
**#711** and **#713** (tests), **#714** (the retraction targets) and **#716**, this section.

> `Correction (2026-11-16)` — **the seventh commit is #715, not #716, and #716 is not in that range at
> all.** `git log 0.72.0..0.73.0` is: #715 (this section), #714, #711, #713, #712, #710, #704. #716 merged
> AFTER the tag and is the first commit of 0.74.0's range -- it is the PR that added the tag-cleanup note
> below, which is presumably how the two numbers changed places. Found while assembling 0.74.0's commit
> list from `git log`, which is the only reason it was found: the sentence is self-consistent and names a
> real PR about this very section.

### The correlation observe paginates, through the walk findby already uses (#710)

The paginated loop is now `restclient.WalkPaginated`, shared by both callers rather than copied. The
loop's own history is the argument: it has needed a per-strategy author bound, a 1000-page hard
backstop, and a three-valued verdict whose arms must never collapse — **only `Exhausted` may become a
404, because the reconciler CREATES on 404.** Each was a defect before it was a feature, so a second
copy would be a second place for every one of them to drift.

What varies is parameterised: the verb whose `responseTransform` normalizes items, the predicate, and a
label so the backstop error names its caller.

> `Note (2026-11-16)` — that is also how the per-item transform arrived, which was the second thing
> `acmp` asked for. `normalizeItems` is a STEP of the walk, so correlation gets it by using the walk
> rather than by a second implementation. It matters because `echoedAt` is a dot-path and cannot select
> an array element by a field value: a token carried in a custom-fields list was unreachable without
> reshaping the item first.

`correlation.observe` gains `pagination` and `responseTransform`. **A synthesised 404 is the only error
that becomes not-found**; everything else, including `Indeterminate` — the walk stopping early —
propagates, because this is the verb where not-found means invoke again.

### `maxPages` and `hasMorePath` on `continuationToken` (#710)

`maxPages` is **optional, and absent means unbounded** — deliberately unlike `pageNumber`'s required
one. The two strategies have different end signals: this one's default rule is token ABSENCE, which
terminates a walk on its own.

> `Note (2026-11-16)` — required was tried and **reverted before release**, and the reason should
> survive the decision. `azuredevops-provider-kog`'s census found **4 live `continuationToken` findby
> kinds and 0 `pageNumber` kinds**, none setting `maxPages`, on every cluster running their chart
> 0.6.0–0.8.0. Required would have rejected an unchanged chart's apply. A default instead would have
> capped walks that work unbounded today — turning an admission failure into a silent runtime one that
> appears only on large tenants.
>
> This file had stated the blast radius was measured-empty, on the strength of ONE cluster's census.
> The second cluster asked had four kinds and five suites.

`hasMorePath` is a terminator for an API whose cursor does **not** disappear on the last page. Zendesk
documents exactly one end signal — repeat *"until the `meta[has_more]` property is false"* — and says
nothing about whether `after_cursor` is present, null or absent there. Under the absence rule such a
page says "more pages", so the walk runs to the bound on every call.

Measured against the real shapes rather than invented ones (#704): `acmp`'s fixture enumerates all
three possible final pages rather than guessing, and the cursor-**present** row is `Exhausted` with
`hasMorePath` and `MorePages` without it — the defect next to its fix.

### `Ready=True` now says when it was ASSERTED rather than observed (#712, issue 709)

A kind declaring **neither `get` nor `findby`** cannot be looked up, so Observe asserted success:
`Ready=True/Available`, with the assumption only in the message. The handler returns no `Synced`, so
the runtime stamps `ReconcileSuccess`. **The CR read `Ready=True/Available` + `Synced=ReconcileSuccess`
whether or not its create ever landed.**

`core-provider` measured the consequence: their rollup reads a child's `Ready=True` as `childHealthy`,
so a parent Composition read healthy over an assertion — and, in their words, *"the rollup can't tell
an asserted True from an observed one"*.

The reason is now **`AssumedUpToDate`**. The STATUS stays `True`:

> `Note (2026-11-16)` — `Unknown` is arguably more honest, and would be worse. `krateoReady` keys
> Unknown on the REASON, so an Unknown it does not recognise falls to its default and holds every
> parent at `Ready=False/Creating` for as long as the assumed path lasts. That swaps a false-healthy
> for a permanent false-converging: the first is wrong and quiet, the second is wrong and looks like a
> stuck deploy. core-provider found this by re-reading their own code after telling me the opposite.

Two things deliberately unchanged: the **return value**, because reporting `exists: false` would
dispatch a create on every reconcile for a kind that can never be observed; and the other
assumed-up-to-date site (a nil response body), which is a different claim — the call was made and
existence WAS observed, only the content was empty.

> `Known issue (2026-11-16, issue 709)` — this makes the state **distinguishable**, not healthy. Until
> a consumer keys on the reason, such a kind still reports as healthy. core-provider's PR #538 maps
> `True` + `AssumedUpToDate` to `childUnevaluated`; their arm matches status `True` ONLY.

### The retraction targets move to 0.72.0 (#714)

All three entries — 0.70.0, 0.69.0, 0.66.0 — now say `use: 0.72.0`.

> `Note (2026-11-16)` — the previous value had a reason and so does this one, and the file records both.
> `use:` names the most-VERIFIED safe version, not the newest. On 2026-11-15 that was 0.71.0, which had
> three consumers' released-artifact verdicts while 0.72.0 had a five-artifact supply-chain sweep and
> nothing else — **published is not exercised.** `azuredevops-provider-kog` then ran a full 12-suite
> regression on 0.72.0 (194/194, the `rdc.env` flip rolling 25/25 controllers both ways, #588 causing 0
> restarts) plus the write-the-id remediation arm with an untouched control. **Expect it to move again:**
> 0.73.0 is unexercised on arrival and should not be named until someone has run it.

### A rule-count ledger for the CRD's CEL cost (#711)

The schema-wide CEL cost budget already had a test — `TestGeneratedCRDIsWithinCELCostBudget`, since
#239, which runs the shipped CRD through the apiserver's own validator and refuses an over-costed
schema. The ledger added here answers the different question it cannot: whether the cost **moved** while
still fitting.

> `Note (2026-11-16)` — the motivating case is #710 itself, which spent **eight** rules while adding no
> validation of its own, purely by embedding `Pagination` and `JQProgram` under `correlation.observe`.
> Reusing a Go type is free; re-instantiating it in a CRD schema is not, and "does it fit" said yes
> correctly. The ledger's own first run caught 70/33 → 78/37.
>
> Two sessions measured the headroom on real apiservers before this landed — `installer` on GKE
> v1.36.4 and `core-provider` under envtest — and both reported the apiserver naming a rule **index**
> that was already there and fine. So a cost rejection reads as a pre-existing CRD problem rather than
> as the newest addition, and the PR that spent the headroom is not the one that goes red.

### The shared walk's arms, and two bounds that nothing covered (#713)

> `Note (2026-11-16)` — after the extraction the package was green and 15 tests matching
> `FindBy|Paginat` ran, and **two mutations of the moved code both passed**: collapsing the `Exhausted`
> arm into a plain error, and plumbing a nil verb into `normalizeItems`. `TestFindBy`'s subtests
> declare no pagination, so they take `CallFindBySingle` and never enter the loop; the header-token test
> matches on page one and returns before the end-of-collection arm. So "21 packages ok" was never
> evidence the refactor preserved behaviour.

Every live suite named as cover for the refactor is `continuationToken`, because azuredevops has no
`pageNumber` kinds — a gap `github-provider-kog` pointed out, who have three live `pageNumber` findby
kinds. #713 covers the `pageNumber` arms, and the **1000-page `MaxFindByPages` backstop**, which
mutated cleanly with no test noticing.

> `Note (2026-11-16)` — the backstop is **unreachable via `pageNumber` by construction**. The loop
> checks it BEFORE fetching and the strategy bound AFTER, so at equal values the strategy one fires
> first, and `NewPaginator` refuses a `pageNumber` `maxPages` above the backstop. It is reachable
> exactly where the code says: a `continuationToken` walk with no author bound — which is also the live
> shape, four azuredevops kinds being precisely that.

> `Note (2026-11-16)` — **the 0.73.0 tag's own message is missing three lines, and this section is where
> the content survives.** `git tag -a -F <file>` applies git's default message cleanup, which strips every
> line beginning with `#`. The message was written with `#710`, `#712` and `#714` as paragraph leads, so
> those three lines were silently removed: two paragraphs now begin mid-sentence and #714's entry is gone
> entirely. The PR numbers still appear in the message's first line, so the references are not lost, but
> the structure is.
>
> It was not re-cut. The release workflows had already started from the tag, and deleting a pushed tag to
> repair prose would risk the release to improve a message — the content is here, where it is editable.
>
> What the stripped lines said: **#710** — a dispatch kind's correlation observe read exactly one page,
> which was a correctness problem rather than a scale one. **#712** — a kind declaring neither get nor
> findby asserted `Ready=True/Available` with the assumption only in the message. **#714** — the
> retraction targets move to `use: 0.72.0`, now that 0.72.0 is exercised.
>
> Use `--cleanup=verbatim` for any tag or commit message carrying `#NNN` at the start of a line.

## 2026-11-15 — 0.72.0

**`excludedSpecFields` is no longer immutable, and the three releases that duplicate external resources
are retracted.** No change to the create path: 0.71.0's fix stands untouched, and the released-artifact
verdicts on it carry forward.

`git log 0.71.0..0.72.0` holds three commits: **#699**, **#700** and **#701**.

### excludedSpecFields becomes mutable (#666, #701)

It carried `self == oldSelf`, which made a blocklist **unrecoverable**. A wide vendor schema inverts
badly — Keycloak's `RealmRepresentation` has 152 fields, so "I want 30 of them" means naming ~122
exclusions — and one wrong entry could only be undone by deleting the RestDefinition and with it every
CR of that kind.

**Append-only would not have helped**, which is why this is fully mutable: the recovery an author needs
is *removing* an entry they should never have added, and append-only forbids exactly that.

A design note warned that a newly mutable input could be edited with nothing happening — the apply
accepted, the CRD unchanged. Both halves were checked rather than assumed: `ExcludedSpecFields` is a
field of `type Resource struct`, which is `spec.resource`, and `resourceHash` is sha256 over the
**whole** marshalled struct. So an edit is drift on the next Observe and Update re-renders.

> `Note (2026-11-15)` — that is the property that makes this field safe to unfreeze, and the one to
> re-check before unfreezing anything else in `spec.resource`. It is not a general guarantee: it holds
> because the hash covers the entire struct, deliberately, per its own comment.

Adding an entry prunes a property the live CRD may already be serving, and any CR that still sets it
will be rejected on its next apply. Generation now names the field in `status.schemaWarnings` and as a
Warning event. It **warns rather than refuses**: the apiserver prunes an undeclared field from stored
CRs without consulting this provider, so refusing would protect nothing already at risk.

The check walks paths **by segment** rather than comparing dot-joined strings, because
`excludedSpecFields` escapes a dot inside a single field name bracket-style (`'["searchCriteria.title"]'`)
— joining segments back with `.` makes a one-segment path containing a dot indistinguishable from a
two-segment nested path, and a false match there would name the wrong field. It reuses
`pathparsing.ParsePath`, the same parser the pruner uses, so the warning and the pruning cannot disagree
about what a path means.

The counts in `docs/api.md` moved with it: **four strict and two append-only, six rules in total**, from
five and seven. The contract test demanded that page change in the same commit and failed until it did.

> `Known issue (2026-11-15, issue 702)` — an `excludedSpecFields` entry naming a parameter that a
> request-direction `fieldMapping` already supplies has a **second function nothing documents**: it
> suppresses the #484 overlap warning. Until this release the field was immutable, so nobody could
> remove an entry and discover that. Measured by `github-provider-kog` across 24 entries of their chart
> on 0.71.0 `oasgen-render`, each rendered with and without itself: **17** produced a byte-identical CRD
> but emitted the overlap warning once removed, and **7** were load-bearing — the CRD gained the field —
> which is also the control proving the harness detects a difference. So removing a "redundant" entry is
> **not** a no-op even when the rendered CRD is unchanged. `docs/api.md` matches `acknowledg|suppress`
> zero times.

> `Note (2026-11-15)` — this file previously said, of the field, that "removing an entry widens the
> spec, which is always safe". That claim is about the **schema**: no stored CR can be invalidated by a
> new optional property appearing. It does not mean the result is wanted — re-exposing a
> server-generated identifier is safe for the apiserver and wrong for the API.

### The generated CRD reference was 641 lines stale (#700)

`docs/restdefinition-crd-reference.md` had fallen 641 lines behind `go/oasgen-provider/crds` across 10
hunks. Whole fields were missing: `serverURL` appeared **nowhere** in the reference that `README.md`,
`docs/api.md` and `docs/index.md` all point readers at.

`apis/generate.go` syncs the generated CRDs to every destination that must carry them, and argues for
exactly that in its own comment:

> "Generating into every destination, rather than listing them in a guard, means the guard cannot fall
> behind the set of destinations: a `git diff` after codegen covers whatever this writes."

The reference was the **fourth** destination and the one that directive did not reach — regenerated by a
command documented only in `docs/development/workflow.md`, so nothing ran it and the drift check never
saw it. Two things made that command unattractive to run: `crdoc --output` overwrites the whole file, so
running it **destroyed** the eight lines of frontmatter the docs site reads, and it ran
`fybrik.io/crdoc@latest`, which makes codegen non-reproducible. The frontmatter is now a checked-in
input the generator prepends, and crdoc is pinned at **v0.6.4** through the tools import, the same way
`controller-gen` already is.

> `Note (2026-11-15)` — the first idempotence result for the new generator was a **false pass**. The
> script had exited 1 (`go:generate` runs a directive with the cwd set to the directory of the file
> holding it, not the module root, so every path resolved one level too high), nothing was regenerated,
> and comparing the unchanged file to itself read as "byte-identical". `serverURL` still being absent is
> what gave it away. A generator's output compared only against itself cannot tell success from a no-op.

### 0.69.0 and 0.70.0 are retracted (#699)

Both carry a **measured** duplicate-create route on any kind declaring no `findby` verb: a create whose
status write loses a race is re-issued on the next reconcile.

| version | evidence |
|---|---|
| **0.70.0** | two consumers independently — a forced-race probe duplicated at the vendor on **3 of 3** trials where the race fired (2 unraced as controls), and a 303 reproduction on real GitHub produced **3 POSTs in 5 minutes** |
| **0.69.0** | the same defect and **strictly worse**: measured at the tags, it carries **both** routes — the no-identifier refusal that feeds #347's retry branch, *and* the absence of the create-marker guard 0.70.0's #693 added |

**0.66.0's `use:` moved from 0.67.0 to 0.71.0.** It was correct when written; 0.67.0 is now known to
carry the lost-write route. An entry must not route a pinning session onto a version its own publisher
knows to be affected. The invariant is now asserted: no entry's `use:` may name a retracted or
known-affected version, and it fails on the old file.

**0.67.0 and 0.68.0 are deliberately not retracted**, and the file records why so the absence reads as a
decision. Both carry the lost-write route **by code reading** — for 0.68.0 the relevant code is
character-identical to 0.69.0, where the duplicate was measured — but nobody has measured one on either:
the consumer who predicted it had their 0.68.0 probe come back **VOID** for an unrelated reason, and a
0.67.0 run measured the silent latch rather than a re-create loop. The rule is to retract what is
*actively harmful*, not what a code reading implicates.

> `Note (2026-11-15)` — so the honest window for a duplicate on a no-`findby` kind is **anything below
> 0.71.0**: 0.66.0–0.69.0 via the lost-write route, 0.69.0–0.70.0 via the refusal route, with 0.69.0 and
> 0.70.0 measured and everything below derived. A fleet that ran any of them may hold orphaned vendor
> objects that **no CR mentions** — the CR reports `Ready=True/Available` carrying the second object's
> id, so only a vendor-side count reveals it. That companion defect is unfixed in every release.

> `Note (2026-11-15)` — `TerminalByDesign` has **no producer**. core-provider's #507 maps it, and
> nothing in this monorepo ever sets it: 0 occurrences at `origin/main`, against a control query that
> returns 4 files for `NoIdentifierRecorded` and a list of the 13 `Reason` constants
> rest-dynamic-controller does define. A consumer mapping a reason is not evidence a producer emits it.

## 2026-11-15 — 0.71.0

**One fix, and it closes the second route into the create loop 0.70.0 left open.** #347's retry branch
read `Synced == ReconcileError` to mean "the create failed, retry it" — a condition **every** error path
sets, including 0.69.0's own no-identifier refusal. So the refusal produced the error the retry branch
read as a failed create, and the pair looped.

`git log 0.70.0..0.71.0` holds two commits: **#697**, the fix, and **#698**, this section.

### The retry branch keys on the create's own outcome (#696, #697)

Measured on 0.70.0 by `github-provider-kog` against real GitHub, using the Info line #693 had just
added — which is the only reason this was visible at all:

```
11:29:00.688  Observe  "Refusing to assume up-to-date: no identifier was ever recorded"
11:29:00.694  ERROR    Cannot observe external resource    -> Synced=False/ReconcileError
11:29:01.725  Observe  "The last reconcile errored, so this resource is not assumed to be up to date"
11:29:01.817  POST     -> 303 -> refused -> Requested:true
```

Three POSTs in five minutes, the same rate as 0.69.0. Verified at tag `0.70.0` by offset inside the
`apiCall == nil` block: `createHasBeenAttempted` (23) answers **true**, correctly — which is *why*
`lastReconcileErrored` (2224) is reached — and the refusal (4066) never runs on the retry pass. **The
two guards do not overlap**, and #693 was not bypassed.

The fix reads `krateo.io/external-create-failed` compared against `external-create-succeeded`:

```go
failed := meta.GetExternalCreateFailed(mg)
if failed.IsZero() { return false }
return failed.After(meta.GetExternalCreateSucceeded(mg))
```

The marker records the **create's** outcome; `Synced` records that *something* errored. #347's measured
case — a create rejected 422 by Zendesk — is still covered, because the runtime stamps
`external-create-failed` on exactly that path, at its single stamp site. The two cases that must **not**
stamp it do not: a status conflict returns before it, and a status write that fails after a landed
create reports `Requested: true`. That pair is precisely why the condition was the wrong channel —
`Synced=ReconcileError` is what they share.

Compared rather than present-checked, because a resource can fail, be fixed and succeed later, and the
failed marker is never removed.

**`lastReconcileErrored` is removed, and its test file is MOVED rather than deleted.** All four
behavioural tests now exercise the new helper, and the AST tripwire pinning the guard's position inside
that branch is re-pointed. **That tripwire fired on this change, which is its job** — its own comment
says a helper that is correct and dead lets every other test in the file pass while asserting nothing.

### Verified on a branch build before tagging

`github-provider-kog` built rest-dynamic-controller locally from the fix's SHA and ran the same harness
against real GitHub, judged against a table written in advance:

| expected | measured |
|---|---|
| 1 POST for the duplicate CR | **1** — and no list GET |
| the refusal still present each resync | yes |
| `"The last reconcile errored…"` gone | **0 occurrences** |
| `Ready=False/NoIdentifierRecorded` persisting | yes |
| the control CR unchanged | `Ready=True/Available` with its id |
| the redirect Warning once | **1**, matching the single create |

This is the first time a fix in this family was exercised **before** its tag rather than after, and it
is why: 0.69.0 and 0.70.0 each shipped a fix that a consumer run then falsified.

### The other route was measured too, and #693 holds under contention

`azuredevops-provider-kog`'s forced-race probe on 0.70.0 answers the question github's run could not:
whether #693's marker fix holds when the status write actually loses a race. Five trials, an 8-second
racer, with trials 1 and 3 as no-race controls:

| trial | vendor objects | race fired | pending-marker | verdict |
|---|---|---|---|---|
| 1 | 1 | no | yes | OK (control) |
| 2 | **2** | yes | yes | DUPLICATED |
| 3 | 1 | no | yes | OK (control) |
| 4 | **2** | yes | yes | DUPLICATED |
| 5 | **2** | yes | yes | DUPLICATED |

Trigger evidence agreed across both channels (log 3, events 3), so the dedup question below did not
arise for them.

**Every duplicate went through the #347 route that #697 fixes**, and #693's route held. Trial 2,
verbatim:

```
12:04:12.078  Create   "The create reached the vendor but its status write failed"  (the race lands)
12:04:57.731  Observe  "Refusing to assume up-to-date: no identifier was ever recorded"   <- #693 HELD: 45s, no re-create
12:04:58.956  Observe  "The last reconcile errored, so this resource is not assumed to be up to date"
12:04:59.371           "Successfully requested creation of external resource"             <- the duplicate
```

`"The last reconcile errored"` appears **3** times, matching the three races — and github measured it at
**0** with #697. So on that corpus #697 is exactly the missing piece. **No third route was observed.**

**One caveat they stated rather than let pass**: `pending-write-conflicts` was **0**, so their racer never
won the marker write and the **withhold ordering was not exercised** in that run. #693's marker read is
measured; the withhold it depends on remains a source reading plus one HOLDS from github's 0.70.0 run.

> **Known issue (2026-11-15): once a duplicate lands, nothing in the CR says so.** On the raced trials the
> CR ends `Ready=True/Available` carrying the **second** object's id, and the first is orphaned at the
> vendor with nothing referencing it. Only a vendor-side count reveals it. Reported by
> `azuredevops-provider-kog` from the same run.
>
> #697 stops the duplicate being created; it does not detect one that already exists, and no release in
> this family does. A fleet that ran 0.66.0 through 0.70.0 on a kind with no `findby` may hold orphans
> that no CR mentions, and the only way to find them is to count at the vendor.

### Known cost: error backoff on a refused resource

A CR in the refused state gets roughly **six Observe passes per three-minute cycle** — the runtime's
error backoff at +0, +1, +3, +7, +15, +31 s — because the refusal returns an error each pass.

**Measured cheap, by the same run**: no HTTP request, and **no resourceVersion change** after the first.
So it is queue work with no vendor traffic and no status churn.

**And it is inherent rather than incidental.** The refusal *must* return an error: returning nil with an
empty observation means "does not exist", which dispatches a create — the defect this family is about —
and returning exists-and-up-to-date is the latch #682 removed. The error return is load-bearing and the
backoff is its price.

`Synced` on such a CR is stable at **`False/ReconcileError`**, not `ReconcileSuccess`. That is honest:
the reconcile genuinely cannot complete. It is **not** a failure signal to key on, and a verdict table
that reads `ReconcileError` as "broken" would report the fixed shape as worse than the duplicating one.

> **Note (2026-11-15): an event count cannot see repeats, so do not use one to measure a cadence.**
> `github-provider-kog` measured `CannotObserveExternalResource` at **count 1, with 1 event object**,
> against **~11 refusals in the same log**. Whatever the batching rule is — unverified — the consequence
> is that the per-resync cadence is **not** observable through events, and the log line is the authority.
>
> Their qualifier is the part worth keeping: *"the redirect Warning's 1 means something only because the
> POST count agrees."* A count of 1 is equally consistent with "fired once" and with "fired eleven times
> and was deduped"; only an independent signal separates them.
>
> This corrects advice I gave four sessions twice over — first that one Warning total was expected, then
> that one reason should climb per resync. Both were wrong, and a detector built on either would have
> reported a correct run as a failure.

## 2026-11-15 — 0.70.0

> **Correction (2026-11-15): this section's fixes were incomplete. #693 closed one route into the create
> loop and a SECOND route stayed open, so the create was still re-issued every resync.** Measured on
> 0.70.0 by two consumers: `github-provider-kog` saw three POSTs in five minutes on their 303
> reproduction, and `azuredevops-provider-kog`'s forced-race probe duplicated at the vendor on every
> trial where the race fired — three of five, with the other two as controls.
>
> **#693 itself holds**, including under contention: after a lost status write the next Observe did not
> re-create, for 45 seconds, with the marker present. What stayed open was #347's retry branch, which
> read `Synced == ReconcileError` — a condition the no-identifier refusal sets by design. Fixed in
> 0.71.0 below as **#697**. Tracked in #696.
>
> **#694 is confirmed working** by the same run: no list GET, the redirect refused, the Warning event
> fired once, and `NoIdentifierRecorded` persisting.


**Two fixes for one defect that 0.69.0 made worse: a create could be issued again, every resync,
silently, and duplicate the external resource.** 0.69.0's #687 was meant to prevent this and instead
added a second route into it — that release's section above now carries the correction.

`git log 0.69.0..0.70.0` holds five commits. Two change behaviour — **#693** and **#694** — and
**#679**, **#691** and **#695** (this section) are documentation. Listed so the range reconciles.

### The guard reads the create MARKERS, not the conditions (#692, #693)

Observe's "nothing has been created yet" test answered from the `Ready` condition:

```go
if !IsConditionSet(mg, Creating()) && !IsConditionSet(mg, Available()) {
    log.Debug(...); log.Debug(...)
    return controller.ExternalObservation{}, nil     // exists=false, err=nil
}
```

`ResourceExists=false` with a **nil error** is the one input `unstructured-runtime`'s `handleObserve`
turns into a fresh `Create` event. **Two measured routes reached it:**

| consumer | route | result |
|---|---|---|
| `azuredevops-provider-kog` | the status write **lost a race**, so `Creating` was never *persisted* | **5 of 5** forced-race trials duplicated at the vendor; extras **orphaned** on CR delete |
| `github-provider-kog` | #687's refusal wrote `NoIdentifierRecorded`, **displacing** `Creating` | 3 POSTs in 5 minutes |

**The fix reads `krateo.io/external-create-{pending,succeeded,failed,not-requested}`.** Those are
**annotations**, written with `tools.Update` against the main resource; conditions go to the **status
subresource** in a different request. github's `managedFields` show the split — `controller` Update owns
the annotations, `controller` Update/status owns the conditions. **So the marker survives exactly the
status-write failure that destroys the conditions**, which is the trigger. The conditions stay as a
second answer, because an adopted resource reaches `Available` with no marker ever set.

It is a named function rather than a fourth negation, because the enumeration is the load-bearing part:
forgetting a reason does not degrade a message, **it recreates an external resource**.

**And the silent line is now `Info`.** Those two `Debug` lines are why this took two consumers and a
source read — at default level the pass that dispatches the create logged nothing at all.

### A redirect that rewrites the method is refused, and such a create is reported as LANDED (#682, #694)

`net/http` rewrites **301, 302 and 303 on a non-GET to a GET** and drops the body, so the response
answers a **different request** than the one made. Measured on real GitHub: a duplicate
`POST .../deployment-branch-policies` answered 303, the followed GET returned
`{"total_count":2,"branch_policies":[...]}`, and that list body was read as the created object —
`clearStatusFields` having already wiped the previous status, with `populateStatusFields` skipping every
path it could not find via a bare `continue`.

**The rule keys on the method, not the status code.** 307 and 308 preserve the method and body and are
deliberately still followed: `CheckRedirect`'s own comment records #132, a renamed vendor repository
served as a 307, where refusing leaves the CR permanently in `ReconcileError`.

**And it is not reported as a create failure.** A 303 "See Other" after a POST means the work is done
and the result is elsewhere — GitHub returns it *because* the policy already exists. So the create
reports `Requested: true` with a Warning event naming the redirect and the remedy. Reporting failure for
a create that landed is #676's mistake in a new place, and with #693 in place the runtime would then
retry it.

### How the three compose

| | |
|---|---|
| #694 | the hop is refused, so no wrong body is read and status is left **untouched** |
| #693 | the marker is seen, so Observe does not report not-exists — **no re-create** |
| #687 | Observe reports `Ready=False/NoIdentifierRecorded`, which is exactly true: the resource exists and this endpoint shape cannot say which one |

**#687's condition becomes durable for the first time.** On 0.69.0 it was written and overwritten about
1.86 seconds later by the create handler — github's watch caught it at `rv=961` — which is why every
sampled count of it read **0**. With no create dispatched it is now the steady state, so a non-zero
count of `Ready=False/NoIdentifierRecorded` is **the fix working**, not a regression: those are CRs that
were previously reporting healthy while unmanaged.

### Also in this release, documentation only

- **#679** adds a reference section for the list-semantics extensions, which had been documented only
  in the changelog.
- **#691** records **who enforces** `retracted-versions.yaml`, because the reach was invisible: a
  version could be retracted, declared correctly, and still shipped with every gate green, since the
  shipping repo's CI never consulted the file. `azuredevops-provider-kog`'s 0.7.0 pinned the retracted
  0.66.0 that way.

### Verification

`go test -race -tags=unit ./...`: 21 packages ok, 0 failures. #693's tests are mutation-verified on
three arms and #694's on three, each expected to fail for a named reason, all six did — including one
that inverts #694's condition so it would refuse #132's 307, and one that leaves the sentinel unwrapped
so `errors.Is` could not reach it through `*url.Error`.

**Two instrument failures of mine were caught during that verification and are worth recording.** A
mutation arm that **removed** a block failed to *build*, which measures nothing — redone so it compiles
and answers wrongly instead. And a test case asserted `Ready=False/Available` when `Available` is
`Ready=`**`True`**, so the test caught my error rather than the code's.

> **Known issue (2026-11-15): neither fix has run on a cluster at the time of tagging.** #693 rests on a
> source reading — the runtime persists `external-create-pending` and **withholds the POST if that write
> conflicts**, its own comment saying *"we _want_ the update to fail if we get a 409 due to a stale
> version"* — corroborated by github's watch showing the marker present across all 18 events, but **not
> measured under contention**. #694 has had no cluster run at all.
>
> This matters more than usual because **0.69.0 is the release whose headline fix a forced-race run then
> falsified.** The verification cannot precede the tag: `azuredevops-provider-kog`'s forced-race probe
> and `github-provider-kog`'s 303 reproduction both need a published version. Their verdicts include
> rows that say *do not pin this*, and github's run will also report whether the marker is persisted
> **before** the first POST — the ordering #693 rests on, which nobody has measured.
>
> Treat 0.70.0 as **published, not exercised**, until those two report.

## 2026-11-14 — 0.69.0

> **Correction (2026-11-15): this section's headline claim is FALSE. #687 does not prevent the duplicate
> it was written for — it CAUSED one.** Measured on 0.69.0 by two consumers independently, neither of
> whom was looking for this:
>
> - `azuredevops-provider-kog` forced a status-write race on a no-`findby` kind: **5 of 5 trials
>   duplicated at the vendor**, extra objects equal to race events exactly, and the extras **orphaned**
>   on CR delete. `NoIdentifierRecorded` appeared **0** times.
> - `github-provider-kog` re-ran their 303 reproduction: **three POSTs in five minutes**, one per
>   resync, ending `Ready=False/Creating`.
>
> **The mechanism.** `NoIdentifierRecorded` is a `Ready` condition, and Ready holds **one** condition —
> so the refusal **displaced** `Creating`. Observe's "nothing has been created yet" guard answered from
> exactly those conditions, read the CR as never-created, and returned `ResourceExists: false` with a
> **nil error**, which is the one input the runtime turns into a fresh `Create`. Both log lines on that
> path were `Debug`, so the pass was silent at default level.
>
> **And the guard predates #687** — the same line is in 0.66.0, 0.68.0 and 0.69.0 — so #687 added a
> second route into an older hole rather than creating it. azuredevops established that; I had
> attributed the whole defect to #687, and a fix for my mechanism alone would not have stopped their
> duplicates. Their path never wrote a Ready condition at all, because the status write that would have
> carried it is the thing that failed.
>
> Fixed in **0.70.0** below, as **#693** (the guard reads the create markers, which survive a failed
> status write) and **#694** (the redirect that caused github's route). Tracked in #692.

> **Correction (2026-11-15): the redirect claim in this section's known-issue block is also false.** It
> said "ten bare `&http.Client{}` instances across four files follow redirects, while `snowplow.go:48`
> refuses them with a documented argument". `restclient.do()` **copies** the caller's client and replaces
> **both** its `Transport` and its `CheckRedirect` with the host policy's, so a per-hop redirect policy
> already existed — those literals are vessels whose `Transport` is never set at any of the 14 sites.
> `hostpolicy`'s own header already listed a redirect as **channel 3** of three measured
> credential-relocation channels, checked at the dial.
>
> I counted `&http.Client{}` occurrences and never read what the call path does with them. The real gap
> was narrower: a redirect to a **permitted** host that **changes the method**, which is what #694 fixes.
>
> **This claim is also in 0.69.0's annotated tag message, where it cannot be corrected.**


**One fix: "I cannot look" is no longer reported as "up to date".** Observe assumed a resource was up to
date, set `Available` and returned `{exists: true, upToDate: true}` **without making any HTTP request**,
whenever no identifier was resolvable and the kind declared no `findby` verb. The CR then read
`Ready=True`/`Available` for as long as it existed, with the external resource unmanaged and spec edits
never reaching the vendor.

`git log 0.68.0..0.69.0` holds four commits. One changes behaviour — **#687** — and three are
documentation: **#685**, **#688** and **#689** (this section). Listed so the range reconciles.

### A create with no identifier is no longer reported healthy (#682, #687)

Measured by `github-provider-kog` on a cluster against real GitHub, with a positive control:

```
DUP  POST -> 303 See Other -> GET Location -> 200 {"total_count":2,"branch_policies":[...]}
     status.id=None, status.name=None
     observed 19:16:11 and 19:19:11 -- "up to date" both times, ZERO HTTP requests
     Ready=True/Available, Synced=True/ReconcileSuccess, NO Observed condition
CTRL POST -> 200 -> status.id=66732937, Observed=True/ResponseObtained   (control passed)
```

GitHub held the policy; the CR held nothing.

**This was #347's latch reached by a route its guard was not built for.** #347 keys on
`Synced == ReconcileError` — *"did the previous reconcile error"* — and this shape is *"the previous
reconcile **succeeded** and still produced no identifier"*. **0.68.0's #681 made it more reachable**, as
that release's own section records: a status-write failure used to error and produce a visible create
loop, and now reports success and lands here instead.

### The discriminator is the get verb, not the identifier count

`IsResourceKnown` collapses two different situations into one `false`:

| situation | observable? | answer |
|---|---|---|
| no `get` verb declared | no, **by declaration** | nothing better exists — the historical assume-up-to-date path is kept |
| `get` declared, identifier absent | **yes in principle** — the handle was lost | refuse to answer |

A test asserts the fallback still exists **and** that the refusal precedes it, because a refusal placed
after the fallback would be unreachable code that still compiles.

### Visible change

A CR in that state now carries **`Ready=False`** with reason **`NoIdentifierRecorded`** and a message
naming the cause, instead of `Ready=True`/`Available`. The message warns that the external resource
**may exist and be unreferenced**, and says explicitly not to delete the CR to clear the condition,
because that orphans it.

**Nothing in rest-dynamic-controller clears this state**, and that is deliberate rather than an
oversight: the get path needs the identifier that is missing, there is no findby to search with, and
Observe is the only thing that writes identifiers into status. It clears on a write to the CR's status,
or on a RestDefinition change declaring a `findby` — after which the kind becomes self-healing. Not on
anything the vendor does.

core-provider's composition rollup maps the reason to **childFailed** beside `CreateUnreachable` and
`NoCreateVerbDeclared` (their #510), so a parent composition reads `Unavailable` and names the child
rather than converging forever.

### Why neither plausible answer was available

Reporting **not-exists** sends the runtime back to Create, which on a vendor that mints the identifier
duplicates the resource on every pass. Assuming **Available** is the defect itself. So this refuses, and
returns the error wrapped in `controller.ReadyAssessed` — which is load-bearing: `handleObserve`
otherwise replaces `Ready` with `ObserveFailed`, and the reason naming the cause would be overwritten
before anyone read it.

This is the **fourth** place in this codebase where "I could not determine this" has to be its own
answer, after `Exists` (a synthesised 404 is not proof of absence), `liveInstanceCount` (a failed LIST is
not zero instances) and the delete guard (unverifiable must not read as deleted). All four are paths
where the plausible value is also the destructive one.

### Known limitation

> **Known issue (2026-11-14, issue 682): the CAUSE measured in that run is still unfixed.** A create
> answered with a 3xx is still followed by Go's HTTP client, and the redirect's body is still read as the
> created object — `clearStatusFields` wipes status and `populateStatusFields` skips every identifier it
> cannot find, silently. What changed is the *report*: such a CR now says `NoIdentifierRecorded` instead
> of claiming to be healthy. The create is still wrong.
>
> `snowplow.go:48` already installs `CheckRedirect: refuseRedirects` on its own clients, with a
> documented argument; every other client in the repo follows redirects. Fixing that is a decision rather
> than a patch, because a vendor answering 308 on a moved endpoint is a legitimate shape, and the two
> candidate shapes differ — refuse 3xx outright, or keep following but refuse to populate status from a
> response whose request was redirected.

### Documentation in this release

- **#685** corrects a claim in `observed_condition.go` that `condition.Upsert` rewrites
  `lastTransitionTime` on every reconcile. True when written, false since `unstructured-runtime` v1.7.5
  — swept across the cached versions: v1.7.1 overwrites, v1.9.0 onward preserve. The
  `openstack-ironic-operator-kog` session had quoted it to argue that core-provider's stall clock could
  not work. The guard stays, with its justification corrected from a correctness argument to a
  request-rate one.
- **#688** documents this file's four annotation labels, which had been applied to 14 of its 20
  blockquotes and defined nowhere, and adds a `Note` to 0.68.0's section recording that the release
  moved where a status-write conflict is reported — both obvious log messages went quiet while the
  wrapped error text survived.

### Verification

`go test -race -tags=unit ./...`: 21 packages ok, 0 failures. #687's tests are mutation-verified on five
arms, each expected to fail for a named reason and all five did — including one that moved the source
test's slice anchor so its **control** had to fire rather than its assertion, confirmed by reading which
message appeared.

**Not exercised at the time of tagging.** The latch this replaces was measured on a cluster; the refusal
itself has only run in unit tests. keycloak-provider-kog has stated in advance that #682 is
**unreachable on their corpus** — their kinds resolve identifiers from spec, so `IsResourceKnown` is
true and the branch cannot be entered — and will report it as unreachable rather than as verified.
That is the right answer and is recorded here so a later reader does not mistake a missing
verification for a skipped one.

**0.68.0 below it IS exercised.** keycloak-provider-kog passed every arm with nothing voided: the
#676 race fired on two of three CRs with a no-race control on the third, one create POST each, zero
409s, `external-create-succeeded` set and no `external-create-failed`, and exactly one Keycloak client
per clientId counted from a full listing with a positive control. Their own tooling printed a blank
Keycloak count at one point, because a 300-character truncation broke the JSON — they recounted rather
than reading the blank as "none".

## 2026-11-14 — 0.68.0

**One defect, fixed on both sides: a create that reached the vendor is no longer reported as a failed
create, and the runtime no longer dispatches a create in the pass that proved it unnecessary.** A
status write that lost a race after a successful create was returned as an error, so the runtime
retried a non-idempotent operation — which on a vendor that mints the identifier means a **duplicate
external resource**.

`git log 0.67.0..0.68.0` holds **four** commits. Two change behaviour and they are the two halves of
the same defect: **#681** (the handler) and **#683** (`unstructured-runtime` v1.17.1 → v1.18.0, which
carries the runtime's #88). **#680** adds `retracted-versions.yaml` at the repo root — it retracts
**0.66.0**, which is a fact about the *previous* release — and **#686** is this section. Listed so
the range reconciles.

The scope was originally the handler fix alone, on the grounds that the runtime half was not yet even
a PR. It was released while this was being prepared, so the scope was reconsidered rather than
inherited: shipping one half would have made the consumer's deliberate race reproduction ambiguous,
because a `create` + 409 pair could mean "the handler fix failed" or "the runtime half is missing".

### A status-write failure is not a create failure (#676, #681)

Three sites in `CreateWithResult` sit **after** a vendor call and returned the status-write error.
The external resource existed; the handler reported a create failure. They now return
`CreateResult{Requested: true}` with a Warning event recording that status could not be written and
will be repopulated by the next Observe.

**The sites were enumerated rather than assumed, and that disproved the issue's own guess.** #676 said
the async and pending ones "may be safe to keep as errors — they should be enumerated rather than
assumed." They are not safe. All three have a vendor call behind them: the delegated RESTAction create
(idempotent by contract), the **dispatch** (not idempotent — a retry fires the action a second time),
and the ordinary create.

**Not gated on the error being a conflict**, deliberately. The two shapes go wrong differently and
both are wrong, read off `unstructured-runtime` v1.17.1 rather than inferred:

| the handler's error | marker | consequence |
|---|---|---|
| a status conflict | `worker.go:1272` returns before the marker at `:1284`; `pending` stays open | `ExternalCreateIncomplete` is true, recovery runs |
| any other write error | `:1284` closes `pending` with `create-failed` | no loop, but a create that **landed** is recorded as failed |

### Measured, and the count is what makes it an explanation

keycloak-provider-kog on real Keycloak, 0.66.0, applying the CR and then immediately a second field
manager's partial apply:

```
15:56:04.656  Observe  "Absence was inferred from an unmatched findby listing … a create will be attempted"
              POST succeeded -- Keycloak has the client
15:56:04.807  Create   ERROR "Updating status": status write conflicted with a newer write
15:56:05.933           "Recovered from incomplete external create", resourceExists=true
15:56:06.136  Create   "Performing REST call" -> 409 "Client mk-co already exists"
              … that create+409 SIX TIMES, to ~15:59
```

The CR read `Creating: create failed: unexpected status: 409` for about three minutes. **No duplicate
was produced only because Keycloak refuses a second client with the same `clientId`** — nothing in
this codebase prevented one.

Six attempts over three minutes is the workqueue's exponential backoff — 2s, 4s, 8s, 16s, 32s, 64s —
which is *why* the chain below is an explanation rather than a story that fits the symptom.

### There is a second, complementary defect, and it is the runtime's

The repeats are not this fix's doing. After the conflict, the **Create** event is requeued unchanged
(`worker.go:391` re-adds `obj`), the recovery confirms the resource exists and stamps `succeeded` at
`:724` — and the same pass then falls through to `:754 case ctrlevent.Create: handleCreate(...)` with
**no guard on what it just established**. That is the create 203ms after `resourceExists=true`.

core-provider confirmed it at their own main and is changing it so a Create event in a pass whose
recovery found the resource dispatches as **Observe**. Their test: a Create requeued after a quiet
conflict with `Exists=true` produces zero Create calls; the control, `Exists=false` past the grace,
produces one.

**This fix stays handler-side by design**, which is core-provider's decision and not expedience: the
runtime deliberately routes "the create's result is unknown" through its existence check with a
two-minute grace, and **only the handler knows the vendor call landed** — which is what `CreateResult`
exists for.

**rest-dynamic-controller's `Exists` is not implicated.** `exists.go:159` catches a synthesised 404 — a
findby that listed successfully and matched nothing — *before* the vendor-404 case, and returns an
error rather than `false`, because nothing matched is not proof of absence (#371). The only arm that
may return `false` is a definite vendor 404.

### The runtime half (#683, runtime #88)

`unstructured-runtime` v1.18.0 adds sixteen lines to `worker.go` and removes none, immediately after
`Recovered from incomplete external create`:

```go
if resourceExists && evt.EventType == ctrlevent.Create {
    evt.EventType = ctrlevent.Observe
}
```

A recovery that found the resource **absent** past the grace period still dispatches the Create. That
control arm is what makes it narrow rather than a blanket suppression of recovered creates.

**#681 stops the cascade starting; #88 stops it continuing** for anything else that returns an error
after a vendor call.

The tag also carries an **opt-in** `coordination.k8s.io` Lease (`Options.Ownership`) and a log line,
both inert here: `Ownership:` appears **zero** times in this repo against a `Pluralizer:` control of
24. The one thing in the bump that needed checking was `readyz.go`, whose logic was **inverted** in
that release so that the nil path now routes through a new `ownershipReadyzError()` — which returns
`nil` when ownership is unset, so `ReadyzCheck` is unchanged. Had it not, a dependency bump would have
altered the readiness probe.

### Who is affected, measured on two corpora

github-provider-kog split their 32 create verbs against GitHub's spec as pinned in their chart:

| group | count | a second create |
|---|---|---|
| idempotent PUT | 10 | no duplicate |
| **duplicates for certain** | **3** | `Issue`, `RepoDeployment`, `Release` with `draft:true` — measured by COUNTING objects, with Issue and RepoDeployment as positive controls |
| refusal documented in the OAS | 3 | refused |
| refused by the vendor, measured | 16 | refused with 422 or 409 |

`Release` was the row I had recorded as unverified; probing it found the **draft** form duplicates
while the published form is refused, because a draft does not create the tag the uniqueness check is
on. The six **org-level** kinds were deliberately not probed (repo-level writes only), so
`RunnerGroup` name uniqueness remains an assumption, and `DeployKey` is **void** — an org policy
refused both attempts, so neither create ran.

aruba-provider-kog: 34 of 36 kinds declare a create, 31 of those declare `additionalStatusFields`, and
on that corpus Observe infers absence from an unmatched findby and a create is then attempted —
measured as `AbsenceInferredFromUnmatchedListing` ×2 followed by `CannotCreateExternalResource`. For
Aruba the outcome is a vendor-dependent convergence failure rather than a clean duplicate.

So "the vendor refuses the second create" is common, and that is what kept the measured case free.

> **Known issue (2026-11-14, issue 682): a create whose response carries no identifier leaves the CR
> permanently `Ready=True`/`Available` with the external resource UNMANAGED, and this release makes that
> state MORE reachable.** Measured on a cluster against real GitHub with a positive control. A duplicate
> create answered with `303 See Other` is followed by Go's HTTP client, the 200 list body is read as the
> created object, and every identifier is silently skipped. Observe then takes the findby branch, finds no
> findby verb declared, and falls through to "assume up-to-date" at `restResources.go:442` — setting
> `Available` and returning `{exists, upToDate}` **without making any HTTP request**. Two observes, zero
> requests, `Ready=True` forever. A spec edit would never reach the vendor.
>
> **The trigger is generic**: no resolvable identifier, plus no `findby` declared, plus a previous
> reconcile that did not error. The redirect is only one cause of the first condition.
>
> This is #347's latch reached by a route its guard was not built for. #347 keys on `Synced ==
> ReconcileError` — "did the previous reconcile error" — and this shape is "the previous reconcile
> **succeeded** and still produced no identifier".
>
> **And #681 above makes it more reachable, which is this release's own cost.** Before it, a status-write
> failure returned an error, so `Synced=ReconcileError`, the guard fired, and the resource entered a noisy
> create loop. Now the create reports success, so for a kind declaring no `findby` the same situation lands
> in this silent latch instead. #681 is still right — a duplicate external resource is worse than either —
> but it trades a visible loop for a false green on that combination, and saying otherwise would be
> claiming a clean win this release does not have.
>
> **Detection, fleet-wide, with no cluster change**: the latch sets a literal message.
>
> ```
> kubectl get <kind> -A -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.conditions[?(@.type=="Ready")].message}{"\n"}{end}' | grep "API call not found for FindBy"
> ```
>
> A match on a resource that should be addressable is an instance. **Do not simply delete the CR**: for a
> kind whose identifier was never recorded, the external resource exists and nothing references it, so
> deleting the CR orphans it rather than cleaning up.
>
> No fix in this release. The two obvious ones are both wrong — reporting not-exists re-creates a resource
> that already exists, and latching `Available` is the defect — so it needs a decision rather than a patch.
> Tracked in #682.

> **Note (2026-11-14): this release moved WHERE a status-write conflict after a create is reported, so
> a detector keyed on either log MESSAGE goes quiet while the race is still happening.** Two messages
> changed and one channel did not:
>
> | signal | 0.67.0 | 0.68.0 |
> |---|---|---|
> | the handler's log message | `Updating status` | *"The create reached the vendor but its status write failed…"* |
> | the runtime's own `Status write lost a race to a newer write`, create path | emitted | **not emitted** — the handler no longer returns the conflict as an error |
> | the wrapped error TEXT | present | **present**, in the handler's log payload and in a Warning event |
>
> So the error text survives. `tools.UpdateStatus` wraps a conflict as
> `fmt.Errorf("%w: %w", ErrStatusConflict, err)`, which carries both
> `status write conflicted with a newer write` and the apiserver's own wording.
>
> What to key on, in order of robustness:
>
> 1. the **Warning event** on the CR, reason `Created`, message containing `status could not be
>    written` — independent of log level and of pod restarts within the retention window;
> 2. the log payload `status write conflicted with a newer write`, which is `tools.ErrStatusConflict`'s
>    own text and a constant in the runtime;
> 3. `the object has been modified`, which is the **apiserver's** wording and varies by Kubernetes
>    version — present today, not guaranteed.
>
> Raised by azuredevops-provider-kog, who asked which string to key on before building a probe rather
> than after. **An earlier version of this note claimed their first choice would have read zero. That
> was wrong**: their grep matched the apiserver's text anywhere in the log rather than being anchored
> on the old message, so it would have worked. I inferred the anchoring from their question instead of
> reading their script, which I had never seen — the correction is theirs.
>
> A status conflict is still written exactly once and never retried (unstructured-runtime #30, removed
> in core-provider #359), so a silent internal retry is not an explanation for a detector reading zero.

### Known limitation

> **Known issue (2026-11-14, issue 466): the Warning event is the only record of a status-write
> failure, and events expire.** If a status write fails *repeatedly* on a resource that does exist,
> the CR's own status is by construction the thing that cannot record it. `status.observedGeneration`
> is the durable channel for this — it works because the signal is the **absence of advance** rather
> than a write made at the bad moment, and `metadata.generation` sits on the main resource so it stays
> readable while status writes fail. The field is already **declared** (plumbing's crdgen injects it;
> aruba-provider-kog measured it served on 36/36 CRDs at two pins), but **nothing in
> rest-dynamic-controller writes it**. Raised by aruba-provider-kog; tracked in #466.

### Verification

`go test -race -tags=unit ./...` across both modules: 42 packages ok, 0 failures, no data race. The
tests are mutation-verified on five arms, each expected to fail for a named reason and all five did —
including one that moved the source test's anchor so its **control** had to fire rather than its count,
checked by reading which message appeared rather than by the test going red.

**Not exercised at the time of tagging.** Nothing in 0.68.0 has run on a cluster. keycloak-provider-kog
will reproduce the race deliberately on the shape that produced it.

## 2026-11-14 — 0.67.0

**One change: `unstructured-runtime` v1.17.0 → v1.17.1, which stops a no-`create`-verb kind writing
`external-create-pending` about five times a second forever.** Cut on the bump alone rather than
bundling other work, because the defect is live on every fresh install of a shipped chart and the
affected consumer has no downgrade path.

`git log 0.66.0..0.67.0` holds **three** commits, and only one is a change to this release: **#677**,
the bump. **#673** and **#675** are the two known-issue blockquotes added to 0.66.0's section below —
they document the previous release and are listed here only so the range reconciles for anyone
checking it against this section.

### The 0.66.0 write loop is fixed (#674)

0.66.0's #664 made the no-create path return `Requested:false` with a **nil error**, which lost the
workqueue's exponential backoff. The trigger was the runtime's own recovery registration:

```go
watchAnnotations.Add(ctrlevent.Create, meta.AnnotationKeyExternalCreatePending, ctrlevent.OnDelete, false)
```

**Deleting `external-create-pending` fires a `Create` event**, and `createNotRequested` deleted it
unconditionally on every not-requested pass — so each pass scheduled the next immediately. v1.17.1
**closes** the marker with a new `krateo.io/external-create-not-requested` timestamp instead of
deleting it, so nothing re-triggers and the next pass waits for the normal resync.

**The loop never consulted Observe**, which is why it continued after the resource became observable
and why only a pod restart cleared it: the Create events came from the annotation delete, so nothing
in the cycle asked whether the resource now existed. That also made the latch look in-memory.

### Visible change

A CR that hits the not-requested path now carries **`krateo.io/external-create-not-requested`**
(RFC3339) and **keeps** `external-create-pending`. The pending marker is no longer deleted.
`ExternalCreateIncomplete` counts the new marker beside `succeeded` and `failed`, so the intermittent
incomplete-create state 0.66.0 produced on these CRs is gone with the same change.

### Who was affected

Any CR of a kind declaring **no `create` verb**. Two consumers, independently:

- a fleet whose `LoadBalancer` and `AiKey` can never be created — held at 0.65.0 and never exposed;
- a per-realm singleton (`get` + `update`, no create, no findby) whose chart creates it alongside the
  realm it reads, so the first observe 404s. **Every fresh install of that chart landed here**, and it
  could not pin 0.65.0 because its own floor requires 0.66.0's keyed array comparison.

The second case is why this shipped alone. 0.66.0's section carries a known-issue note pointing here.

### Evidence, since this is someone else's fix and I only bumped it

The runtime owner's pass count through the real loop (informer, queue, workers, annotation watches), on
a no-create object over 2s with a 30s resync:

```
fixed                                      1 create pass
v1.17.0's delete restored             21,795
v1.17.0's delete WITHOUT the watch         1      <- isolates the TRIGGER from the write
```

A consumer measured the pass **type** on a cluster — `"event":"create"` 5, `"event":"observe"` 0 —
which ruled out the generic-informer explanation this repo had originally filed, by channel rather
than by argument. And a second consumer bisected the backoff loss directly: on 0.65.0 the same shape
retried at `:25, :27, :29, :33, :41, :57`, doubling; on 0.66.0 it did not back off at all.

I read the v1.17.0→v1.17.1 diff rather than taking the description, and checked separately that
`ExternalCreateIncomplete` counts the new marker — because the intermittent incomplete state was a
second symptom and a fix for one is not automatically a fix for the other.

### Not in this release

**#676** — a successful create whose status write loses a race leaves `external-create-pending`
unclosed, so the object enters the incomplete-create state and its recovery re-enters `Create`;
measured as six `409`s on a vendor with a unique key, and duplicates on any vendor without one. Three
releases old, needs a concurrent field manager to trigger, fix not yet written. Goes to 0.68.0.

## 2026-11-14 — 0.66.0

**A slow update was silently losing a credential rotation; a create that was never requested stops
reporting success; and an array of objects a vendor returns reordered can now converge. Six PRs,
derived from `git log 0.65.0..0.66.0` rather than from memory.**

### A status clear no longer destroys the keys this controller owns (#667, #668)

**UPGRADE CONSEQUENCE, and the reason to take this release.** `clearStatusFields` removed the whole
`status` subtree — on every `Create` and every `Update` with a response body — while
`populateStatusFields` wrote back only identifiers and `additionalStatusFields`. Its stated purpose is
stale identifiers, and three keys the controller owns were collateral: `conditions`,
`observedGeneration` and `resolvedResolversDigest`.

Two consequences, the second serious:

- **every condition type except the one the caller re-added was destroyed.** `Ready` survived only
  because Observe re-decides it each pass, which made the surviving set *"whatever something else
  happens to rewrite"* — a property nobody declared. `ResolverDigestRecorded` was the first condition
  whose write is gated on a non-recurring branch (`if bootstrappedDigest`), so it never came back at
  all;
- **a pending update lost the rotation.** The clear ran *before* the pending/success split, so it
  wiped `status.resolvedResolversDigest` on both branches. The success branch re-records it; the
  **pending** branch deliberately does not, because a pending update has not landed — but the digest
  was already gone, so the next Observe saw `recorded == ""`, took the bootstrap path and **adopted
  the rotated fingerprints**. The rotation was retired without ever reaching the vendor, while every
  visible signal said healthy.

That second one is the exact failure #598's own comment says the design avoids. The comment is right
about the branch it guards and blind to the clear running above it.

**If you rotated a Secret on 0.65.0 and the vendor still shows the old value, this is why.** Verify
out of band rather than trusting a quiet CR: a write-only field has no response-side backstop, so a
retired rotation is indistinguishable from a landed one by any means a client has.

Fixed by giving `clearStatusFields` the preserve-list `writeObservedStatus` already had, so the two
sites share one rule — **the list is the contract, and it is three keys.** Anything new in a generated
Kind's status must be added to it; everything else in `status` is still dropped, which is what the
function is for.

**How it was found, because the method matters more than the fix.** A consumer reported
`ResolverDigestRecorded` present before a rotation and absent after — one reading, on a cluster
already torn down, and they said plainly they could not tell whether it was removed or never re-added.
Their script's verdict line would have printed a tidy *"the digest was recorded"*; they corrected it to
separate the measured count from the inference and reported the thing they actually read. **A clean
summary would have given nobody a reason to open the code.**

### Create can now say it created nothing (#657, #660, #664)

Adopts `unstructured-runtime` v1.17.0's `ResultCreator`. The runtime read **every** `nil` return from
`Create` as "a create was requested": it stamped `external-create-succeeded` and emitted a Normal
`CreatedExternalResource` event reading *"Successfully requested creation of external resource"*.
Three of this handler's paths had called nothing, so those CRs durably claimed a create that never
happened, and the false annotation kept them out of incomplete-create recovery.

| path | now returns |
|---|---|
| no `create` verb declared | `Requested:false`, **both** Ready and Synced |
| `PendingReferent` — a `dependentLookup` whose referent is absent | `Requested:false`, Ready only: a wait, not a refusal |
| `AuthorizationDenied` — a permanent 403 | `Requested:false`, both halves |

`NoCreateVerbDeclared` is revived and **now survives**, which is why #645 had removed it as inert.
**Pair it with core-provider >= 2.13.96**, whose rollup classifies that reason as `childFailed` rather
than `childConverging` — so a composition containing a kind that can never be created stops reporting
as still converging forever.

**And it removes a suppressor of the sibling arm.** `createIsUnreachable` requires
`external-create-succeeded` to be unset, so a CR whose create had hit one of the three false-success
paths had `CreateUnreachable` suppressed **permanently**. 0.64.0 was a floor for that arm, not a
ceiling.

Both remaining `nil` returns are correct and were left alone: an async 202 really did request a
creation, and the being-deleted refusal already returned `Requested:false`.

> **Known issue (2026-11-14, issue 672): this stops NEW false stamps and cannot clean up OLD ones, and
> the affected population is every existing installation.** Up to and including **0.63.1** this branch
> returned `nil`, so the runtime stamped `krateo.io/external-create-succeeded` on CRs whose create had
> never been attempted. **Nothing in rest-dynamic-controller ever clears that annotation** — every
> reference is a read except one that sets it. And `createIsUnreachable` gates the #629 diagnostic on
> `GetExternalCreateSucceeded(mg).IsZero()`.
>
> So a CR of a no-`create`-verb kind that was reconciled under ≤ 0.63.1 carries a false stamp
> permanently, and `CreateUnreachable` stays suppressed for it on **every** later version, 0.66.0
> included. The resource reports `Ready=True`/`Available` while its only effect never fires, which is
> the inverted health signal #629 exists to remove.
>
> **This is not an edge case.** Anyone pinning 0.66.0 on a cluster with history has no-`create` CRs
> that predate the fix by definition. A fresh cluster is unaffected — there is no path to the stamp —
> which is also why a validation run on one cannot see this. Detection needs no cluster change:
>
> ```
> kubectl get <kind> -A -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.annotations.krateo\.io/external-create-succeeded}{"\n"}{end}'
> ```
>
> A non-empty value on a resource that was never created is an instance.
>
> **Do NOT remove only that annotation.** A falsely-stamped CR carries **both**
> `external-create-pending` and `external-create-succeeded` — the runtime's success path sets
> `succeeded` and does not clear `pending`. `meta.ExternalCreateIncomplete` returns
> `pending.After(latest)` where `latest` is `succeeded` (or `failed`, if later), so deleting
> `succeeded` alone makes `latest` the zero time, `pending.After(zero)` becomes **true**, and the
> resource flips into the `errCreateIncomplete` state: *"cannot determine creation result - remove the
> krateo.io/external-create-pending annotation if it is safe to proceed."* That trades a suppressed
> diagnostic for a resource demanding human adjudication of a create that never happened — worse than
> the state it was in.
>
> **Remove both annotations, or delete and recreate the CR.** For a no-`create`-verb kind the recreate
> costs nothing at the vendor, because nothing was ever created there — which makes it the better
> advice for exactly these kinds. The durable fix is cross-repo and is tracked in #672.

> **Known issue (2026-11-14, issue 674): this release makes a no-`create`-verb kind write
> `external-create-pending` about five times a second, forever, and NOTHING IN THE CR SHOWS IT.**
> Measured on these released images by a consumer: 2839 and 3918 watch deliveries in eleven minutes
> for two such CRs, ~4-6 writes/second each, flat — while the conditions stayed correct and stable at
> `Ready=False/NoCreateVerbDeclared` + `Synced=False/NoCreateVerbDeclared` throughout. **Correct
> conditions are exactly what hides this**, so look at the write rate rather than at `status`.
>
> The cause is this release's own change. `Requested:false` with a **nil error** replaced a branch that
> returned an **error**, and the error path both kept `external-create-pending` and took the
> workqueue's backoff. The runtime registers
> `watchAnnotations.Add(Create, AnnotationKeyExternalCreatePending, OnDelete, …)`, so **deleting that
> annotation fires a `Create` event** — and `createNotRequested` deletes it unconditionally on every
> not-requested pass. Each pass therefore schedules the next immediately.
>
> A second symptom follows: at a read where `pending` happens to be present — roughly half of them —
> `succeeded` and `failed` are both absent, so `ExternalCreateIncomplete` returns true. Intermittent,
> not permanent; the known issue above is the same state reached from the opposite direction.
>
> **Affected shape:** any CR of a kind declaring **no `create` verb**. The only known instances
> anywhere are one consumer's two kinds, and they are **holding at 0.65.0**, so no fleet is exposed —
> which is luck about who has the shape, not a narrow defect. `PendingReferent` and
> `AuthorizationDenied` take the same route and would loop identically; both are unreachable in every
> probed corpus, which is the only reason this is one shape rather than three.
>
> **Do not take 0.66.0 for a composition containing a no-`create`-verb kind.** The fix belongs in
> `unstructured-runtime` — no handler return avoids it, since an error is the pre-#664 behaviour with
> the condition overwritten — and 0.67.0 will carry the bump. Tracked in #674.

### An array of objects returned reordered can converge (#650, #658, #669)

`x-kubernetes-list-type` and `x-kubernetes-list-map-keys` pass through from an OAS document into the
generated CRD (#658), and the drift comparison honours them (#669): a declared map-list is matched by
**key tuple** instead of by position. Opt-in through the document; every other array stays positional.

Measured, not theorised: `KeycloakClient.spec.protocolMappers` declared `[groups, mcp-audience]` and
returned `[mcp-audience, groups]`, with `the update has been issued 7 times without resolving it`
while the client was correct in Keycloak — and `krateo-keycloak-realm` 0.5.x's `krateo-ide` client
disabled by default because of it.

Three things it deliberately does **not** do. A duplicate key tuple is **reported**, not resolved —
picking one of two elements with the same identity would make the verdict depend on map iteration
order. The key tuple is **length-prefixed**, because `{ns:"x|y",name:"z"}` and `{ns:"x",name:"y|z"}`
are different elements that a separator-joined tuple would silently match. And the **length** rule is
unchanged, so a vendor that *adds* elements of its own still reports drift — pre-existing, and a
different question that needs a different answer.

### A resolver pointer the request never carried is not drift (#663, #670)

Two individually-correct mechanisms produced a resource that could not converge.
`stripResolverSources` removes a resolver's `inCustomResource` from the request body, leaving the
containing object present-but-**empty**; `CompareExisting` reports a CR-populated object against a
remote-**empty** one as drift. So a `secretRef` whose pointer sits inside an object the vendor echoes
back sent `extra: {}`, the vendor stored `{}`, and the comparison correctly flagged it. **The strip
created the asymmetry the comparison flagged.** Measured on a live Ironic node: five real PATCHes at
180s intervals before the non-convergence guard latched it.

**It was intermittent, which is worse than permanent.** `extra` on that Node also carries operational
keys — written at **runtime** by a bridge script, not by any template — so the same definition fires on
a freshly-enrolled node and converges on that node later. A fleet divides into permanently-latched and
healthy with no apparent pattern, and the healthy ones read as proof the definition is fine. It also
means "add a sibling key" is not a mitigation: the sibling's presence is a property of *when you look*.

**The fix counts keys rather than weakening the check**, and that distinction is the whole change. The
remote-empty check exists for another measured case on the same vendor — `instance_info: {}` against a
CR carrying `image_source` and `image_checksum`, which reported no drift, issued no PATCH and went
`Synced=True` having never applied its desired state. Same shape, opposite correct answers,
discriminated by one thing: **whether the request could ever have carried the key.**

`inCustomResource` remains **optional**, and omitting it skips the strip entirely — still the better
authoring wherever the vendor tolerates the pointer in the payload, because this fix changes the
comparison and not the request.

### Two comments corrected (#665)

Both argued against something that was later built, and neither was retracted by the change that built
it — the replacement arrived as a *new* comment elsewhere, which contradicts nothing a reader has
already read.

One claimed no durable warnings surface existed and that `status.generationWarnings` was an open
proposal. `status.schemaWarnings` has existed since #462, written **unconditionally** so it clears when
the warnings clear, **36 lines below that comment in the same function** — and the stale note produced
a wrong public claim in #662 before anyone noticed. The other called `maxPages` *"the field proposed in
#119"* when #119 closed COMPLETED and `maxPages` shipped with it: required, no default, in the
published CRD, enforced per walk.

The second was found by **enumeration, not by symptom** — grep every non-test comment citing an issue
as open or proposed, then ask the *server* for each issue's state. The symptom gave one; the
enumeration gave both.

## 2026-11-14 — 0.65.0

**A rotated `secretRef` Secret is now detected as drift and pushed, which it never was; a condition
removed because it could not win; and two claims in 0.64.0's own section below were wrong, one of
them now measured on a cluster.**

### A rotated Secret's value now reaches the vendor (#598, #646, #649)

**A value sourced from a `secretRef` never appeared in the CR's spec**, because its mapping points at
a Secret rather than a spec path — and a vendor that masks it does not return it in the observe body
either. So the drift comparison compared nothing that moved when the Secret's content changed, and
the rotated value was never pushed. A rotation shipped only if something *else* in the spec happened
to change, which for a resource at steady state is never.

The generated CRD now declares `status.resolvedResolversDigest`, and the controller compares it on
observe and records it after a successful update.

**IT DOES NOT HASH THE SECRET VALUE, and the issue asked for that.** #598 proposed a digest of the
resolved values salted with the resource UID. The salt does not work: the UID is readable by the same
`get` that reads the digest. Measured single-threaded on one machine against a UID-salted SHA-256 of
the value — a 6-digit PIN falls out in **0.25 seconds**, eight lowercase letters in about ten seconds
on one mid-range GPU. The values a `secretRef` points at are, by construction, ones the CR's readers
were not granted. So each referenced Secret is fingerprinted by **name + key + `resourceVersion`**
instead, and no secret material enters the digest or is read during observe at all. The cost is that
a write to a Secret which does not change its value still moves the `resourceVersion`, so an update
re-sends an unchanged value — one idempotent request, visible and attributable.

**Scope is the UPDATE verb's resolvers only.** A digest difference's only remedy is an update, so a
resolver the update does not send is one whose rotation an update cannot push — including it would
report a difference every update fails to retire: one vendor write per poll, per CR, forever. A
`secretRef` on `create` but not `update` is therefore deliberately not rotation-detected, and a kind
with no `update` verb is out of scope entirely.

**An absent digest is a bootstrap, not a difference**, and that is what keeps an older CRD from
becoming an update loop. The generated status is a closed schema, so a CRD generated before this
release **prunes** the field — the write exits 0 and returns no error. Treating the resulting empty
value as "differs from everything" would mean compare, differ, update, write, pruned, repeat, forever,
on every consumer that has not regenerated. Instead the current fingerprints are adopted, no update
is issued, and the first real rotation afterwards is detected normally.

**THE FIRST CONSUMER TO ADOPT A `secretRef` ON AN UPDATE VERB PAYS A CRD REGENERATION**, and nothing
in the change advertises it. The field is declared *conditionally* — only when the update verb carries
a `secretRef` — which keeps every other kind's CRD byte-identical, and means a definition that adopts
one crosses the gate and has its CRD re-rendered. Where a kind's old served version is still being
consumed, that regeneration unserves it and can block renders that pin it until a parent pin moves.
Raised by a consumer who is about to be exactly that first adopter.

**AND THE REGENERATION KNOB DOES NOT DEFER THAT ONE.** `OASGEN_PROVIDER_REGENERATE_ON_UPGRADE=false`
is the supported way to stage an upgrade — upgrade the provider, keep the CRDs where they are,
regenerate deliberately later — and the gate it suppresses scopes only the render, so the generated
controllers are still deployed and still take the new image. But the regeneration gate is a
three-clause OR:

```go
if oasHash != cr.Status.OASHash || resHash != cr.Status.ResourceHash || generatorChanged {
```

and the knob only suppresses the third. `resourceHash` is a digest of the whole of `spec.resource`,
so **adopting a `secretRef` is itself a `spec.resource` change** and moves the second clause. The knob
defers a regeneration caused by an *upgrade*; it does not defer one caused by the *adoption*. Those
read as the same thing from a release note and they are not.

Two consequences. An upgrade alone does re-render every managed CRD without anyone re-applying
anything, because `generatorChanged` defaults on. And anyone trying to construct a pre-0.65.0 CRD on a
0.65.0 provider — to see what an un-regenerated consumer experiences — has to apply the definition
**before** the upgrade, not after: applying it afterwards moves `resourceHash` and regenerates, with
the knob off and nothing appearing wrong. Both found by a consumer building that exact state.

**It says so when it cannot work.** After writing, the digest is read back from the object the
apiserver returned. If it is absent, the resource reports `ResolverDigestRecorded=False` with reason
`DigestFieldAbsent`, naming the remedy — re-apply the RestDefinition to regenerate the CRD. That is on
a condition type of its own and deliberately not on `Ready`: `Ready` is decided elsewhere on that
path, so a write there would be overwritten and inert, which is the defect #637 shipped. The report
survives exactly where the field does not — `status` is closed, but `conditions.items.properties.type`
is a plain string with no enum, so a new condition type is storable on precisely the un-regenerated
CRDs that prune the digest.

### `NoCreateVerbDeclared` is gone, because it could not win (#645)

0.64.0 added that reason to a findby-only kind's `Ready` condition. It is removed. The condition was
written and persisted, and then the runtime's create-failure branch replaced `Ready` with `Creating` a
moment later in the same reconcile — so it existed briefly and was gone from any settled CR.

Keeping it would cost a status write per reconcile to produce a reason visible for under a second,
which is worse for an alert rule than no reason at all: whether the rule sees it depends on poll
timing. The full explanation still reaches clients, on `Synced=False` / `ReconcileError`, carrying the
error text verbatim. If the runtime's contract changes so a provider-set `Ready` reason can survive
`Create`, this becomes viable again.

### Corrections to 0.64.0's section below

Both are mine, both were written here, and one is now measured rather than reasoned.

> **"A findby-only kind now reports `Ready=False` with reason `NoCreateVerbDeclared`."**

`Ready=False` is true. **The reason is `Creating`**, in any CR that has settled — see #645 above. A
consumer measured the reason absent after a 240-second settle, and I had separately called the write
"inert", which is wrong in the other direction: it was written and persisted, then overwritten. A
settled read cannot distinguish "never written" from "overwritten", and neither of us said so.

> **"the runtime then recorded a Normal `CreatedExternalResource` event … once per poll, forever"**

Not once per poll. I inferred that from the mechanism — nil return every reconcile, therefore event
every reconcile — and never looked at the recorder.

Every event goes through a **state-aware** recorder that suppresses an unchanged repeat and force-
republishes one after `EVENTRECORDER_MAX_SILENCE` (default 5 minutes). Measured in-cluster by a
consumer, with absolute timestamps on a 3-minute resync: a constant-message event published at
07:48 and again at **07:54** — suppressed at 07:51, republished on the first reconcile past the
floor — while an event whose message embeds a counter published on **every** reconcile, because a
changing message is a different event to that recorder. There is no aggregation at all: each
occurrence is a separate Event object with its own `firstTimestamp`, and `count`, `deprecatedCount`
and `series.count` are never populated.

So the measured rule is: **constant message → a ~5-minute floor; message carrying a counter → every
reconcile.** Worth knowing when reading a Krateo event stream, and worth knowing that a count field
is not there to read.

### Also

- **`go.opentelemetry.io/otel/sdk` and `otel/sdk/log` bumped** (#651, #652). Both indirect, neither
  imported by this repository's own code; `otel/sdk/log` reached v1 at 1.47.0.
- **Test arms that are permanently the only coverage are now marked as such** (#644), with the real
  regression used as the fixture rather than a reconstruction. #629's positive path can only ever fire
  in the defect state, so no consumer can exercise it and the synthetic arms are not interim.

### What is NOT verified in this release

**No part of #598 has been exercised on a cluster.** Its branches live in `Observe`, which is
reachable only from the integration suite — that suite provisions a kind cluster and does not run
outside CI. The evidence is the unit suites with `-race`, an 11-row cross-module contract asserted
from both modules, source assertions on the three wiring properties whose violation is silent, and
mutation arms for each: adding a digest write on the drift path fails, stubbing the read-back fails,
dropping one letter from the field name fails, and swapping the bootstrap and not-applicable branches
fails.

Two consumers are running the cluster arms on this tag. The one that matters most is an **older CRD**:
confirm `DigestFieldAbsent` appears and that **no** update is issued, because if the bootstrap rule is
wrong the symptom is one vendor write per poll, per CR, on every consumer at the moment they upgrade.
This section will be corrected if either arm disagrees with it.

## 2026-11-13 — 0.64.0

**Two shapes that reported themselves healthy while doing nothing now say so, the `/render` body cap
and its memory limit became one knob instead of two, and three documentation claims I had written
turned out to be false.**

### TWO CONSUMER-VISIBLE BREAKS, both deliberate

**`resources.limits.memory` is now REJECTED by `controller-render-service`'s values schema, not
ignored.** It is removed from `values.schema.json`, which is `additionalProperties: false`, so a
values file that set it becomes invalid. That is the point: the body cap and the memory limit are
physically coupled, and the broken pair is now inexpressible rather than merely discouraged.

**A findby-only kind now reports `Ready=False`** with reason `NoCreateVerbDeclared`, and `Create`
returns an error. It previously returned nil, which tells the runtime the create SUCCEEDED — and the
runtime then recorded a Normal `CreatedExternalResource` event reading *"Successfully requested
creation of external resource"*, once per poll, forever, for a resource absent at the vendor (#624,
#637). Anything asserting `Ready=True` on such a kind will go red, correctly.

### One knob for the `/render` body cap, with the memory limit derived (#628, #638, #641)

`maxBodyMiB` on `controller-render-service` is now the only knob; the container's memory limit is
derived from it. `oasgen-render` additionally reads its own cgroup memory limit and **refuses to
start** when the cap exceeds what that limit can serve, which closes the routes no chart schema can:
the generic `env` passthrough, and hand-written manifests.

**The derivation is a baseline plus a rate, not a multiplier.** The measured live heap is ~29× the
document at 3.1 MiB but ~242× at 35 KiB — a fixed cost plus a marginal rate — so a flat `cap × 29`
would derive a limit the Go runtime cannot start in at a small cap. It is `128Mi + cap × 36.25`,
floored at `512Mi`.

**The floor is what this chart shipped before the knob existed**, so at the default `maxBodyMiB: 4`
the rendered limit is byte-identical to 0.63.1. Upgrading changes nothing for anyone who does not
raise the cap. Above it: 16 → 708Mi, 32 → 1288Mi.

> The comment that documented this coupling named `helm/oasgen-provider/values.yaml`'s
> `render.resources.limits.memory` — a path that appeared **exactly once in the repository, in that
> comment**. A reader who went looking for the key found nothing and could reasonably conclude the
> coupling was stale rather than the path.

### An unreachable create is reported instead of looking healthy (#625, #629, #633, #636)

For a kind whose `create` is its only write verb, the runtime queues a Create only on
`!ResourceExists`, so the create is reachable exactly one way: a findby that does not match. The
moment findby matches, the kind's only effect never fires again — and it reported
`Ready=True/Synced` while doing so. **Triaging by CR health you would pick the broken one**, because
the *working* arm reports `Ready=False/Pending`.

Generation time now emits `CreateUnreachableForActionOnlyKind` for that shape, and **suppresses** the
older `IdentifierNotInFindByResponse`, which told such an author to remove the identifiers that make
their kind work. Both read **one** verdict computed once. Runtime sets `Ready=False` with reason
`CreateUnreachable` plus a warning event.

**The clause that makes it a diagnostic rather than a false positive:** after a successful create,
findby matches — that is the normal terminal state. A provisioned node is still in the collection
forever. So the trigger also requires that **no create has ever succeeded**, which
`unstructured-runtime` answers through its own `krateo.io/external-create-succeeded` annotation.
Keying on the match alone would have warned about every successfully provisioned resource of exactly
the kind this exists to help.

### THREE CLAIMS IN THIS LOG WERE FALSE, and the census behind one of them was a false pass (#396, #639)

A property-level `not` **never reaches a generated CRD**. `oas2jsonschema`'s `Schema` type has no
`Not` field at all, so the keyword dies in the OAS→schema step, before crdgen. Measured by
`github-provider-kog` against the released `oasgen-render` binaries from both 0.62.0 and 0.63.1, with
a `maxLength` control proving the probe can see a change. Open as #396 since 2026-11-03.

So: **0.63.0's note promising an enforced admission constraint changed nothing for any consumer**;
the runbook's claim that every drop warns is true of crdgen's drops and false for `not`, which never
arrives; and **0.63.0's census reported "zero documents affected" by counting `not` nodes** — the
number was right and the reason was wrong, because no RestDefinition could have been affected
regardless. A census that returns 0 for a reason other than the one being tested proves nothing about
the thing being tested.

And **0.63.1's claim that an upgrade does not clear a latch was wrong** (#634). `generatorChanged` is
the third drift input, OR'd with the two content hashes, and on by default. The 0.63.1 annotated tag
carries that sentence permanently and cannot be corrected; the log is authoritative.

### Smaller

- `populateStatusFields` no longer appends into the identifier slice's backing array (#640). Latent —
  nothing reads past `len` today — and fixed because `restResources.go` already guarded the identical
  pattern **with a comment saying why**, which makes one unguarded site worse than either alone. Both
  now use `slices.Concat`, so the guarantee is stated by the function rather than reconstructed from a
  make-then-append pair. That also cleared three pre-existing `go/allocation-size-overflow` alerts,
  one of them on the very line held up as the correct pattern.
- `docs/detecting-a-stale-generated-crd.md` is new (#635): how to tell whether a cluster has a CRD
  stale relative to its document, the three traps that make a detector report the wrong answer, and
  why the act of fixing it destroys the ability to measure it.

### Measured by consumers, not by me

The latch of 0.63.1 was **reproduced deliberately** on 0.62.0 — two kinds, with a control proving the
edit is observable when the Deployment is present — then cleared by the upgrade to 0.63.1 with
`generatorBuild` as the only drift input that moved, and 0.63.1 refused to latch under the identical
procedure. The documented recovery was run for the first time: the probe appeared **17 seconds** after
the patch. A separate 45-day-old cluster measured 34 of 34 definitions with no latch, and confirmed
across eleven releases that the OAS digest is version-independent — a claim this repo had only read
off its own source.

## 2026-11-13 — 0.63.1

**One correctness fix, and it is the worst kind: a stale generated CRD that reports itself up to date and
never recovers.** Shipped on its own rather than bundled so that a correctness fix is not entangled with
anything else, not because a population of affected clusters is known — see the blast radius below.

### A content hash is no longer stamped for a render that did not happen

`status.oasHash` and `status.resourceHash` are what Observe compares against the live OpenAPI document
and the live `spec.resource` to decide whether the generated CRD is current. `Create`'s
CRD-already-exists path compared only **one** of the three drift inputs — the generator build — and then
advanced all three anyway.

So an OAS content change whose first observation arrived through `Create` had its hash advanced with
nothing rendered. **That latches permanently.** Every later Observe compares the new document against a
stored hash *of the new document*, finds no drift, and the CRD keeps its old schema while the
RestDefinition reports `Ready`. A missed detection recovers on the next pass; this never does.

Measured by the installer session on a **throwaway** GKE cluster built for the purpose: after a chart
upgrade that changed only its OAS asset, the CRD was byte-identical to its pre-upgrade capture while
`status.oasHash` held the digest of the **new** document — still unchanged ten minutes later.

```
live ConfigMap        the 0.7.1 document (uniqueItems x2)
status.oasHash        0ac62ae41a8840ab…  = the digest of THAT document
live CRD              sha b19488160f24…  = the 0.7.0 schema, x-kubernetes-list-type absent
```

**The precondition is narrower than "an OAS change", and a second consumer established that.**
`Create` is only reached that way when Observe returns early because the generated controller's
Deployment is absent, so it never gets as far as its own drift checks. keycloak-provider-kog ran the same
chart upgrade with the Deployment present: the CRD regenerated correctly and the hash matched a fresh
install's. So the window is an OAS change whose first observation coincides with a generated-controller
redeploy — which is exactly what a provider upgrade, downgrade or restart produces.

**And it could not be escaped.** Once `status.generatorBuild` reaches the newest available build, no
generator move clears the latch: a downgrade is deliberately not a trigger, and a re-upgrade leaves
`running == stored`. On a cluster sitting at the current newest there was no route back.

**Blast radius: no latch has been found on any long-lived cluster.** The only observed instance is the
one above, on a throwaway cluster, and the window was opened by a deliberate oasgen downgrade that the
reporter performed as part of a different experiment. Two long-lived clusters were then checked on the
affected path and neither was latched: one does not have that KOG installed at all, and the other's CRD
matches its own chart's asset exactly. So this is a real defect with a real reproduction and **no known
affected installation** — stated that way because an earlier draft of this section claimed two live
clusters were affected, which was an extrapolation rather than a measurement.

**Every released version can form this latch, measured at twenty consecutive tags.** The two
preconditions are Observe's OAS drift check being present (so a content change is detectable at all) and
`Create` stamping the hashes with no OAS comparison on that path:

```
0.45.0 … 0.63.0    drift check: present    unconditional stamps: 2    -> latchable at every tag
```

So it long predates 0.62.0, and 0.63.0 carries it too. **Choosing between earlier pins does not change
exposure; only 0.63.1 removes it** — and only for latches not yet formed.

That check also covered **one kind on one path**, out of 33 and 43 RestDefinitions on those two clusters.
The instrument that would answer the general question is a render-and-diff of every OAS-backed CRD
against the document its RestDefinition points at, and it is being built. Until it runs, "no latch found"
is the honest claim and "no latch exists" is not.

**And do not verify this by reading a CRD's description text.** That tell worked across one chart pair
because the text happened to change, and it does not generalise: two earlier versions of the same KOG
carry an identical description, so the same comparison reports "consistent" across a span where the
schema genuinely differs. Render and diff, or measure nothing.

The fix applies the rule `renderGeneratedCRDs` already existed for. Its own comment put it exactly, about
the generator build: *"a rule written in a comment is enforced by whoever reads it; a single function that
renders and then stamps cannot be half-used by a third call site."* That reasoning was never specific to
one field. All three stamps now live inside it, after the render, and `Create`'s unconditional writes are
gone — which is what makes the affected pass self-heal: nothing is stamped, so the next Observe sees the
drift it should see and Update regenerates.

> **Correction (2026-11-13): an upgrade DOES clear an existing latch.** This paragraph first said the
> opposite, and the 0.63.1 annotated tag message carries that wrong sentence permanently — an annotated
> tag cannot be corrected, so this is the authoritative text.
>
> `generatorChanged` is not a separate concern from the hashes: it is the **third drift input, combined
> with OR** (`restdefinition.go:1451`), so a build bump forces a re-render *regardless of what the stored
> hashes say* — including hashes a latch has falsified. And it is on by default (`main.go:54`,
> `env.Bool("..._REGENERATE_ON_UPGRADE", true)`). `docs/configuration.md` already said so plainly: *"a
> provider upgrade also re-renders every definition once, so an upgrade refreshes this field across the
> fleet."*
>
> **So a latch survives only where no upgrade crosses a MAJOR.MINOR.PATCH boundary.** A restart at the
> same version does not clear it — release triples are compared and the sha is dropped, so two builds of
> one release are "no change". Nor does an upgrade with `OASGEN_PROVIDER_REGENERATE_ON_UPGRADE=false`,
> the fleet-staging escape hatch. Both of those are the cases the patch below is for.
>
> This narrows the exposure materially. The 0.45.0–0.63.0 span is unchanged — that is when a latch can
> FORM — but a latch on a cluster that upgrades at all self-heals at the next release.
>
> **How the error was made, because the shape recurs.** I verified that nothing in the `Create` path
> re-renders, which is where the fix lives, and generalised "the latch survives its own code path" into
> "the latch survives everything". I never enumerated what *else* triggers a render. The falsifying
> observation came from a consumer who noticed every RestDefinition logs `Applying CRD` on a version
> bump — a fact with no answer inside the file I was reading.
>
> To clear a latch that an upgrade will not reach, do it directly; it is non-destructive:
>
> ```bash
> kubectl patch restdefinition <name> -n <ns> --subresource=status --type=merge >   -p '{"status":{"oasHash":""}}'
> ```
>
> Do it while the generated controller's Deployment is **Ready** — patching while it is absent routes the
> next pass through `Create` again. Deleting the RestDefinition would also clear it and takes the CRD and
> every CR of that kind with it.

**Three existing tests were passing because of this defect**, which is the most useful thing the fix
turned up. The shared reconcile fixture declared a verb with no `path` and no `method`, and its own
comment said why: *"no test in this harness has ever needed the generator to RUN."* That was only
survivable while `Create`'s false stamp kept the regeneration gate quiet. The fixture is now generatable
at source, and the helper that used to patch it **asserts** that instead — a fixture that cannot generate
cannot exercise the loop it is named for.

## 2026-11-13 — 0.63.0

**One breaking change for consumers, shipped deliberately alone.** A `not` in an OpenAPI document now
generates an enforced constraint where it previously generated nothing, so a CR value that was accepted
can be rejected at admission. A census of six consumer corpora found zero affected documents, but the
change is real elsewhere, and this release carries nothing else that could be confused with it.

### A `not` subschema now carries more than `const`/`enum`

> **Correction (2026-11-13): this capability is UNREACHABLE from a RestDefinition, so this release
> note changed nothing for any consumer.** A property-level `not` written in an OpenAPI document
> never reaches crdgen through this provider: `oas2jsonschema`'s `Schema` type has **no `Not` field
> at all** (nor `OneOf`), so the keyword is discarded in the OAS→schema step, one layer before
> crdgen. Measured by `github-provider-kog` against the released `oasgen-render` binaries from both
> 0.62.0 and 0.63.1: `not: {type: string, pattern: "^prod-"}` renders as `{"type":"string"}` with
> `warnings=[]`, while a `maxLength: 7` control survives. The enum-only case — which crdgen
> supported even at plumbing v1.33.0 — is lost too, which is what proves the loss is upstream of
> crdgen rather than a crdgen capability question. Known and open as **#396** since 2026-11-03.
>
> The plumbing change described below is real. It is simply not reachable from here.
>
> **And the census in this release's own notes was a false pass.** It reported zero documents
> affected across six corpora by counting `not` nodes. The number was right and the reason was
> wrong: no RestDefinition could have been affected regardless. A census that returns 0 for a
> reason other than the one being tested proves nothing about the thing being tested — found by
> *injecting* `not` where the census only counted it.

The provider's CRD generator moved from plumbing v1.33.0 to **v1.60.0**. `(*transpiler).emitNot` used to
hand-build its subschema as `&JSONSchemaProps{Enum: forbidden}` and drop any `not` carrying no `const` or
`enum`. It now calls `junctorMemberSchema`, the shared builder every other junctor member already used, so
`pattern`, `minimum`, `maximum`, `multipleOf`, `minLength`, `maxLength`, `format` and `required` reach a
`not` for the first time.

```yaml
# this document
not:
  type: string
  pattern: "^prod-"
```

generated **no constraint at all** before — no `const` or `enum`, so the whole `not` was dropped — and now
generates `not: {pattern: "^prod-"}`, which the apiserver enforces. **A field value of `prod-eu` that was
accepted is now rejected on the next write to the CR**, not at generation time, so a CRD that generates
cleanly can still reject objects that already exist.

**None of the machinery is new.** `junctorMemberSchema`, `isVacuousJunctorMember` and
`copyScalarValidation` all exist at v1.33.0. A hand-built literal was bypassing them, so what a `not` *can*
express did not change — only what `emitNot` bothered to put in one.

Measured rather than assumed:

- **Which nodes produce a `not` at all did not change.** `isScalarOnly`'s body is byte-identical at
  v1.33.0 and v1.56.0 and re-checked at v1.60.0, verified by diffing the two with a control proving the
  diff detects an altered copy. A `not` on an object, an array or an untyped node is still dropped.
- **Six consumer corpora, 96 documents: zero affected.** plumbing#102 names one document that *is*
  affected (`agentgateway-policies` 0.2.20, four sampling fields unenforced), so this is not theoretical —
  just not ours.
- **`exclusiveMinimum`/`exclusiveMaximum` are emitted nowhere**, so a `not` built only from those is still
  vacuous and still dropped.
- **A dropped `not` is visible.** The generator's warnings reach `status.schemaWarnings` and a
  `SchemaWarnings` Warning Event, so `kubectl get restdefinition <name> -o jsonpath='{.status.schemaWarnings}'`
  says what was dropped.

`docs/upgrade-crd-not-schemas.md` carries the keyword-by-keyword table and what to check before upgrading.

**Nothing in CI could have caught this.** The `CRDs drift guard` covers the hand-written `RestDefinition`
CRD, which comes from `controller-gen`; `crdgen` generates *consumers'* CRDs at runtime from their
documents. There is no in-repo artifact whose change would have signalled it, which is why the census was
run against real documents instead.

**The bump also broke rest-dynamic-controller, found by building rather than by comparing version
numbers:** `objectclient.Uninstall` returns two values from plumbing v1.55.0. Both call sites in
`internal/tools/secretrbac/secretrbac.go` are updated. RDC was already an `objectclient` and `audit`
consumer, so `audit` came along as plumbing's own requirement rather than as a new dependency of ours.

> **Note on the git history.** The squash commit for the bump reads *"plumbing v1.33.0 -> v1.56.0"*. The
> code is **v1.60.0** in both modules — v1.56.0 was the first version carrying the content comparison the
> audit work needs, and v1.60.0 was taken instead so the dry-run ops came with it, in a follow-up commit
> whose subject never reached the PR title. Read `go.mod` at the tag, not the log subject.

### `Apply` and `Uninstall` report what they did

Internal, with no behaviour change for any caller: both now return what the write did —
`created`/`updated`/`unchanged`/`dryRun`, and the object as it was before and after — so the audit
contract's "did this write change anything" question is answered once in the write layer instead of by
each caller. All 15 production call sites are updated and every one still only checks the error.

`unchanged` is **read from the apiserver** rather than decided locally: a write that changes nothing is not
persisted, so the resourceVersion the apiserver returns is the one that was sent. That keeps per-kind
semantics — RBAC rules compared as sets, a Secret's `stringData` folded into `data` — out of a generic
apply helper.

**That premise was measured, and measuring it took a deliberately failing run.** controller-runtime's fake
client bumps the resourceVersion even for a byte-identical Update, so the arm that covers `unchanged` can
only run against a real apiserver — and CI runs without `-v`, so a passing run prints no per-test output
and cannot tell you the test executed. Inverting the assertion made one run fail and record the answer:

```
--- FAIL: TestApplyUnchangedComesFromTheApiserver/.../ConfigMap
        expected: "updated"   actual: "unchanged"
--- FAIL: TestApplyUnchangedComesFromTheApiserver/.../RBAC_Role
        expected: "updated"   actual: "unchanged"
```

So a byte-identical Update against a real apiserver returns the resourceVersion it was sent, for an RBAC
Role as well as a ConfigMap — which was the case that mattered, because `unchanged` working for a
ConfigMap and not for the kind this audits would have been a design that was sound and useless at once.

**A dry run needed its own op, and in this provider that is not hypothetical.** A dry-run Update returns
the stored resourceVersion untouched — exactly the signature of a genuine no-op — so without `dryRun` it
would have been reported as `unchanged`; a dry-run Create would have been reported as `created`. And in
oasgen, dry-run **is** the Observe path: `Observe` dry-runs the five RBAC applies on every poll, so a
write op there would have had an audit caller recording writes that never happened, every three minutes,
per definition. The literal matches plumbing's `objectclient` so the two writers' records read as one set.

### Documentation

- `docs/upgrade-crd-not-schemas.md` — the `not` change above, with its measurement and what to check.
- `docs/provider-runtime-create-markers.md` — the external-create marker annotations are an **ordering**,
  not a presence check, so their precision is load-bearing. This provider pins provider-runtime v1.8.2,
  which writes all three at second precision, so two markers written in the same second cannot be ordered;
  v1.18.0 writes RFC3339Nano. Parsing is compatible in both directions, but the older writer's truncation
  is lossy, so "compare parsed times, not strings" is necessary and **not** sufficient. The note also
  records that this does **not** explain the wedge this log describes at 0.60.0 — that was diagnosed and
  fixed, twice, and a reader would otherwise conclude a version bump addresses it.

## 2026-11-12 — 0.62.0

**Four defects, three of them found by consumers running the thing rather than reading it.** Two are on
paths that destroy external resources; one made a resource never converge.

### A failed instance list no longer uninstalls a CRD that has instances

`liveInstanceCount` answered a no-match or NotFound list with a bare zero, and that zero authorises
`crd.Uninstall` — which cascade-deletes every CR of the kind and runs each one's external-resource
finalizer. The listing failure has two causes with **opposite** correct outcomes, and only the CRD's
presence separates them: a CRD that is gone means the kind cannot have instances, while a CRD that is
present means instances may exist and merely be unlistable by this client.

The branch now asks `crd.Get` — a plain Get on an apiextensions type, so it does not depend on the
RESTMapper knowing the generated kind, which is the very thing in doubt. CRD absent means zero; CRD
present refuses; an unanswerable question refuses with a distinguishable message.

**This was reachable and destructive one release ago, and it was measured.** github-provider-kog
reproduced it on 0.60.0 by patching the generated CRD's only version to `served: false`, restarting the
provider so its RESTMapper never saw the kind served, then deleting the RestDefinition with one live CR:

```
10:26:37   the Milestone RestDefinition is deleted, 1 live Milestone CR
10:26:42   "CRD successfully uninstalled" name=milestones.github.krateo.io
then       CRD  deletionTimestamp=10:26:42Z  Terminating=True
                reason=InstanceDeletionInProgress
           CR   deletionTimestamp=10:26:42Z  finalizers=[composition.krateo.io/finalizer]
```

The apiserver began cascade-deleting the instance. It stalls only because `Undeploy` had already deleted
the generated controller in the same call, so nothing remains to run the CR's finalizer — **the external
DELETE is pending, not cancelled.** A follow-up arm measured what happens when the controller returns:
one DELETE for an unannotated CR, zero for one annotated `krateo.io/deletion-policy=orphan`. So a CRD
stuck `Terminating` after this chain is a delayed charge, and the remedy is to annotate every surviving
CR **before** anything restores the controller. That annotation is honoured on generated kinds, by
`unstructured-runtime`; `management-policy: observe-delete` overrides it (#609, #611, and see #616).

On 0.61.0 and later the branch is unreachable from the RestDefinition delete path, because #576's
discovery guard refuses one layer earlier — both read `observedVersion(cr)` and `deploy.Undeploy` has one
caller. That is cover, not correctness: `Undeploy` is exported and the contract "a zero means there is
nothing to protect" was false.

### A RestDefinition deleted before its next reconcile no longer sticks in Terminating

`manageFinalizers` added the `restresources-still-exist` finalizer whenever it counted instances, with no
check for whether the object was already being deleted. The apiserver forbids that, and the rejection was
returned as an error — so `observe` failed on **every** later pass and nothing downstream ran, including
the live-instance guard that exists to refuse the uninstall.

```
metadata.finalizers: Forbidden: no new finalizers can be added if the object is being deleted,
found new finalizers [composition.krateo.io/restresources-still-exist-finalizer]
```

The window is ordinary: a CR created after that function's previous pass and before the RestDefinition was
deleted. Measured by github-provider-kog on 0.60.0 with the CR created 10 seconds after the previous
reconcile and the RestDefinition deleted 32 seconds later.

**The operator saw the wrong reason, and the obvious response was the destructive one.** An apiserver
message about finalizers says nothing about the live instances that are the actual reason the object
cannot go away, and the usual response to a stuck finalizer is to remove it by hand — which unblocks the
cascade. openstack-ironic-operator-kog had raised that hazard about the discovery branch; this was a
second route to the same state. Not adding the finalizer loses nothing, because the delete path has its
own check against the live cluster at the moment of the destructive act (#612, #614).

> **Runtime verification (2026-11-13, issues 612 and 614).** Verified on the released artifacts by
> github-provider-kog, with the before/after on one cluster and a control, which is the only shape that
> distinguishes a fix from a coincidence.
>
> ```
> 0.61.0 (0.61.0-05546c0e672f), before the fix
>   finalizers at delete: [finalizer.managedresource.krateo.io] only   <- the issue-612 path IS taken
>   2 "no new finalizers can be added" apiserver errors within 60 s
>   no "Refusing to uninstall CRD" line;  CRD and CR intact;  RD stuck Terminating
>
> 0.62.0 (0.62.0-8d6143fce7b7, chart sha256:4c7815db…), with the fix
>   finalizers at delete: the same, so the same path
>   23:05:25  "Refusing to uninstall CRD: live instances exist" repositories.github.krateo.io
>   0 "no new finalizers" errors;  CRD and CR intact;  RD stays Terminating
>   CONTROL (no live CR):  RD and CRD ABSENT, "CRD successfully uninstalled"
> ```
>
> So the operator now sees the live-instance refusal **and its remedy text** instead of an apiserver
> message about finalizers, and the definition is still kept. The arm recorded the RD's finalizers at
> delete time deliberately: with `restresources-still-exist` already present the run would have proved
> nothing about this path, and would have been discarded as void rather than counted as a pass.

> **Runtime verification (2026-11-13, issue 593).** Verified against a live Azure DevOps by
> azuredevops-provider-kog, with a same-cluster 0.61.0 control and a second chart as the only other
> variable. The prediction was stated before the run, so the rows could falsify it:
>
> ```
> 0.62.0, chart 0.2.0 (default stateFilter)
>   d=0    ORPHANED+warning   CreatedExternalResource 22:16:12
>                             > OrphanedExternalResourcePossible 22:16:14
>                             > DeletedExternalResource
>   d=5    FIX-WORKED         project 404 at +10 min
>   d=15   FIX-WORKED
>   d=30   FIX-WORKED
>
> 0.61.0 control, same cluster          4/4 ORPHANED-SILENT
> 0.62.0, chart B (stateFilter=all)     4/4 FIX-WORKED, no warnings
> ```
>
> The falsifier — an ORPHANED-**silent** row on 0.62.0 — did not fire. Only d=0 loses the race, because
> the project is still `new` there, and that case is **announced** rather than silent: the finalizer is
> released, because holding it would leave a CR nobody can delete, and the Event names the identifiers so
> the orphan is recoverable. d=5 already wins.
>
> Note for anyone building an alert on this: the Event reason is **`OrphanedExternalResourcePossible`**.
> `reasonPossibleOrphan` is the Go identifier, not the emitted value — I quoted the identifier to the
> consumer building the matcher, and only their looser `"orphan"` match saved the arm from reporting a
> false falsification.

### A reordered set-typed array is no longer reported as drift forever

`compareSlices` walked both slices by index. A vendor that stores a field as an unordered set returns its
members in an arbitrary order, so the comparison reported drift on every resync, the controller issued an
update, and the resource never converged. Measured by keycloak-provider-kog on Keycloak 26.6.3 with a
`KeycloakRealm` whose `enabledEventTypes` holds 23 names: `Ready=False`/`Updating` forever, one PUT every
three minutes. **With two elements the order happened to match**, which is why it took a 23-element realm
to find.

An array whose OAS schema declares `uniqueItems: true` with **scalar** items is now compared as a
multiset. Everything else stays positional.

> **Correction (2026-11-12, issue 617).** This section says the realm was `Ready=False`/`Updating`
> "forever, one PUT every three minutes". **The PUT loop is bounded**, and the end state is worse than
> the sentence implies rather than better.
>
> Measured by keycloak-provider-kog on 0.61.0, same realm and same 23 event types:
>
> ```
> 15:16:30 15:19:30 15:22:30 15:25:30   four "Successfully requested update"
> 15:28:30                              "Drift is not converging; no longer issuing the update"
> ```
>
> So the non-convergence guard fires after four attempts and the resource **latches** `Ready=False`.
> "Never converges" was right; "re-PUTs forever" was not. The original wording came from the consumer's
> first report and I carried it without asking how the loop ended — and the correction matters in both
> directions: it is less load on the vendor than described, and a latched failure is **harder** to
> notice than a visible loop, not easier.

> **Runtime verification (2026-11-12, issues 617 and 548).** This release was tagged stating that no fix
> in it had executed on a cluster at this version. Two now have, measured by keycloak-provider-kog as a
> before/after on **one** GKE cluster and **one** Keycloak 26.6.3, against the released artifacts with
> the chart digests verified at pull:
>
> ```
> #617  0.61.0  four PUTs, then the drift guard gives up, Ready=False
>       0.62.0  ZERO updates; "External resource is up to date" x6 over ~15 min of running
>               cluster time; Ready=True
>       CONTROL removing LOGIN out of band -> EXACTLY ONE PUT -> restored -> up to date
> #548  0.61.0  orphan left ENABLED; admin USER events [CREATE] only
>       0.62.0  "the identifier was re-derived through findby"; 0 users left; [CREATE, DELETE]
>       CONTROL a username the user profile rejects behaves IDENTICALLY on both versions:
>               "not addressable, releasing finalizer without calling the API", no USER events
> ```
>
> **The two control rows are what make these verifications rather than observations.** Zero updates
> alone is equally consistent with a comparison that has gone blind and returns equal for everything —
> which would be a worse defect than the one fixed, silently disabling drift detection on every array.
> The out-of-band removal producing exactly one PUT is what separates those. Likewise the never-landed
> row: the obvious wrong way to fix an orphan is to always attempt a delete, and a row that behaves
> identically on both versions is what rules that out.

```
uniqueItems: true  +  scalar items (string/integer/number/boolean)  ->  multiset
everything else                                                     ->  positional, unchanged
```

**Nothing changes for a document that does not declare `uniqueItems`.** Scalar items only: a multiset of
objects needs a key to match elements by, which is `identifiersMatchPolicy`'s question, and it is the same
line plumbing's crdgen draws between `x-kubernetes-list-type: set` and a CEL uniqueness rule — so a
document that generates a native Kubernetes set produces a multiset comparison here (#617, #618).

### A CR deleted inside the create window no longer leaves an orphan

The delete path re-derives the identifier through findby before releasing the finalizer, so a resource
created but not yet recorded in status is still found and deleted. A 500 on the DELETE decides nothing and
defers to the observe verb (#548, #593).

### Test suites refuse to provision kind outside CI

Five TestMains create a kind cluster, once per `go test` invocation. On a developer machine that daemon is
shared, and a full build has filled its disk before now. The suites now allow CI — gated on the runner's
own `CI`/`GITHUB_ACTIONS` — and an explicit `KRATEO_ALLOW_LOCAL_KIND` opt-in, and otherwise print what to
run instead and exit **non-zero**. Non-zero is load-bearing and was measured: with a zero exit `go test`
prints nothing for a package it considers passing, so the explanation never reached the terminal and the
run reported `ok` for a suite that had executed no tests (#613).

### Upgrade notes

```
#617   a declared set-typed array is now compared as a MULTISET. A document that does not
       declare `uniqueItems: true` is unaffected; one that does will stop reporting a
       reordering as drift. If you relied on order mattering in an array you also declared
       unique, that combination was already contradictory -- but check it.
#612   a RestDefinition that is ALREADY stuck in Terminating from this defect is not healed
       by upgrading: the finalizer it could not add is not the one holding it. Delete the
       instances, and the delete path's own guard will then release it with a message naming
       the count.
#609   a CRD left stuck `Terminating` with `InstanceDeletionInProgress` by an earlier release
       holds PENDING deletions. Annotate every surviving CR
       `krateo.io/deletion-policy=orphan` BEFORE re-creating the RestDefinition or running a
       helm upgrade that restores the kind, or the external DELETEs fire.
#613   `go test -tags=unit,integration` now FAILS locally instead of provisioning kind. Use
       `-tags=unit`, or set KRATEO_ALLOW_LOCAL_KIND=1 if the Docker daemon is yours.
```

### Verifying the upgrade

`status.generatorBuild` should read `0.62.0-<sha>` on every RestDefinition afterwards. Nothing in this
release touches the generator, so a correct render produces byte-identical CRDs and every
`resourceVersion` legitimately stays put — read the provider's `Applying CRD` / `Applied CRD` log pair
instead, and count provider restarts, since a restart discards earlier lines.

**An `rdc.env` change is eventually consistent with a ~5-minute worst case**, newly documented in
`docs/configuration.md`: kubelet volume propagation before the provider's mounted asset carries the value,
then each RestDefinition's next poll. Every definition has its own phase, so a fleet updates in
instalments and a partial result is the expected intermediate state. Measure in poll intervals of
**running** cluster time; a stopped cluster does not poll (#619).

### What this release does NOT contain

```
#543   AUDIT-4 conformance for rest-dynamic-controller and oasgen's RBAC/admission writes
#598   rotating a secretRef Secret's content still never reaches the vendor
#512   the configuration generator still accepts offset/limit/filter/sort/projection
#616   krateo.io/deletion-policy on generated kinds is honoured but undocumented
#555   a resolver-supplied path or query parameter is still REQUIRED in the generated spec
```

## 2026-11-12 — 0.61.0

**A failed instance list is no longer read as zero instances.** One fix, and an honest note about the half of it that is still open.

### The finalizer that stops a RestDefinition being deleted while its instances live

`manageFinalizers` listed the instances of a generated Kind and used the count to decide whether to hold the `restresources-still-exist` finalizer. **Two distinct failures became a count of zero**, so the guard was defeated by the failure mode it is most likely to meet — a RestDefinition could be deleted while instances of its Kind were still live.

Both silent zeros came from matching error **text**:

```
"the server could not find the requested resource"   items nilled, error CLEARED
"no matches for"                                     the whole block skipped, so the error stayed
                                                     non-nil, was never returned or logged, and was
                                                     then OVERWRITTEN by the Update calls, which
                                                     reused `err`
```

The question is never "did it count correctly" but **"may the finalizer be removed on this evidence"**, and only one answer permits removal:

```
no-match or NotFound  ->  ask the authority: the CRD itself
      the CRD check failed   -> return the error   (unknown; HOLD)
      the CRD EXISTS         -> return an error    (instances may exist, cannot be counted)
      the CRD is ABSENT      -> no instances CAN exist; proceed with zero
anything else         ->  return the error
```

The `Update` calls now use their own error variables, so a dropped error is a **compile** error rather than a silent one.

### I proposed two reachable triggers for that guard and both were refuted

Worth recording, because the comment in the source now says so and a future reader who finds a third will otherwise re-propose one of these:

```
#314's RESTMapper-lag race    refuted by measurement -- controller-runtime's dynamic mapper tests its
                              inner error with IsNoMatchError, reloads discovery and retries, so a
                              stale mapper never surfaces at this call site
a pruned served version       refuted from the source -- prunableServedVersions skips the version it
                              was called with (which at its call site IS observedVersion) AND skips any
                              version holding instances, so neither the version we list at nor one with
                              instances can be pruned
```

So the guard is **defensive**: no reachable path is known that delivers a no-match while the CRD is present. That is not a reason to decide from the error alone — asking the CRD costs one `Get` on a path that has already failed, and being wrong costs a RestDefinition whose instances are live. **Right even when the question is academic.**

**The cost of the safe direction is not free, and an operator should know it.** On the generic branch the finalizer is held *indefinitely* if discovery never recovers, so the failure mode of safety is a RestDefinition nobody can delete — and the obvious remedy, removing the finalizer by hand, is precisely the destructive act the guard exists to prevent. See `docs/configuration.md` for what to check first.

### STILL OPEN: the same assumption, one layer down, where it is destructive

`liveInstanceCount` in `internal/tools/deploy` reads a no-match as zero too, and **a zero there authorises `crd.Uninstall`**, which cascade-deletes every instance of the kind and makes the controller fire an external DELETE at the vendor for each one. It is reached precisely *when* the fix above has already failed, so the two layers do not compose for the case either was written to catch.

Filed as **#609** and deliberately not fixed in this release. Every *reachable* failure there is already safe — a non-no-match error is propagated, so an unreachable discovery, an RBAC refusal or a timeout all reach "cannot determine" and refuse. The unfixed case needs the same trigger nobody can find.

### The call-site wiring audit

A decision function and the values fed into it can each be correct, and individually covered, while **the connection between them is asserted nowhere**. That gap shipped 0.59.0's non-firing restart gate, so the audit looked for siblings.

```
7 decision functions enumerated   2 with test gaps, both over CORRECT code
                                  2 measured clean, one with a control
                                  0 live production bugs
```

- **`resourceExistsForController`'s argument order** was unguarded: two `bool` parameters, so a swap compiles silently, and swapping them at both call sites failed no test in the package. What it protects is #122, where reporting an existing-but-rolling-out Deployment as non-existent re-set `krateo.io/external-create-pending` 75 seconds *after* create succeeded and wedged the resource.
- **`anchorOf`** had no test naming it and five surviving mutations — but both branches at its call site `continue`, so none of them can change the Role that comes out. What was unverified is a diagnostic distinction a consumer specifically asked for, which is why it got a test rather than a shrug.
- **The swallowed-error census came back clean**, with a control: 0 call sites discarding an error with `_` in the two audited packages, against 14 elsewhere in the monorepo. So 0.59.0's defect was an outlier rather than the house style — which changed the remedy from a package sweep to a single test.

**My own enumeration had the defect the audit was looking for.** I reported 6 functions; a mechanical re-count found 7. My pattern required the receiver to be `e *external`, so a method on a different type was invisible to the sweep that claimed to cover the package. It surfaced only because two counts of the same thing disagreed by one.

The method is written up in `docs/development/testing.md`, including the traps: baseline-green first, assert the anchor count before writing, restore in a `finally`, and **a compile failure is not a kill**.

### Also in this release

- **`docs/configuration.md`** gains the endpoint allowlist — `rdc.endpointAllowlist` and `rdc.allowAnyHost` were absent from a page that calls itself the whole config surface, and that is the one security control on it. With the matching rule nothing documented anywhere: `(host, port, scheme)` compared exactly, **but an omitted port defaulted from the scheme first, on both sides**. So a redirect that only adds or drops the scheme's default port is *permitted*, and "a redirect that changes only the port is refused" is too strong.
- **`docs/development/testing.md`** also corrects a misnomer: there is **no envtest** in this repository. Five packages stand up a real `kind` cluster in `TestMain`; nothing imports `controller-runtime/pkg/envtest`. The word appeared in three places, all prose, and I had repeated the claim myself before checking.
- The **0.60.0** section above gains its runtime evidence, measured and paired on one cluster in one sitting.

## 2026-11-11 — 0.60.0

**The restart gate that 0.59.0 shipped had never fired, and this release is the one where a `rdc.env` change reaches a running controller.** 0.59.0's own section claimed otherwise — *"a `rdc.env` change such as `REST_CONTROLLER_DEBUG` … every generated controller must roll to pick it up"* — and that sentence was false when it was written. It has a correction block of its own.

Both defects were found by consumers measuring their own clusters. Neither was visible to any check of ours, and the first was invisible to its own unit tests.

### A `rdc.env` change now reaches the generated controller

`Deploy` built one `NamespacedName` — `"<rd>-controller"` — and used it both for the Deployment and for the live ConfigMap read that the restart decision depends on. But the two assets render their names differently:

```yaml
# as the provider renders them -- i.e. AFTER Helm's pass over the assets, which is why
# grepping the asset files shows these wrapped in {{ "{{" }} … {{ "}}" }} escapes
assets/rdc/configmap.yaml   ->  name: {{ .name }}-configmap
assets/rdc/deployment.yaml  ->  name: {{ .name }}
```

So the read asked for a ConfigMap at the **Deployment's** name, got `NotFound` on every reconcile of every kind, and concluded there was no previous ConfigMap — which the code deliberately treats as a first deploy needing no restart. The decision returned "no" unconditionally (issue 599).

**The failure looked exactly like success.** The ConfigMap *was* updated, `helm upgrade` reported success, and no error, event or condition appeared anywhere. But `envFrom: configMapRef` is read once at container start, so the running controller kept its install-time environment indefinitely.

Measured by a consumer on 25 generated controllers across two `rdc.env` changes, 400 s settle each: every ConfigMap's data changed, written by the provider itself; `deployment.kubernetes.io/revision` stayed at 1 for all 25 across both — 50 of 50 rows with zero rollouts; one ReplicaSet per Deployment, created at install; every pod still at `restartCount 0` with its install-time start time; and zero occurrences of the restart log line in a fully covered window.

**Who was affected, and who was not.** An `rdc.image.tag` change alters the pod template, so **Kubernetes** rolls the Deployment and the gate is never consulted — image upgrades on 0.59.0 did take effect. A `secretRef` Secret name-set change is RBAC only and correctly causes no restart. **Only config-only changes were lost**, which is every value travelling through `rdc.env`: log level, OTEL settings, timeouts, anything an operator sets.

**The fix is structural rather than a corrected string.** The ConfigMap is rendered first and the live object is read at the *rendered object's own key*, so the two names are one value instead of two that have to agree. Re-deriving the `-configmap` suffix in Go would restore the defect the next time an asset's name expression changes. A lookup that could not be performed is now an error rather than an absence, and a rendered object with no name is refused.

### The restart annotation now survives the apply

`kube.Apply`'s update path is a full `Update` with the *rendered* Deployment, whose asset declares no annotations at all — so every `Deploy` stripped whatever `kubectl.kubernetes.io/restartedAt` the live object held (issue 595). Removing that annotation changes the pod template exactly as much as adding it does, so it would have cost a second, pointless roll per ConfigMap change as soon as the gate above started firing.

`kube.ApplyOptions` gains **`FromCurrent`**, called on the update path with the live object and the desired one, so a caller can carry over a field **another writer owns** — copied from the very version being replaced, re-run on each retry attempt, and never called on create. The name, argument order and "not called on create" rule match `ApplyOptions.FromCurrent` in `plumbing/kubeutil/objectclient` (v1.41.0+), which solved the same problem for core-provider; ours differs only in that `desired` is the typed object the caller already holds.

It carries **one** annotation deliberately. A blanket carry-over would also preserve annotations left by a since-removed sidecar injector or a deleted mutating webhook, and those should disappear when they stop being applied.

**Why both fixes are in one release.** Neither alone produces correct behaviour: with only the first, the restart fires and the next apply erases its annotation; with only the second, the annotation is preserved but never written. The end-to-end test's passes fail in different places for each half.

### A latent defect in this package's error handling

`retry-go` v3.0.0's `Error` is a `[]error` with no `Unwrap()`, so `errors.Is` and `errors.As` could not see through it: every error `Apply`, `Uninstall` and `UninstallFromReference` returned was unmatchable by any caller, and read in a log as `All attempts fail:\n#1: <the real error>` — the real error indented under a header that says nothing.

Latent, because nothing in the tree classified an error from those three. It became load-bearing with `FromCurrent`, whose error is the caller's own verdict about the object. `retry.LastErrorOnly(true)` is now set on all three `retry.Do` sites. **If you grep provider logs for `All attempts fail`, that string is gone** — the underlying error text is unchanged and is now the whole message.

### What was tested, and the gap that found the test

`restartNeeded` already had a dedicated suite — six input shapes, both outcomes, and an assertion that the table contains both. The live read has one too. **Both were correct, and the bug was entirely in the wiring between them.**

Mutation testing against those two suites showed two mutations that disable the restart outright, and **both survived**: comparing the rendered ConfigMap against itself, and forcing the "ConfigMap existed" flag to false. A decision function and the values fed into it can each be correct and individually covered while the connection between them is asserted nowhere.

So this release adds an end-to-end test that drives the real `Deploy` against the real renderer with a fake client, and changes the ConfigMap's data **the way a `helm upgrade` does — by rewriting the template file**, since Helm ranges `rdc.env` into the asset at chart-render time and the provider's input is a file on disk. It asserts three passes: a first deploy must not roll, a data change must, and an unchanged reconcile must leave the stamp untouched. It kills both surviving mutations and four more.

### Runtime evidence: measured, paired, on one cluster in one sitting

Measured by `azuredevops-provider-kog` on a fresh disposable GKE cluster, **25 generated controllers**, with the before and after arms on the **same corpus in the same sitting** — which no earlier measurement of this behaviour had. The criterion throughout was **revision delta plus pod creation time**, never `generation` or `restartedAt` alone.

Every arm carried a gate that had to hold for the arm to count: on a flip, all 25 ConfigMap data hashes must *change*; on the upgrade-only arm, all 25 must stay *unchanged*. A gate failure voids the arm rather than reporting a pass.

| # | arm | result |
|---|---|---|
| 1 | **0.59.0**, add an `rdc.env` key | gate OK, **0 of 25 rolled** — the defect reproduced |
| 2 | bare upgrade to **0.60.0**, no flip | hashes unchanged 0/25, **25/25 rolled on the rdc image bump alone**, 0 restart lines in either provider pod |
| 3 | **0.60.0**, remove the key | **25/25 rolled exactly once** (rev 2→3), `restartedAt` `None → T`, no rev 4 |
| 4 | **0.60.0**, add it back | **25/25 rolled exactly once** (rev 3→4), `restartedAt` `T → T'`, no rev 5 |

**Arm 1 is the one that makes the rest mean something.** It reproduces the defect on a fresh install with a gate proving the flip landed — so "0 rolled" is a measurement rather than an absent signal, which the original report could not distinguish.

**`"Restarting the generated controller"` went from 0 occurrences to 25, one per controller.** On every released version before this one that line had never appeared at all.

**Arm 4 separates the two halves of `FromCurrent`,** which no other arm can: an existing stamp is carried over *and* a fresh one is still written on top (`21:22:03Z → 21:30:35Z`). Preserving a field and letting its owner rewrite it are different behaviours, and only a second consecutive restart distinguishes them.

**That the annotation survives is evidenced by the absence of a further ReplicaSet,** not only by its value. A strip is necessarily a pod-template change, so it would necessarily leave another revision behind; across two polls at the default 3-minute interval there was none.

**Not measured: the RBAC-only path on 0.60.0.** A `secretRef` Secret-name-set change must roll nothing, and that arm did not run — the cluster lacked the vendor credential it needs. The evidence for it is an earlier arm on **0.59.0** (revision 1→1, no new pod). Nothing in this release touches that path, but it is untested *on this version* and is recorded as such rather than inferred.

### Every version bump rolls every generated controller, once, by design

Arm 2 measured this and it is worth stating plainly, because it is easy to mistake for a restart bug: the generated controller's image tag defaults to `.Chart.AppVersion`, which the release process substitutes into the chart. So a provider upgrade changes the rendered pod template and **Kubernetes** rolls each controller once — nothing provider-specific is involved, and it happens on every upgrade. Note that `git diff` between two tags shows no chart change at all, because the substitution happens at package time.

So "the controllers rolled after an upgrade" is never by itself evidence that the restart gate fired. Read the provider log, or the template diff: an image-driven roll carries no `restartedAt`, a restart-driven one does.

### Also in this release, with no behaviour change

- **`docs/usage.md`** gains the case `--set-string` does not cover: **removing** an `rdc.env` key needs
  `--reset-values`, or a `-f` values file with the key absent, because a bare `helm upgrade` reuses the
  previous values and so cannot express a deletion.
- **`docs/configuration.md`** gains three sections written from this release's investigation: finding out
  who last wrote a field (`managedFields`, and the `--show-managed-fields` flag without which `kubectl`
  strips it); whether a generated controller actually restarted (and why `generation`, `restartedAt` and
  `observedGeneration` do not answer that); and which changes reach a running controller versus which
  need a restart.
- The **0.59.0** section below gains a correction. It had said a `rdc.env` change rolls every generated
  controller, which was false, and a first correction then said one ConfigMap change produces two rolls,
  which was also false and wrong in the reassuring direction. Both were derived from reading the code
  rather than from watching a pod.

## 2026-11-11 — 0.59.0

> **Correction (2026-11-11, issues 595 and 599).** This section describes a generated controller being
> restarted "only when its ConfigMap data changed". **On this release it is never restarted at all.**
>
> `Deploy` builds one `NamespacedName` — `"<rd>-controller"` — and uses it both for the Deployment and for
> the live ConfigMap read that the restart decision depends on. But the two assets render their names
> differently: `assets/rdc/configmap.yaml` renders `{{ .name }}-configmap` while
> `assets/rdc/deployment.yaml` renders `{{ .name }}`. So the read asks for a ConfigMap at the
> **Deployment's** name, gets `NotFound` on every reconcile of every kind, and concludes there is no
> previous ConfigMap — which the code deliberately treats as a first deploy needing no restart. The
> decision therefore returns "no" unconditionally (issue 599).
>
> **The failure is silent and looks like success.** The ConfigMap *is* updated, `helm upgrade` reports
> success, and no error, event or condition is produced anywhere. But `envFrom: configMapRef` is read once
> at container start, so the running controller keeps its install-time environment indefinitely. Anything
> travelling through `rdc.env` is affected: log level, OTEL settings, timeouts, any operator-supplied
> variable.
>
> Measured by a consumer on a live cluster: 25 generated controllers, two `rdc.env` changes, 400s settle
> after each. Every ConfigMap's data changed, written by the provider itself. `deployment.kubernetes.io/revision`
> stayed at 1 for all 25 across both changes — 50 of 50 rows with zero rollouts — one ReplicaSet per
> Deployment created at install, every pod still at `restartCount 0` with its install-time start time, and
> **zero** occurrences of the restart log line in a fully covered window.
>
> **WORKAROUND on this release:** delete the generated controller's pods (`kubectl delete pod -l
> app.kubernetes.io/name=<kind>-controller -n <namespace>`) after changing `rdc.env`. The replacement pods
> read the updated ConfigMap at start. Upgrading to 0.60.0 removes the need.
>
> **A second defect, which this one was hiding.** `kube.Apply` performs a full `Update` with the *rendered*
> Deployment, and the rendered asset carries **no annotations at all** — so every `Deploy` strips whatever
> `kubectl.kubernetes.io/restartedAt` the live object holds (issue 595). That cannot be observed here,
> because nothing ever writes the annotation in the first place; it would have cost a second, pointless roll
> per ConfigMap change once the gate started firing. Both are fixed together in 0.60.0, because either fix
> alone leaves the behaviour wrong.
>
> **This release is a regression on this axis, and the earlier wording understated it twice.** 0.58.0
> restarted on *every* reconcile reaching `Deploy` — noisy, and it lost the pod's logs, but the change did
> arrive. An earlier version of this correction said one ConfigMap change produces "two rolls"; that was
> wrong in the reassuring direction, and it was wrong because it was derived from reading the code rather
> than from watching a pod.

**Two defects that made an upgrade noisy and a controller's logs unreadable.** Neither is new in 0.57.0 — both predate it — but 0.57.0 gave each a reason to fire often enough to be noticed, and both were found by consumers measuring their own upgrades rather than by any check of ours.

### A successful reconcile no longer reports an error

Every reconcile that persisted status logged `cannot update managed resource status: ... the object has been modified`, on a pass that had succeeded (#579).

provider-runtime reads the managed object **once** at the top of a reconcile, hands that same pointer to `Update`, and after we return writes its own status onto it — through a helper that calls its function once and does **not** retry on conflict. Our `persistStatusWithRetry` wrote through a freshly read object, so the server's `resourceVersion` moved and the runtime was left holding the one it had read. Its write then failed, the reconcile returned an error, and its condition write for that pass was lost.

**Deterministic, not racy, which is how it was found: one conflict per definition, every time.** Measured on the 0.57.0 upgrade as **25 of 25** kinds by one consumer and **4 of 4** by another. An exact one-to-one ratio is the signature of something structural.

```
persistStatusWithRetry occurrences in restdefinition.go
  0.54.0  6      0.55.0  6      0.56.0  6      0.57.0  6
```

Unchanged, so this is not a 0.57.0 regression. #549's `generatorBuild` stamp is only what made **every** kind take the status-writing path at once on an upgrade.

**If you built tooling that tolerates this, you can retire it after upgrading** — but retire the *tolerance*, not the check. The benign signature was that message **plus** a preceding `Updated RestDefinition` for the same definition; "any other error is a failure" is the half doing the work.

### A generated controller is no longer restarted on every reconcile

Each change to the set of Secret names a kind's `secretRef` resolvers reference restarted that kind's controller, losing its logs (#588).

The restart was **unconditional**. Its comment said *"Deployment needs to be restarted if the hash changes to get the new configmap"* and no hash was ever consulted. Once #510/#527 made a secretRef Secret-name change a reason to redeploy, that became one restart per CR referencing a new Secret, or per CR that was the last to reference one — **a per-CR action with a per-Kind blast radius.**

A consumer measured **8 Deployment revisions in 15 seconds**, against 2 across a full day on 0.56.0. Their falsifier then confirmed it in both directions with the ConfigMap's resourceVersion **and** data hash provably identical across the restarts — which refutes the comment using the comment's own stated justification.

**Only the ConfigMap's data can require a restart**, and the mount style is why: the generated controller reads it through `envFrom: configMapRef`, so the values are fixed at container start *and* the reference is by name. A data change therefore leaves the pod template untouched, and the Deployment has nothing to roll on. Everything else the pod consumes — the image, explicit `env:`, volumes, resources — is *in* the pod template, which a Deployment rolls by itself. RBAC needs no restart at all: authorization is evaluated by the apiserver on every request and the controller caches no decision.

**The readiness check is removed deliberately.** `if dep.Status.ReadyReplicas == dep.Status.Replicas` was written as a guard against restarting something already broken, but with an unconditional restart above it, it was the only brake on a restart storm — and it braked *less* the healthier the controller was, since a mid-roll pod is not ready. A ConfigMap change arriving while a pod is still rolling is exactly when a restart is required, because that pod may have started before the change.

**What still restarts, so this is not read as "restarts stopped":** a `rdc.env` change such as `REST_CONTROLLER_DEBUG`, which renders into the ConfigMap's `data`. That is a real environment change and every generated controller must roll to pick it up.

### Runtime evidence for #588 is outstanding

Stated rather than left to be inferred from a green release. The defect was measured on a cluster; **the fix has not been.** It is supported by unit tests — six table rows with both outcomes present, a mutation run killing both a revert-to-always-restart and an over-fix-to-never-restart, and a structural test that RBAC cannot reach the decision — and by nothing that has executed against an apiserver.

The arm that matters is not the one confirming the defect is gone. It is the opposite: **a real ConfigMap change must still roll every controller.** If it does not, this release silently leaves controllers with stale environment, which produces no error and no restart to notice. That arm runs on these released images.

### Also in this release, with no behaviour change

`docs/configuration.md` gains two sections: how to force a reconcile when measuring something — the poll interval is **3 minutes** by default, so a shorter wait measures your own patience — and why any timed measurement needs the cluster's control plane checked first, since object reads stay correct under a starved `kube-controller-manager` while timing and garbage collection do not.

## 2026-11-11 — 0.58.0

**A parameter the controller supplies is no longer demanded from the author — and a `secretRef` whose value would land in a request URL is now refused at generation rather than warned about.** One behaviour change, cut as its own release because a consumer's test run was blocked on it.

### A resolver-supplied path or query parameter is no longer required — BREAKING for a CR that set one

A path or query parameter whose value comes from a resolver — a `dependentLookup`, a `secretRef` — and which declares **no** `inCustomResource` stayed `required` in the generated CRD. The author was asked to supply a value the controller supplies, and a CR that correctly omitted it was rejected at admission (#555).

Measured by keycloak-provider-kog on a real Keystone kind, `PUT/DELETE /projects/{project_id}/groups/{group_id}/roles/{role_id}` with all three ids from `dependentLookups`:

```
released 0.57.0   spec.required = configurationRef, group, groupDomain, group_id,
                                  project, projectDomain, project_id, role, role_id
0.58.0            spec.required = configurationRef, group, groupDomain,
                                  project, projectDomain, role
                  project_id / group_id / role_id gone from `properties` too, not only
                  from `required`; a names-only CR is admitted by server-side dry-run
```

**The field is removed from `properties`, which is where the upgrade consequence lives.** A CR that previously set one of these values does not get an error — the apiserver **prunes** the value silently, because the field is no longer in the schema. The same class of change already produced that warning text for `fieldMapping`-supplied parameters; this extends it to resolver-supplied ones.

**Before upgrading, check whether any live CR sets a parameter its resolver supplies.** The generated CRD's `status.schemaWarnings` names every parameter that was omitted and why, so the list to screen against comes from the definition itself rather than from guesswork. One consumer screened five affected kinds and found none setting a pruned field; that is the expected result, since such a value was never read.

**A resolver with a `when` guard is deliberately NOT treated as supplying the value.** The guard means it may not fire, so the parameter stays settable. That distinction is the whole reason the generator now carries the guard's presence rather than inferring from the resolver's type alone.

### A `secretRef` bound to a path or query parameter is refused at generation — BREAKING

A `secretRef` resolver whose destination is a path or query parameter puts a secret **into the request URL**, where it reaches access logs, proxies and `Referer` headers. There is no safe way to generate that, so generation now fails, listing every offending entry sorted rather than stopping at the first.

**Refusal rather than a warning, deliberately.** A warning would leave a RestDefinition that looks like it works and leaks on every request. If generation now fails on a definition that previously generated, the parameter was carrying a credential in a URL and the fix is to move it into the request body or a header.

### Also in this release, with no behaviour change

A 68.91 MiB compiled binary was tracked at `go/rest-dynamic-controller/rest-dynamic-controller` — the default output of `go build` run in its own package directory, which no `*.ext` ignore pattern could match. It is untracked and the in-place build outputs are now ignored by exact anchored path (#564, Part 1). Verified unreferenced first, with three controls, because "0 references" is the claim that licenses deleting it; both images build their own binaries inside their Dockerfiles. **This removes it from the tip, not from history**, so a shallow checkout is smaller while a full clone and any `git log -S` credential sweep still reach the blob. The history rewrite is a separate, deliberate decision.

And the request-body suppression rule is now asserted (#582). `Call` declines to send an empty **map** as a body — which a consumer's Keystone kind depends on, via `requestTransform: '{}'` — while deliberately sending an empty **array**, because `[]` is a well-formed no-op JSON Patch that a vendor expects to receive. Before this, that asymmetry existed in one file and in zero tests, one tidy-up away from breaking either consumer.

### What was and was not exercised

```
EXERCISED  two real fixtures on a live cluster, against a control on released 0.57.0
           an 11-kind regression: 11/11 Ready, 18/18 CRD schemas identical to 0.57.0
NOT        the `when`-guarded arm (a guarded resolver leaves the parameter settable)
NOT        the secretRef-in-URL refusal
NOT        any vendor call -- the verdict was the generated CRD, so no Keystone or
           Keycloak request was made
```

The two unexercised behaviours have unit tests and nothing more. Stated here rather than left to be inferred from a green release.

### Known cosmetic issue

The informational omissions above surface in `status.schemaWarnings` prefixed `generation error: …`, which reads as a failure on a RestDefinition that is `Ready=True` and working as intended. The prefix, not the warning, is wrong.

## 2026-11-11 — 0.57.0

**Two credential leaks into the controller log, one of which needed no opt-in flag; two breaking changes to how a path is read and where RBAC comes from; and an upgrade that could record a regeneration it had skipped.** Both leaks were found by consumers on published releases, and the regeneration defect was reproduced on demand by one.

### A verbose dump no longer writes the vendor's response to the log

`redact` had exactly **one** call site, on the request. The response was written verbatim, so a vendor that echoes a secret back put it in the controller log in cleartext. Measured on published 0.56.0: a client with a `clientSecretRef` showed `***REDACTED***` in the request and its resolved secret in the findby response — and because that vendor's admin API returns every confidential client's `secret` in plaintext, so would every **other** client's in the realm (#566).

**Wider than reported, in two ways.** The report named the pretty-JSON dump; `httputil.DumpResponse` leaked identically, so an operator who turned pretty JSON off was no safer and a fix to one branch would have looked complete. And `redact` also matches credential **header names**, so response headers came back untouched — a `Set-Cookie` session token or an echoed credential header reached the log.

Three leaks, and only two are value-redactable:

```
the resource's OWN resolved secretRef value, echoed back   -> in RedactValues; now redacted
OTHER objects' secrets in a collection response            -> NOT known values. No value-based
                                                              rule can ever match them.
response headers                                           -> now redacted
```

The second is answered differently: **a collection read does not have its body written at all.** `FindBy` marks the call — the single entry point for both the paginated and single-call paths, and never `Call`, which also serves get, create, update and delete. The omitted body leaves a notice stating the **reason**, because a body missing without explanation reads as an empty response, which is a different and misleading diagnosis.

### And the vendor's error text is redacted too — a channel that needed no flag

`logVendorBody` writes the vendor's error body at **INFO on every failed call**, with no `krateo.io/connector-verbose` required, and Info is deliberate so that it is visible on a default install. **So this was the wider of the two leaks while being the narrower in content.** `VendorBodySnippet`'s own comment already stated the rule — *"never log a credential at any level: Krateo exports logs to the platform's log store, so a log line is narrower than a condition but is not private"* — and nothing enforced it. Both construction sites now redact, `Call` and the pagination path (#567).

**Neither leak is a regression.** Both are as old as the code paths they sit in. **For anyone upgrading from 0.56.0 or earlier: logs captured while `connector-verbose` was on, and logs containing vendor error bodies from any version, should be treated as containing whatever those responses carried.**

**What redaction still does not cover**, stated so it is not over-read: a value is redactable only if a `secretRef` resolver produced it. A credential that reaches the request any other way is not in the list. Redaction is a backstop for the mechanism that keeps credentials out of the CR, not a general-purpose scrubber — and the stronger mitigation, confirmed by a consumer, is that a secret parameter absent from the generated schema cannot be carried in cleartext by a CR at all.

### One walker for custom-resource reads — BREAKING

The provider's RBAC collector and the controller's read walked the same paths with different walkers, so a grant and a read could select different elements. rest-dynamic-controller now uses plumbing's typed walker, and three spellings change behaviour (#540, #544):

```
a.0      against an ARRAY  -> REFUSED (was resolved).  The dotted index.
a['0']   against an ARRAY  -> REFUSED (was resolved).  A quoted KEY, used to index.
a[0]     against a MAP     -> REFUSED (was resolved).  An index, against something with no indices.

a[0]     against an ARRAY  -> STILL WORKS, unchanged
a['0']   against a MAP     -> STILL WORKS, unchanged
spec.a.b plain map traversal, any depth -> STILL WORKS, unchanged
[?key=value] predicate selectors        -> STILL WORKS, unchanged
```

**So a bracketed index is NOT in itself a problem, and plain dotted paths are untouched.** Only two of the three refusals can be spotted from the path alone — the **dotted numeric segment** (`a.0`) and the **quoted numeric key** (`a['0']`). The third, `a[0]` against a map, depends on the shape of the data at that point and cannot be decided from the text; a screen flagging every `[N]` will report paths that are fine.

`a.0` and `a['0']` parse to byte-identical segments, so this could not be fixed without dropping the dotted index.

**What to look for: a dotted numeric segment (`a.0`) or a quoted numeric key (`a['0']`) — not every bracketed index.** An earlier version of this note said "a dotted numeric segment or a bracketed index", which is over-broad: `a[0]` against an array is unaffected, and a screen keyed on it sends readers to inspect working paths.

**Before upgrading, screen every value that is a path INTO the custom resource — and screen it BY KEY NAME anywhere in the document, not by walking a list of locations.** The complete key set, derived from the API types rather than written by hand:

```
inCustomResource              customResourceFieldPresent     (resolver.when)
nameFromCustomResource        customResourceFieldAbsent      (resolver.dependentLookup.when,
keyFromCustomResource                                         and dependentLookups[].when)
equalsFromCustomResource      plus the identifiers[] and additionalStatusFields[] lists
```

`inPath`, `inQuery`, `inBody` and `inResponse` are API-side and are NOT affected — a useful negative control for any screen.

**Screen by key name because the locations are the part everyone gets wrong.** These keys live in at least five places: inside `fieldMapping[]`, inside the deprecated-but-live `requestFieldMapping[]`, under a `secretRef` resolver, under **two different** `when` shapes with **different key names** (`ResolverWhen.customResourceFieldPresent` at `resolver.when`; `LookupWhen.customResourceFieldAbsent` at `resolver.dependentLookup.when`), and inside `dependentLookups[]`, which is its own field on `spec.resource` and not nested in `fieldMapping` at all.

**The location list in this note was wrong three times before it was replaced by the rule above** — it initially named four keys and missed the whole `dependentLookups` subtree, then missed `equalsFromCustomResource`, then missed `customResourceFieldAbsent` and the second `when` position. Each gap was found by a consumer measuring their own corpus, not by reading it again. A key-name screen cannot have that failure mode, because it does not need to know where the key lives.

**And plant the positive control inside the subtree you are worried about.** One corpus's first screen printed zero hits while never looking at the key most likely to carry the dotted form: it excluded jq subtrees with a *substring* match on `transform|jq|script|body`, and `script` matches **`verbsDescription`** — "De-**script**-ion" — so the subtree holding every `requestFieldMapping` was skipped. It was caught only because a planted hit sat inside `requestFieldMapping` and the run returned 2 of 3. Anchor the exclusion (`^(.*transform|jq|script|.*body)$`), and plant the control where the risk is, not at the top level.

Parse the rendered definitions rather than grepping for paths, because `[0]` inside a `responseTransform` jq body is not a path — though note a bare grep for a key NAME is immune to the subtree-skipping bug above, and one corpus's grep was the more honest instrument for exactly that reason.

Consumer corpora measured clean: 24 of 24 kinds live, 0 of 669 path-valued strings, 0 across 10 documents including path-item-level parameters, and **one corpus re-screened against the complete key set above** — 25 definitions, its single `dependentLookups` entry walked (all CR paths plainly dotted, 0 indexed forms), `customResourceFieldPresent` unused, with controls planted inside both `dependentLookups[].parameters` and a `resolver.when` returning 2 of 2. The other cleans predate the complete key set and cover the `fieldMapping` family only.

### rest-dynamic-controller stops granting itself RBAC — BREAKING

**A subject cannot grant itself less than nothing.** Self-imposed least privilege needs a broader grant to narrow *from*, and 0.54.0 removed exactly that. The controller's self-provisioning of per-CR `secretRef` Roles is gone; the provider does it from the generated Kind's instances, as shipped in 0.56.0. Teardown of Roles left by older releases is kept, so an upgrade still cleans up after itself (#540, #571).

### An upgrade can no longer record a regeneration it skipped

`status.generatorBuild` was stamped **before** the CRDs were rendered. So a reconcile that took the not-yet-deployed path stamped the new build, `generatorChanged` then compared equal forever, and the CRD kept the old schema with no error anywhere — a provider reporting the new build while serving the old schema. Reproduced on demand by a consumer (delete the per-kind controller Deployment, then upgrade) and confirmed on two corpora. The stamp now happens only after a successful render (#541, #549).

**Do not reproduce it by scaling those Deployments to zero**: `*Spec.Replicas == Status.ReadyReplicas` makes `0 == 0`, so a scaled-to-zero Deployment reads as **ready**, not absent (#550).

### Verifying the upgrade

`status.generatorBuild` should read `0.57.0-<sha>` on every RestDefinition afterwards — the tag joined to the merge commit, since `BUILD_VERSION` is `<ref_name>-<sha>`.

**Do not read the render half off the CRD's `resourceVersion`.** None of this release's changes touch the generator, so a correct render produces byte-identical CRDs and the apiserver does not persist a no-op update — every `resourceVersion` legitimately stays put. The channel that does carry it is the provider's own per-kind `Applying CRD` / `Applied CRD` log pair, which is emitted on a default install because the chart hardcodes `--debug`. A kind with neither line is the defect; a kind with `Applying` and no `Applied` is a failed apply. Count provider restarts too — a restart discards earlier lines, and "no line because nothing rendered" and "no line because the log was truncated" are otherwise indistinguishable.

### What this release does NOT contain

```
#555   a resolver-supplied path or query parameter is still REQUIRED in the generated spec
#548   a CR deleted inside the create window can still leave an enabled orphaned external resource
#543   AUDIT-4 conformance for rest-dynamic-controller and oasgen's RBAC/admission writes
#568   no test asserts that every client reaching the network carries its credential
#569   nothing screens tracked text for conflict markers
#564   a 69 MiB compiled binary is still tracked on main
```

### Upgrade notes

```
forward-only       two breaking changes; a downgrade after upgrading is not a supported path
order              CRDs chart first, then the provider
the dotted index   check your path-valued fields BEFORE upgrading (see above)
self-granted RBAC  if anything relied on the controller provisioning its own per-CR Roles,
                   that is gone; the provider's grant is now the only one
#545               the secretRef-RBAC change shipped as #571, not #545: GitHub closed #545
                   irreversibly when its base branch was deleted by #544's squash merge, and
                   the same commit was reopened as #571 rebased onto main
```

## 2026-11-09 — 0.56.0

**Every resource using a `secretRef` field mapping has been unable to reconcile since 0.53.0. This release fixes that, and makes a Secret-sourced field conditional so a resource that legitimately lacks one can exist at all.** Both halves were verified on a live cluster by the consumer who reported them.

### A secretRef Secret is readable again

0.54.0 removed a blanket cluster-wide secrets grant, on the stated grounds that the narrow per-Secret Role already covered the same ground. **It did not.** The narrow Role was built from Configuration-level authentication references only, so every Secret named by a `secretRef` field-mapping resolver lost its grant. A consumer measured **25 of 36 kinds no longer reconciling** (#510).

rest-dynamic-controller had been provisioning that Role itself, and cannot: **a subject cannot grant itself less than nothing.** Self-imposed least privilege needs a broader grant to narrow *from*, and that is what 0.54.0 removed. The provider now does it (#527):

```
collected from   every live instance of the generated Kind, grouped by namespace -- because
                 secretRef names live in the CRs, not in the RestDefinition, so a per-definition
                 collector cannot size the grant
Role name        <plural>-<group>-v1alpha1-secretrefs, SEPARATE from the auth Role's
status record    status.secretRefRBACNamespaces, SEPARATE from the auth path's
provisioned at   Create and Update; revoked when a namespace's last instance goes; torn down on
                 RestDefinition delete -- the first two are measured, the third is not (see below)
```

**The separation is load-bearing rather than tidiness.** The shared provisioner revokes the Role in every namespace that is in the caller's record and no longer in its collected set — so two callers sharing either the name or the record would delete each other's namespaces on every reconcile, with a symptom (a credential readable one moment and `Forbidden` the next) that no single-caller test can observe.

**Lenient about a resource's data, strict about a definition's.** An instance whose paths cannot be read contributes no names and is logged, because one malformed resource must not cost every other resource of the kind its credential. A path that is not a valid field path at all affects every resource of the kind, so it fails the reconcile.

**Four Debug lines distinguish the cases a narrow Role hinges on.** An absent credential *entry* and a present entry with an absent *reference* both read as "nothing there" at the leaf, so the collector resolves the path's **anchor** — the prefix up to its last selector — to tell them apart.

**Timing, measured on a cluster**: 8 resources across 7 namespaces provisioned in **62 s**; a deleted resource's Secret revoked in **101 s**; a hand-deleted Role reprovisioned in **184 s**.

**The teardown figure needs a caveat, which a consumer found by asking how to reproduce it.** A RestDefinition delete is REFUSED while instances of its generated Kind exist — correctly, and the code states the contract: *"the RestDefinition keeps its finalizer and the CRD, its CRs and their external resources all survive."* So the secretRef Role cannot coexist with a RestDefinition delete in progress: by the time the delete proceeds, the last instance is gone and the Role has already been revoked by the namespace path.

**The measured 6 seconds is therefore an ABSENCE, not a removal** — it shows the Roles were gone 6 s after the RestDefinition delete, not that the teardown removed them. **The teardown code path for the secretRef Role is unobserved**, and structurally unobservable from outside for the reason above. The 6 s applies to the auth Role, which has no such constraint.

### A Secret-sourced field can now be conditional

A `secretRef` resolver was unconditional. A kind mapping two credentials from two Secrets — a password and a one-time-password seed — could therefore never create a resource carrying only one of them: the absent mapping's path was unresolvable and the create hard-errored (#524).

```yaml
resolver:
  type: secretRef
  when:
    customResourceFieldPresent: spec.credentials[?type=otp]
  secretRef:
    nameFromCustomResource: spec.credentials[?type=otp].secretDataSecretRef.name
```

```
no `when`                    unconditional -- every definition written before this is unaffected
the path RESOLVES            the mapping resolves, as today
the path is ABSENT or NULL   the mapping is SKIPPED: no Secret read, nothing sent
the path is AMBIGUOUS        an ERROR, naming the guard
```

**An entry present with its reference missing still fails loudly**, because the guard is satisfied and the reference becomes required. **Only a missing entry skips.** The distinction is the point: a guard that swallowed a malformed entry would turn it into a silently reduced resource, which is the outcome the guard exists to prevent.

**The guard suppresses the vendor call, not only the mapping.** The pass that decides which dependent lookups to run consults it too — without that, a guarded mapping still made its request and discarded the result, and a *failed* request would have failed the whole reconcile for a mapping the definition says does not apply.

### What this release does NOT contain

```
prompt revocation      provisioning and revocation are bounded by the drift poll -- 3 minutes at
                       the default OASGEN_PROVIDER_POLL_INTERVAL, 101 s measured. Nothing watches
                       the generated Kind, so a deleted resource's Secret stays readable until the
                       next poll. The DELETION direction is the security-relevant one. (#510, open)
an explicit field manager   every Role is applied with no FieldManager, so the API server derives a
                       generic name. A consumer's managedFields read shows manager="manager".
                       Two writers that both omit one can collapse into a single entry, which is
                       exactly what a provenance read is meant to detect. (#510, open)
a lowercase kind       a RestDefinition whose spec.resource.kind is not capitalised generates a CRD
                       that nothing reconciles: the controller's definition lookup compares the raw
                       spelling against the served Kind. The provider half is fixed here; the
                       controller half is not. (#530)
generation-time validation   a resolver's configuration is validated only when a resource causes it
                       to be resolved, so a guarded mapping can hide a malformed definition
                       indefinitely. The skip decides whether we RESOLVE, never whether the
                       definition is VALID. (#533)
```

### Upgrade notes

**Move the provider and the CRDs chart together.** The release adds three CRD fields (`status.secretRefRBACNamespaces`, `status.secretRefDigest`, `spec…resolver.when`). If `oasgen-provider-crds` lags the provider, the API server **prunes** them: `kubectl apply` succeeds, the stored object lacks the field, and only the resource fails. For a kind with a `secretRef` resolver a pruned digest means a redeploy every poll **and** a revocation that silently becomes a no-op; for a kind without one, nothing. **Verify by reading the field back from the live object, not by checking that apply succeeded.**

**`status.secretRefDigest` is written for every kind**, including those with no `secretRef` resolver — the digest of an empty set is a non-empty constant. `status.secretRefRBACNamespaces` is omitted when empty.

## 2026-11-08 — 0.55.0

**One path parser instead of three, a new signal when a resource's absence was inferred rather than reported, and a generator fix that removes a workaround our own documentation was carrying.** Nothing here changes the behaviour of a working RestDefinition; two of the three are about making a silent failure visible.

### Three implementations of one path dialect became one

A field path — `spec.auth.secretRef.name`, `a['key.with.dots']`, `items[0].id` — was parsed by **three separate implementations** across the two modules and a shared library, with **three different accepted grammars and no test comparing them**. Measured, 20 inputs through two of them: **8 disagree.**

That divergence is why a provider-side resolver and the controller could disagree about what a path addresses, which is the root of a release-blocking RBAC regression (#510) and of the generator defect below. The parser now lives once, in `plumbing/fieldpath`, written from this repo's specification and its disagreement table (#513, #515, #516, #520).

**For rest-dynamic-controller this is a refactor with five stated changes**, none of which affects a path in any shipped example:

```
""              was ([""], nil) -- a usable-looking value that passed every length guard and then
                addressed a property named "". Now an error.
".id"           the leading dot was KEPT, so the path addressed a property literally called ".id".
                No document has one, so it resolved to nothing, silently, on every reconcile.
                Now stripped. This is a FIX, not a compatibility break.
a['my key']     a space anywhere used to reject the whole path. Whitespace is now allowed where it
                is delimited -- inside quotes or a selector -- and still refused in a bare name.
a.['b']['c']    adjacent bracket groups were refused; a bracket group is self-delimiting.
['']            an explicitly quoted empty key is a key. An unquoted a[] is still malformed.
```

**For oasgen-provider the no-dot spelling `a['b.c']` starts working**, which its own documentation had promised and its parser refused — while the spelling that worked, `a.['b.c']`, was undocumented. **Existing definitions using the dot spelling are unaffected.** Array indices and `[?key=value]` selectors are now refused *at the path*, with a message naming the reason and a working escape, because every path in the generator addresses the property names of a schema and a schema has no indexed elements.

Both adoptions **removed** a third-party dependency rather than adding one.

### A resource can now tell you its absence was inferred, not reported

A `findby` that lists successfully and matches nothing is treated as "the resource does not exist", and a create follows. That is the right default — it is how every findby-only kind reaches its first create — but it is **indistinguishable from a resource that exists and could not be matched**, and the second case creates a duplicate at the vendor (#485, #511).

The resource now says which it was:

```
first inference    Normal event. At first contact there is no id to GET, so an unmatched listing is
                   the only possible answer. Nothing is wrong.
a repeat           Warning. Absence was inferred AGAIN for the same spec generation, so the create
                   that followed the previous inference did not make the resource findable.
```

**The severity follows the count rather than the verdict**, because a consumer measured the first version of this on a live fleet and found **5 of 5 emissions were healthy first creates and none were pathological** — a Warning on every healthy resource of every findby-only kind is one readers learn to skip, and then it cannot do its job in the case it exists for.

There is a second destination for the same verdict: when a create is already pending, the controller reports exists-and-up-to-date **without observing anything**. A consumer's fleet sat in that state for **318 consecutive observes with 0 creates** — findby was issued, returned 200 with a full list, and no item carried the identifier because no `responseTransform` existed. Nothing was failing, which is why nobody looked. That path now reports once after a consecutive-repeat budget, so a create in flight stays quiet and a latch that will never resolve does not.

### A bracket-escaped binding is recognised as supplying its parameter

A parameter whose **name** contains a dot must be bracket-escaped in a `fieldMapping`, because an unescaped dot reads as a path separator:

```yaml
inQuery: '["searchCriteria.sourceRefName"]'
inCustomResource: spec.sourceRefName
```

The supplied-parameters map was keyed on that **raw string** while the lookup used the **OAS parameter's name**, so they could never match. The exemption never fired and the parameter appeared as a spec field alongside the one the author meant to expose — forcing **"exclude *and* bind"** for something that is bound (#514, #522).

**Our own `docs/REAL_EXAMPLES.md` carried that workaround**, listing two escaped names in `excludedSpecFields` *and* binding both. So the generated spec came out clean and the missing exemption was invisible to every reader of the example. Those two exclusions are now redundant; the other ten in that list are genuinely unbound and remain necessary.

The comparison is **asymmetric** on purpose: a parameter name is atomic, a mapping value is a path, so a mapping value names a parameter only when it parses to exactly one segment. A dotted name written *without* brackets is ambiguous and keeps the old behaviour — this can only turn a non-match into a match, never the reverse.

### A false warning on a content-filter mapping, fixed before release

The path refusal above shipped with a claim that was wrong: that **every** path the generator parses addresses the property names of a schema. **`inCustomResource` does not** — it is parsed for a validation screen, but what it addresses at runtime is a **CR instance**, which can hold arrays and which the controller walks with a walker handling both indices and `[?key=value]` filters.

A consumer found it within an hour of the refusal merging, by grepping their own definitions. Theirs uses `spec.credentials[?type=otp].secretDataSecretRef` in sixteen places, and the generator would have reported it as *"not a parseable field path"* while advising the author to *"point inCustomResource at a field the CR carries, or remove the mapping"* — **about a path the controller resolves correctly.** The kind still installed; the advice would have broken it.

**Fixed before tagging rather than shipped and patched.** Schema paths still refuse indices and filters, where they genuinely cannot address anything; document paths accept the whole grammar.

### Not in this release

**#510 (every secretRef resource fails on 0.53.0+) is NOT fixed here.** This release lands its prerequisite — the provider and the controller can no longer disagree about a path — and nothing more. The provider-side collector, the generated-Kind watch and Role provisioning are unwritten. If your resources use `secretRef`, **0.52.0 remains the last release on which they reconcile.**

Also open and not addressed: #517 (an index-path identifier can never match, because twelve walker call sites cannot follow the segments the parser produces), #519, #521, #486, #508.

## 2026-11-07 — 0.54.1

**A patch release for two defects 0.54.0 introduced. If you are on 0.54.0 and any of your resources authenticate with an apiKey header scheme, upgrade: on 0.54.0 they cannot read their credential and do not reconcile at all.**

### No apiKey-authenticated resource could read its Secret

The per-resource `Role` that scopes a generated controller's Secret access is built from the Secret references collected off `Configuration` instances. **That collection covered `basic` and `bearer` only.** The generator produces a third shape — `apiKey` in a header, whose credential lives at `spec.authentication.apiKey.tokenRef` — and it was never collected, so no `Role` was provisioned in the Secret's namespace.

**0.54.0 is where that became fatal.** Until then the shipped RDC `ClusterRole` granted `resources: ["secrets"]` cluster-wide, so an incomplete narrow `Role` cost nothing. 0.54.0 removed that blanket grant (#473, #475) on the stated reasoning that the narrow `Role` already covered the same ground — **true for `basic` and `bearer`, never true for `apiKey`.** The failure is at observe, so an affected resource never reaches `Ready`.

That release note said removing the grant "is self-healing on upgrade, and for most installs it changes nothing". **Read it now as: for installs whose resources authenticate with `basic` or `bearer`.**

Measured on released 0.54.0 images by a consumer whose fleet is **25 apiKey kinds and 11 bearer**:

```
can-i get secrets -n default        no
can-i get secrets -n krateo-system  no    <- not a namespace-scoping problem
can-i get the CR's own kind         yes   <- the ClusterRole was fine
```

**There is no consumer-side workaround.** No namespace holds a `Role` the ServiceAccount could use, so moving the Secret does not help.

**The fix removes the possibility rather than the instance** (#500, #501). The reference paths are now *derived* by reflecting over the very types the authentication schemas are rendered from, so the collector and the generator can no longer disagree. Adding a fourth scheme without its credential path now fails a test that is written over the space of schemes rather than over a list of keys.

**Why no fixture caught it:** that consumer's fleet carries both shapes against one vendor — 11 documents declare `http/bearer`, 25 declare `apiKey` in a header, several sending an identical `Authorization: Bearer ` value either way. A suite with one scheme per document passes while a collector handles a subset, however many documents it has.

### `status.schemaWarnings` was never written on a fresh install

The field added in 0.54.0 reached the log and a Warning Event but **not status**, on any newly created RestDefinition — so a new install showed a clean fleet while carrying broken declarations, which is the state the field exists to make visible. Create's first phase wrote a hand-listed set of fields onto a freshly re-read object, and the warnings were not in that list (#492, #493).

**Upgrades were never affected.** Measured: on 0.48.0 → 0.54.0 the `generatorBuild` change re-renders every RestDefinition through the Update path, which writes the whole status. So:

```
upgraded fleet, status.generatorBuild reads 0.54.0 or later   schemaWarnings is trustworthy
fresh 0.54.0 install                                         it is NOT -- read the provider log
fresh 0.54.1 install                                         trustworthy
```

The field's documentation now also states what its serialised form cannot say: **absent means either "no warnings" or "not rendered by a version that writes the field"**, and `status.generatorBuild` is what separates them.

### Not fixed here

Both of these were found by consumers installing the release on real clusters, after it passed a five-artifact supply-chain sweep and two green CI suites. The open reports from the same exercise — a `Deploy` write that overwrites `spec.replicas` (#495), a generated `Configuration` CRD that is never uninstalled (#496), an informational notice labelled `generation error:` (#497), a findby's optional query parameters becoming spec fields (#498), Swagger 2.0 being generation-only in RDC (#499), and a resource that always exists being unable to model delete as a reset (#502) — are **not** in this release.

## 2026-11-07 — 0.54.0

Five changes. **One stops a generator upgrade from writing values into your CRs that nobody authored**, one puts the current render's schema warnings on status, and the rest make the generator's warnings mean something — two new ones that catch real mistakes, and two old ones silenced because no author could act on them.

### A vendor's `default` is no longer materialised onto an optional parent

0.53.0 took plumbing to v1.27.0, whose `crdgen` gives an optional object `default: {}` when something beneath it is defaulted, so the apiserver materialises the parent and the nested defaults apply. **That behaviour was designed for Helm `values.schema.json`, where a `default` is an instruction.** In a vendor's OpenAPI document a `default` *describes what the server assumes* — and materialising it turns documentation into a value the controller transmits, because rest-dynamic-controller sends top-level spec values whole.

This release sets `crdgen`'s `NoParentDefaults`, which the library added for exactly this case (#488).

**Why it matters more than a shape change.** Measured on a live apiserver by a consumer: patching **only the CRD** makes an already-stored object read back with the defaulted children and **no etcd write** — `resourceVersion` unchanged. One unrelated write then persists them. **Reverting the CRD does not remove them**, and `f:properties` never enters `managedFields`, so the value is present and unowned and `kubectl apply` will not prune it. `generation` never bumps either, because incoming and stored are both defaulted before comparison. **There is no artifact to diff and nothing keyed on generation can see it.**

The worst measured payload is a property literally named `default`, documented *"Only one default vpc for region is admissible"* — so a CR whose author omitted the parent would assert a region-unique role.

**If you are on 0.53.0 and your documents carry the shape, this release does not undo what is already stored.** The opt-out protects renders from here on; a value already persisted by an unrelated write is indistinguishable from an authored one. Consumers whose vendor documents have defaults under optional parents should check before upgrading rather than after.

**One consumer's fleet measured 240 stamped paths across 36 definitions, and 238 of them were on the Configuration CRD** — the one you cannot see in `spec.resource`. Every definition, every verb, through the intermediate objects the generator builds itself with no `required` list, bottoming out in a query parameter whose vendor default is `api-version`. The remaining 2 were resource CRDs, and **exactly 1 of the 240 is a hazard today**: the region-unique `default` above. The other 239 materialise a benign value. The same measurement at this release reports **0**, with the 0.53.0 run as the baseline proving the instrument reports stamps when they exist. This repo's own 18 documents carry the shape **zero** times, which is why the golden CRD is byte-identical across the change — and why the test for it renders a purpose-built document instead of relying on the golden.

**This fixes one path and not the defect behind it (#471), and the distinction is the part to read.** The flag stops the generator stamping an *omitted* parent. It does nothing to the vendor's child `default` keywords, which are still copied into the generated schema as structural defaults — so an author who writes the enclosing object **themselves** still gets the siblings filled in:

| the CR says | reads back as | 0.54.0 |
|---|---|---|
| omits the parent entirely | would have materialised once stamped | **prevented** |
| `parent: {}` | every defaulted sibling | **unchanged** |
| `parent: {oneField: false}` | that field **plus** every defaulted sibling | **unchanged** |

The third row is the live one, it needs no upgrade and no re-render to occur, and it is why **#471 stays open**. Do not read this release as "oasgen no longer materialises vendor defaults": it removes a path plumbing v1.27.0 added, and restores the pre-0.53.0 shape for the omitted-parent case only.

### The current render's schema warnings are on status

`status.schemaWarnings` carries the generation and validation warnings from the **current** render (#462, #478). Previously they reached only an event, whose lifetime is an hour and whose text is truncated — so a warning about a CRD that is still wrong was gone while the CRD was still wrong.

It is written unconditionally, including to empty, so **an empty list means "this render produced none", not "nothing has run"**.

### A `fieldMapping` on one verb no longer strips the parameter from every other verb

A request-direction `fieldMapping` omits its parameter from the generated spec, because the mapping supplies the value. **That omission was resource-wide** (#457, #479): declaring it on `get` also removed the parameter from `create`, which still needed the author to supply it — so the CR had no field to carry it and creation failed on a parameter the document requires.

The omission is now per-action. A parameter is dropped from the spec only when **every** verb that declares it supplies it by mapping.

### Two new warnings that catch real mistakes

- **A request-direction `fieldMapping` whose source names nothing the CR will carry** (#458, #482) — the mapping is declared, the value is never resolvable, and the request goes out without it. Screens both verb `fieldMapping` and `dependentLookups[].parameters`.
- **A parameter the generated spec omits because a `fieldMapping` supplies it** is now reported when the mapping's own source is unresolvable (#409), so the two do not cancel into silence.

### Two warnings removed because no author could act on them

- **A field named in both `excludedSpecFields` and a `fieldMapping`** (#484). The mapping omits the parameter; the exclusion then finds nothing and reports itself inert — which is true, and true *because the mapping did the job*. Both declarations are correct, and `excludedSpecFields` is immutable, so there was no action behind the message. The suppression already existed in the other direction.
- **A documented 2xx that declares no body.** The vendor says success carries no content, so there is nothing to compare. A success carrying content we *cannot read* still warns — that one means something was returned and the generator could not use it.

Measured on a consumer's 32 definitions: **29 warnings → 5**. The 5 remaining are three "create response declares no properties" and two genuine type mismatches, all actionable.

## 2026-11-05 — 0.53.0

Eight changes. **One narrows a privilege, one makes a generator upgrade re-render your CRDs, and one adds a field to every generated CRD's status.** The rest are correctness fixes and documentation.

### The RDC ClusterRole no longer grants every Secret in the cluster

The per-Configuration auth-secret `Role` exists to scope a generated controller's credential access to exactly the Secrets its Configuration instances reference. **It scoped nothing.** The shipped ClusterRole granted `resources: ["secrets"]` cluster-wide, with no `resourceNames`, bound to the same ServiceAccount — so the narrow Role was a strict subset of a grant its subject already held.

That grant is gone (#473, #475).

**It is self-healing on upgrade, and for most installs it changes nothing** — the narrow Roles were provisioned all along, merely redundant.

**But not for every install, and this is the part to read.** A pair of RestDefinitions that collided on the pre-#440 group-less name can be in a state where one of them has *no* Role naming its Secret and no RoleBinding for its ServiceAccount, while still recording the namespace as provisioned. Its credential was reachable **only** through the blanket grant this release removes. Measured on a live cluster, on two `Form` kinds in different API groups sharing one `forms-v1alpha1-authsecrets` Role that named only the first one's Secret.

That is why the second change here is not optional: **`Observe` now checks the Role exists.** The old auth-secret check compared a digest of the secret *references*, and that channel cannot express "the Role is missing" — so the state above left `Observe` reporting up-to-date, with the repair reachable only from `Create`/`Update`, which the runtime calls only on drift. Nothing ever called it.

At 0.53.0 the name `Observe` looks up is group-qualified, so **both** colliding RestDefinitions report their Role missing, drift once, and are reprovisioned with their own Role naming their own Secret — the sync now running ahead of the deployment. The heal is pinned by a test built from that live starting state, including a mutation showing that a check made "tolerant" of the legacy name would silently restore the outage (#477).

**If you run a pair of RestDefinitions whose kinds share a resource plural across different API groups, and you are upgrading from below 0.52.0** (where #440's group-qualified name landed), expect one drift-and-reprovision per affected resource on the first reconcile after upgrade. If instead you have disabled the upgrade re-render, see the note under `status.observedGeneration` — the same flag gates this.

`syncAuthSecretRBAC` also moved ahead of the deployment, so the pod no longer starts before the Role authorising its first credential read.

### An upgrade now re-renders your existing kinds, once

`status.generatorBuild` records the provider build that last generated each CRD, and a higher build triggers one re-render (#453, #460). This is what makes a release's generation fixes reach kinds that already exist — previously they applied only to kinds generated after the upgrade.

**The trigger is directional.** It compares release triples only, so a *rollback* does not re-render: a downgrade re-rendering would prune fields from live CRs that the newer generator had added.

Default on. Set `OASGEN_PROVIDER_REGENERATE_ON_UPGRADE=false` to stage a large fleet and re-render deliberately — but note that generation fixes then do not apply until something else changes the OAS or `spec.resource`.

### Every generated CRD gains `status.observedGeneration`

Via plumbing v1.27.0 (#452, #474). Five additive lines on a subresource:

```yaml
observedGeneration:
  description: ObservedGeneration is the metadata.generation the controller last reconciled.
  type: integer
  format: int64
```

It cannot prune a spec value and cannot be materialised onto spec. **Measured across 12 plumbing minors, this is the only change to a generated CRD**, confirmed independently on 32 kinds by a consumer: 32/32 differing by exactly three leaf values, 0 changed paths under `spec`, and Configuration CRDs 32/32 identical.

**Nothing writes the field yet.** The declaration ships first by design. If you run with `REGENERATE_ON_UPGRADE=false`, your CRDs will not declare it, and when a future release starts writing it the value will be pruned silently — harmless until something reads it, at which point absent is indistinguishable from "never reconciled" (#466).

### crdgen's warnings now reach you

Since the generator was adopted, crdgen computed warnings about the CRD it was producing and `Generate` dropped them: nothing read, returned or logged them. `GenerateWithWarnings` exists as of plumbing v1.26.1, and they now land in the same channel as oasgen's own generation warnings — the controller's log and the RestDefinition's event (#452, #474).

The first one you are likely to see concerns a vendor `default` on a nested optional object:

```
the generated CRD: spec.opts: given default {} so its nested defaults apply when it is
omitted; the apiserver stores the defaulted values in every object that omits it
```

### A composite parameter value is refused instead of dropped

A request-direction `fieldMapping` whose value is an object or array was silently discarded for path, query, header and cookie destinations — the request went to the vendor without it. It is now refused with the destination and field named (#463, #464).

### A synthesised 404 says so

When a listing succeeds and nothing matches, rest-dynamic-controller invents a 404 to signal absence. Its message was indistinguishable from a 404 the vendor actually sent, which sent at least two investigations looking for a vendor response that never existed. The synthesised case now says so explicitly (#461, #465).

### A status conflict no longer discards a permanent-failure classification

A benign 409 while writing status overwrote the verdict that a failure was permanent, so a resource that should have stopped kept retrying (#468, #469). Adopting unstructured-runtime v1.9.0 is what makes this the common path rather than a rare one: at v1.7.6 `tools.UpdateStatus` retried a conflict by re-submitting the caller's stale status onto a fresh resourceVersion — **a lost update that was live for this component with none of its own code involved.** Measured: one `UpdateStatus` cost five apiserver writes on a persistent conflict at v1.7.6, and one at v1.9.0.

### Documentation

- **A vendor `default` becomes a real structural CRD default** — what the cluster stores for a field you never set, and why that matters when the vendor's own default later changes (#471, #472). The behaviour is deliberate and unchanged; it was simply undocumented.
- **Neither image has a shell**, so `kubectl exec` cannot work; the section says what to do instead (#311, #467). Both images are `gcr.io/distroless/static:nonroot` — rest-dynamic-controller was first and 0.41.0 brought the provider into line — so this applies to both, and `kubectl debug --target` is the escape hatch.
- **The resource CRD's api version is derived from the OAS document, and that derivation changed in 0.28.1** (#315, #467). The rule itself was already documented; that it *changed* was recorded only here in the log, and a changelog entry is not documentation. **An example, runbook or CI manifest written before 0.28.1 was correct when written and becomes silently wrong on upgrade** — the document did not change, the CR did not change, and the kind the cluster serves did. The `*Configuration` half is unaffected: it was `v1alpha1` then and is `v1alpha1` now.

## 2026-11-05 — 0.52.0

Eighteen changes, and two of them are upgrade consequences rather than improvements. **Read the
first two sections before upgrading**; the rest is a long run of silent-failure fixes.

### 0.51.0 broke a CR that 0.50.0 accepted, on a FRESH install. This release reverses it.

0.51.0 wired #410's guard, which refuses an `excludedSpecFields` entry naming a **required** OAS
parameter — so the field stayed in the spec and stayed in `required`. Measured on kind with 0.50.0
as the control, fresh installs both sides, teardown between:

```
0.50.0 clean install : CR applies, write reaches the vendor
0.51.0 clean install : The Sample "sample-1" is invalid: spec.id: Required value
0.51.0 via upgrade   : UNAFFECTED
```

The guard's premise is *"dropping a required parameter leaves a kind that cannot build its own
request URL"*, and that premise is **false when the status schema carries the value**:
rest-dynamic-controller fills request parameters from spec and then status, so a vendor-assigned id
placed in status by `additionalStatusFields` reaches the URL once create or findby populates it. The
guard already granted exactly this exemption to a request-direction `fieldMapping`; it was never
carried across to status. 0.52.0 carries it across (#455, #456).

This broke `examples/rdc-sample-resource`, which ships that configuration, whose README hands the
user the commands, and whose `resource.yaml` is the CR 0.51.0 rejects. A migration note would have
asked the author of a correct definition to change it.

**Why the upgrade path was unaffected, and why that is not reassurance.** Nothing regenerated:
oasgen does not regenerate an existing CRD on upgrade, because no stored hash covers the generator's
version (#453). **So a clean upgrade is not evidence a version is safe** — the same artifact on a
fresh cluster behaves differently, and the place that surfaces is disaster recovery, rebuilding from
manifests, which is the worst moment to discover a schema change. If you validated 0.51.0 by
upgrading, you did not test this.

**If you fresh-installed 0.51.0, this release alone does NOT fix your CRD.** The generator is fixed;
your already-generated CRD is not, because nothing regenerates on upgrade (see the next section). The
schema with `id` in `required` stays until something triggers a re-render. Force one by editing the
referenced OAS document and waiting out the poll interval — the procedure is below — or your CRs stay
rejected on a tag that fixed the bug. Upgrading from 0.50.0 or earlier is unaffected: those CRDs never
had the overreach.

### Generation fixes in this release do NOT reach your existing kinds

A consequence of #453 that applies to most of what follows. Upgrading changes the binary; it changes
nothing any reconcile compares, so generation never re-runs and an already-generated CRD keeps the
schema the old binary produced.

What DOES follow a chart upgrade: the per-RestDefinition rest-dynamic-controller **Deployment and
its image**, because that is rendered from a mounted asset whose content changes with the chart. So
this release's runtime fixes arrive and its generation fixes do not.

To adopt a generation fix on an existing kind, edit the referenced **OAS document** (a comment
suffices) and re-apply the ConfigMap, then wait — the poll interval is 3 minutes by default
(`OASGEN_PROVIDER_POLL_INTERVAL`, settable via the chart's `env`), and a shorter window looks exactly
like the mechanism being broken. **Do not** delete and recreate the RestDefinition to force it: that
fires the external DELETE on every live CR of the kind.

### A findby could adopt the wrong object, and then delete it

`identifiersMatchPolicy` defaults to `OR`, and `OR` adopts the first item matching **any one**
identifier. On a live fleet a resource naming one child adopted its **parent**, reported
`Synced=True`, and the parent was deleted.

The default is **not** changed — flipping it would silently re-point every resource currently
matching on a subset, and where a vendor's collection response is abridged (Ironic's node list
carries 7 of ~40 fields) `AND` over identifiers the collection does not carry can never match,
turning a wrong adoption into a permanent not-found and then a duplicate create. Instead:

- generation warns when a findby **that actually runs** declares 2+ identifiers and omits the policy
  (#444). It fires only where the policy can choose something: the response must be able to carry
  more than one item, and observation must not be delegated to a RESTAction — a findby under
  `observeApiRef` is declared and never executed. Across 2652 definitions, an ungated rule would
  have been wrong about every one of its hits;
- generation warns when a declared identifier is absent from the items findby returns, resolved
  against **findby's own** response rather than the get-preferred one, and for every kind rather than
  only create-less ones (#445);
- at runtime, an `OR` match decided on a subset now reports which identifier matched and which were
  never consulted (#444). Diagnostic only: control flow is unchanged.

### A fieldMapping no longer deletes the spec property it reads from

`inCustomResource: spec.node_id` for path parameter `{node_id}` named the very property the
suppression removed, so the rule deleted its own source and the verb could not build its URL (#419,
#421). Took five days to diagnose from a vendor's 406.

### spec.serverURL

A RestDefinition can now override the document's `servers[0]` (#374, #437). The override is applied
before the host policy is parsed, so the declared URL is what the allowlist sees, and a document
carrying per-operation `servers` is refused rather than silently honoured in part.

### Readiness now means something on every answer

`Ready` is set on every Observe answer rather than only some (#417, provider-runtime 1.8.2), and
rest-dynamic-controller claims `Ready` where the handler knows better than the runtime (#436, #438,
unstructured-runtime 1.7.6). The narrowness is deliberate and worth stating: a `CreateFailed` /
`UpdateFailed` terminal claim covers a **policy refusal** — an explicit authorization denial from the
vendor — with no invented status-code permanence rule. A 500 stays transient.

### Six silent failures that reported success

Each of these returned a zero value, an empty result or nothing at all, and every layer above read it
as success:

- `Lookup` reported a failed lookup as a successful empty one (#427, #428);
- a failed oas-version backfill was hidden at Debug (#425, #426);
- `findItemInList` discarded a structural comparison error, and "nothing matched" is the one answer a
  caller may act on by **creating** (#439, #442);
- auth-secret RBAC teardown failures were unreported, and the loop stopped at the first namespace
  rather than cleaning the rest — with `status.AuthSecretRBACNamespaces`, the only record of what
  needed cleaning, disappearing with the object (#430, #432);
- a permanent API error was retried for ~50 seconds before being surfaced (#429, #431);
- the auth-secret RBAC name omitted the API group while claiming to follow the convention that
  includes it, so two resources with the same name in different groups collided. Carries a migration
  off the old name (#440, #443).

### Schema fidelity and CI

`nullable` and `minProperties` now reach the generated CRD instead of being discarded at the
boundary (#420, #423, #424). The unselectored MutatingAdmissionPolicy is now a guarantee rather than
a default (#422). `fmt_check` and `tidy_check` are enabled for both modules — they exist in the
org-shared reusable, default **off**, and had never run here; enabling them found and fixed a
`go.sum` carrying two versions of one module, and `golang.org/x/net` declared `// indirect` while
being imported directly by the host-allowlist's IDNA normalisation (#449).

### A runbook for the 0.41.0 to 0.50.0 pin decision

`docs/upgrade-0.41-to-0.50.md` (#434) carries the evidence a consumer needs to decide that jump: the
security floor per version, what the host allowlist and DNS pinning did and did not cover between
0.41.0 and 0.49.0, and a vendor probe with its date and the reason its staleness runs one way rather
than both. It records "unprotected is not exploited" explicitly, because the opposite reading is the
one a reader supplies for free.

### What this release is, and is not, verified against

**Verified on 0.52.0 itself:** both modules pass `gofmt`, `go vet -tags=unit,integration` and the
full `-race` suite — checked on the combined post-merge tree rather than per-branch, because every
branch's own CI had run against a base two commits behind; and both modules are `go mod tidy`-clean
under the gate this release enables.

**Verified on 0.50.0 and 0.51.0, NOT on 0.52.0:** that both charts install on kind (k8s 1.36.1) with
the controller pod reaching Ready and zero restarts, that a RestDefinition reconciles to `Ready` end
to end, and that all five GHCR artifacts publish. Those runs are what found the 0.51.0 breakage
above. They say nothing about this tag's artifacts.

**Not verified:** no pod has been started from a 0.52.0 image — the kind validation ran against
0.50.0 and 0.51.0. The `excludedSpecFields` reversal is covered by unit arms and by the shipped
example's configuration, **not** by a fresh-install run on 0.52.0 itself. Fleet exposure to the
0.51.0 breakage is unmeasured beyond two consumer audits (one fleet of 36 entries clear, one fleet of
four clear only because a mapping on one verb exempts the parameter on all of them — see #457).

**And the release page is a fallback, not real notes.** The org's generated-notes credential hit its
usage limit (resets Nov 9); `release-notes` reports success while emitting boilerplate. **This file
is the record.**

## 2026-11-04 — 0.51.0

Follow-ups to 0.50.0's host validation, plus two generation fixes. 0.50.0 stopped a credentialed
request from reaching a host the OpenAPI document never named; this release closes the channel it
left open — an HTTP **redirect**, which moves a request to another host after the policy has already
approved the first one — and makes two silent host-policy decisions audible. On the generator side,
a guard that was written but never consulted is now wired, and a parameter the generated spec omits
is reported rather than vanishing.

**Nothing in this release requires an upgrade step.** It is a drop-in over 0.50.0.

**What this release does NOT contain**, stated because the next one carries a prerequisite: the
provider-runtime v1.8.x adoption, and with it the change to what `Ready` means on a RestDefinition
during a CRD regeneration. That is 0.52.0, and it will require core-provider 2.13.56 to be live on a
cluster before a 0.52.x build is pinned there. 0.51.0 has no such ordering.

### A redirect could carry the credential to another host

The host policy validates the host of the request it is given. It cannot validate the host of a
request it never sees, and a `302` produces exactly that: Go's `http.Client` follows redirects by
default, so a server that is permitted could hand the client a `Location` pointing somewhere that is
not, and the credential would travel there with it.

**Go's own rule is narrower than "same host", not wider, which is why this needed fixing rather than
documenting.** `net/http`'s `shouldCopyHeaderOnRedirect` strips `Authorization` on a cross-host hop
— but it deliberately **permits a hop to a subdomain** of the origin (its comment: *"Permit sending
auth/cookie headers from foo.com to sub.foo.com"*) and it **ignores the port**, comparing
`url.Hostname()`. So `api.example.com` → `evil.api.example.com` keeps the header, and so does a hop
to a different port on the same name. Relying on the standard library here means relying on a rule
that was written for cookies.

Both clients that present a credential now refuse redirects outright, and refuse them in **two**
places each: in the constructor, and again in `WithHTTPClient`, because a caller supplying its own
client would otherwise silently opt out of the refusal.

- `internal/tools/authn/authn.go` — this one matters most. It presents the controller's own
  **projected, audience-bound ServiceAccount token** to authn's `/serviceaccount/login` endpoint. A
  redirect here does not leak a tenant's vendor credential; it leaks the controller's own cluster
  identity.
- `internal/tools/snowplow/snowplow.go` — carries the authn-issued JWT.

**Exercised in a cluster, not only in tests.** Two servers, A permitted and B not: eight POSTs to A
were served `302` pointing at B, and **zero** subject requests reached B. The same arm on 0.49.0
delivered the credential to B. B sat on an RFC1918 ClusterIP — a class the host policy deliberately
leaves reachable — so the refusal is attributable to the redirect guard rather than to B being
unreachable, which is the control that makes the result mean anything.

### A document server URL the policy discarded is now reported

`hostpolicy.Parse` derives the permitted set from the OpenAPI document's own `servers`. A URL it
could not use was dropped silently, so an operator whose document declared a server in a shape the
parser rejected got a smaller permitted set than their document described, with nothing said. The
first symptom was a request refused for a host the document plainly names.

`Parse` now returns `[]Discarded` alongside the policy — each with the URL and the reason — and
`BuildClient` logs them. The signature change is deliberate: a dropped input that the caller cannot
enumerate is a dropped input nobody will ever notice.

### An operator allowlist entry carrying a path is now refused

`endpointAllowlist` matches **hosts**. An entry like `api.example.com/v1` was accepted and then
truncated to its host, so it matched `api.example.com` entirely — including every path under it. An
operator writing the path believed they had narrowed the allowlist; they had written an entry that
was broader than it looked, and the config read as if the narrowing were in force.

Such an entry is now **refused**, with a message that names both replacements rather than leaving
the author to guess:

> a path plays no part in matching a host, so "/v1" is ignored: write "api.example.com" for
> that host alone, or "*.api.example.com" for its subdomains

A document-derived entry still truncates, because a document's `servers` legitimately carry paths
and refusing those would reject valid documents. The distinction is the origin of the entry, not its
shape — an operator writing a path has made a mistake, a document carrying one has not.

Entry parsing also now lowercases the **whole** entry in one place at the top of the function,
rather than per-component further down. Hostnames are case-insensitive; the previous arrangement
left a component that was compared case-sensitively.

### excludedSpecFields can no longer remove a required parameter

`removeExcludedSpecFields` had a `requiredParameters` guard available and never consulted it. So
excluding a **required** parameter succeeded: the field left the generated spec, the parameter
stayed required by the vendor, and every request built from such a CR was missing a value the API
demands. The failure arrived from the vendor as a 400, far from the stanza that caused it.

The guard is wired. An exclusion naming a required parameter is refused at generation time, which is
where the author can still act on it. Checked on top-level names only, which is the scope
`excludedSpecFields` itself has.

### A parameter the generated spec omits is now reported — and the first attempt was wrong

A request-direction `fieldMapping` supplies a parameter's value, so the generator omits that
parameter from the spec: correct, because the author is not asked for a field the controller never
reads from them. But it happened **silently**, and the generated spec sets neither
`additionalProperties: false` nor `x-kubernetes-preserve-unknown-fields` — so an existing CR that
set the field had the value **pruned by the API server** rather than rejected. `kubectl apply`
succeeded, `get -o yaml` stopped showing it, and nothing said why. Meanwhile an `excludedSpecFields`
entry that matched nothing *did* warn. A stanza doing no work was loud while a property leaving the
schema was quiet; the asymmetry was backwards.

The generator now reports the removal, naming the property, the verb whose mapping removed it, and
the CR path the value comes from — because a message saying a field was removed, but not by what,
sends the author to read every verb's `fieldMapping`.

**The first version of this warning had a false positive, and it is worth recording what it was.** A
name can be both a path parameter *and* a create-body property. Suppressing the parameter does not
remove the body's property, so the field stays in the spec — often in `required`. A consumer
measured exactly that and the warning told them to remove a field their CR could not be admitted
without. The warning now asserts the **outcome** (is the property actually gone) rather than the
**mechanism** (does a mapping supply the parameter), and stays silent when the author declared the
field in `excludedSpecFields` — which is their own acknowledgement that the field is not theirs to
set, and which, because `ExcludedSpecFields` is strictly immutable, they could not withdraw in
response to the warning anyway.

Measured across one consumer's 32 assets: **48 warnings down to 29, with the error set identical** —
so the reduction is false positives leaving, not real findings being swallowed.

## 2026-11-03 — 0.50.0

A credentialed request's destination host was never validated. That is the release: three channels
could send the vendor credential to a host the OpenAPI document never named, and the oldest is
present at **every tag this repository has** — the earliest containing it is 0.20.0, which is the
monorepo fold, so the code is older still and lived in the standalone rest-dynamic-controller repo
before that. Alongside it, generation stopped depending on Go's map iteration order, and a
credential-header exemption that covered one name out of nine was widened to the set its own
reasoning already described.

### A credentialed request could reach a host nobody declared

**Three channels, each sufficient on its own.** The first: `buildPath` joins a path-parameter
**value** into the URL, so a value containing `..` can replace the request **host** — a field on a
CR choosing the destination server for a credential the CR's author is not supposed to hold. The
second is an off-host redirect. The third is DNS moving the target between the check and the
connect.

**Channel 1 has a precondition, and it is now pinned as a test rather than left in prose, because
getting it wrong in prose is exactly what happened.** The host moves only if there are at least as
many settable `..` parameters strictly to the LEFT of the substitution point as there are path
segments to its left, counting the host and every segment the base URL's own path contributes. So
`/folders/{id}` is unreachable — one `..` eats `folders` and stops — while `/{a}/{b}` relocates to
whatever `b` says. One parameter is **always exactly one segment**, because `url.PathEscape` leaves
`.` alone but escapes `/`: a value of `../..` arrives as `..%2F..` and eats nothing, so a single
parameter can never close a two-segment gap however it is spelled. **No consumer's documents have
the reachable shape**; one measured all 123 of theirs and every path begins with a fixed segment.

I had told that consumer the channel was live for them. It was not, and they falsified it with a
measurement I should have made before making the claim. The correction is theirs, the test is the
part I owe: a precondition that each consumer has to decide for themselves belongs in something
executable, not in a release note where the next person re-derives it.

**A corollary a consumer drew from the same table, which is a defence available today.** A base URL
that contributes a path prefix **absorbs one `..` per segment**, so `https://host/v1` is strictly
safer here than `https://host`. Moving a fixed prefix out of the paths and into the base URL
therefore costs nothing semantically — `buildPath` joins them anyway — and buys one absorbing
segment. For a document that genuinely needs a leading path parameter, that is a mitigation the
document's author controls without waiting for anything from us.

**The second channel is where the common understanding is wrong, and it is worth stating precisely
because every consumer had to decide whether they were exposed.** `net/http` strips `Authorization`
when a redirect crosses hostnames, and nothing else. So **the header name decides whether a
credential survives a hop — not the security-scheme type.** An OAS `apiKey` scheme names its own
header, and five consumers audited themselves on this: four name `Authorization` and were never
exposed on that channel — three of them `type: apiKey`, which is the pairing that makes the
scheme-type reading look right until it is not. The fifth names `X-Auth-Token` and **confirmed in a
cluster** that the token reached a host its document does not declare. `X-Api-Key` and
`PRIVATE-TOKEN` are in the same class.

**The check sits at the request path, not in the call builder.** Four sites call `cli.Call` directly
and bypass `APICallBuilder` entirely — the async handler, the async requeue, dispatch, and the
lookup runner — so a builder-side check would have left all four open while looking complete. Every
request now passes `PermitsURL` on `req.URL`, dials the **resolved IP literally** so DNS cannot move
the target underneath it, and re-checks on each redirect hop. A same-host redirect is still
followed, with its body replayed: a renamed vendor resource served as a 307 is a legitimate hop and
breaking it would strand the CR.

**What is deliberately still reachable.** RFC1918 ranges pass, because on-prem installs need them —
one consumer's documented local walkthrough sets its endpoint from a ClusterIP, and refusing private
ranges would have broken both the walkthrough and the probe harness that verifies this fix.
Loopback, link-local, multicast and unspecified addresses are refused unconditionally; the test seam
that permits loopback is reachable only from Go and cannot be switched on by configuration.

**With no configuration, the permitted set is the document's own:** base `servers[0]` plus each
operation's `servers[0]` override. All five consumers measured their fleets from `servers[]` and all
five need **nothing** — 1 distinct server URL and 0 per-operation overrides each, across 59
documents in total, and across 255 operations for the three of them that counted operations as well
as documents. One of them templates its single host and fails its own render unless the placeholder
is found exactly once, which is the shape that makes a measurement trustworthy rather than a count
that happened to come out right. Where more is needed there is now `rdc.endpointAllowlist` and
`rdc.allowAnyHost` in chart values (and `-endpoint-allowlist` / `-allow-any-host` on the binary), as
the security half of the non-default base URL request. An entry with no scheme prefix means **https
only**; `*.example.com` matches one-or-more leading labels and **not** the bare suffix; bare `*` is
refused at parse; port and scheme both participate in matching.

**An exact entry matches exactly, which is the axis the standard library's own strip gets wrong.**
Measured against the shipped `Parse`/`PermitsURL` rather than described:

| permitted entry | target | |
|---|---|---|
| `https://api.example.com` | `https://api.example.com` | permitted |
| `https://api.example.com` | `https://evil.api.example.com` | **refused** |
| `https://api.example.com` | `https://api.example.com.attacker.net` | **refused** |
| `https://api.example.com` | `https://api.example.com:8443` | **refused** |
| `https://api.example.com` | `http://api.example.com` | **refused** |
| `*.example.com` | `https://evil.api.example.com` | permitted — one-or-more labels |
| `*.example.com` | `https://example.com` | **refused** — not the bare suffix |
| `*.example.com` | `https://api.example.com.attacker.net` | **refused** |

The suffix-confusion row matters as much as the subdomain one: a rule written as a string suffix
test would admit `api.example.com.attacker.net`, which is a name an attacker can register.

**Two entry forms misparse, which is #402 and is not fixed here.** `example.com/*` parses with no
error into an EXACT entry — path truncation runs before the wildcard checks, so the `*` sits in the
discarded path and is never seen — and `HTTPS://Example.COM` is rejected as an unsupported scheme,
because the prefix test is byte-exact lowercase while the host is lowercased later. So within one
entry the case is handled for the host and not for the scheme. Neither widens anything: each yields
a permitted set narrower than written, or no policy at all, and both fail loudly. `*.example.com/*`
is unaffected — the leading `*.` is consumed before truncation.

**And one narrowing that is NOT loud, which is #403 and is the counter-example to the sentence
above.** An unparseable server URL from the *document* — as opposed to the operator's allowlist — is
skipped with no log, no error and no condition, and the comment beside the skip says *"Not fatal,
and not silent."* The first clause is true and deliberate: one malformed `servers:` entry must not
make a whole kind unreconcilable. The second is false, and I wrote it. When the dropped URL is the
one the controller actually dials, every request to it is refused with a message naming the host and
listing the permitted set — which reads as a policy misconfiguration rather than as an unreadable
document. It is the only silent narrowing in the package, and it is the same shape as #394's
retracted claim: a true clause carrying a false one, where the false clause is what stops the next
reader looking for a signal that does not exist.

Both #402 forms were found by writing the package's behaviour out as a conformance list for a peer,
which is also why they were not found earlier: nothing in the test suite exercises an entry whose
written form and parsed form differ.

**`allowAnyHost` turns off the redirect check too, which its name does not say.** With the permitted
set widened to everything, a credential will follow a redirect anywhere — the per-hop check still
runs, it just has nothing left to refuse. An operator who sets it to get past a misconfiguration and
leaves it on has no destination protection at all, not merely a wider first hop. It logs loudly at
startup for that reason.

A related precondition, stated because it is currently satisfied by luck rather than by
construction. Each redirect hop is evaluated against the permitted set independently, not against
the host the request originally asked for. With a single-host permitted set — which is what all five
consumers have — those two rules coincide exactly. With a wildcard entry they diverge: a chain could
walk from one allowlisted host to another. That is deliberate, because the allowlist IS the
operator's declared trust set and refusing a hop to a host they explicitly listed would be this code
overriding a stated decision. But the equivalence holds because of how consumers are configured, not
because of anything the code enforces.

> **Answer the "do I need an allowlist?" question from `servers[]` in your document — not from the
> logs.** One consumer's first attempt grepped the controller's logs for dialled hosts and got eight,
> including a Helm download and an apt repository. The controller dials none of them: they are strings
> **inside the CR body**, a 49 KB cloud-config that the controller logs. Answering from that would have
> asked for an allowlist covering hosts nothing ever contacts, which is worse than a refusal on upgrade
> because it would look like it was working. It is also a sharper argument for the fix than the `..`
> channel: a credential leaking to a host named in your own payload is a channel where the operator's own
> audit method confirms the wrong thing.

**The boundary, stated rather than left to inference.** This covers the rest-dynamic-controller's
own request path. Requests delegated through `*ApiRef` leave via snowplow and are **not** covered;
snowplow has measured the same three channels on its side and tracks them in snowplow#1106 and
snowplow#1112. One consumer's fleet is where the two paths meet — its delegated kind's credential
travels outside the path this release hardened, on the same host today, so there is no exposure but
there is no protection either.

**And two of this controller's OWN internal clients are outside it too, which is #401 and is not
fixed here.** The authn client presents the pod's projected ServiceAccount token and the snowplow
client presents the authn JWT, both on a plain `http.Client` with no redirect policy. Measured while
answering a peer's question about their equivalent code: `net/http` drops `Authorization` on a
redirect that leaves the original host, **but it permits a hop to a SUBDOMAIN of that host and
ignores the port.** So relying on the standard library's strip protects one header name and only
against a non-subdomain change. For a cluster-DNS endpoint a subdomain is hard to arrange; for one
configured as an external hostname it is the ordinary case. Fixing it is the first PR of the next
release rather than this one, because this release closes the worse channel and consumers are
waiting on it.

### Generation was not deterministic

**Four sites built emitted output by ranging a Go map**, whose iteration order is randomised per
run. Two renders of the same unchanged document could therefore produce different CRD field order,
different `required` order and different `enum` order — which makes a diff between two renders
unreadable, and made a consumer's "70 of 72 identical" verdict un-rerunnable rather than wrong.

The dedups now preserve **first-seen** order and return `nil` for `nil`, and the two schema-assembly
sites sort explicitly. One of them sorts on the **emitted** name rather than the source key, because
the case transform applied on the way out is not order-preserving — sorting before it would have
produced a stable order that is not the order that ships.

### A credential-header exemption one name wide

`SetAuth` attaches the credential to the `*http.Request`; validation runs 114 lines earlier against
the options map, which the credential is never written into. The validator **structurally cannot
see** it, so a document that declares its own credential header as a required parameter had every
request refused with `missing header: <name>` before the credential was ever attached.

The exemption was a substring test for `authorization`. This repo enumerates **nine** credential
header names, and the carve-out covered one of them while its own reasoning covered all nine. It is
now the **union** of both tests rather than a replacement: the substring test still exempts
`X-Custom-Authorization`, which is not in the enumerated nine, so the change can only widen. All
three places that need to know which headers carry a credential — log redaction, this exemption, and
the spec-population refusal — now read one list.

### And the smaller ones

A header or cookie parameter declared in the document is now **populated from the CR**, refusing the
nine credential names rather than letting a CR field supply a credential. `RequestedParams` now
merges the **path item's** parameters, without which a document that declares its parameters at
path-item level could not work at all. `findby`'s `itemsPath` is documented — it shipped
undocumented and cost a consumer two months; the earlier conclusion that it *was* documented came
from a bare-string grep whose three hits were all a different scope,
`correlation.observe.itemsPath`. A causeless 401 is no longer retried, since it may be
**post-dispatch** and the retry can duplicate an audited write. unstructured-runtime moves to
v1.7.4, so the **reason** an existence check could not answer reaches the CR instead of being
swallowed.

And one retraction: a comment merged in the host-policy work claimed a test existed that did not.
The count of `NewTLSServer` in the package was zero. A true clause carrying a false one is the
failure mode that matters here, because the false one is what the next reader relies on.

### What this release is, and is not, verified against

**Published, not exercised.** At the tag: five GHCR artifacts, both images multi-arch, the published
charts rendering the right refs, and the repository's check runs green at the tagged commit. None of
that is a request reaching a responding endpoint. Two consumers have runtime verification queued —
one has a probe harness that stands up two in-cluster hosts, points the controller at A and reads
**B's** access log as ground truth, where B is a host the document never names; on 0.49.0 it printed
that the credential followed the redirect to a different host, and under this release it must print
a refusal instead. The other will move its live Zendesk tests. Both are load-gated on a shared
machine and neither had run when this was written.

The reason that distinction gets its own paragraph: a release here once reported five green
artifacts after swapping the base image, and no pod had ever started from it. "Published" and
"exercised" are two claims and only one of them was true.

**And the control for that verification had to move when the expected answer moved**, which is the
sharpest version of this anyone has put to me. On 0.49.0 the reassuring answer was that B's log
showed the header ABSENT, so the harness first had to prove B could *detect* a header before its
silence meant anything. On 0.50.0 the reassuring answer is the refusal, so the companion arm has to
prove the controller **still works** against A — a policy that refuses the redirect *and* breaks the
legitimate call looks like a pass from B's log alone. Same instrument, same test, different required
control. "Does this check have a control" is the wrong question; "can this check fail in the
direction I am hoping for" is the right one.

## 2026-11-02 — 0.49.0

Two defects that produce a wrong answer rather than a failure, a credential cache that could not see its
own invalidation, a documentation page that was wrong, and three tests over code that already worked. Most
of it came from a parallel verification pass over issues nobody had re-read since filing.

### A findby no-match is no longer proof of absence

**The existence checker could duplicate a live vendor resource.** `Exists` reaches findby precisely when a
resource is not addressable by identifier — "the normal state for a create whose response we never
recorded", in its own words — and a findby that matches nothing **synthesises** a 404. That 404 was then
read as "a definite negative ... the ONLY path that may return false".

So: create succeeds at the vendor, the response is lost, findby cannot match, `Exists` reports absence, and
the runtime creates a second one. The file's comment had the stake exactly right —
*"Returning false here is what would recreate a live resource"* — and the synthesised 404 slipped past it
because on the wire it is indistinguishable from the vendor's own.

**The distinction is a field rather than a change to `IsNotFoundError`, because the two callers need
different answers.** `Observe` is right to treat no-match as not-exists: creating is the intent and no
duplicate is possible yet. `Exists` is the one caller for whom a wrong `false` is unrecoverable.

Three synthesis sites, enumerated rather than assumed — and one of them revises an existing claim. The
paginated `Exhausted` branch asserted "authoritative absence"; `Exhausted` is authoritative about **the
scan**, not about **the predicate**. If the resource is present and our identifiers do not appear in the
item shape, no item matches a complete scan either. The `notFoundCodes` remap is deliberately *not*
marked: there the vendor signalled absence with a non-standard code, which is authoritative.

**The mutation that mattered was the ordering one.** Swapping the two cases leaves the code compiling, the
predicate present, and the fix **completely inert**, because `IsNotFoundError` is true for a synthesised
404 as well.

### A declared constraint that was enforced by nothing

When a parameter's name collided with a request-body property, the parameter's **entire schema** was
discarded — only `required` survived. Measured on a live cluster: a field declared in both kept the body's
`pattern`, while one declared **only** as a path parameter lost it.

Now the keywords merge, with the ambiguous case refused rather than decided: merge when the body is
silent, say nothing when they agree, **warn and keep the body's** when they differ. Silently merging a
conflict would be the same defect in the other direction — tightening a constraint the author never asked
for. `enum` is never combined, because an intersection may be empty and a union weakens both.

**This tightens generated CRDs.** A CR that previously passed admission with a malformed value can now be
rejected, which is the point, and the consumer whose definition the defect was measured on has been told
ahead of the release.

### The authn JWT cache could not see its own invalidation

The JWT used to call snowplow was returned on **time alone**. authn has no revocation, so ordinary expiry
needed nothing — but an authn signing-key rotation invalidates every outstanding token early, and the
cache could not observe that. The controller then presented a dead token until the refresh skew: an
outage lasting up to the token's remaining lifetime, cleared by a pod restart, **which makes it look like
a transient**.

A 401 now drops the cache and retries **exactly once**. snowplow can answer 401 for reasons a fresh token
cannot fix, and retrying those in a loop turns a clear failure into a hot one. Safe to re-issue only
because `/call` is a GET, which is stated at the call site so nobody extends the pattern to a mutating
verb.

### The immutability contract was documented wrong

`docs/api.md` listed `additionalStatusFields` as strictly immutable, then contradicted itself two lines
later. The marker enforces **append-only**. So the page told readers they could not append a status
field, when appending is exactly what the rule permits — and is what unblocked an installer roll. It is
**five strict and two append-only**, verified against the markers, with a gate that fails if the shape
changes. The gate deliberately does not parse the prose: a test comparing two hand-written things can
drift in both at once.

### And three tests over code that already worked

A generated CRD carrying a CEL uniqueness rule is now checked against **the apiserver's own validator**,
because that branch had none and an over-budget rule means the CRD does not register at all. A path-item
parameter redirected by a `fieldMapping` is pinned, closing an issue that had been fixed for two days and
never closed. And the generated authentication field sets are pinned, because they are derived from Go
structs by reflection — so a renamed json tag silently changes a credential CRD, on the one path where a
leak has already happened.

The `kubeVersion` rationale also now ships as a chart **annotation**: `helm package` discards every
comment in `Chart.yaml`, so the floor was reaching consumers with no reason attached.

## 2026-11-01 — 0.48.0

Two defects that destroy or misreport state, a dependency bump, and a test for a credential path. Both
defects were found by consumers running against real or mocked vendors, and in both cases **my first
diagnosis was wrong and a peer corrected it**.

### Helm can no longer delete the RestDefinition CRD

Deleting it reaps every `RestDefinition` and every CRD they generated, and those CRs' finalizers then
**delete real external resources at the vendor**. Measured on a live cluster upgrading from the pre-split
chart: the crds chart created it at `22:21:23`, the provider chart's upgrade removed it at `22:21:24`.

**The provider's own guard cannot prevent it.** `deploy.go` refuses to drop a generated CRD that still has
instances, precisely to avoid this cascade — but Helm deletes the *parent* directly and asks the controller
nothing. There is no code path from the delete to the check.

**The annotation is on the Go type, not the chart**, and finding out why cost the most time here. Added to
the chart template by hand it disappeared on the next `make generate`: `apis/generate.go` ends with
`cp ../crds/*.yaml .../helm/oasgen-provider-crds/templates/`, so the chart's CRD is a copy of
controller-gen's output. **The repo would have shown the protection while the published chart lost it.** As
a `+kubebuilder:metadata:annotations` marker it is part of the generated artifact and reaches all three
copies.

The rationale sits above a **genuine blank line**, also found by checking rather than by care: written as
part of the doc comment, controller-gen folded twenty lines into the CRD's `description` and shipped an
essay about Helm internals into every cluster's schema.

**Verified in Helm's source, not its docs.** I had claimed `keep` "would have saved this cluster", found
the docs ambiguous and 2020-era reports of it being ignored on exactly this path, and corrected myself to
"not established" before resolving it from `pkg/kube/client.go` at v3.19.0. The comparison is `==`, so the
value is **case-sensitive**: `keep` works, `KEEP` does nothing.

`docs/usage.md` documents the half this does not cover — an existing pre-split install needs the annotation
applied **before** the upgrade, because the old release's manifest is immutable and is what Helm diffs
against. Cost, stated in both places: `helm uninstall` now leaves the CRD behind.

### An unresolved path placeholder no longer reaches the wire

Observed on a live mock as twelve requests for `GET /v2.1/flavors/%7Bid%7D` — the **literal path template**,
because `{id}` is mapped from a status field the create had never populated. Worse than a failure: a real
API answers it plausibly. Nova returns `404 itemNotFound`, indistinguishable from "this resource does not
exist", so the controller concludes something about existence from a URL it should never have built.

**Two halves.** Request validation ignored **path-level parameters** entirely — `pathItem` was fetched and
discarded, so a document factoring shared parameters up to the path item, which is the idiomatic form,
presented *zero* parameters to validation. And substitution silently left unmatched placeholders in the
path. Both are fixed; the second consults the document not at all, because a leftover `{...}` is wrong
whatever the vendor declared.

**My first diagnosis blamed the vendor's document and was wrong.** I asked the reporter to confirm
`required: true` was missing; it was present in all six of theirs. They proposed three candidates, named
which to check first, and path-level inheritance was it. Their reason for insisting is the part worth
keeping: a wrong framing left in an issue sends the next reader to check their own document, find it
correct, and conclude the issue does not apply to them.

**The import cycle is how the defect existed.** `builder` imports the restclient package, so restclient
could not import `builder` back — four callers that merely reason *about* a request each had the check, and
the one place that actually substitutes placeholders could not reach it. The helper now lives in
`pathparsing`, which imports only the standard library.

**Mutation testing caught a vacuous suite.** Reverting the path-level merge left everything green, because
every test for the new helper called it *directly* — which is precisely the original defect, a helper that
exists and is never reached. A test that drives the real validator against a document in the failing shape
now fails that mutation.

Ordering was measured afterwards: the literal precedes the substituted form and there is no interleaving,
so the guard is **stricter than the observed behaviour requires**. Left that way deliberately — a residual
placeholder has no legitimate meaning at any point, and the guard should not depend on one vendor's timing.

### Also

`provider-runtime` to v1.7.2 in `go/oasgen-provider` (a conflict persisting late-initialized fields now
requeues rather than surfacing as `ReconcileError`); govulncheck reports **0 called** vulnerabilities, and
the two uncalled ones predate the bump. And the generated authentication field sets are now pinned by a
test, because they are derived from Go structs by reflection — so a renamed json tag silently changes the
Configuration CRD, on the one path where a credential leak has already happened.

## 2026-11-01 — 0.47.0

A crash, a diagnostic, and a document. Cut for the first of those: it is in every released version,
including the one three sessions pinned yesterday.

### An unresolvable `$ref` in a request body crashed the controller pod

`populateFromAllOf` dereferences `schema.Type` on its first line and had no nil guard, which was
reachable rather than theoretical:

```
populateFromAllOf(nil)  ->  runtime error: invalid memory address or nil pointer dereference
```

**Two independent halves**, both fixed, because either alone prevents the crash and leaving one is a trap
for the next caller. The function is now nil-safe, matching how `validatePatterns` guards the same
recursion in the generator's sibling package. And the `allOf` loop now checks `BuildSchema`'s error
**before** recursing — on error it returns `(nil, err)`, so recursing first was the route to the nil.

**The array branch of the same function, ten lines above, already checked first.** The identical failure
handled correctly in one branch and incorrectly in the other is what made this a defect rather than a
design choice.

A panic here takes the pod down for **every** resource that controller manages, not just the one with the
bad reference — and the author sees a `CrashLoopBackOff`, which points nowhere near a typo in a schema.

**Mutation-testing the halves separately found a gap that would otherwise have shipped.** Restoring the
original ordering left every behavioural test passing, because the new nil guard absorbs the nil; the
ordering is still wrong and nothing observable fails, so a reader tidying the loop could reintroduce it
freely. It now has its own source-level assertion, scoped to the `range schema.AllOf` loop so it cannot
pass by matching the array branch.

Scope stated rather than implied: the route to nil is **code-derived**. libopenapi rejects an unresolvable
`$ref` earlier, inside its own build, so the end-to-end test asserts such a document is handled without a
panic from our side and skips if the library refuses it first.

### A resource waiting on a missing referent now says which one

`Pending` was set by four code paths and could not be acted on. Two are async and polled — and that
machinery already handles terminal failure. **The other two are not async at all:** a `dependentLookup`
whose referent is missing sets `Pending` with no operation handle, so nothing will ever move it off. If
that referent never appears — a typo, a CR nobody created — the resource waits forever, and the reason is
`Pending` either way, indistinguishable from an async create thirty seconds in and about to succeed.

The detail existed where it was produced (`dependent lookup %q found no item matching %q`) and reached
only a Debug log, which is off by default. It now reaches the CR.

**The message is the only difference, deliberately.** `IsConditionSet` compares Type, Status and Reason
and ignores Message, so the `notfound && IsConditionSet(mg, Pending())` guard keeps matching and no
control flow changes. A distinct Reason is what a reader would expect a new constructor to have, and it
would stop that guard matching — silently reporting a waiting resource as not existing. The test pins the
**sameness** rather than the difference.

This also corrects the issue it came from: that issue claimed such a resource "reports healthy". It does
not. `Pending` is `Ready=False`, and the runtime's up-to-date branch sets `ReconcileSuccess` without
setting `Available`. Checking that before building anything is what kept this from being a behaviour
change in search of a defect.

### Enveloped APIs: declare the unwrapped body, wrap on the wire

Declaring the envelope in the OpenAPI document **looks like it works and silently does the wrong thing**.
The body's field list comes from top-level properties only, so an envelope yields a body of one field and
everything beneath it passes through unfiltered. The create succeeds; what is invisible is that write-only
fields travel too — a Zendesk `comment` re-posted on every drift-triggered PATCH, adding a comment each
time, with nothing erroring.

The documented shape puts the envelope in `requestTransform`/`responseTransform`, which is the designed
seam. A live four-kind e2e against a real Zendesk confirmed the hazard does not bite when it is done that
way — the first time the recommended alternative had been exercised end to end rather than reasoned about.

## 2026-10-31 — 0.46.0

Two live defects, both reported by consumers running against real vendor APIs, and both of the same
shape: the controller answered a question it could not answer, rather than saying so.

### A create the vendor refused no longer latches to `Available`

Measured against a real Zendesk and then reproduced deliberately, with Debug, against stable single-pod
controllers. A Group CR whose create was rejected with `422` became **permanently `Ready=True`/`Available`
with no identifier**, for a resource that never existed:

```
17:32:56  no conditions -> exists=false -> Create          (this part was already correct)
17:32:58  Create 422 "Private groups are not enabled for this account", retried
17:34:46  no get or findby -> "Resource is assumed to be up-to-date."    <- the latch
17:34:58  Ready=True Available, status.id absent                         <- never created
```

**The runtime sets `Creating` on both create outcomes** and separates them only on `Synced`
(`ReconcileSuccess` at `worker.go:823`, `ReconcileError` at `:760`). So the observe path for a kind with
neither a `get` by identifier nor a `findby` could not tell a created resource from one the vendor
**refused** to create — and read its own condition as evidence about the external world. Nothing recovered
from it, because the observation that would clear `Available` is the one that kind cannot make.

It now reports not-exists when the previous reconcile errored, which sends the runtime back to `Create`:
it fails again and re-sets `Creating`/`ReconcileError`. Visibly stuck and retrying is the honest steady
state for a vendor refusing a create. It deliberately does **not** error — that would replace the vendor's
own explanation with ours, and "Private groups are not enabled for this account" is the most useful
sentence in the whole trace.

**Scope, measured rather than assumed:** the reporter's full four-kind live e2e passes 26/27 on 0.44.0, so
this was confined to the failure path. **Two wrong diagnoses preceded the fix** and both are recorded in
#347 rather than quietly replaced — the first impossible (`ValidateRequest` rejects an empty path
parameter, so the request it assumed is never sent), the second with the trigger backwards. Only a
Debug-level log line settled it.

### A denied `403` stops, without breaking throttle recovery

`401` and `403` had no special handling anywhere, so an authorisation failure was an ordinary error retried
on every reconcile indefinitely. The split is not symmetric:

| code | meaning | action |
|---|---|---|
| `401` | the credential may be **replaced** under us | **retry, indefinitely** |
| `403` | the credential is not **permitted** to do this | **permanent, stop** |

**A 401 must stay retryable.** Rotation is delegated to a secret manager here — the controller never mints
a token, it reads one from a Secret on every reconcile with no cache — and retrying is precisely how a
rotation mid-reconcile is survived. Treating a 401 as permanent would turn every rotation into a ticket.

**But a 403 is not reliably permanent, so this is not the two-line fix it looks like.** GitHub answers both
rate-limit kinds with "403 or 429", and warns that continuing to request while limited may get an
integration banned. A naive "403 stops" rule breaks throttle recovery for a primary blueprint; a naive
"403 retries" rule risks a ban. `StatusError` now carries the response headers, which are the only channel
separating the two, and `x-ratelimit-remaining` is checked for the **value** `0` rather than presence —
GitHub sends it on successful responses too.

**`Delete` is deliberately not guarded**, and that asymmetry is pinned by a source-level test because it
reads like an oversight. Stopping there removes the finalizer, so the CR disappears while the external
resource still exists and still costs money. Retrying a denied delete forever is wasteful; orphaning is
unrecoverable, and only one of the two is fixed by fixing the credential.

### A refused delete now says why

The same shape again, on the delete path, and found by following a consumer's passing remark rather than
by looking for it. When a vendor refuses a delete **and** the get verb confirms the resource is still
there — the most informative combination available — the vendor's reason was **discarded**:

```go
return fmt.Errorf("delete of %s did not remove the external resource, which is still present; retrying", mg.GetName())
//                 deleteErr dropped entirely
```

Six CRs sat in `Terminating`, each retrying a `DELETE` their API refuses because the node is in an
`active` state, and the CR said only "still present; retrying". True, and useless. The operator had to
tail the controller and match a log string against the vendor's API reference to learn the cause.

`deleteErr` is now wrapped with **`%w`**, not `%v`. The distinction is invisible in the output: the
message reads identically, while `%v` would stop `HasStatusErr` seeing the status code through the
wrapper and silently disable the 404 short-circuit that releases a finalizer for an already-absent
resource. A test asserts on the unwrap rather than on the text, because that is the half a reviewer
cannot see.

**And #334's stated reasoning was wrong.** Its comment claimed the vendor's body goes "in the error, not
in a condition", because errors reach only logs and events "which are operator-only". But the runtime's
`ReconcileError(err)` sets `Message: err.Error()` — so **every error a handler returns becomes the
`Synced` condition's message**, and that body has been CR-visible since the moment #334 shipped. Not
exposure #334 introduced; returned errors became condition messages long before it. But the comment
asserted an absence that was never real, and it was cited as a premise earlier the same day before anyone
checked it. Corrected in place rather than deleted, with the real consideration restated: a vendor error
body is attacker-influenced text that **does** land in an API object readable with `get`.

### A RestDefinition cannot model a one-shot action, and the docs now say so

A physical server was re-imaged **four times in thirty minutes** because a destructive one-shot was
modelled as a reconciled resource. `Observe` must answer *does it exist* and *is it up to date*, and a
fire-once action has no external object that answers either — so the shape it forces (a `findby` written
so it never matches, a trigger setting `Pending`, an `Observe` returning up-to-date from that guard
without clearing it) means **such a CR can never report `Ready`**. One held a fully provisioned
composition at `Ready=False` through 51 reconciles with zero transitions.

`RESTAction` is not the alternative and the docs now say that too, because it is the first thing authors
reach for: it runs on every call, persists no attempted-or-completed record, and rendering one from a
chart does not execute it. **There is no primitive with completion semantics**, so an author looking for
one finds nothing and reaches for a resource.

The rule, in the reporting author's words: **a destructive one-shot must not be driven by a signal the
action itself cannot change.** Their trigger — `provision_state == active AND spec.extra empty` — is also
the steady state of a node whose cloud-init failed, so the wipe never cleared its own condition. The
documented alternative is to express the target state instead of the transition and let drift detection
enforce it, which makes the trigger something the action *can* change.

### Two `rdc.*` traps, each of which cost a session an investigation

`rdc.env` values must be set with `--set-string`: the ConfigMap schema types them as strings and Helm's
`--set` infers `true` as a boolean, so the obvious spelling is rejected with a message that names the type
and not the fix. And an explicit `rdc.image` pin **survives a provider upgrade**, tag included — one
session's provider moved 0.42.0 to 0.44.0 while all four of its generated controllers kept running RDC
`0.41.0-p3b`. Since the two halves release in lockstep at identical versions, a pin left behind means they
are no longer the pair that was tested together.

## 2026-10-31 — 0.45.0

Three changes, all of which only matter when something else goes wrong. Cut because a consumer is blocked on
the first of them.

### unstructured-runtime moves to v1.7.1, for the create-recovery conflict retry

0.44.0 still pinned v1.6.1 and had done since 0.41.0. v1.7.1 re-fetches inside `retry.RetryOnConflict`
instead of issuing a bare `Update` against a stale object.

**0.44.0's own fix already removed the cause** — RDC implements the runtime's read-only existence check, so
recovery no longer falls through to a status-writing `Observe` and there is nothing to conflict with. v1.7.1
covers conflicts RDC does **not** cause: another writer, a concurrent reconcile. The two are complementary,
and treating the first as making the second optional is what left the pin behind for four releases.

**The reason this is a release rather than a tidy-up** came from the consumer who asked whether 0.44.0 was a
superset of their local patch builds. It was not, and their argument for waiting is better than the one for
shipping without it:

> On 0.44.0 without v1.7.1, a wedge has two candidate causes and I couldn't tell them apart; I'd be handing
> you an ambiguous number. With v1.7.1 in, a wedge means the conflict has a second source.

A measurement whose failure mode has two indistinguishable explanations is worth less than no measurement,
because it invites a conclusion. The pin exists so their test can mean something.

Verified by content rather than by version number: `RetryOnConflict` present, `ExistenceChecker` still
exported so 0.44.0's implementation still binds, and the `"Cannot reconcile external-create annotations"`
log string kept **verbatim** — it is what that consumer grepped to measure 51 consecutive failures, and is
likely on a dashboard somewhere.

### `go mod download` is retried in both Dockerfiles

`proxy.golang.org` failed transiently three times in this repo on 2026-10-30 — `golang.org/x/text` on a docs
PR, `krateo-platformops/plumbing` in **0.43.0's image build**, and `go.opentelemetry.io/otel` in a test job.
Each needed a manual re-run, and the plumbing one left 0.43.0 with its images unpushed, its charts correctly
withheld by the pipeline's own guard, and the installer's pin gate refusing the tag.

The shared workflows now retry this, but only in GitHub Actions steps. A Dockerfile `RUN` is outside that,
so the image build — the longest job in the release — was the one place still unprotected.

**The retry here is deliberately blind, where the shared action's is deliberately selective.** That action
retries only transient network errors so a missing module or checksum mismatch still fails first attempt and
the pin gate keeps its meaning. A Dockerfile has no gate to weaken: the build either produces a binary from
the modules `go.sum` names, or it fails three times in a few seconds with the real error surfacing.

### A correction to 0.44.0's tag message

That tag claims the check-runs at its commit were verified green. **They had not been looked at** — pushing
a tag triggers a fresh run set, so 16 were still running at the time. The content verification in the same
sentence was real; one clause was verified and the other aspirational, joined by "and".

A tag message cannot be amended without force-pushing it, so the correction lives in this log — see the note
at the head of the 0.44.0 section. The verification was performed afterwards and nothing had failed, but
that was established after the claim rather than before it.

## 2026-10-31 — 0.44.0

> **Correction (2026-10-31).** This release's annotated tag message states that *"the check-runs at this
> commit verified green"*. **That was not true when written.** The content verification in the same sentence
> was real — each of the eight claims was grepped against the tagged commit — but the check-runs had not been
> looked at, and pushing a tag triggers a fresh run set, so at that moment 16 of them were `in_progress`.
>
> The claim existed specifically to record that the 0.43.0 cut's omitted step had been closed, which the
> installer's pin gate had caught. **It recorded closing the gap instead of closing it.**
>
> A tag message cannot be amended without force-pushing the tag, so the correction lives here. The
> verification was performed afterwards and the result was: **19 success, 3 skipped, 0 failures**, with the
> two multi-platform image builds and the chart-images gate still running. Nothing failed — but that was
> established after the claim, not before it.
>
> The mechanism is worth naming because it is not carelessness: **one clause of that sentence was verified
> and the other was aspirational, joined by "and".** The true half is what made the false half easy to write.


Eight changes. Two silently dropped data that an author had explicitly supplied, and both were found by
running the previous release against real infrastructure rather than by review.

### Every `configurationFields` header and cookie was silently discarded

oasgen keys the generated Configuration schema by the OpenAPI parameter **location**, which the
specification defines as singular — `path`, `query`, `header`, `cookie`. RDC read `"headers"` and
`"cookies"`. `path` and `query` worked only because OpenAPI's singular happens to match what the consumer
asked for.

So a `configurationFields` entry for a header or cookie was generated into the Configuration CRD, accepted
by the apiserver, set by the author — and **never sent**.

**Both repos' test suites were green while contradicting each other.** oasgen's asserted it *emits*
`header`; RDC's fixtures asserted it *reads* `headers`. Neither suite was wrong about its own side; nothing
had ever compared the producer's output to the consumer's expectation. Every other defect in this release
had one side untested — this one had both sides tested, which is why it survived.

Measured consequence, on physical hardware: an Ironic microversion declared this way was dropped, so Ironic
negotiated its **minimum** (1.1), every port create answered 406, the node ended up with no port, and the
deploy failed with *"does not have any port associated with it"* — **three layers from the cause, naming a
missing port rather than a dropped header.**

Nothing could have depended on the plural form: the generated CRD declares `header`, so a CR written with
`headers:` is rejected as an unknown field. The feature was not unused, it was **unusable** — which is also
why the fix cannot regress anything.

### Create-recovery no longer wedges on a status write

Incomplete-create recovery fell through to `Observe`, which calls `tools.Update` on its first statement and
writes the status subresource further down. That bumped the object's `resourceVersion`, so recovery's own
bare `Update` conflicted **on every pass** and `krateo.io/external-create-pending` was never reconciled.
Measured at **51 consecutive failures and 0 recoveries** over 20 minutes on one baremetal node, with the
damage surfacing two layers away as a deploy rejected for a field the spec never got to push.

RDC now implements the runtime's read-only existence check, so recovery never reaches a status-writing
method. Paired with unstructured-runtime v1.7.1, which survives a conflict it did not cause, the two cover
each other.

**`false` from that check means "I looked and it is definitely not there", never "I could not look."** Only
a clean 404 returns it. A delegated observe, an async operation in flight, an unaddressable resource, and a
`Pending` condition all return an **error** instead — because the runtime clears the pending marker and
**re-runs create** on a `false` once its 120-second grace period lapses, and for a baremetal provider that
is a duplicate physical server.

The `Pending` case is worth stating separately. `Observe` treats *not-found plus Pending* as **exists**,
which is right for drift detection and wrong here. And a measured annotation history shows the inverse case
is equally real — a node whose external object existed throughout went `succeeded → pending → failed` in
ninety seconds while being de-enrolled and re-created. **Both booleans are sometimes wrong, so refusing is
not caution: it is the only answer that is never false.**

### A request body is now labelled with the media type the document declares

0.42.0 made the body **schema** come from the declared media type instead of assuming JSON, and left the
`Content-Type` header hardcoded. For a document declaring `application/json-patch+json` the result was a
body that correctly matched the patch schema, labelled `application/json`. A strict server answers 415; a
lenient one accepts a patch document as a plain object.

**Right body, wrong label — and correct-looking at every layer inside this repo**, which is why the tests
assert what a server receives rather than what we set. An explicit `verbsDescription[].headers` entry
naming `Content-Type` still overrides it, which is the author's escape hatch for a document that declares
the wrong thing.

### A failed call carries the vendor's own explanation

A rejected status produced `invalid status code: 400` and nothing else, because the error was built **before
the response body was read** — so the explanation was discarded at the source, on exactly the path where it
carries the most information.

One measured case: a CR said `create failed: unexpected status: 400: invalid status code: 400` while the
vendor had said *"There is already a backup policy assigned to the resource"*. That sentence was the entire
explanation for a 34-minute investigation.

The body now reaches the **error** — logs and events — and not the condition. A condition is readable by
anyone with `get` on the CR; an error is operator-only, and a vendor error body is attacker-influenced text
that may echo submitted content. Read through a bounded reader, because an error body is untrusted input of
unknown size; truncation is marked, so a clipped sentence is not mistaken for the whole answer.

### The audit trail no longer asserts a method it never issued

Delegated writes recorded `HTTPMethod` as POST, PUT or DELETE — guessed from the verb's semantics — while
correctly omitting the status code they equally did not know. Same ignorance, two treatments. On a delegated
path RDC issues no HTTP request at all; snowplow's stages do, with methods RDC never sees.

An audit record is a compliance artifact, so a field that is confidently **wrong** is worse than an absent
one: a reader reconciling it against snowplow's own record would try to match a PUT that never existed. The
method is now omitted exactly as the status code already was.

### Documentation and tests

The Requirements section said *"A Kubernetes cluster."* with no version, while both charts declare
`kubeVersion: ">=1.36.0-0"` and enforce it at **render** time. The trap worth documenting is that
`helm/kind-action` defaults **below** the floor while `kind`'s own CLI default is above it — so a CI job
fails where every local run passed, with an error that reads like a chart defect.

Two test changes record what their assertions do **not** cover: one set is vacuous by construction and says
so, because a vacuous assertion presented as a guard is worse than none.

## 2026-10-30 — 0.43.0

Three changes, all found by installing the previous release against real infrastructure rather than by
review. Each was in a code path that looked correct until something ran against it, and each reported
success while doing less than asked.

### Four of five resources in a real blueprint were uncreatable

`addParametersToSpec` made every path parameter a **required** top-level spec field named after the
*parameter*, and nothing consulted `requestFieldMapping`.

A definition mapping `{id}` from `status.flavor.id` is correct modelling: the vendor assigns the id at
create and the controller reads it back for get and delete. But the generated CRD required `spec.id`, and
**an author cannot supply a value that does not exist yet.** Measured on generated CRDs, five for five:

```
ComputeFlavor      invalid: spec.id: Required value
ComputeKeypair     invalid: spec.name: Required value
ComputeServerGroup invalid: spec.id: Required value
ComputeAggregate   invalid: spec.id: Required value
Instance           invalid: spec.id: Required value
```

The rule already existed. **0.38.0** established it for resolver-supplied *body* fields — *"requiring
it forces the author to write a value that is then overwritten"* — **eleven lines above this defect**, and
it was never extended to parameters.

A mapped parameter is now **absent from spec entirely**, not merely un-required. An optional `spec.id` that
nothing reads is worse than a hard error: the request takes the mapped value, so an author who set it would
be silently ignored. It is also kept out of the list that protects a field from `excludedSpecFields`
pruning, which exists on the grounds that *"dropping one leaves a kind that cannot build its own URL"* —
false once a mapping supplies it.

The cross-document parameter-conflict check still runs for a suppressed parameter. A mapped parameter is
still **sent**; only its source changes, so two verbs sourcing it from documents whose schemas disagree
remains an authoring error. The first version of this fix skipped that bookkeeping and would have switched
the check off for precisely the parameters it touches.

### A populated spec object compared EQUAL to an empty remote object

`CompareExisting` skips a key the response does not hold — correct, and the mechanism a `responseTransform`
relies on when it deletes a field an API masks. But the map branch recursed **without comparing sizes**, so
when the response held a parent as `{}`, every child key was looked up in an empty map, found absent,
skipped by that rule, and the result was **equal**.

Measured against a real Ironic: the node answered `"instance_info": {}` while the CR carried
`instance_info.image_source` and `image_checksum`. No drift, no PATCH, **`Synced=True` having never applied
the desired state.** It surfaced two layers away as a deploy rejected for a missing image field — an
application error that reads like a chart bug and says nothing about comparison.

This is the map-shaped twin of a slice defect fixed earlier; `compareSlices` has compared lengths ever
since, for the same reason: **present-and-empty is the author declaring the thing empty, not declining to
have an opinion.**

The guard is at the function entry rather than before each recursive call. There are three call sites, and
a guard repeated at each would reproduce the exact asymmetry that caused the bug — a rule applied in one
branch and forgotten in another. At the entry a future fourth caller is safe by construction.

It composes with the masked-field remedy rather than conflicting: a transform that **deletes** a key makes
it *absent*, not *empty*, and absent is still skipped. Deliberately unobservable stays silent;
observably-empty now speaks. There is an explicit test for that, because if it regresses every
`responseTransform` using `del()` starts drifting forever.

### A JSON Patch body could not be sent

Both request-building sites asserted the body was a `map[string]any` and errored otherwise, while
`ApplyRequestTransform` returns `any` and happily emits a slice. **So a `requestTransform` producing
RFC-6902 was reachable, documented, and non-functional** — it died one layer later as
`invalid body type: []interface {}`.

Confirmed against a real Ironic, whose PATCH accepts only a top-level array: `PATCH /v1/nodes/<id>` with a
559-byte patch array now returns 200.

An **empty array is deliberately not treated like an empty object**. `[]` is a well-formed no-op patch and
must reach the server; `{}` is the assembled-but-unset body this code declines to send. Folding them
together would make a legitimate no-op indistinguishable from "nothing to send".

### Why these three are one release

The first makes a resource creatable; the second makes drift detectable; the third makes the resulting
write sendable. A node whose `instance_info` existed as `{}` upstream needed all three: without the second
no PATCH was issued, without the third the patch could not be built, and without the first the kind could
not be created at all.

## 2026-10-30 — 0.42.0

Eight changes. Nothing here can destroy infrastructure; the theme is that several signals were reporting
something the code had never established, and consumers acted on them.

### Ready did not mean "you can apply a CR of this kind"

A RestDefinition reported `Ready=True` before the apiserver was serving the kinds it generates. The obvious
workflow — **wait for Ready, then apply** — failed with `no matches for kind`, which is indistinguishable
from a missing CRD.

Measured on fresh clusters, twice: five RestDefinitions reported Ready at t+148s while five of the ten
generated CRDs appeared at t+149s. Small window, reproducible, and far more expensive to diagnose than to
hit — it took three rounds and a wrong hypothesis to localise, against a controller that was behaving
correctly throughout.

Nothing on the readiness path read a CRD. `deploy.Lookup` hashes the ConfigMap and fetches nothing else, and
no `Established` condition was read anywhere in the provider.

**The gate asks the RESTMapper, not the CRD object**, and that distinction is the fix rather than a detail. A
CRD can exist and report `Established` while the specific **version** a CR will be written through is not
served — and the two generated halves are served at *different* versions. `no matches for kind X in version
V` names a version, and the RESTMapper is what produces it, so asking the component that rejects the apply
is the only check that cannot disagree with it. A first attempt at this fix gated on `Established` and was
withdrawn after a measurement showed `Established` **and** discoverable **and** the apply still failing.

A `NoMatch` is an answer; discovery being unreachable is not. The latter returns an error and leaves the
condition alone, because reporting `Creating` on every discovery hiccup would make a healthy install flap.

### The generated API version is now in the default `kubectl get` output

`kubectl get restdefinition` showed `NAME/READY/AGE`. The `API VERSION` column existed but was
`priority=10`, so it appeared only under `-o wide`.

The resource CRD's version derives from the OAS `info.version` (normalized by crdgen) while the
Configuration CRD is always `v1alpha1` — so **the two halves one RestDefinition generates are served at
different versions**, and an OAS declaring `info.version: "2.1"` yields `v2-1` for one and `v1alpha1` for the
other. That split is not guessable.

Three parties independently concluded the value was unpublished: a blueprint author hardcoded `v1alpha1` in
seven examples, a consumer reimplemented crdgen's version normalizer in their own validator, and an issue
was filed asking the provider to publish a field that already existed. **`.status.resource.apiVersion` and
`.status.configuration.apiVersion` had carried it all along.** Nobody ran `kubectl get restdefinition -o
yaml`.

So this was a discoverability defect, not a missing capability, and a column fixes more than documentation
would: it puts the answer in front of the reader at the moment they are asking. `CONFIGURATION` is added at
`priority=10` as well, since the asymmetry is what causes the confusion. An empty value there means **no
Configuration CRD was generated** — only `configurationFields` or security schemes produce one — rather than
"unknown".

### Request bodies are no longer sent chunked

Every bodied request went out with `Transfer-Encoding: chunked`. The request is built as a struct literal,
so `http.NewRequest`'s type assertion never runs — and `io.NopCloser` would defeat it anyway — leaving
`ContentLength` at 0 with a non-nil body, which `net/http` reads as "length unknown".

Chunked framing buys nothing for a body already fully buffered in memory, and some HTTP front ends refuse
it. **Confirmed on real hardware**: against a lab Ironic, a power-on `PUT` went from `400 "Missing mandatory
parameter: target"` to `202 Accepted` on identical bytes through the same relay, and a provision transition
moved a physical blade from `manageable` to `available`.

Worth recording what this is *not*: it was first reported as the cause of those 400s, then a control — same
client, same framing, same relay, different server — showed a local Ironic parsing the identical chunked
body. So chunked bodies are readable as sent, and that lab's front end is what could not read them. The fix
removes an unnecessary framing layer and a legitimate reason for a front end to refuse us; it is not a
server-side bug fix, and nobody should expect it to resolve a specific 400.

**Upgrade note.** This fixes the *cause* of an interrupted create but does not clear what one leaves behind.
A resource that failed this way keeps `krateo.io/external-create-pending`, and in unstructured-runtime up to
v1.6.1 that marker triggers a separate wedge whose own recovery `Update` conflicts with the status write its
own `Observe` just performed — measured at 51 consecutive failures and 0 recoveries over 20 minutes. After
upgrading, check for leftover markers:

```sh
kubectl get <kind> -A -o json | jq -r '
  .items[] | select(.metadata.annotations["krateo.io/external-create-pending"])
  | "\(.metadata.namespace)/\(.metadata.name)"'
```

Anything listed needs unstructured-runtime v1.7.1 to recover. The symptom otherwise surfaces two layers away
as an application error that never mentions annotations.

### A request body's media type is resolved instead of assumed

The request-body schema was looked up as `application/json` and nothing else. A document declaring
`application/json-patch+json`, or any other single media type, lost its body schema entirely — and the
request was still **sent**, built from no schema, with `compareScope: updatable` degraded. A vendor's 415
would have been legible; this was not.

The media type is now resolved: `application/json` when present with a schema, a single declared entry
otherwise, and an error naming the candidates when the document is genuinely ambiguous. A body with no
schema at all returns empty rather than erroring — an earlier version of this change was too strict and an
existing test caught it.

### Create refuses to run for an object that is being deleted

`Create` could issue an external create for a CR whose `deletionTimestamp` was already set, producing an
external resource that nothing would ever reconcile or remove once the delete completed.

### Three signals that reported something the code never established

**A delete with no `delete` verb logged at ERROR.** A RestDefinition declaring no delete verb is a
supported shape — a kind whose external resource is deliberately not removed by Krateo — and that branch
logged an error on its own success path. A session migrating 17 live `PullRequest` records against a real
GitHub organisation would have seen 17 ERROR lines at exactly the moment an operator's correct instinct is
to stop at the first error; they had to be warned in advance to ignore them. Now Info, naming the
deliberate consequence.

**A node-disk failure was reported inside a ConfigMap sentence.** A full node disk produced `fetching OAS
document "configmap://…": failed to create directory: no space left on device`. Every frame was true, but
the only identifier was a ConfigMap path while the failure was the node's filesystem, so the reader checked
all twelve ConfigMaps before reading to the end of the line. The inner error now names the subsystem.

**snowplow's failed-stage header was discarded.** `Resolve` read the response and threw away every header,
including `X-Snowplow-Stage-Outcomes`. snowplow answers 200 whether or not a stage failed, so a RESTAction
whose teardown stage **failed** was indistinguishable from one that succeeded. The value now reaches the
caller, with the observe path warned separately because a partial `.status` feeds both the existence and the
drift predicates — it can create a resource that already exists, or report drift forever.

One thing that does **not** follow from this: the header lists only *failed* stages, so empty means "nothing
reported a failure", not "work was done". A stage that ran, fetched and iterated over **zero items**
succeeds and appears nowhere in it. That was the real shape of a delete campaign where snowplow returned
200, outcomes were `[]`, the controller logged a successful dispatch, and only a verification GET knew the
resource was still there.

### An optional node-local OTLP endpoint for the generated controllers

`rdc.otel.nodeLocalEndpoint`, default false. When enabled, the generated controllers send OTLP to a
collector on their own node via `HOST_IP` rather than to a cluster-wide service.

### Documentation

A migration guide for installs made before 0.20.0, when the rest-dynamic-controller was a separate
repository and chart. Also corrections to the 0.41.0 entry: the image-size figures now say which is
compressed and which is on-disk — 33.9 MB over the wire against 117 MB unpacked, where the earlier text
quoted one number for both questions — and the orphan-delete precondition is quantified from the 400-trial
measurement rather than described.

## 2026-10-29 — 0.41.0

Six changes. The first can destroy a customer's infrastructure and is the one to act on.

### `deletion-policy: orphan` could delete the external resource anyway

`krateo.io/deletion-policy=orphan` says "tear down the CR, leave the external resource". Up to and
including 0.40.0 it did that **most of the time**.

unstructured-runtime's worker removed the finalizer and then **did not return**. Execution fell through to
`handleDelete`, whose fetch is a live apiserver `Get`, and called `externalClient.Delete`. So the external
resource was destroyed on the very pass that honoured the orphan policy — whenever that fetch beat the
apiserver's reap of the now-finalizer-free object.

**`orphan` was therefore non-deterministic rather than safe**, which is worse than a plain bug: a handful
of trials showing no delete looks like proof. Fixed upstream in `unstructured-runtime` v1.6.1 (#23/#24
there) and picked up here (#286).

Two things worth knowing beyond the fix:

- **It removes the cliff; it does not enforce the precondition.** A single additional finalizer still
  changes the behaviour completely and nothing warns you. v1.6.1 pins that distinction by test
  (`orphan-one-finalizer`, `orphan-two-finalizers`) but does not enforce it at runtime. **For resources
  you cannot replace, strip the finalizer explicitly rather than relying on the annotation.**
  <br><br>
  **Measured, so this is not a theoretical caution** — 400 trials across four timing offsets by the
  installer session: the **sole**-finalizer case failed **0 of 40** at every offset, and an **extra**
  finalizer failed **40 of 40**. Not "may"; every time. The checkable pre-check is therefore the rule
  rather than any claim about controller behaviour:
  <br><br>
  ```
  kubectl get <kind> <name> -n <ns> -o jsonpath='{.metadata.finalizers}'
  # SAFE only if exactly ["composition.krateo.io/finalizer"].
  ```
- `composition.krateo.io/finalizer` is unchanged across v1.4.0, v1.5.0, v1.6.0 and v1.6.1 — checked at
  each version rather than at the endpoints, because a constant that changed and changed back would pass
  an endpoint comparison. Two downstream migration runbooks depend on that exact string.

Found while answering a question from a session whose CRs manage physical hosts in production. The defect
had survived for months **beside a test named for the exact scenario**, which drove the event as
`EventType: Observe` — so the fall-through landed in `handleObserve` and the buggy branch never executed.

### A delete that could never complete now completes

A delegated delete verified its own success with a GET and returned that GET's error as its own. For a
**transient** failure that is right — "could not check" means retry, and that rule is what stops a live
resource being orphaned. For a **permanent** one it wedged: a CR whose create failed never had an
identifier written to its status, so the GET path could never be built, the error repeated on every
reconcile, and the finalizer was held until a human stripped it by hand.

Measured at **eight hours** on a CloudServer whose create the vendor refused, while a Vpc in the same
state deleted immediately — because the native delete path has had this guard all along and the delegated
one never consulted it (#261).

Live-validated on a real cluster: a CR wedged for 41 minutes deleted **20 seconds** after the fix rolled
out, with the finalizer released and no verification GET attempted.

One measurement note for anyone validating an RDC change the same way: RDC Deployments are rendered by
oasgen from a ConfigMap template, so changing `rdc.image.tag` re-renders each generated controller **on
its own reconcile schedule**. Controllers move at different times. Verify the candidate image is running
on the specific controller under test before reading anything — the failure mode is a false negative,
which is the kind that gets believed.

### Every external write now leaves an audit record

Each external write emits the canonical Krateo AuditEvent as an OTLP LogRecord, carrying the CR's `uid`
and the `generation` the reconcile acted on — the keys that let a record of the *user's action* and a
record of the *external effect* be matched (#253). Enable with `-otel-logs-enabled` /
`OTEL_LOGS_ENABLED`, which is deliberately **separate** from `-otel-tracing-enabled`: audit records are a
compliance signal, not diagnostics, and nobody should have to turn on traces to get them.

**Ten emit sites, not three**, and the difference is the useful part. Enumerating the verbs gives
Create/Update/Delete and looks complete once each native path is instrumented. Enumerating what actually
*calls out* gives ten: each verb has a native path **and** a delegated one through snowplow's `/call`,
plus `dispatch`, which is the external write for resources that declare no create verb at all. Reads are
not audited.

**`http.response.status_code` is present on native writes and absent on delegated ones.** That is
deliberate. A delegated call goes through snowplow, so the only status this controller can observe is
*snowplow's*, not the vendor's — reporting it would state in a compliance record that the external API
answered when nothing of the sort was established. Correlation is unaffected: snowplow's own write audit
carries the same keys for the call it served.

`server.address` is reduced to `host[:port]`, dropping scheme, userinfo, path, query and fragment,
centrally in the emitter rather than at each call site. That is not tidiness: vendor paths routinely carry
identifiers and at least one in use carries a token-shaped segment.

### `Observe` no longer strands a resource on a failure that will never clear

`Observe` returned an error for failures that are permanent and **local** — a template that will never
render — and provider-runtime resolves an unresolved create handshake only *after* Observe answers. An
error return skips that, so a half-created RestDefinition whose template cannot render was wedged on every
runtime version, and no bump could fix it (#266).

`deploy.Deploy` was the hard part: it renders manifests **and** dry-run Applies them, and reported both as
the same bare error. Only the render half is permanent; a dry-run Apply can fail because the apiserver is
briefly unavailable and must still retry. The two are now distinguished at the source by a typed error
rather than by inspecting a message, and Observe answers `ResourceExists: false` with an `Unavailable`
condition **naming the template that cannot render** — without which the object shows only that it is not
ready, which is indistinguishable from the expected two-phase intermediate state.

### The provider image is 65% smaller, and that was never only about pull time

331 MB → **116 MB**. The runtime stage shipped a full Go toolchain, apt and a shell to run two static
binaries, none of which anything invokes (#271).

> **Correction (2026-10-29):** 331 MB → 116 MB is the **on-disk** size, which is what `docker images`
> reports and the right figure for the disk-competition argument below. It is the **wrong** figure for
> pull time, which moves compressed layer bytes. Measured from the registry manifests:
>
> ```
> 0.40.0  arm64  338.4 MB compressed, 11 layers
> 0.41.0  arm64   33.9 MB compressed, 16 layers      -> ~10x, not 2.8x
> 0.41.0  amd64   38.0 MB compressed, 16 layers
> ```
>
> So the pull improvement is an order of magnitude and this entry understated it. Both numbers matter and
> they answer different questions: on-disk for what the image costs a node, compressed for what a pull
> costs. Quote whichever the claim actually rests on. Raised by the aruba-provider-kog session, who
> measured it independently before I did.

The argument that justified touching it is second-order: the chart mounts `/tmp` as an `emptyDir` backed
by the **node** filesystem, and the provider needs writable disk there to parse an OAS document at all. So
the image competes for the same disk the parser needs. On a node whose disk filled, five RestDefinitions
sat `READY=False` with no controllers created, surfacing upstream as an unexplained readiness timeout
several layers from the cause. That error message is tracked separately (#277).

### Update's status write is retried

The last of the three status writes left as a bare `Status().Update`. It cannot wedge the resource the way
a lost create-phase write can — by the time Update runs the create handshake is long resolved — but a lost
write loses `status.digest`, `status.oasHash` and `status.resourceHash`, so the next Observe compares a
stale digest against a freshly rendered one, reports drift that does not exist, and Update runs again. A
redeploy loop that resolves itself, competing on a bulk install for exactly the API server attention that
caused the conflict (#264).

### Known and tracked

- A `Create` can be dispatched against a CR whose `deletionTimestamp` is already set, repeatedly. On a
  resource whose create succeeds this provisions something the in-flight delete then orphans, with the
  identifier possibly never persisted (#279). **Pre-existing, not new in this release.**
- RDC has no recording external client, so no delete-path test can assert whether the external call was
  actually made (#284).
- `charts.krateo.io` still serves oasgen-provider appVersion `0.10.0`, thirty releases old and below the
  credential-redaction fix. Use the OCI charts (#278).
- `helm package` strips `Chart.yaml` comments, so the `kubeVersion` floor's rationale is invisible in the
  published chart (#280).
- A node-disk failure is reported inside a sentence whose subject is a ConfigMap (#277).
- A parameter whose name collides with a body property still loses its validation keywords (#257).
- `uniqueItems` on an array of objects can emit a CEL rule whose static cost the API server may refuse
  (#270).

## 2026-10-29 — 0.40.0

A security release as much as a correctness one. Four changes; the first is the one to act on.

### 36 and 34 standard-library vulnerabilities the code actually called

`govulncheck`'s symbol results, before and after:

| | before | after |
|---|---|---|
| oasgen-provider | **36 called** | 0 called |
| rest-dynamic-controller | **34 called** | none found |

Thirty of them had **no fix at any dependency version** — `net/http` and `crypto/x509` in the standard
library, including GO-2026-6809, *"distrust compromised intermediate certificates"*. Those close by
building on a newer toolchain and by nothing else.

That one matters most for **rest-dynamic-controller**, whose entire job is outbound TLS to vendor APIs: a
certificate-trust defect there sits in the path of every external call it makes.

Both modules move to `go 1.26.0` with `toolchain go1.26.10`, and all three Dockerfile stages to
`golang:1.26-trixie` (currently 1.26.10). `golang.org/x/net` goes to v0.62.0, which also carries `x/text`
to v0.43.0 and `x/tools` to v0.51.0 (#267).

Two things worth knowing if you maintain something similar:

- **The `go` line stays at 1.26.0, not 1.26.10, deliberately.** A patch-level `go` line would force every
  consumer's floor up on every standard-library CVE — a recurring cost imposed downstream to make one
  scanner quieter. The `toolchain` line does the same job for our builds and scans without touching
  anyone's floor.
- **No CI change was needed, and the reason is easy to get wrong.** The `go` command honours the
  `toolchain` line by itself under `GOTOOLCHAIN=auto`, regardless of which Go is installed — measured with
  a 1.25.1 install reporting `go1.26.10` inside the module. So whatever Go a runner installs is
  superseded. What `govulncheck` responds to is the **selected toolchain**, not the `go` directive; a test
  series that varies the directive moves two things at once and establishes neither.

### A conflicted create is now prevented from wedging, not merely bounded

0.39.0 moved to provider-runtime v1.5.1, which **bounds** an unresolved create handshake: Observe is asked
whether the resource exists, and if it is absent the marker is re-observed for a two-minute grace period
and then cleared so create runs again. Measured on a real cluster at 2m0.1s over nine passes.

v1.7.0 **prevents** it. A conflicted create is stamped `external-create-failed` and retried on the **next
reconcile** instead of waiting out the window. On a bulk install that window is incurred per object, so on
the ~60-RestDefinition installs where this was measured 2 of 2 wedged, the saving is not marginal (#265).

v1.7.0 also carries a diagnostic that matters here specifically: when Observe errors while a create
handshake is unresolved, `Synced` reads *"external create pending since `<t>` cannot be resolved until
observe succeeds: observe failed: `<err>`"*. That is the state #266 produces — and without it, a
RestDefinition stalled by a permanent render failure shows only the Observe error, whose symptom ("CRDs
present, no Deployment") is **indistinguishable from the expected two-phase intermediate state** described
in the 0.39.0 entry. With it, the object names both facts.

### The status Create persists is now snapshotted rather than listed

0.39.0 fixed an instance of a dropped-status-field bug and left the class. The guard shipped with it
verified that the applier applied what it was *given*; it could not verify that the collection site had
collected everything — and the collection site was where the bug was. Deleting any of seven captures left
the entire suite green, 7 of 7.

The status this phase establishes is now written onto the object and **snapshotted**, so the set is
complete by construction: the step that provisions auth-secret RBAC already writes its two fields there in
place, and a future step that sets another lands in the same place with nothing to remember (#263).

One deliberate subtlety: conditions come from the **target**, not the snapshot. The write runs against a
freshly read object, so replacing its condition list wholesale would discard a condition another writer
set between the read and the write — which is the whole reason the write re-reads per attempt. The data
fields are the phase's to own; the condition list is shared.

Create's **first** phase is deliberately unchanged, because it establishes a *subset* — it runs before the
Deployment and auth-secret steps exist to be recorded, so snapshotting there would write back the rest of
the persisted status and clobber fields it has no business touching.

### Swagger 2.0 parameters carry their validation keywords

0.38.0 carried `pattern`, `minLength`, `maximum` and the rest into the generated CRD — for **OAS 3
documents only**. In OAS 3 a parameter has a `schema`, which goes through the same converter the request
body uses, so parameters inherited the fix for free. Swagger 2.0 has no `schema` on a non-body parameter:
the facets sit inline, so they have to be copied by hand, and they were not.

The two notations therefore disagreed about whether a declared constraint was enforced — the same
`pattern` on a path parameter reached the CRD from a 3.x document and vanished from a 2.0 one. Fixed, with
a test that compares whole rendered field schemas between equivalent documents in both notations, so a
keyword added to one path and not the other fails without anyone remembering to extend the assertions
(#255).

One limit worth stating, because it is not fixable here: `libopenapi` types 2.0's `minimum`, `maximum` and
`multipleOf` as integers where the specification defines them as numbers. A fractional value fails to
unmarshal and is discarded below both model levels, so it can be neither carried nor warned about —
measured, `minimum: 0` survives while `maximum: 2.5` vanishes. Integer bounds, the common case, are
unaffected.

### Known and tracked

- `Observe` still errors on permanent local failures, which stalls a RestDefinition entirely (#266).
- `Update`'s status write still has no retry; it cannot wedge, but a lost conflict costs a redeploy loop
  that resolves itself (#264).
- A `uniqueItems` on an array of objects can emit a CEL rule whose static cost the API server may refuse,
  and nothing tests that branch (#270). No document in use declares it.
- A parameter whose name collides with a body property still loses its validation keywords to merge
  precedence, in both notations (#257).

## 2026-10-28 — 0.39.0

A RestDefinition could wedge permanently on first install. The fix is in three parts, and the
load-bearing one is a dependency bump rather than anything written here.

### The permanent wedge was a stale provider-runtime pin

A RestDefinition could stick on `krateo.io/external-create-pending` reporting *"cannot determine creation
result"*, on every reconcile, with no remedy short of a human deleting the annotation. Measured at 2 of 2
fresh clusters under a ~60-RestDefinition install.

The reason it was terminal was not in this repo. In provider-runtime **v1.3.0**, when `Create` returns an
error the reconciler returns early on a resource-version conflict *before* stamping
`external-create-failed`, so `pending` stays the newest annotation and the guard that checks it refuses to
proceed, forever. `kerrors.IsConflict` unwraps, so wrapping the error — which this provider does — does
not avoid it.

**provider-runtime fixed that in v1.4.0** and oasgen-provider was pinned to v1.3.0, three minor versions
behind a fix released for this exact failure. 0.39.0 moves to **v1.5.1**.

What the upstream fix does (provider-runtime#10): an unresolved create now asks `Observe` whether the
external resource exists, rather than refusing on the ambiguity. Present → adopt it. Absent → keep
refusing and re-observe for a two-minute grace period, so a create that did land is adopted rather than
duplicated. Still absent after that → the create genuinely did not land, so clear the marker and let
create run again. The duplication guard is not relaxed; it is relocated into a window bounded by evidence.

Two consequences worth knowing:

- **Resources already wedged recover by themselves** on this version. On 0.38.0 and earlier they could
  not, and the documented remedy was manual annotation surgery.
- **Recovery takes about two minutes — between 2m00 and roughly 2m15**, not until the next reconcile. Not
  *up to* two minutes: the window has to elapse in full before the marker is cleared, so recovery is never
  sooner, and it lands on the first 15-second re-check past the two-minute mark. Measured end to end
  through the real reconciler at 2m0.1s over nine passes. The `external-create-pending` timestamp is
  stored to the second, so the window can end up to a second early.

  The conflict branch upstream still does not stamp `failed`, which is why the window is waited out rather
  than short-circuited. That remains open upstream and is an optimisation, not a second wedge.

  Do not confuse this with the **one-minute** `creationGracePeriod` this provider configures. That one
  covers a create known to have *succeeded* whose external resource is not yet visible; this one covers a
  create whose outcome was never recorded at all. Different windows answering different questions, and
  only the second is involved here — so a resource taking longer than a minute to recover is not evidence
  of a second fault.

If you maintain another provider on this runtime, check your pin. `git-provider` was already on v1.4.0;
`core-provider` and `chart-inspector` were not.

### Both of Create's phases now survive a status conflict

`Create` is deliberately two-phase: the first applies the CRDs and returns, and the next reconcile, now
seeing the CRD, creates the controller Deployment. So **"CRDs present, no Deployment" is an expected
intermediate state**, not damage — worth knowing before diagnosing a slow install from that reading
alone. What was unsafe was how each phase ended: a plain status write, returned as an error on conflict,
reaching the runtime path above.

Both are now retried, re-reading the object on each attempt so a concurrent change is merged rather than
clobbered by a stale write.

Phase 1 (#252) also records the version its CRDs were applied at. Without it `observedVersion` fell back
to `v1alpha1`, the next `Observe` looked the CRD up at the wrong version and reported it absent, and the
runtime re-entered the create handshake on a resource it had already created. The re-check that absorbs
that is gated on having a parsed document, and the branch that reads saved status has none — so recording
the version is what demotes that re-check from sole guard to safety net.

Phase 2 (#259) is the more consequential of the two, and was the one left unprotected by #252. It records
`status.digest`, `status.oasHash` and `status.resourceHash` — the values `Observe` compares against to
decide whether anything drifted. Losing that write does not merely lose a status field: the next `Observe`
compares an empty digest against a freshly rendered one, reports drift that does not exist, and `Update`
redeploys on every reconcile thereafter.

A conflict that never clears still surfaces, bounded. Retrying forever would turn a wedge into a hang,
and swallowing it would report a success that wrote nothing.

**Not the same bug as #122.** That fix (`resourceExistsForController`) shipped in 0.23.1 and was present
on the clusters where these were measured. Three distinct things now produce "cannot determine creation
result", so an older diagnosis of that symptom may describe a different cause.

### How this was found, and what the first attempt got wrong

By installing it. Both failure paths were measured on independent fresh GKE clusters by the snowplow
session, which ran a check before teardown that separated them — one showing the status update conflicting
70ms after the CRDs applied, the other showing `pending` re-set 97 seconds *after* the succeeded stamp.
Neither is reachable from a unit test: the trigger is concurrency during a bulk install, and the symptom
is an annotation the runtime leaves behind rather than anything this repo writes.

The first fix (#252) covered phase 1 only and was shipped as a complete fix for the wedge. An independent
review failed it, correctly, on two counts: phase 2 was still exposed, and the permanent wedge was in the
runtime rather than here. Both are addressed above. The review's own analysis was of v1.3.0 — the version
this provider pinned — which is how a fix released three versions earlier came to be described as missing.
Checking what the dependency already does belongs before deciding what to write.

One limit stated rather than glossed: the decisive log line for the phase-2 diagnosis was not retained and
the cluster is gone, so that half is consistent with the evidence rather than proven by it.

## 2026-10-28 — 0.38.0

Two fixes, both the same shape: a constraint the document declared, that the generated CRD did not carry.

### Validation keywords reach the CRD

A `pattern`, `minLength`, `minimum` or `maxItems` written in an OAS document was read off it and then
discarded. The CRD enforced none of them, so the document looked as though it declared a constraint that
nothing applied — which is worse than not supporting them, because the declaration reads as working
(#249).

Now carried: `pattern`, `minLength`, `maxLength`, `minimum`, `maximum`, `exclusiveMinimum`,
`exclusiveMaximum`, `multipleOf`, `minItems`, `maxItems`, `uniqueItems`.

This was never a regression. The domain schema had no fields to hold them and the adapter never read
them, so they died at that boundary, before CRD generation was reached. Anyone who checked the
transpiler and found it handles `pattern` was looking at the right file and the wrong layer: the
transpiler does carry them; oasgen never handed it any.

Three behaviours worth knowing if you write these:

- **A `pattern` is compiled before it is emitted, and dropped with a warning if it fails.** Kubernetes
  compiles CRD patterns as RE2 while OpenAPI specifies ECMA-262, so a backreference or lookahead is valid
  in a document and rejected by the API server — which refuses the *entire* CustomResourceDefinition, not
  just that one validation. Losing a constraint is better than losing the resource, and the warning names
  the path so it can be rewritten.
- **`exclusiveMinimum`/`exclusiveMaximum` are the boolean form**, which is what OAS 3.0 means and the only
  thing a CRD can express. OAS 3.1 redefines them as numbers; that form is dropped and logged rather than
  coerced, because treating `exclusiveMinimum: 5` as `true` would attach the bound to whatever `minimum`
  happens to say.
- **`uniqueItems` becomes `x-kubernetes-list-type: set`** in the generated CRD. That is the API
  server-enforced form and the stronger one — structural schemas forbid a raw `uniqueItems: true` — so its
  absence under that name is correct rather than a propagation failure.

### A field the controller supplies is no longer required of the author

A request-body field that a `fieldMapping` resolver supplies was still emitted into the CRD's
`spec.required`, so an author had to write a value the controller then overwrote (#246).

For `secretRef` — shipped since 0.12.0 — that was worse than redundant: the obvious thing to put in a
required field called `token` is the credential, in cleartext, in the CR spec, which is precisely what
`secretRef` exists to avoid. For a `dependentLookup` it made the feature inert, since a `when` guard keyed
on the field's absence can never fire if the field is mandatory.

The requirement is dropped; the property stays. The API still accepts the field, and an explicitly
supplied value is exactly what a lookup's `when` guard exists to honour. Only `inBody` entries are
affected — a resolver on a path or query parameter does not correspond to a spec property, and a required
*parameter* that merely shares a name with a resolver-supplied body field keeps its requirement.

### Also

A guard that deep-copies a schema with every field populated and compares field by field, because
`Schema.deepCopy` assigns fields one at a time by hand and a field added elsewhere and forgotten there is
dropped silently — with no symptom at the point of the mistake, only a missing constraint in a CRD layers
away. That is how the keyword bug above stayed invisible until someone measured a generated CRD on a live
cluster. The guard found a second instance on its first run: `Extensions` was already being dropped,
unrelated to any of this.

## 2026-10-28 — 0.37.0

One addition: a CR can now tell "nothing to report" from "nothing was read".

### An absent status field no longer means two different things

A resource declares `additionalStatusFields` such as `failureReason` or `error`, and the controller
populates them from the observe response. A field the response does not contain is skipped, which is
correct — most APIs omit a diagnostic they have nothing to say about. The consequence was that a CR looked
identical in three quite different situations (#233):

1. the API returned a body and genuinely had no diagnostic — **healthy**;
2. the API returned a body and the diagnostic sat at a path the RestDefinition does not name —
   **mis-declared**;
3. the observe obtained **no body at all** — the diagnostic exists and is not being read.

The third is the one that costs something: a resource whose failure reason is being dropped is
indistinguishable from a healthy one. Measured on five live resources, every healthy payload omitted both
`failureReason` and `error` even though both are declared in the vendor's published specs — so absence is
the ordinary case, not a signal.

A new `Observed` condition records whether the last observe had a body to read from:

```yaml
- type: Observed
  status: "True"
  reason: ResponseObtained
```

```yaml
- type: Observed
  status: "False"
  reason: NoResponseBody
  message: "the observe obtained no response body, so declared status fields were not
    populated and their absence is not evidence about the external resource"
```

It sits alongside `Ready` rather than replacing it, and says nothing about any individual field — which is
deliberate. The obvious alternative, warning when a declared status path is missing from the response,
would fire on every healthy resource of that API. That is the same mistake as warning that an async
create's `202` does not match the resource schema, when carrying an operation handle instead is exactly
what a `202` is for (#228).

**Not `Ready=False`.** A bodyless observe does not mean the resource is unhealthy — existence may have been
decided from a status code alone. What is unknown is the content of its status, and conflating the two
would make every bodyless observe look like a failure.

Two details worth knowing if you build on it. The condition **transitions** rather than only ever being
set, because `lastTransitionTime` marks when a status last changed: an always-True marker would report the
first observe forever. And it is applied only when it would actually change, because the write path does
not diff — setting it unconditionally would issue a status update on every reconcile, for as long as a
resource exists.

## 2026-10-27 — 0.36.0

A new primitive and the removal of the limitation that made a correct RestDefinition send an incomplete
request.

### Dependent lookups: a pre-call read that feeds the request

Some APIs require a value the user cannot know. Creating a git reference needs the sha to branch from,
and "a branch off the default branch" means two dependent reads before the create: the repository's
`default_branch`, then that branch's `object.sha`. There was no way to express that, so the one route
needing it kept a hand-written proxy alive for an otherwise fully native provider — 28 of its 31 kinds
needed none (#120).

A **dependent lookup** is a named pre-call read whose selected value is substituted into a field of the
main request:

```yaml
dependentLookups:                      # a sibling of verbsDescription
- name: defaultBranch
  method: GET
  path: /repos/{owner}/{repo}
  parameters:
  - {inPath: owner, inCustomResource: spec.owner}
  select: .default_branch
- name: baseSha
  method: GET
  path: /repos/{owner}/{repo}/git/ref/{ref}
  parameters:
  - inPath: ref
    fromLookup: defaultBranch          # the chain: link 1's result is link 2's parameter
    valueMapping: {type: jq, jq: {inline: '"heads/" + .'}}
  select: .object.sha

verbsDescription:
- action: create
  fieldMapping:
  - inBody: sha
    resolver:
      type: dependentLookup
      dependentLookup:
        name: baseSha
        when: {customResourceFieldAbsent: spec.sha}
```

Lookups are declared as a sibling of `verbsDescription`, which is the specific thing the removed
`apiLookup` resolver could not do: its action had to name one of the five CRUD verbs, a closed enum, so
an auxiliary endpoint was inexpressible.

The other shape is a **match** over a list — find the item whose field equals a CR value and take a field
off it, which is what binding a Keycloak flow by alias instead of UUID needs. The two shapes arrived as
separate requests from two providers and turned out to be one feature.

**Three things it deliberately does not do**, each an absence rather than a safeguard:

- **No "take the first match".** An ambiguous match is always an error, and the message does not name the
  candidates it declined to choose between. `apiLookup` scoped its list server-side and then took
  `items[0]`; against Keycloak, whose flow endpoint ignores the alias filter and returns everything, that
  bound the **wrong id while reporting success**.
- **`fromLookup` may only name an earlier lookup**, enforced in the CRD. A cycle is unrepresentable
  rather than detected, so there is no solver and no runtime check to get wrong.
- **`GET` only.** A POST search needs a request body and there is no field to declare one; admitting POST
  would ship a value that cannot work.

A `when` guard skips a lookup when the CR already supplies the value, so an explicit field wins and costs
no API calls.

Nothing can use this yet: a lookup can only name a path its document declares, and the GitHub provider's
documents carry no git-ref operation. Worth saying plainly — the proxy there dies as soon as the CR
carries `spec.sha`, which is free and independent of this feature.

### A resource with an update verb and no create verb now has a usable spec

A document with no create endpoint is not necessarily a read-only resource. Aruba's CMP profile declares
`PUT`/`GET`/`DELETE` for `aiKeys` and no `POST` at all, and that was treated as read-only: the spec
contained nothing but parameters and identifiers, so the declared `update` sent a body missing the vendor
fields it was supposed to carry — and generation reported **no diagnostic at all**, because each half was
behaving exactly as written (#236).

The spec base is now resolved by explicit precedence — create, then dispatch, then update — rather than by
position in `verbsDescription`. That ordering is the substance of the fix and not a detail: the previous
code took the first create-or-dispatch verb it met, so simply admitting `update` alongside them would have
let the order of YAML keys decide which body defines the spec, silently, for every resource declaring
both.

For the AiKey case the spec goes from `metadata` reduced to the identifier alone, with no `properties`, to
the vendor's full metadata — `name`, `location`, `tags` — plus `properties`. Such a resource is
adopt-then-update rather than read-only, and the generated identifier description now says so.

The fallback applies only when no create or dispatch verb is **declared**. A create verb that is declared
but carries no body keeps its existing empty base: borrowing the update body there would inject fields
into the spec of a resource that defines itself differently.

### Also

A guard that runs the shipped CRD through the apiserver's own validator, so a validation rule whose
estimated cost exceeds the budget fails a test instead of failing `helm install`. That failure mode took a
CRD down once before (#169), and nothing in the repo could catch it: the CEL tests evaluate rules with
`cel-go`, which happily runs a rule the apiserver would refuse to install. It immediately earned its place
— two rules written during this release measured over 100× the per-rule budget and moved to generation
time as a result.

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
