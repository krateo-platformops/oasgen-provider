---
type: Runbook
title: oasgen-provider — testing
description: Running the two-module test suite the way CI does (build tags, -p 1, -race).
resource: github.com/krateo-platformops/oasgen-provider
tags: [kog, testing, ci]
timestamp: 2026-08-07T00:00:00Z
---

# Testing

This is a monorepo with two Go modules; run the suite per module
(`go/oasgen-provider/`, `go/rest-dynamic-controller/`). CI
(`.github/workflows/test.yaml`) runs, for each module:

```sh
go test -race -tags=unit,integration -p 1 -timeout 20m ./...
```

Two things there are load-bearing:

- **`-tags=unit,integration` is deliberate**: the cluster suites are only compiled under
  the `integration` tag, and dropping the tag silently *skips* those tests rather than
  failing.
- **`-p 1`** keeps packages that stand up clusters from racing each other.

**There is no envtest in this repository.** Five packages stand up a real `kind` cluster in
`TestMain`, through `sigs.k8s.io/e2e-framework/support/kind`, and nothing imports
`controller-runtime/pkg/envtest`:

```
go/oasgen-provider/internal/controllers/restdefinition   ~450s
go/oasgen-provider/internal/tools/kube                    ~77s
go/oasgen-provider/internal/tools/crd
go/oasgen-provider/internal/tools/deploy
go/rest-dynamic-controller/internal/controllers          ~532s
```

Each is `//go:build integration`, so `-tags=unit` runs every other test in those packages
with no cluster at all — which is how you iterate locally, since **only CI provisions
`kind`**; local cluster work uses a disposable GKE cluster or GCE instance. `TestMain` runs
once per `go test` invocation, so a per-test loop creates a cluster per iteration.

For a quick unit-only iteration inside a module:

```sh
go test -tags=unit -cover ./...
```

`go/oasgen-provider/scripts/test.sh` is the local coverage wrapper
(`go test ./... -coverprofile` + `go tool cover -func`); note it runs untagged, so it
exercises only the untagged subset.

## Testing the wiring, not just the halves

**A decision function and the values fed into it can each be correct, and individually
covered, while the connection between them is asserted nowhere.** That gap shipped a
release (issue 599): the restart gate's own truth table was covered over six input shapes
with both outcomes, the live ConfigMap read was covered on its own, and the gate never
fired once in 0.59.0 because `Deploy` handed it a flag that was always false.

Unit tests on each half cannot see this. The check that does is to **mutate the call
site**.

### The method

For each decision function, change the *call* rather than the body, and see whether any
test fails:

1. **Swap an argument for another already in scope of the same type.** This is the one
   ordinary coverage never catches, because it compiles silently. `resourceExistsForController(deployOk, deployReady)`
   takes two `bool`s; swapping them inverts the #122 rule and, before `observe_exists_call_site_test.go`,
   failed nothing in the package.
2. **Pass the wrong single variable** — the same shape without the swap.
3. **Force a constant** (`true`, `false`, `nil`). If this one survives too, the path is not
   reached at all and the problem is bigger than wiring.

A mutation that **survives** names a test you do not have. A mutation that fails to
**compile** is not evidence of anything — rewrite it so it compiles, or you have measured
your own typo. Both happened while this method was being applied; one mutation had to be
rewritten twice.

### The harness

```python
# 1. BASELINE FIRST. If the suite is already red, no mutation result means anything.
rc, out = run_tests()
assert rc == 0, "baseline red -- abort"

# 2. assert the anchor count before writing, so a missed replace cannot read as a pass
assert src.count(old) == expected_count

# 3. mutate, run, RESTORE -- in a try/finally, so an exception cannot leave the tree dirty
# 4. classify: a compile failure is NOT a kill
if "build failed" in out or "declared and not used" in out:
    verdict = "COMPILE-FAIL"      # invalid mutation, no information
elif rc == 0:
    verdict = "SURVIVED"          # a missing test
else:
    verdict = "killed"

# 5. re-run the baseline at the end and assert it is green again
```

### What it found here

| target | mutation | verdict |
|---|---|---|
| `restartNeeded`'s call site | rendered compared against rendered | SURVIVED → killed by `deploy_restart_wiring_test.go` |
| `restartNeeded`'s call site | `cmExisted` forced false | SURVIVED → killed |
| `resourceExistsForController`'s call sites | the two `bool`s swapped | SURVIVED → killed by `observe_exists_call_site_test.go` |
| `resourceExistsForController`'s call sites | existence taken from readiness | SURVIVED → killed |

### Two things that make the resulting tests worth having

**Drive the real entry point, and change the input the way production does.** The restart
test rewrites the ConfigMap *template file*, because Helm ranges `rdc.env` into the asset at
chart-render time — so the provider's input is a file on disk and a `helm upgrade` is a file
change. A test that poked the rendered object would have asserted something the system never
does.

**Assert the premise through the same helper the code under test uses.** Both call-site tests
check `deployment.LookupDeployment` reports exists-and-not-ready *before* calling `Observe`.
Without that, a fixture that drifts makes the test exercise a different branch and pass for
the wrong reason — which is what happened on the first two attempts, and the premise
assertion is what caught it rather than a wrong green.
