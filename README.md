# flux-cluster-generator

A lightweight Kubernetes controller that watches for **kubeconfig secrets** with the configured label (defaults to `fluxcd.io/secret-type=cluster`) and generates a  
[`ResourceSetInputProvider`](https://fluxcd.io) (RSIP) resource that will include the `Secret` name, namespace and kubeconfig key as `defaultValues` of the RSIP and optional additional values from `labels` based on the configuration documented below.

This enables GitOps-style generation of Kuberentes resources for multiple Kubernetes clusters using one `ResourceSet` similar to the [Argo CD Cluster Generator for `ApplicationSets`](https://argo-cd.readthedocs.io/en/stable/operator-manual/applicationset/Generators-Cluster/).

---

## Overview

- Watches `Secret` objects that match:
  - A configurable label selector (e.g. `fluxcd.io/secret-type=cluster`)
  - An optional namespace label selector
  - Or explicit namespaces in `--watch-namespaces`
- Creates/updates a `ResourceSetInputProvider` (RSIP) in a target namespace
- Ensures RSIPs are deleted when their source secret is removed or no longer matches
- Copies selected labels and prefixes from the source secret into the RSIP

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
