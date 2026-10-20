# oasgen-provider

The Krateo Operator Generator (KOG): a Kubernetes controller that turns an OpenAPI
Specification (OAS) document — 3.1, 3.0 or Swagger 2.0 — plus a `RestDefinition` CR into a generated CRD and a
dedicated controller that reconciles external REST resources — no custom operator code.

## What is this

A monorepo carrying both halves of KOG and their Helm charts, shipping on one version line:
`go/oasgen-provider/` (the generator: owns the `RestDefinition` CRD, generates the resource
and `*Configuration` CRDs from the OAS, deploys one controller per RestDefinition),
`go/rest-dynamic-controller/` (RDC: the generic controller those deployments run, driving
observe/create/update/delete against the external API), and `helm/` (the `oasgen-provider`
and `oasgen-provider-crds` charts, including the RDC templates the provider renders).
Full picture: [docs/index.md](docs/index.md).

## Install

Normally installed by the **Krateo installer** (`features.oasgenProvider: true`, on by
default), which pins both charts. Standalone:

```sh
# The RestDefinition CRD first, then the provider:
helm install oasgen-provider-crds oci://ghcr.io/krateo-platformops/charts/oasgen-provider-crds \
  --version 0.20.0
helm install oasgen-provider oci://ghcr.io/krateo-platformops/charts/oasgen-provider \
  --version 0.20.0 --namespace krateo-system --create-namespace
```

Details and the local-render recipe: [docs/usage.md](docs/usage.md).

## Configure

See [docs/configuration.md](docs/configuration.md). Most used:

| Setting | Default | Effect |
|---|---|---|
| `rdc.image.tag` | `0.19.0` | The rest-dynamic-controller image every generated controller runs — a hand-maintained joint-contract pin, not auto-tracked. |
| `env.OASGEN_PROVIDER_MAX_RECONCILE_RATE` | `1` | Concurrent RestDefinition reconciles (chart ConfigMap; binary default is 3). |
| `global.imageRegistry` | `""` | One registry override for both the provider and RDC images (mirror / air-gapped installs). |

## Examples

- [examples/github-repo](examples/github-repo) — a `RestDefinition` managing GitHub
  repositories from an in-repo OAS document, plus the Configuration and Repo CRs.

## Docs

- [docs/index.md](docs/index.md) — the map (bundle + guides + archives)
- [docs/overview.md](docs/overview.md) — what it does and how it works
- [docs/usage.md](docs/usage.md) — how to install / consume it
- [docs/configuration.md](docs/configuration.md) — the whole config surface
- [docs/api.md](docs/api.md) — the `RestDefinition` CRD contract
- [docs/examples.md](docs/examples.md) — examples index
- [docs/release.md](docs/release.md) — how a release ships
- [docs/log.md](docs/log.md) — curated history

Guides: [docs/USAGE_GUIDE.md](docs/USAGE_GUIDE.md) (step-by-step),
[docs/REAL_EXAMPLES.md](docs/REAL_EXAMPLES.md) (edge-case RestDefinitions),
[docs/restdefinition-crd-reference.md](docs/restdefinition-crd-reference.md)
(generated field reference).

## Develop & release

`cd go/oasgen-provider && go test -tags=unit,integration -p 1 ./...` (same for
`go/rest-dynamic-controller`); local kind loop via `go/oasgen-provider/scripts/` — see
[docs/development/workflow.md](docs/development/workflow.md). Tag `X.Y.Z` (no `v`) ships
the image + both charts — release runbook: [docs/release.md](docs/release.md).
