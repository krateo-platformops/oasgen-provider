---
type: ExampleIndex
title: oasgen-provider — examples
description: Index of the runnable examples under examples/ and where the deeper sample material lives.
resource: oci://ghcr.io/krateo-platformops/charts/oasgen-provider
tags: [kog, examples]
timestamp: 2026-08-10T00:00:00Z
---

# Examples

- [github-repo](../examples/github-repo/README.md) — a `RestDefinition` managing GitHub
  repositories from an in-repo OAS document (ConfigMap-served), plus the
  `RepoConfiguration` and a `Repo` CR. Preconditions: oasgen-provider installed, a GitHub
  token.
- [rdc-sample-resource](../examples/rdc-sample-resource/README.md) — the whole
  `RestDefinition` → RDC → CR chain against a **mock API you can run locally**, mirroring
  the rest-dynamic-controller integration testdata. Needs no external credentials, which
  makes it the fastest way to watch a generated controller converge end to end.

Deeper material:

- [REAL_EXAMPLES.md](./REAL_EXAMPLES.md) — real-world edge-case RestDefinitions
  (requestFieldMapping, dot-escaped `excludedSpecFields`, `identifiersMatchPolicy: AND`).
- [USAGE_GUIDE.md](./USAGE_GUIDE.md) — the end-to-end walkthroughs (simple GitHub Repo
  case; the TeamRepo case that needs a plugin web service).
- [`go/oasgen-provider/samples/`](../go/oasgen-provider/samples/) — sample
  RestDefinitions and OAS documents (GitHub, Azure DevOps, MLflow) used by the guides and
  tests.
