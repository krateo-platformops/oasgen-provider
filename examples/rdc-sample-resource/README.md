---
type: Example
title: sample-resource — the full RestDefinition → RDC → CR chain on a mock API
description: A ConfigMap-hosted OAS, the RestDefinition that spawns an RDC instance, and a bearer-authenticated Sample CR, mirroring the repo's integration testdata.
resource: restdefinitions.ogen.krateo.io
tags: [kog, example, sample, rdc]
timestamp: 2026-08-10T00:00:00Z
---

# sample-resource

The whole chain end-to-end: `oas-configmap.yaml` hosts the OpenAPI document of a small
CRUD API, `restdefinition.yaml` makes oasgen-provider generate the `Sample` /
`SampleConfiguration` CRDs and deploy a `rest-dynamic-controller` instance for
`samples.sample.krateo.io/v1alpha1`, and `resource.yaml` creates a bearer-authenticated
`Sample` whose lifecycle RDC drives against the API.

## Preconditions

- A stock Krateo installer deploy (oasgen-provider running; the `demo-system`
  namespace exists).
- An HTTP service implementing the bundled OAS, reachable from the cluster at
  `servers[0].url` in `oas-configmap.yaml` (default
  `http://sample-api.demo-system.svc.cluster.local:30007` — edit it to match your
  endpoint). The reference implementation is bundled in this repo and is what the
  integration suite runs, from `go/rest-dynamic-controller/`:
  `go run ./internal/controllers/mockserver` (listens on
  `:30007`; authenticates requests whose bearer token is `test` — exactly what
  `resource.yaml`'s Secret carries).

## Run

```sh
kubectl apply -f oas-configmap.yaml -f restdefinition.yaml
kubectl wait restdefinition/sample -n demo-system --for=condition=Ready --timeout=180s
kubectl apply -f resource.yaml
```

Watch it converge (the `krateo.io/connector-verbose` annotation on the CR dumps the
HTTP exchanges in the controller logs):

```sh
kubectl get sample sample-1 -n demo-system -o yaml   # status.id populated, Ready=True
kubectl logs -n demo-system deploy/sample-controller # the spawned RDC instance
```

Deleting `sample-1` deletes the external resource; deleting the `RestDefinition`
tears down the RDC instance and the generated CRDs.
