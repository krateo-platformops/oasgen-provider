---
type: Component
title: oasgen-provider — index
description: The map of the oasgen-provider doc bundle — the Krateo Operator Generator that turns OAS 3.0/3.1 documents into CRDs and dynamic REST controllers via the RestDefinition CRD.
resource: oci://ghcr.io/krateo-platformops/charts/oasgen-provider
tags: [kog, restdefinition, openapi, generator]
timestamp: 2026-08-10T00:00:00Z
---

# oasgen-provider

oasgen-provider is the **Krateo Operator Generator (KOG)**: from an OpenAPI 3.0/3.1
document and a `RestDefinition` CR it generates a resource CRD (plus a `*Configuration`
CRD for auth/config) and deploys a dedicated **rest-dynamic-controller** (RDC) instance
that reconciles the external REST resource. This monorepo carries both Go modules
(`go/oasgen-provider/`, `go/rest-dynamic-controller/`) and both Helm charts
(`helm/oasgen-provider/`, `helm/oasgen-provider-crds/`) on a single plain-semver version
line.

## The bundle (start here)

- [overview](./overview.md) — what it does and how it works: the generate → deploy →
  reconcile pipeline, actions vs verbs, the plugin (wrapper web service) pattern.
- [usage](./usage.md) — install via the Krateo installer pin or direct
  `helm install oci://…`; requirements; the local-render recipe.
- [configuration](./configuration.md) — the whole config surface: both charts' values,
  provider env/flags, the RDC env contract.
- [api](./api.md) — the `RestDefinition` CRD contract: the five actions, the full field
  surface, immutability, generated resources, supported auth — plus the RDC runtime
  contract (status, conditions, events, self-provisioned RBAC, metrics).
- [examples](./examples.md) — the runnable examples under `examples/`.
- [release](./release.md) — how a release ships (one tag → both images + charts on GHCR).
- [log](./log.md) — curated history.
- [llms.txt](./llms.txt) — the version-pinned agent index of this bundle.

## Guides & references (docs/, authoritative)

- [USAGE_GUIDE](./USAGE_GUIDE.md) — the step-by-step KOG walkthrough: GitHub repos
  end-to-end, and the extended TeamRepo case that needs a plugin.
- [REAL_EXAMPLES](./REAL_EXAMPLES.md) — real-world `RestDefinition` manifests for edge
  cases (requestFieldMapping, dot-escaping, identifiersMatchPolicy AND).
- [restdefinition-crd-reference](./restdefinition-crd-reference.md) — the generated
  field-by-field CRD reference (crdoc output; regenerate after `make generate`).
- [development/workflow](./development/workflow.md) — the local kind loop, the in-repo
  chart and RDC templates, regenerating CRDs.
- [development/testing](./development/testing.md) — running the suite the way CI does.

## Code-adjacent

- [go/rest-dynamic-controller/README.md](../go/rest-dynamic-controller/README.md) — the
  RDC's own summary: how it reconciles, its env contract.
- [go/oasgen-provider/samples/](../go/oasgen-provider/samples/) — sample RestDefinitions
  and OAS documents (GitHub, Azure DevOps, MLflow) used by the guides.

## Archive (`tags: [archive]`)

- [USAGE_GUIDE_PRE_0.6.0](./USAGE_GUIDE_PRE_0.6.0.md) — the usage guide as it applied to
  oasgen-provider ≤ 0.6.0. Historical record: what was true for those versions, **not**
  current truth — the current guide above always wins.
