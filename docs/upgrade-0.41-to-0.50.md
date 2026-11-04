# Upgrading from 0.41.0 to 0.50.0

**This is evidence for a pin decision, not a recommendation.** It records what the span changes, which tag
first carries each security fix, what is *not* a barrier, and what needs an operator action. Choosing the pin
is the operator's call; nothing here argues for a particular one.

Every version claim below was measured against the tags rather than read off a changelog. The method, and the
control that makes its negatives trustworthy, is in [Method](#method-how-each-row-was-dated) at the end.

---

## 1. The security floor

The feature floor and the security floor are different numbers, and the security floor is the one that is easy
to miss — the smallest delta is exactly what lands *between* a vulnerability and its fix. Each row is the
**first tag containing that commit**:

| First tag | Change |
|---|---|
| **0.46.0** | A denied `403` stops retrying instead of retrying forever, without breaking throttle recovery |
| **0.49.0** | The cached authn JWT is invalidated on a `401`, so a signing-key rotation self-heals |
| **0.50.0** | Endpoint policy: decide whether a credentialed request may reach a destination (#379 part 1) |
| **0.50.0** | Endpoint policy **enforced on every credentialed request** (#379 part 2) |
| **0.50.0** | A header or cookie parameter populated from the CR now **refuses the nine credential header names** |
| **0.50.0** | Every credential header is exempt from validation, not only names containing `authorization` |
| **0.50.0** | A `401` with no cause may be post-dispatch, so retrying it can duplicate an audited write |
| *unreleased* | Redirects refused where a credential is presented (#401) |
| *unreleased* | A discarded document server URL is reported rather than silently dropped (#403) |
| *unreleased* | An operator allowlist entry carrying a path is refused; the whole entry is lowercased (#402) |

**Five of the ten land in 0.50.0**, and the three unreleased ones are queued for 0.51.0.

---

## 2. The headline: credentialed requests get an endpoint policy at 0.50.0

This is the largest single difference in the span, and it is about the credential your controllers actually
hold.

| Tag | `hostpolicy.go` | `(*Policy).CheckRedirect` | `(*Policy).DialContext` | wired into `restclient.go` | control: `restclient.go` present |
|---|---|---|---|---|---|
| 0.41.0 – 0.49.0 | **ABSENT** | – | – | **0** | EXISTS |
| 0.50.0 | EXISTS | 1 | 1 | **1** | EXISTS |

Two mechanisms, both absent below 0.50.0:

- **`CheckRedirect` re-applies the host list to every hop.** This matters more than it sounds, because
  net/http's own `Authorization` strip is **not** a complete guard: `shouldCopyHeaderOnRedirect` matches on
  header name and only against a *non-subdomain* change, so the standard library **permits** a credentialed
  hop to a subdomain. That is documented stdlib behaviour, not a bug — but it means "Go strips Authorization
  on redirect" is false as a general statement.
- **`DialContext` resolves once and dials the chosen address literally**, with a `net.Dialer.Control`
  backstop that re-checks, so no second resolution can happen between the check and the socket. That closes
  DNS rebinding. Loopback, link-local, multicast and the unspecified address are refused regardless of
  configuration.

### Will an upgrade to 0.50.0 break a working install?

The chart defaults are `rdc.endpointAllowlist: []` and `rdc.allowAnyHost: false`, and the policy is built from
**your document's `servers[]` plus the operator allowlist**. So a vendor contacted at the host its own OpenAPI
document declares needs **no configuration**. An allowlist entry is needed only where the controller is
pointed at a host that does not appear in its document.

**Verify that on a staging install rather than taking it from this page** — it is the one claim here that
depends on your documents rather than on our tags.

**What a static check of your documents can and cannot settle.** One consumer checked theirs and reported the
bound rather than just the result, which is the right shape:

```
documents scanned                           : 13
documents WITHOUT a top-level servers[]     :  0
distinct hosts declared across all servers[]: 1
per-path / per-operation servers overrides  :  0
hosts anything actually reaches             : that same single host
```

Uniform, so on that fleet the policy built from `servers[]` admits 100% of traffic and the upgrade cannot
break the install on that axis. **But that verifies the input, not the mechanism**: it confirms the documents
are uniform and match the dialled host, and it does *not* confirm that the policy is assembled from
`servers[]` the way this page says. Only watching a real install admit a real request does that.

So: a document scan is a **necessary** condition and worth running first, because it is cheap and it can rule
the upgrade out. It is not **sufficient**, and a page like this one cannot make it so.

> **Answer "do I need an allowlist?" from `servers[]` in your document — not from the logs.** One consumer
> grepped the controller's logs for dialled hosts and got eight, including a Helm download and an apt
> repository. The controller dials none of them: they are strings **inside the CR body**, a 49 KB cloud-config
> that the controller logs. That method would have requested an allowlist covering hosts nothing ever
> contacts — worse than a refusal on upgrade, because it looks like it is working.

### The boundary of what 0.50.0 hardened

**It covers the rest-dynamic-controller's own request path.** Requests delegated through `*ApiRef` leave via
**snowplow** and are **not** covered; snowplow tracks the same channels as snowplow#1106 and snowplow#1112.

If your fleet has both kinds, a delegated kind's credential travels outside the path this release hardened.

**This is not hypothetical, and the shape is easy to miss because the delegated set is small.** One consumer
measured their own split:

```
definitions with any *ApiRef : 1 of 36
delegated actions            : 2 of 167   (update and delete, on their compute instance kind)
```

So 35 of 36 kinds and 165 of 167 actions are native and covered. They probed every path both halves call,
none redirects, and both reach the same host — no exposure today, and no protection either.

**Check which of your kinds use `*ApiRef`, not how many — and expect the answer to be your heaviest ones.**

That second clause is the part that matters, and it is structural rather than a fact about one fleet.
**`*ApiRef` is reached for precisely where a lifecycle is too complex for a single call**: multi-call
teardowns, ordered detach-then-delete, power sequencing. Those are disproportionately the **destructive verbs
on the expensive resources**. So a delegated set is not a random 3% of a fleet — it is biased toward exactly
the operations where a credential escaping would matter most.

The consumer above is the canonical shape: their compute kind has no single update endpoint at all, so power
state, elastic IP and security-group association, and subnet and volume attach/detach are a *sequence* — and
delete must detach data volumes first. That is why it delegates, and it is why the uncovered verb is the
destructive one.

A reader told "one kind out of 36" will reasonably downgrade this. A reader told "the uncovered path is
`delete` on your compute instances" will go and look.

---

## 3. What is NOT a barrier

Checked so that nobody re-checks them.

**The Kubernetes floor does not move.** `kubeVersion: ">=1.36.0-0"` is declared at **every** tag from 0.41.0
to 0.50.0 inclusive, and it is enforced at render time. If 0.41.0 installs on your clusters today, 0.50.0's
floor is not a new obstacle. (A CI trap worth knowing, unrelated to the pin: `helm/kind-action` defaults
*below* the floor while `kind`'s own CLI default is above it.)

**Credential-header redaction is far below this span.** Header-**name** redaction first ships in **0.26.1** —
`credentialHeaderRe` is absent at 0.26.0 and present at 0.26.1 and every tag since. The covered set at 0.41.0
is **byte-identical** to `main`'s and leads with `Authorization`:

```
Authorization, Proxy-Authorization, Cookie, Set-Cookie, X-Api-Key,
Api-Key, X-Auth-Token, X-Amz-Security-Token, Private-Token
```

So 0.41.0 is clear of the 0.25.x credential-leak window, and the covered set does not grow anywhere in
0.42.0–0.50.0. Below 0.26.1, `Authorization: Bearer <token>` reaches controller logs in cleartext whenever
verbose connector logging is on.

---

## 4. Operator actions the span requires

**Check for leftover `external-create-pending` markers.** The interrupted-create fix in this span fixes the
*cause* but does not clear what an earlier interrupted create left behind. A resource that failed that way
keeps `krateo.io/external-create-pending`, and in unstructured-runtime up to v1.6.1 that marker triggers a
separate wedge whose recovery `Update` conflicts with the status write its own `Observe` just performed —
measured at 51 consecutive failures and 0 recoveries over 20 minutes.

```sh
kubectl get <kind> -A -o json | jq -r '
  .items[] | select(.metadata.annotations["krateo.io/external-create-pending"])
  | "\(.metadata.namespace)/\(.metadata.name)"'
```

Anything listed needs **unstructured-runtime v1.7.1** to recover. The symptom otherwise surfaces two layers
away as an application error that never mentions annotations.

**An explicit `rdc.image` pin survives a provider upgrade, tag included.** If you pin it, the upgrade will not
move it — which is either what you want or a trap, depending on whether you remember.

---

## 5. One thing to know about failure messages, whatever you pin

From this span onward a failed vendor call carries the **vendor's own error body** into the resource's
`Synced` condition message, so the cause is visible without reading controller logs. A condition message is
readable by anyone with `get` on the resource — a wider audience than a Secret's RBAC.

Nothing we hold leaks there: credential headers are redacted by name and secretRef-resolved values by value.
But if **your vendor** echoes request headers or other sensitive context in its error bodies, that text
reaches the CR. Only the operator pointed at that vendor can measure this; we cannot test it for an arbitrary
API. Tracked as oasgen-provider#433.

---

## Method: how each row was dated

Stated because a version table is easy to produce and easy to produce wrongly.

- **First-carrying tag** comes from `git tag --contains <commit> | sort -V | head -1`, not from a changelog
  and not from a release note.
- **Path existence is checked separately from content**, with `git cat-file -e <tag>:<path>`. A `git show` of
  a path that exists at no tag emits nothing and `grep -c` returns `0` — and **a missing file is
  indistinguishable from an absent string.** A table built without this check reports a clean negative for a
  typo in a filename.
- **Content matching uses fixed strings** (`grep -F`), never a regex, wherever the target is a literal.
- **Every sweep carries a control string** that must be present at *every* tag, including the tags where the
  target is absent. In section 2 the control is `restclient.go` existing at all tags while the wiring count
  goes 0 → 1; in section 3 it is `func redact` returning 1 at every tag while `credentialHeaderRe` goes
  0 → 3 at 0.26.1. **A control that returns the same value as the measurement everywhere has shared the
  measurement's broken premise and corroborates nothing** — it converts "my number is unverified" into "my
  number is confirmed", which is worse than having no control.

### Scope, and what this page does not cover

- It covers **oasgen-provider and rest-dynamic-controller** at tag granularity. It does not cover the
  installer, core-provider, composition-dynamic-controller or snowplow, each of which has its own cadence.
- It is **artifact-level**, derived from tags and chart values. It is not a record of anything exercised on a
  running cluster: "published" and "exercised" are different claims, and only the first is made here.
- The security table lists changes whose *subject* is security. It is not an assertion that nothing else in
  the span has security consequences.
