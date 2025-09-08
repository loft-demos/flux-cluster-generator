# flux-cluster-generator

A lightweight Kubernetes controller that watches for Kubernetes `Secrets` containing a KubeConfig (for use as a Flux KubeConfig reference for Flux `HelmRelease` and/or `Kustomization` resources)  that have a specific configurable label (defaults to `fluxcd.io/secret-type=cluster`). Based on that `Secret`, the controller generates a [`ResourceSetInputProvider`]([https://fluxcd.io](https://fluxcd.control-plane.io/operator/resourcesetinputprovider/)) (RSIP) resource that will include the `Secret` name, namespace and kubeconfig key as `defaultValues` and `.metadata.labels` of the RSIP and  optional additional `defaultValues` added from the `Secret` `labels` based on configuration documented below.

This enables GitOps-style generation of Kuberentes resources for multiple Kubernetes clusters using one [Flux `ResourceSet`](https://fluxcd.control-plane.io/operator/resourceset/) similar to the [Argo CD Cluster Generator for `ApplicationSets`](https://argo-cd.readthedocs.io/en/stable/operator-manual/applicationset/Generators-Cluster/).

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

The controller supports flags for tuning and customization:
	•	--rsip-namespace: Target namespace for RSIPs
	•	--label-selector: Label selector for source secrets
	•	--secret-key: Key in secret data containing kubeconfig (default config)
	•	--copy-label-keys: Comma-separated label keys to copy into RSIP
	•	--copy-label-prefixes: Copy all labels starting with given prefixes
	•	--rsip-name-template: Optional template for RSIP names (default falls back to prefix + project + cluster)

