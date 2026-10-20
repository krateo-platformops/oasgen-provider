---
type: Example
title: oasgen-provider — GitHub Repo example
description: A RestDefinition managing GitHub repositories from an in-repo OAS document, plus the generated Configuration and a Repo instance.
resource: restdefinitions.ogen.krateo.io
tags: [kog, example, github]
timestamp: 2026-08-07T00:00:00Z
---

# GitHub Repo example

Generates a `Repo` CRD + controller for GitHub repositories from the sample OAS document
shipped in this repo, then creates a repository declaratively.

> **The resource CRD's version is derived from the OAS document.** `info.version` is
> normalized into a legal CRD version name (lowercased, every run of non-alphanumerics
> becomes `-`, a leading digit gets a `v`), so GitHub's `1.1.4` becomes
> `v1-1-4` — *not* `v1alpha1`. Only the `*Configuration` CRD is always `v1alpha1`.
> `kubectl get restdefinition <name> -o wide` prints the one in force.

**Preconditions**

- oasgen-provider installed (stock Krateo installer deploy, or the direct
  `helm install` from [docs/usage.md](../../docs/usage.md)).
- A GitHub personal access token able to create/delete repos in your org.
- Network access from the cluster to `api.github.com`.

**Run** (from the repo root):

```sh
# 1. Namespace + the OAS document as a ConfigMap:
kubectl create namespace gh-system
kubectl create configmap repo \
  --from-file=go/oasgen-provider/samples/usage_guide/assets/repo.yaml -n gh-system

# 2. The RestDefinition (generates the Repo + RepoConfiguration CRDs and the controller):
kubectl apply -f examples/github-repo/restdefinition.yaml
kubectl wait restdefinition gh-repo --for condition=Ready=True -n gh-system --timeout=600s

# 3. Credentials + the Configuration CR:
kubectl create secret generic gh-token --from-literal=token=<your-token> -n gh-system
kubectl apply -f examples/github-repo/configuration.yaml

# 4. A repository (edit `org` in repo.yaml first):
kubectl apply -f examples/github-repo/repo.yaml
kubectl describe repo.github.ogen.krateo.io/gh-repo-1 -n gh-system
```

**Teardown**: `kubectl delete repo.github.ogen.krateo.io gh-repo-1 -n gh-system` deletes
the GitHub repository too (the CR is the source of truth); then delete the
RestDefinition and the namespace.

The full narrative version of this example (including verification steps and
troubleshooting) is the first walkthrough in
[docs/USAGE_GUIDE.md](../../docs/USAGE_GUIDE.md).
