---
type: Runbook
title: oasgen-provider — release
description: How a release ships — one plain-semver tag drives the test gate, the multi-platform image, and both OCI charts — and why the CRD chart must never be renamed.
resource: oci://ghcr.io/krateo-platformops/charts/oasgen-provider
tags: [kog, release, ci]
timestamp: 2026-10-15T00:00:00Z
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

## Never rename or replace the `oasgen-provider-crds` release

`oasgen-provider-crds` owns `restdefinitions.ogen.krateo.io`. A `helm uninstall` of that release
takes the CRD with it, **and every RestDefinition on the cluster goes with the CRD** — each one
owning generated CRDs and live CRs. On the cluster this was observed from, that was 30
RestDefinitions.

That is not a hypothetical uninstall. It is precisely what a **chart rename** does: the installer
deletes the old release and installs the new one, and the delete is the destructive half. The
0.26.0 split of `oasgen-render` into `controller-render-service` was safe only because it moved a
Deployment, a Service and a Secret — all recreated harmlessly. A CRD is not.

### The remedy is not the one you would reach for first

`helm.sh/resource-policy: keep` on the CRD is the obvious answer and is **not** what Krateo uses.
`helm uninstall` reads that policy from the release's **stored manifest**, not from the live
object, so adding it to a live CRD out of band does nothing at all. It only works if it was in the
chart when the release was installed — which, for any already-deployed release, it was not. Adding
it now would protect future installs, not existing ones, and would make the CRD un-uninstallable
forever, leaking it on legitimate teardown.

**The remedy is `krateo.io/deletion-policy=orphan` on the Composition**, set *before* the old
release is removed. The controller then drops the finalizer without uninstalling the release at
all, and ownership of the CRD transfers to the new release via helm take-ownership. This was
observed working during the `alert-troubleshooter` → `alert-provider` rename: at one sample the old
composition was gone and all 25 live `Alert` CRs were still present, before the new release adopted
the CRD. Without the annotation they would have been destroyed in exactly that window.

That annotation lives in the installer, not here — which is why this is written down rather than
fixed. The published CRD carries no protective annotation (verified on 0.27.1), and neither does
`core-provider-crds` for `compositiondefinitions.core.krateo.io` / `kubernetestargets.core.krateo.io`,
so this is an org-wide shape rather than something oasgen got wrong.

If you are ever renaming, splitting or replacing `oasgen-provider-crds`: set the orphan annotation
on the Composition first, confirm it took, and only then let the old release go.
