---
type: Usage
title: oasgen-provider — usage
description: How to install oasgen-provider — the installer pin or direct OCI helm install — and how to consume it by authoring RestDefinitions.
resource: oci://ghcr.io/krateo-platformops/charts/oasgen-provider
tags: [kog, install, helm]
timestamp: 2026-08-10T00:00:00Z
---

# Usage

## Requirements

- A Kubernetes cluster.
- An OpenAPI Specification 3.0/3.1 document for each API you want to manage (see the
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
