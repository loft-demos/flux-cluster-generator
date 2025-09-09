# flux-cluster-generator

_An Argo CD type Cluster Generator (ApplicationSets) for Flux ResourceSets_

The **flux-cluster-generator** is a lightweight Kubernetes controller that watches for [Flux KubeConfig reference `Secrets`](https://fluxcd.io/flux/components/helm/helmreleases/#kubeconfig-reference). For every Flux KubeConfig reference `Secret` that meet specific, configurable criteria, it creates and manages [Flux `ResourceSetInputProviders`](https://fluxcd.control-plane.io/operator/resourcesetinputprovider/) derived from those `Secrets`. 

The controller watches for Kubernetes Secrets that:
- Contain a valid KubeConfig (for use as a Flux KubeConfig reference for HelmRelease and/or Kustomization resources).
- Match a configurable label selector (default: `fluxcd.io/secret-type=cluster`).
- Optionally watch namespaces that match a configurable label selector (e.g. `flux-cluster-generator-enabled=true`)

For each matching `Secret`, the controller creates a corresponding `ResourceSetInputProvider` (RSIP).
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

## Key Features

- Watches `Secret` objects that match:
  - A configurable label selector (e.g. `fluxcd.io/secret-type=cluster`)
  - An optional namespace label selector
  - Or explicit namespaces in `--watch-namespaces`
- Creates/updates a `ResourceSetInputProvider` (RSIP) per matching `Secret` in a target namespace
- Ensures RSIPs are deleted when their source `Secret` is removed or no longer matches
- Copies selected labels and prefixes from the source `Secret` into the RSIP as labels and `defaultValues`
  - The labels enable the triggering of one or more `ResourceSets` based on the `inputsFrom label selector.
  - The `Secret` based `defaultValues` provides `inputs` to be used in the `ResourceSet` templated resources.

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
  values:
    args:
      copyLabelKeys: "env,team,,app-subdomain"
      namespaceLabelSelector: "flux-cluster-generator-enabled=true"
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

## Example Config for the flux-cluster-generator controller and Matching Secret

Config:

```yaml
  containers:
    - name: flux-cluster-generator
      image: ghcr.io/loft-demos/flux-cluster-generator:0.1.1
      args:
        - --rsip-namespace=flux-apps
        - --label-selector=fluxcd.io/secret-type=cluster
        - --secret-key=value
        - --rsip-name-prefix=inputs-
        - --cluster-name-label-key=vci.flux.loft.sh/name
        - --project-label-key=vci.flux.loft.sh/project
        - --copy-label-keys=env,team,,app-subdomain
        - --copy-label-prefixes=flux-app/
        - --namespace-label-selector=flux-cluster-generator-enabled=true
        - --max-concurrent=2
        - --cache-sync-seconds=120
        - --zap-log-level=info
```

`Secret` (and `Namespace`) that matches the above config: 

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: flux-apps
  labels:
    flux-cluster-generator-enabled: 'true'
---
apiVersion: v1
kind: Secret
metadata:
  name: vci-vcluster-flux-demo-flux-cluster-generator-demo-kubeconfig
  namespace: flux-apps
  labels:
    app-subdomain: beta.us.demo.dev
    app.kubernetes.io/managed-by: vcluster-platform-flux-secret-controller
    env: dev
    flux-app/hello-world: 'false'
    flux-app/podinfo: 'true'
    fluxcd.io/kubeconfig: 'true'
    fluxcd.io/secret-type: cluster
    vci.flux.loft.sh/name: flux-cluster-generator-demo
    vci.flux.loft.sh/namespace: p-vcluster-flux-demo
    vci.flux.loft.sh/project: vcluster-flux-demo
    vcluster.com/import-fluxcd: 'true'
  annotations:
    vci.flux.loft.sh/kcfg-sha256: 60ca29a7e7ce91d0e6d899b296760c2e170d01589697f0cfcef49b8b9bcee48c
data:
  value: eyJ...xXf==
type: Opaque
```

Generated `ResourceSetInputProvider`:

```yaml
apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSetInputProvider
metadata:
  labels:
    app-subdomain: beta.acme.com
    env: dev
    flux-app/hello-world: 'false'
    flux-app/podinfo: 'true'
    mirror.fluxcd.io/clusterName: flux-cluster-generator-demo
    mirror.fluxcd.io/managed: 'true'
    mirror.fluxcd.io/project: vcluster-flux-demo
    mirror.fluxcd.io/secretKey: value
    mirror.fluxcd.io/secretNS: flux-apps
    mirror.fluxcd.io/secretName: vci-vcluster-flux-demo-flux-cluster-generator-demo-kubeconfig
  name: inputs-vcluster-flux-demo-flux-cluster-generator-demo
  namespace: flux-apps
spec:
  defaultValues:
    appSubdomain: beta.acme.com
    env: dev
    fluxAppHelloWorld: 'false'
    fluxAppPodinfo: 'true'
    kubeSecretKey: value
    kubeSecretNS: flux-apps
    kubeSecretName: vci-vcluster-flux-demo-flux-cluster-generator-demo-kubeconfig
    name: flux-cluster-generator-demo
    project: vcluster-flux-demo
  type: Static
```

Example `ResourceSet` trigged by the above RSIP:

```yaml
apiVersion: fluxcd.controlplane.io/v1
kind: ResourceSet
metadata:
  labels:
    env: dev
    flux-app/podinfo: 'true'
  name: podinfo
  namespace: flux-apps
spec:
  inputsFrom:
    - apiVersion: fluxcd.controlplane.io/v1
      kind: ResourceSetInputProvider
      selector:
        matchLabels:
          flux-app/podinfo: 'true'
  resources:
    - apiVersion: source.toolkit.fluxcd.io/v1beta2
      kind: HelmRepository
      metadata:
        name: podinfo
        namespace: flux-apps
      spec:
        interval: 12h
        type: oci
        url: oci://ghcr.io/stefanprodan/charts
    - apiVersion: helm.toolkit.fluxcd.io/v2
      kind: HelmRelease
      metadata:
        labels:
          env: dev
          flux-app/podinfo: 'true'
          mirror.fluxcd.io/clusterName: << inputs.name >>
        name: podinfo-<< inputs.project >>-<< inputs.name >>
        namespace: flux-apps
      spec:
        chart:
          spec:
            chart: podinfo
            sourceRef:
              kind: HelmRepository
              name: podinfo
              namespace: flux-apps
        install:
          createNamespace: true
          remediation:
            retries: 4
        interval: 1m
        kubeConfig:
          secretRef:
            key: << inputs.kubeSecretKey >>
            name: << inputs.kubeSecretName >>
        releaseName: podinfo
        storageNamespace: podinfo
        targetNamespace: podinfo
        values:
          ingress:
            enabled: true
            hosts:
              - host: podinfo-<< inputs.name >>-<< inputs.project >>-<< inputs.appSubdomain >>
                paths:
                  - path: /
                    pathType: ImplementationSpecific
          replicaCount: 2
          ui:
            color: '#3455ff'
```

