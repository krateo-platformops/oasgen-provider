---
type: Runbook
title: oasgen-provider — local development workflow
description: The kind dev loop, the in-repo chart and RDC templates (mount-and-render), and CRD regeneration.
resource: github.com/krateo-platformops/oasgen-provider
tags: [kog, development, kind]
timestamp: 2026-08-10T00:00:00Z
---

# Local development workflow

## The loop

From `go/oasgen-provider/`:

```bash
./scripts/kind-up.sh     # create a kind cluster if there isn't one
./scripts/reload.sh      # build into it + apply crds/ + helm upgrade --install
./scripts/kind-down.sh   # tear the cluster down
```

`reload.sh` builds the provider with `ko` into `kind.local`, applies this repo's
generated `crds/`, and installs the chart with the local image overridden:

```
--set image.repository=kind.local/oasgen-provider
--set image.tag=latest
--set image.pullPolicy=Never
```

Override `NAMESPACE`, `RELEASE` or `CHART` as environment variables if the defaults
(`demo-system`, `oasgen-provider`, the published OCI chart) don't suit.

## The provider is installed from the chart, and the chart lives in this repo

There is no `manifests/` directory. Everything the provider needs at runtime — its
Deployment, its RBAC, and the **RDC templates it renders per `RestDefinition`** — comes
from the in-repo chart at [`helm/oasgen-provider/`](../../helm/oasgen-provider/)
(published as `oci://ghcr.io/krateo-platformops/charts/oasgen-provider`).

This repo used to carry a second copy of the RDC templates under `manifests/rdc/` (and,
before the monorepo fold, the chart lived in a separate chart repo). That second copy
never shipped: the `Dockerfile` copies only `go.mod`, `go.sum`, `main.go`, `apis/` and
`internal/`. So it was a *reference* copy that drifted from the one that actually ships,
and three separate bugs came out of that divergence — each visible from only one of the
two copies. `core-provider` spawns its CDCs through the identical mount-and-render
mechanism and has never carried such a copy.

These files are not deployment manifests in the usual sense. The provider mounts them at
`/tmp/assets/rdc-deployment`, `/tmp/assets/rdc-configmap` and `/tmp/assets/rdc-rbac` and
renders them for every `RestDefinition`, so whichever copy is installed **is** the
controller's behaviour. A drifted copy does not produce a config mismatch; it produces a
different product.

## Changing the RDC templates

They live at [`helm/oasgen-provider/assets/rdc/`](../../helm/oasgen-provider/assets/rdc/).
To test a change against a locally built provider, point `CHART` at a rendered copy of
the in-repo chart.

One wrinkle: the chart working tree carries CI placeholders (`version: CHART_VERSION`,
`appVersion: APP_VERSION`) that the release workflow substitutes at tag time, and `helm`
refuses to install a chart whose version isn't valid semver. Substitute them into a
throwaway copy first:

```bash
cp -r helm/oasgen-provider /tmp/oasgen-chart
sed -i '' -e 's/CHART_VERSION/0.0.0-dev/' \
          -e 's/APP_VERSION/0.0.0-dev/' /tmp/oasgen-chart/Chart.yaml

cd go/oasgen-provider && CHART=/tmp/oasgen-chart ./scripts/reload.sh
```

(`sed -i ''` is the BSD/macOS form; on GNU sed use `sed -i`.)

## Running rest-dynamic-controller directly

The other module in this monorepo is normally deployed *by* the provider, one instance per
`RestDefinition`, and is never installed by hand. For debugging it also runs fine outside
the cluster against a kubeconfig. From `go/rest-dynamic-controller/`:

```bash
export REST_CONTROLLER_GROUP=sample.krateo.io \
       REST_CONTROLLER_VERSION=v1alpha1 \
       REST_CONTROLLER_RESOURCE=samples \
       REST_CONTROLLER_SERVICEACCOUNT_NAME=default \
       REST_CONTROLLER_SERVICEACCOUNT_NAMESPACE=default
go run . -kubeconfig "$HOME/.kube/config" -debug
```

The ServiceAccount name/namespace pair is **hard-required** — `main.go` exits without it,
because that identity is the RoleBinding subject for the `secretRef` RBAC the controller
provisions for itself.

A mock CRUD API implementing `testdata/restdefinitions/cm/oas.yaml` is bundled for this
loop, and is the same server the integration suite and
[`examples/rdc-sample-resource`](../../examples/rdc-sample-resource/README.md) use:

```bash
go run ./internal/controllers/mockserver   # listens on :30007
```

Per-CR HTTP debugging is enabled with the `krateo.io/connector-verbose: "true"` annotation
on a managed CR, independently of `-debug`.

## Regenerating CRDs

From `go/oasgen-provider/`:

```bash
make generate
```

`go mod tidy` followed by `go generate ./...`, which drives the build-tagged
`apis/generate.go` `controller-gen` directive and writes `./crds`. Commit the
regenerated CRDs alongside the API change — the PR CRD-drift guard fails otherwise, and
`reload.sh` applies them from this repo, so a stale `crds/` means you are testing against
the old schema. The CRD chart (`helm/oasgen-provider-crds/templates/`) carries the same
manifest and ships it at the release tag.

After an API change, also regenerate the field reference:

```bash
go run fybrik.io/crdoc@latest --resources crds/ --output ../../docs/restdefinition-crd-reference.md
# then restore the OKF frontmatter block at the top of the file
```

## Tests

See [testing.md](./testing.md).
