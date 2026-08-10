---
type: Runbook
title: oasgen-provider — release
description: How a release ships — one plain-semver tag drives the test gate, the multi-platform image, and both OCI charts.
resource: oci://ghcr.io/krateo-platformops/charts/oasgen-provider
tags: [kog, release, ci]
timestamp: 2026-08-10T00:00:00Z
---

# Release

One version line: pushing a plain-semver tag `X.Y.Z` (**no `v` prefix** — the workflows
trigger on `[0-9]+.[0-9]+.[0-9]+`) releases everything.

## What the tag runs

1. **`test.yaml`** (release gate, also run on PRs and pushes to main): the full suite for
   both Go modules — `go test -race -tags=unit,integration -p 1 …` per module. A tag
   cannot publish an image that fails its own tests.
2. **`release-tag.yaml`** → the shared `component-image-build` reusable builds **both**
   multi-platform images (linux/amd64 + linux/arm64), one per module under `go/`:
   - `ghcr.io/krateo-platformops/oasgen-provider:X.Y.Z`
   - `ghcr.io/krateo-platformops/rest-dynamic-controller:X.Y.Z`

   The matrix runs `fail-fast: false`, so one module's failure no longer cancels the
   other's build. Before pushing, the reusable asserts the tag is **contained in `main`**;
   a tag cut from an unmerged branch is refused rather than published under a version
   number implying it was reviewed.
3. **`release-oci.yaml`** (the canonical org-wide package workflow) discovers every
   first-class chart and pushes both to
   `oci://ghcr.io/krateo-platformops/charts/`:
   - `oasgen-provider` `X.Y.Z`
   - `oasgen-provider-crds` `X.Y.Z` (same tag — one repo tag publishes app + CRD charts
     at one version, so the installer pins both at one version)

   `CHART_VERSION` placeholders become the tag; `APP_VERSION` becomes the latest semver
   tag of the app repo (normally the same tag). `workflow_dispatch` can override either.

## The image-existence gate

`release-oci.yaml` will not publish a chart that references an image which does not exist.
Before packaging, the shared `chart-images` reusable resolves every image reference the
chart renders and checks each one against the registry; a miss fails the release.

This exists because a chart once shipped pinned to a never-published RDC tag, so every
install `ImagePullBackOff`'d ([#62](https://github.com/krateo-platformops/oasgen-provider/issues/62)).
The gate has since caught a real recurrence: on `0.21.0` the RDC image push was denied
(the GHCR package was still linked to the standalone repo), and the gate blocked the chart
rather than shipping it broken.

Because `rdc.image.tag` is empty and derives from the chart `appVersion`, the two
components must release **in lockstep at identical versions** — one tag builds both images
and both charts, so the derived tag always resolves. Nothing about the RDC version needs
bumping by hand before tagging.

## PR checks

`release-pullrequest.yaml`: the image build (push=false), the shared test suite, the
CRD-drift guard (`go generate ./apis/...` must leave `crds/` clean — regenerate and
commit when changing `apis/`), and the docs-standard lint (`lint-docs`).

## After the release

- Verify both charts on GHCR:
  `helm show chart oci://ghcr.io/krateo-platformops/charts/oasgen-provider --version X.Y.Z`.
- Bump the installer pins (`oasgen-provider` and `oasgen-provider-crd` entries in the
  installer's `component-pins.yaml`) to pick the release up in platform deploys.
- Release notes live in GitHub Releases; notable changes are curated into
  [log.md](./log.md).
