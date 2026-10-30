---
type: Runbook
title: Migrating an install from before 0.20.0
description: Three hazards a pre-monorepo install meets on the way to a current release, in the order they bite.
tags: [kog, upgrade]
timestamp: 2026-10-30T00:40:00Z
---

# Migrating an install from before 0.20.0

An install made before **0.20.0** — the first release of the monorepo layout — meets three distinct
hazards on the way to a current release. They are unrelated to each other and all three were found in a
single migration attempt, so anyone taking this path will meet them together.

Read all three before starting. The first can destroy data.

## 1. The CRD split can delete every RestDefinition

**Destructive. Do this first or not at all.**

A pre-split chart bundled `restdefinitions.ogen.krateo.io`; the current layout ships it in a separate
`oasgen-provider-crds` chart. Installing the new CRDs chart while upgrading the provider produces:

```
Error: Unable to continue with install: CustomResourceDefinition
"restdefinitions.ogen.krateo.io" exists and cannot be imported into the current release:
annotation validation error: key "meta.helm.sh/release-name" must equal
"krateo-oasgen-provider-crds": current value is "krateo-oasgen-provider"
```

Adopting the CRD with an annotation patch fixes that error and **does not fix the problem**. Helm removes
resources present in the old release manifest and absent from the new one, and does not consult the
ownership annotation when doing so. Measured, one second apart:

```
22:21:23  krateo-oasgen-provider-crds   install   -> creates the CRD
22:21:24  krateo-oasgen-provider        upgrade   -> DELETES it
```

The blast radius is not the CRD. Deleting it cascades: **every RestDefinition goes, and every generated
CRD is reaped with them** — so every CR of every generated kind is deleted, and their finalizers fire.
Where those CRs manage external infrastructure, a routine `helm upgrade` becomes a teardown.

**This bypasses the live-instance guard.** `deploy.Undeploy` refuses to drop a generated CRD that still
has instances, precisely to prevent that cascade — but it only runs on the provider's own teardown path.
Helm deleting the parent CRD never reaches it.

**Procedure:** upgrade the provider release **first**, then install the CRDs chart. By the time the CRDs
release owns the CRD, the provider's manifest no longer lists it, so there is nothing for Helm to remove.

Before starting, confirm what you would lose:

```sh
kubectl get restdefinitions -A
# and for each generated kind it owns:
kubectl get <kind>.<group> -A
```

Tracked as oasgen#297.

## 2. A required path parameter may become a required spec field

Not destructive; it rejects CRs that used to be accepted.

Releases before the path-item parameter fix read **only** operation-level `parameters`. Current releases
also read **path-item** level parameters, which is correct per the OpenAPI specification — one real
document declares them on 273 of its 274 paths, and they were invisible before.

A parameter that becomes visible and is marked `required: true` becomes a **required spec field**. So a
document like

```yaml
/v1/nodes/{uuid}:
  parameters:
    - name: uuid
      in: path
      required: true
```

now produces `spec.required = [..., "uuid"]`, and a CR without `spec.uuid` is rejected with
`spec.uuid: Required value`.

**This is not a regression in the required-field logic**, which is unchanged. The parameter was previously
never seen — so it was neither required nor usable.

**It is still wrong when a `requestFieldMapping` redirects the parameter**, because the author is
supplying the value from a different field and the path parameter's own name is then required and never
read:

```yaml
requestFieldMapping:
  - inCustomResource: spec.name
    inPath: uuid              # uuid is still demanded of the author
```

Tracked as oasgen#299. **Do not work around it by inventing values the API assigns** — a value your chart
derives is a value your chart then owns forever.

## 3. The generated API version is derived differently

Not destructive; it changes the served version, which changes every CR's `apiVersion`.

Older releases produced `v1alpha1` regardless of the document. Current releases derive the version from
the document's `info.version`, so `info.version: 1.0.0` produces **`v1-0-0`**.

Consequences:

- every CR's `apiVersion` changes, so manifests pinned to `v1alpha1` stop matching
- the generated controller's watch selector is exact equality on `krateo.io/oas-version`, so instances
  labelled for the old version are not delivered to the controller watching the new one

The multi-version coexistence machinery handles the transition when it is allowed to run. It cannot help
if the CRD is deleted and recreated underneath it, which is hazard 1 — another reason to resolve that
first.

## Checklist

```
[ ] inventory  kubectl get restdefinitions -A, and every generated kind's CRs
[ ] confirm every CR's finalizers are exactly ["composition.krateo.io/finalizer"]
      kubectl get <kind> <name> -n <ns> -o jsonpath='{.metadata.finalizers}'
      measured: a sole finalizer failed 0 of 40 trials; an EXTRA finalizer failed 40 of 40
[ ] upgrade the PROVIDER release first
[ ] then install oasgen-provider-crds
[ ] re-check the inventory: the RestDefinition count must be unchanged
[ ] check for newly-required spec fields on each generated CRD before re-applying CRs
[ ] check the served version: a changed info.version changes every apiVersion
```

The inventory re-check after the upgrade is the step that catches hazard 1 having happened, and it is
cheap. Do it even when the upgrade reports success — Helm reported success on the run that deleted
everything.
