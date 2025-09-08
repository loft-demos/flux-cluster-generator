# flux-cluster-generator

The **flux-cluster-generator** is a lightweight Kubernetes controller that watches for Kubernetes `Secrets` containing a KubeConfig (for use as a Flux KubeConfig reference for Flux `HelmRelease` and/or `Kustomization` resources)  and that have a specific configurable label (defaults to `fluxcd.io/secret-type=cluster`). Based on that `Secret`, the controller generates a [`ResourceSetInputProvider`]([https://fluxcd.io](https://fluxcd.control-plane.io/operator/resourcesetinputprovider/)) (RSIP) resource that will include the `Secret` name, namespace and kubeconfig key as `defaultValues` and `.metadata.labels` of the RSIP and  optional additional `defaultValues` added from the `Secret` `labels` based on configuration documented below.

This enables GitOps-style generation of Kuberentes resources for multiple dynamic Kubernetes clusters using one [Flux `ResourceSet`](https://fluxcd.control-plane.io/operator/resourceset/) similar to the [Argo CD Cluster Generator for `ApplicationSets`](https://argo-cd.readthedocs.io/en/stable/operator-manual/applicationset/Generators-Cluster/). While the documentation for Flux `ResourceSets` does include a [_Multi-cluster example_](https://fluxcd.control-plane.io/operator/resourcesets/app-definition/#multi-cluster-example), it is very static and would require adding and removing cluster inputs in every `ResourceSet` you would like to use across multiple clusters. The **flux-cluster-generator** works more like the Argo CD Cluster Generator for `ApplicationSets` in that it will dynamically associate a new Kuberenetes cluster represented as a Kubernetes Flux KubeConfig reference `Secret` and allow the reuse of Flux `ResourceSets` across all of those clusters by generating a cluster specific `ResourceInputProvider` based on each of those `Secrets`. While originally designed for dynamic vCluster environments (and paired with the [**vcluster-platform-flux-secret-controller**](https://github.com/loft-demos/vcluster-platform-flux-secret-controller)), it can be used with any Kubernetes Flux KubeConfig reference `Secret`.

---

## Overview

- Watches `Secret` objects that match:
  - A configurable label selector (e.g. `fluxcd.io/secret-type=cluster`)
  - An optional namespace label selector
  - Or explicit namespaces in `--watch-namespaces`
- Creates/updates a `ResourceSetInputProvider` (RSIP) in a target namespace
- Ensures RSIPs are deleted when their source secret is removed or no longer matches
- Copies selected labels and prefixes from the source secret into the RSIP as labels and `defaultValues`

---

## Installation

`flux-cluster-generator` is packaged as a Helm chart and published as an OCI artifact.

Add the OCI Helm repository (Flux can read OCI directly):

```yaml
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: HelmRepository
metadata:
  name: flux-cluster-generator
  namespace: flux-cluster-generator
spec:
  interval: 12h
  type: oci
  url: oci://ghcr.io/loft-demos/charts
```

Deploy with a HelmRelease:

```yaml
apiVersion: helm.toolkit.fluxcd.io/v2beta2
kind: HelmRelease
metadata:
  name: flux-cluster-generator
  namespace: flux-cluster-generator
spec:
  releaseName: flux-cluster-generator
  targetNamespace: flux-cluster-generator
  chart:
    spec:
      chart: flux-cluster-generator
      version: "0.1.1" # change to the desired version
      sourceRef:
        kind: HelmRepository
        name: flux-cluster-generator
  interval: 30m
  install:
    createNamespace: true
```

## Flags & Configuration

The controller supports flags for tuning and customization (all flags are exposed as Helm values):

- `--rsip-namespace`: Target namespace for RSIPs
- `--label-selector`: Label selector for source secrets
- `--secret-key`: Key in secret data containing kubeconfig (default `value`)
- `--rsip-name-prefix`: Prefix for generated RSIP names (default input
- `--copy-label-keys`: Comma-separated label keys to copy into RSIP
- `--copy-label-prefixes`: Comma-separated label KEY PREFIXES to copy into RSIP (e.g. flux-app/)
- `--rsip-name-template`: Optional template for RSIP names (default falls back to prefix + project + cluster)
- `--namespace-label-selector`: Label selector for Namespaces to include (e.g. flux-cluster-generator-enabled=true)
- `--watch-namespaces`: Comma-separated namespaces to watch (empty = all)
