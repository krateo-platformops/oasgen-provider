---
type: Usage
title: oasgen-provider — usage
description: How to install oasgen-provider — the installer pin or direct OCI helm install — and how to consume it by authoring RestDefinitions.
resource: oci://ghcr.io/krateo-platformops/charts/oasgen-provider
tags: [kog, install, helm]
timestamp: 2026-10-20T00:00:00Z
---

# Usage

## Requirements

- **A Kubernetes cluster at 1.36 or newer.** Both the `oasgen-provider` and
  `oasgen-provider-crds` charts declare `kubeVersion: ">=1.36.0-0"`, so this is enforced by Helm
  at **render** time — `helm install` and `helm template` both refuse on an older cluster, before
  anything is applied. The floor exists because the charts use `MutatingAdmissionPolicy`, which
  is GA at 1.36. (`controller-render-service` declares no bound, deliberately: it ships no CRDs
  and no controller.)

  > **If you install these charts from CI, pin the node image explicitly.** This is the one
  > place the requirement bites silently: `helm/kind-action` defaults to a node image **below**
  > the floor, while `kind`'s own CLI default is above it. So a job that passes on a developer's
  > machine fails in CI with
  > `chart requires kubeVersion: >=1.36.0-0 which is incompatible with Kubernetes v1.35.0`,
  > which reads like a chart defect rather than a runner default.
  >
  > ```yaml
  > - uses: helm/kind-action@<pinned-sha>
  >   with:
  >     node_image: kindest/node:v1.36.1   # pin >= 1.36; the action's own default is lower
  > ```
  >
  > Reported by a consumer whose every local run had passed (#330).
- An OpenAPI Specification document for each API you want to manage — 3.1, 3.0 or
  Swagger 2.0 (2.0 since 0.32.0, read by its own adapter: it is **not** converted to 3.x,
  because `status.oasHash` must keep hashing the document the vendor actually published)
  (see the
  [USAGE_GUIDE](./USAGE_GUIDE.md#what-to-do-when-the-openapi-specification-oas-is-missingincomplete-or-not-at-version-30)
  when the OAS is missing or incomplete).
- Network access from inside the cluster to the API endpoints being managed.

## Via the Krateo installer (the normal path)

The installer deploys and pins both charts under the `oasgenProvider` feature flag,
**enabled by default**:

```yaml
# Installer CR (spec excerpt)
features:
  oasgenProvider: true
```

The pins live in the installer's `component-pins.yaml` (`oasgen-provider` +
`oasgen-provider-crds`, with the CRD chart as a dependency of the provider).

## Direct install (standalone)

Both charts are published to GHCR as OCI artifacts on every release tag:

```sh
# 1. The RestDefinition CRD:
helm install oasgen-provider-crds \
  oci://ghcr.io/krateo-platformops/charts/oasgen-provider-crds --version 0.21.0

# 2. The provider:
helm install oasgen-provider \
  oci://ghcr.io/krateo-platformops/charts/oasgen-provider \
  --version 0.21.0 --namespace krateo-system --create-namespace
```

For mirrored / air-gapped registries set `global.imageRegistry` (rewrites both the
provider and RDC image registries — see [configuration.md](./configuration.md)).

Installing the provider is the *only* install step. **rest-dynamic-controller has no chart
of its own and is never installed by hand**: for each `RestDefinition` the provider renders
a Deployment + per-instance ConfigMap + RBAC from `helm/oasgen-provider/assets/rdc/` and
starts one RDC instance with `-group`/`-version`/`-resource` pinned to the generated GVR.
Extra environment for every spawned instance goes through the chart's `rdc.env` map.

### Two things about `rdc.*` that have each cost a session an investigation

**`rdc.env` values must be set as strings.** The per-instance ConfigMap schema types them as
strings, and Helm's `--set` infers `true` as a *boolean*, so the obvious spelling is rejected:

```bash
# rejected: "got boolean, want string"
helm upgrade ... --set rdc.env.REST_CONTROLLER_DEBUG=true

# correct
helm upgrade ... --set-string rdc.env.REST_CONTROLLER_DEBUG=true
```

The error names the type rather than the fix, so it reads like the field does not work. It does;
this is the only spelling of it. (`REST_CONTROLLER_DEBUG` is also the supported way to turn on the
generated controllers' Debug logging — the `-debug` flag is the same switch but lives on the
Deployment, which the provider re-renders on every reconcile, so a `kubectl set env` is reverted
while a chart value is not. Note that changing `rdc.env` changes the ConfigMap hash, which
**rolls every generated controller** — intended, but wait for the rollout before judging behaviour.)

**An explicit `rdc.image` pin survives a provider upgrade, including the tag.** This is correct
Helm behaviour and it surprises people, because upgrading the provider then does *not* move the
controllers:

```yaml
# USER-SUPPLIED VALUES, set once for a local build
rdc:
  image:
    registry: ""                  # empty -> the local image, not ghcr.io
    repository: rest-dynamic-controller
    tag: 0.41.0-p3b
```

With `--reuse-values`, that pin is preserved across `helm upgrade`, so a provider moved from
0.42.0 to 0.44.0 kept running **RDC 0.41.0-p3b** on every generated controller. The provider and
the controllers are released in lockstep at identical versions, so a pin left behind means the
two halves are no longer the pair that was tested together.

To return to the released image, set both fields — clearing the tag alone leaves the empty
registry pointing at a local image that may not exist:

```bash
helm upgrade ... --reuse-values \
  --set rdc.image.registry=ghcr.io/krateo-platformops \
  --set rdc.image.tag=<version>
```

It propagates within one reconcile; there is no need to touch the generated Deployments, and
patching them by hand would be reverted anyway.

### Upgrading from a pre-split install: annotate the CRD FIRST

**If you are upgrading from a `charts.krateo.io`-era install (chart 0.11.x / appVersion 0.10.0, where the
CRD was bundled in the provider chart), do this before anything else:**

```bash
kubectl annotate crd restdefinitions.ogen.krateo.io helm.sh/resource-policy=keep
```

**Without it the upgrade deletes the CRD**, and that deletion cascades: every `RestDefinition` goes, every
CRD they generated is reaped, and those CRs' finalizers fire — **deleting real resources at the vendor.**
Measured on a live cluster: the `oasgen-provider-crds` chart created the CRD at `22:21:23` and the provider
chart's upgrade removed it at `22:21:24`, one second apart, taking four RestDefinitions and four generated
CRDs with it. Only the `*Configuration` siblings survived, because they are not reaped with their parent.

Helm removes resources that are in the **old** release's manifest and absent from the **new** one. The CRD
moved to its own chart, so it is exactly that — and **adopting it with an ownership annotation patch does
not help**: that fixes the import check, and the removal is a separate decision Helm makes without
consulting ownership.

**The provider's own guard cannot save you here.** It refuses to drop a generated CRD that still has
instances, precisely to avoid this cascade — but Helm deletes the *parent* CRD directly and asks the
controller nothing, so there is no path from the delete to the check.

Two details that cost people time:

- **The value is case-sensitive.** `keep` works; `KEEP` does nothing. Helm compares with `==`, not a
  case-insensitive match.
- **Current charts carry this annotation already**, so a fresh install is protected and only the
  pre-split upgrade path needs the manual step. The trade is that `helm uninstall` now leaves the CRD
  behind — removing it is a deliberate `kubectl delete crd restdefinitions.ogen.krateo.io`. For a CRD
  whose removal fires finalizers against live infrastructure, naming the thing you are deleting is the
  intended cost.

## Consuming it: author a RestDefinition

Once the provider runs, usage is entirely declarative:

1. Put the OAS document where the provider can read it — a ConfigMap
   (`configmap://<namespace>/<name>/<key>`) or an `http(s)://` URL.
2. Apply a `RestDefinition` mapping actions to endpoints
   ([api.md](./api.md) is the contract; the
   [USAGE_GUIDE](./USAGE_GUIDE.md) is the walkthrough):

   ```sh
   kubectl apply -f examples/github-repo/restdefinition.yaml
   kubectl wait restdefinition gh-repo --for condition=Ready=True -n gh-system --timeout=600s
   ```

3. Create a `*Configuration` CR referencing your credentials Secret, then instances of
   the generated Kind. See [examples/github-repo](../examples/github-repo/README.md).

Changing `oasPath` over time is possible, but **avoid changing the `create` request body
or its parameters**: the generated CRD schema derives from them, and drifting them
usually means deleting and recreating the RestDefinition.

### A RestDefinition models a reconciled resource, not a one-shot action

**This is the most expensive mistake an author can make here.** A physical server was re-imaged
**four times in thirty minutes** because a destructive one-shot was modelled as a reconciled
resource. Read this before declaring anything whose verb *does* something rather than *describes*
something — a wipe, a reboot, a rebuild, a drain, a rotate.

`Observe` must answer two questions: **does it exist**, and **is it up to date**. A fire-once action
has no external object whose state answers either. So modelling one forces a shape that cannot work:

- the `findby` has to be written so that it **never matches** — that is what makes every new CR
  "not found", and therefore fire;
- the trigger response sets `Pending`;
- `Observe` then returns exists + up-to-date from the `Pending` guard **without clearing it**.

The consequence is structural, not a bug to be fixed: **such a CR can never report `Ready`.**
Tolerable while it is transient, fatal when it is long-lived — Krateo's composition controller
aggregates child readiness, so one such CR held a fully provisioned composition at `Ready=False`
through 51 reconciles with zero transitions.

**`RESTAction` is not the alternative**, which is worth stating because it is the first thing authors
reach for. It is not fire-once — it runs on every call; it persists no attempted-or-completed record;
it answers `200` with failed stages named only in a response header; and rendering one from a chart
does not execute it. There is no primitive with completion semantics today, so an author looking for
one finds nothing and reaches for a resource. That is the trap.

#### The rule

> **A destructive one-shot must not be driven by a signal the action itself cannot change.**

The measured case: *"wipe this node"* was inferred from `provision_state == active AND spec.extra
empty` — which is also **the steady state of a node whose cloud-init failed**. The wipe never cleared
its own trigger, so it re-fired on every reconcile. This is the same defect as a drain wedge and as
RDC's `Pending` ambiguity: **a signal that cannot distinguish "needs doing" from "was done and did
not take".**

#### What to do instead: make the action declarative

Express the *target state*, not the transition, and let drift detection do the work:

```yaml
# Not "reboot this node" -- "this node's power state should be off".
verbsDescription:
  - action: findby                 # lift the OBSERVED state onto the key the spec uses
    method: GET
    path: /v1/nodes/{node_id}
  - action: update                 # the verb that performs the transition
    method: PUT
    path: /v1/nodes/{node_id}/states/power
```

```yaml
# and keep it quiet mid-flight: prefer the target the API is already moving toward
responseTransform:
  power_state: 'target_power_state // power_state'
```

The drift comparison now answers *"is it in the target state?"* rather than *"has this fired?"*. That
gives continuous enforcement and self-healing, and — the part that matters — **the trigger becomes a
signal the action can change.** It also reports `Ready` honestly, because there is now an external
state that confirms it.

If an action genuinely has no observable result — nothing the API will tell you afterwards that
differs from before — then it cannot be modelled here, and declaring it anyway produces the loop
above. Say so in your blueprint rather than writing a `findby` that never matches.

### An enveloped API: declare the unwrapped body and wrap on the wire

Some API families wrap every body in both directions — Zendesk sends and returns
`{"ticket": {…}}`, `{"group": {…}}`, and so on for every resource. **Declaring that envelope in the
OpenAPI document looks like it works and silently does the wrong thing.**

The body's field list comes from the **top-level properties only**:

```go
// clienttools.go, RequestedBody
for sch := schema.Properties.First(); sch != nil; sch = sch.Next() {
    bodyParams.Add(sch.Key())
}
```

So an envelope-declaring document yields a body of exactly one field, `{"ticket"}`, and everything
under `spec.ticket` is passed through **unfiltered**. The visible result is a working create. The
invisible one is that write-only fields travel too — a Zendesk `comment`, for instance, is re-posted
on **every drift-triggered PATCH**, adding a comment to the ticket each time. Nothing errors.

**Declare the unwrapped body and move the envelope into the transforms:**

```yaml
# on create and update
requestTransform:  { inline: '{ticket: .}' }
# on create, get and update
responseTransform: { inline: '.ticket' }
# none on delete -- there is no body in either direction
```

This is the intended mechanism rather than a workaround, and the code says so — `builder.go` on
`RequestTransform`:

> this verb's whole-document jq program for the outgoing body […] applied by
> `fieldmapping.ApplyRequestTransform`, **after the body is assembled** and immediately before the call.

So the field filtering still happens on the real fields and only the finished body is wrapped. The
`github-provider-kog` blueprint is reported to use this same shape for `PullRequest` and `GitRef`; that
is a separate repository and not verified here.

#### The cost, stated plainly

> the assets describe the CR shape, not the wire shape

That inverts the premise the provider is built on. An OpenAPI document is supposed to describe the
**vendor's API**, so one edited to remove envelopes is no longer a vendor artifact you can refresh from
upstream — the envelope knowledge now lives in a hand-maintained asset that a human has to keep in step
with the vendor. For an API family where *every* resource is enveloped, that cost is paid per resource.

Keep the envelope in the `requestTransform`/`responseTransform` pair rather than anywhere else: those
two lines are the entire diff from the vendor's own shape, so when the upstream document is refreshed,
re-applying them is a mechanical step rather than an audit.

## Local render (no cluster)

The chart working tree carries CI placeholders (`version: CHART_VERSION`,
`appVersion: APP_VERSION`) that the release workflow substitutes at tag time; `helm`
refuses non-semver versions, so substitute into a throwaway copy first:

```sh
cp -r helm/oasgen-provider /tmp/oasgen-chart
sed -i '' -e 's/CHART_VERSION/0.0.0-dev/' -e 's/APP_VERSION/0.0.0-dev/' /tmp/oasgen-chart/Chart.yaml
helm template test /tmp/oasgen-chart
```

(`sed -i ''` is the BSD/macOS form; on GNU sed use `sed -i`.)

Note the RDC image tag derives from `appVersion`, so the substitution above also renders
`rest-dynamic-controller:0.0.0-dev` — fine for inspecting the manifests, but if you
actually install this throwaway copy, set `rdc.image.tag` to a published version or the
generated controllers will `ImagePullBackOff`.

## Local development loop (kind)

`go/oasgen-provider/scripts/` carries the kind-up / reload / kind-down loop — build the
provider with `ko` into the kind cluster and install it via the in-repo chart. See
[development/workflow.md](./development/workflow.md).
