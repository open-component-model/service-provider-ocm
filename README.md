# service-provider-ocm

An [openMCP](https://github.com/openmcp-project) Service Provider that installs and manages
[OCM K8s Toolkit](https://github.com/open-component-model/ocm-k8s-toolkit) on workload clusters
via Flux HelmReleases.

## How It Works

When an `OCM` resource is created on the onboarding cluster, the controller:

1. Replicates the configured image pull secret into the tenant namespace and wires it into the `OCIRepository`
2. Creates a Flux `OCIRepository` pointing at the specified Helm chart URL and version
3. Creates a Flux `HelmRelease` that deploys the chart onto the workload cluster via a kubeconfig reference

## API Reference

### OCM

The domain service API. Created on the onboarding cluster, one per tenant.

```yaml
apiVersion: ocm.services.openmcp.cloud/v1alpha1
kind: OCM
metadata:
  name: my-ocm
spec:
  url: ghcr.io/open-component-model/charts/ocm-k8s-toolkit
  version: v0.1.0
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `spec.url` | `string` | yes | OCI URL of the Helm chart (`oci://` prefix is added automatically if missing) |
| `spec.version` | `string` | yes | Chart version tag |

### ProviderConfig

Cluster-scoped operational configuration. Controls Helm behavior, reconciliation tuning,
and image pull secret replication.

```yaml
apiVersion: ocm.services.openmcp.cloud/v1alpha1
kind: ProviderConfig
metadata:
  name: ocm-provider-config
spec:
  pollInterval: 5m
  imagePullSecret:
    name: my-registry-secret
  helmConfig:
    targetNamespace: ocm-system
    interval: 2m
    values:
      manager:
        concurrency:
          resource: 10
    install:
      crds: CreateReplace
      retries: 5
      createNamespace: true
    upgrade:
      crds: CreateReplace
      retries: 3
      cleanupOnFail: true
      force: false
      remediationStrategy: rollback
```

#### `spec`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `pollInterval` | `duration` | `1m` | How often the controller polls for changes |
| `imagePullSecret` | `LocalObjectReference` | — | Secret to replicate from the controller's namespace into tenant namespaces and set as `secretRef` on the `OCIRepository` |
| `helmConfig` | `object` | — | Helm-related configuration (see below) |

#### `spec.helmConfig`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `targetNamespace` | `string` | `default` | Namespace for HelmRelease `targetNamespace` and `storageNamespace` |
| `interval` | `duration` | `1m` | Reconciliation interval for both `OCIRepository` and `HelmRelease` |
| `values` | `object` | — | Arbitrary Helm values passed directly to the HelmRelease |
| `install` | `object` | — | Helm install configuration |
| `upgrade` | `object` | — | Helm upgrade configuration |

#### `spec.helmConfig.install`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `crds` | `string` | `Create` | CRD install policy (`Skip`, `Create`, `CreateReplace`) |
| `retries` | `int` | `3` | Number of install retries |
| `createNamespace` | `bool` | `true` | Create target namespace if it doesn't exist |

#### `spec.helmConfig.upgrade`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `crds` | `string` | `CreateReplace` | CRD upgrade policy (`Skip`, `Create`, `CreateReplace`) |
| `retries` | `int` | `3` | Number of upgrade retries |
| `cleanupOnFail` | `bool` | `true` | Clean up on failed upgrade |
| `force` | `bool` | `false` | Force resource updates |
| `remediationStrategy` | `string` | `rollback` | Strategy on failure (`rollback`, `uninstall`) |

## Running E2E Tests

```shell
task test-e2e
```

## Licensing

Copyright 2025 SAP SE or an SAP affiliate company and service-provider-ocm contributors.
See [LICENSE](LICENSE) for details.
