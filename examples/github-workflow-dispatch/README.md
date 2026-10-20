---
type: Example
title: oasgen-provider — GitHub workflow_dispatch example
description: A correlated dispatch verb — triggering a GitHub Actions workflow from a CR and finding that run again by a CR-supplied correlation token.
resource: restdefinitions.ogen.krateo.io
tags: [kog, example, github, dispatch]
timestamp: 2026-10-20T00:00:00Z
---

# GitHub `workflow_dispatch` example

The `dispatch` action (0.32.0) for an endpoint that fits none of the CRUD five: triggering
a GitHub Actions workflow. Two POSTs are two runs, so the call is not idempotent, and
GitHub answers `204` with an **empty body**, so nothing comes back to key on — which is
also why `async` cannot reach it (`operationRef` extracts a handle *from the trigger
response*, and there is no handle).

What makes it safe is a **CR-supplied correlation token that the API echoes back**:

| | |
|---|---|
| token found in the observe collection | the invocation happened ⇒ `Ready`, and nothing fires |
| token absent | the resource does not exist ⇒ the dispatch fires |

So the token **is** the resource's identity. Changing `inputs.correlationId` is a *new
invocation*; editing anything else is not drift at all (a dispatch is compared on its
token alone, which is why `compareScope` is refused on one).

## The part that is easy to get wrong

**GitHub does not echo `workflow_dispatch` inputs in the runs list.** What it does echo is
`run-name`, so the workflow has to put the token there itself — then it arrives as each
run's `display_title`, which is what `correlation.echoedAt` reads.
[`workflow-run-name.yaml`](./workflow-run-name.yaml) is that workflow; it must exist in
the target repository (as `.github/workflows/build.yml`) **before** the first dispatch.

Establishing that fact is the whole point of `echoedAt` being required: without the
`run-name`, the token never appears, and the controller reports
`DispatchedButNotObserved` after 5 checks instead of dispatching again.

The OAS document here is curated rather than GitHub's published one, for one reason:
GitHub types `inputs` as a free-form object, and a correlation token must resolve to a
field that **exists in the generated spec schema** — generation refuses a token the CR
cannot carry. So `inputs.correlationId` is declared by name.

> **The `WorkflowRun` CRD is `v1-0`, not `v1alpha1`.** A resource CRD's version is derived
> from the OAS document's `info.version` (`"1.0"` here), normalized into a legal CRD version
> name. Only `WorkflowRunConfiguration` is always `v1alpha1`.
> `kubectl get restdefinition gh-workflow-run -o wide -n gh-wf-system` prints the one in force.

**Preconditions**

- oasgen-provider ≥ 0.32.0 installed (the `dispatch` action does not exist before it).
- A GitHub token with `actions:write` on the target repository.
- The target repo contains the workflow from `workflow-run-name.yaml`.
- Network access from the cluster to `api.github.com`.

**Run** (from the repo root):

```sh
# 1. Namespace + the OAS document:
kubectl create namespace gh-wf-system
kubectl apply -f examples/github-workflow-dispatch/oas-configmap.yaml

# 2. The RestDefinition (generates the WorkflowRun + WorkflowRunConfiguration CRDs
#    and the controller):
kubectl apply -f examples/github-workflow-dispatch/restdefinition.yaml
kubectl wait restdefinition gh-workflow-run --for condition=Ready=True -n gh-wf-system --timeout=600s

# 3. Credentials + the Configuration CR:
kubectl create secret generic gh-token --from-literal=token=<your-token> -n gh-wf-system
kubectl apply -f examples/github-workflow-dispatch/configuration.yaml

# 4. One invocation (edit owner/repo/workflow_id first):
kubectl apply -f examples/github-workflow-dispatch/workflowrun.yaml
kubectl describe workflowrun.github.ogen.krateo.io/nightly-build-2026-10-20 -n gh-wf-system
```

**What to expect**

- A `Dispatch` event naming the method, path and token, then `Creating` while the run is
  not yet visible — eventual consistency is normal and tolerated.
- Once the run appears: `Available`, and status populated **from the matched run**
  (`id`, `display_title`, `status`, `conclusion`, `html_url`) — the item, not the
  collection.
- `krateo.io/dispatched-token` on the CR records the token that was successfully POSTed.
  It is written only **after** the POST succeeds, so a dispatch that never actually
  happened is still retried.
- Reconciling again does **nothing**. Deleting the CR also does nothing remotely: the run
  is immutable history, so `delete` is a no-op that releases the finalizer.

**Fire it again**

Edit `inputs.correlationId` to a new unique value. Reusing a previous token means "that
run already happened", so nothing fires.

```sh
kubectl patch workflowrun.github.ogen.krateo.io/nightly-build-2026-10-20 -n gh-wf-system \
  --type merge -p '{"spec":{"inputs":{"correlationId":"nightly-build-2026-10-21"}}}'
```

**Preview without a cluster**

This example renders clean (no errors, no warnings) through the local render service, which
runs the controller's own generation code:

```sh
go build -o /tmp/oasgen-render ./go/oasgen-provider/cmd/oasgen-render && /tmp/oasgen-render &
# POST {"restDefinitions":[<restdefinition.yaml as JSON>],"oas":{"<oasPath>":"<document>"}}
curl -s -X POST localhost:8081/render -H 'Content-Type: application/json' --data-binary @req.json
```

It is also the fastest way to see the three generation-time refusals this verb carries —
mistype `echoedAt`, point `observe.path` at a path the document does not declare, or name
a token root the spec has no field for, and each comes back as an error against the exact
field. See [docs/api.md](../../docs/api.md#dispatch-and-correlation) for the full list,
including the admission-time (CEL) rules.
