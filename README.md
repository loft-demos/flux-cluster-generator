# flux-cluster-generator

The **flux-cluster-generator** is a lightweight Kubernetes controller that watches for [Flux KubeConfig reference `Secrets`](https://fluxcd.io/flux/components/helm/helmreleases/#kubeconfig-reference) and, creates and manages [Flux `ResourceSetInputProviders`](https://fluxcd.control-plane.io/operator/resourcesetinputprovider/) based on those `Secrets`. 

The controller watches for Kubernetes Secrets that:
- Contain a valid KubeConfig (for use as a Flux KubeConfig reference for HelmRelease and/or Kustomization resources).
- Match a configurable label selector (default: `fluxcd.io/secret-type=cluster`).
- Optionally watch namespaces that match a configurable label selector (e.g. `flux-cluster-generator-enabled=true`)

For each matching Secret, the controller creates a corresponding ResourceSetInputProvider (RSIP).
- The generated RSIP includes the Secret’s name, namespace, and KubeConfig key as defaultValues and as `.metadata.labels`.
- Optional extra defaultValues can also be derived from the Secret’s labels, based on configuration (see below).

## Why use this?

Flux supports [`ResourceSets` for multi-cluster GitOps workflows](https://fluxcd.control-plane.io/operator/resourcesets/app-definition/#multi-cluster-example). However, the official examples are static—you must manually add or remove cluster inputs in each ResourceSet definition whenever clusters are added or removed.

The **flux-cluster-generator** brings the same dynamic behavior that [Argo CD’s Cluster Generator for ApplicationSets](https://argo-cd.readthedocs.io/en/stable/operator-manual/applicationset/Generators-Cluster/) provides:
- When a new Kubernetes cluster is represented as a Flux KubeConfig reference `Secret`, the controller automatically generates an RSIP.
- `ResourceSets` can then be reused across all clusters, without requiring per-cluster manual edits.
- When a cluster is removed (and its Secret deleted), the corresponding RSIP is also cleaned up.

This enables dynamic, GitOps-driven multi-cluster resource management.

While originally designed for dynamic vCluster environments (and paired with the [**vcluster-platform-flux-secret-controller**](https://github.com/loft-demos/vcluster-platform-flux-secret-controller)), it can be used with any Kubernetes Flux KubeConfig reference `Secret`.

### Use cases

- **Dynamic vCluster environments**

  Originally built for vCluster Platform `VirtualClusterInstances` where it is common to have ephemeral or short-lived vCluster instances appear and disappear frequently. And when paired with [**vcluster-platform-flux-secret-controller**](https://github.com/loft-demos/vcluster-platform-flux-secret-controller), the Flux KubeConfig reference `Secret` will be created when a `VirtualClusterInstance` is created with a label selector.

- **Any Flux-managed clusters**

  Works with any Kubernetes Secret that Flux recognizes as a KubeConfig reference, making it useful outside of vCluster as well.

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
