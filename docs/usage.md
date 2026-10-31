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
