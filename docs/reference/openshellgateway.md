---
type: CRD Reference
title: OpenShellGateway
description: Cluster-scoped singleton CRD that defines an OpenShell gateway deployment.
resource: gateway.ogo.aknochow.io/v1alpha1
tags: [crd, gateway]
---

# OpenShellGateway

**API Group:** `gateway.ogo.aknochow.io`
**Version:** `v1alpha1`
**Scope:** Cluster (one per cluster)

## Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `namespace` | string | `ogo` | Namespace for gateway and managed resources |
| `image` | string | `ghcr.io/nvidia/openshell/gateway` | Gateway container image |
| `imageTag` | string | (latest) | Tag override for gateway and supervisor images |
| `supervisorImage` | string | `ghcr.io/nvidia/openshell/supervisor` | Supervisor image sideloaded into sandbox pods |
| `replicas` | int | `1` | Gateway pod replicas |
| `logLevel` | enum | `info` | Log level: `trace`, `debug`, `info`, `warn`, `error` |
| `resources` | ResourceRequirements | | Pod resource requests/limits |

### `spec.database`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `secretName` | string | yes | Secret containing PostgreSQL URI (key: `uri`) |

### `spec.sandbox`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `namespace` | string | (gateway ns) | Namespace for sandbox pods |
| `defaultImage` | string | `ghcr.io/.../base:latest` | Default sandbox container image |
| `imagePullPolicy` | enum | | `Always`, `IfNotPresent`, `Never` |
| `workspaceStorageSize` | string | `2Gi` | PVC size for sandbox workspace |
| `runtimeClassName` | string | | Optional RuntimeClass (kata, gvisor) |
| `appArmorProfile` | string | `Unconfined` | AppArmor profile for sandbox pods |

### `spec.tls`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Enable TLS on the gateway pod. Set to `false` when using Envoy Gateway. |
| `serverCertSecretName` | string | | Pre-existing TLS Secret reference |
| `certManager.enabled` | bool | `false` | Use cert-manager for server TLS |
| `certManager.issuerName` | string | `letsencrypt` | cert-manager issuer name |
| `certManager.issuerKind` | enum | `ClusterIssuer` | `ClusterIssuer` or `Issuer` |

### `spec.route`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | (auto) | Create an OpenShift Route. Auto-detected on OpenShift. |
| `hostname` | string | | Custom hostname for the Route |
| `gatewayAPI.enabled` | bool | (auto) | Create Gateway + GRPCRoute. Auto-detected when Gateway API CRDs are installed. |
| `gatewayAPI.gatewayClassName` | string | `eg` | GatewayClass name |

### `spec.auth`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `allowUnauthenticated` | bool | `false` | Allow unauthenticated access (dev only) |
| `openshift.enabled` | bool | (auto) | Enable auth-bridge for OpenShift SSO |
| `openshift.adminGroup` | string | | OpenShift group for admin role |
| `openshift.tokenTTL` | string | `8h` | OIDC token lifetime |

### `spec.networkPolicy`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Create NetworkPolicy restricting sandbox SSH to gateway pods |

## Status

| Field | Type | Description |
|-------|------|-------------|
| `phase` | enum | `Pending`, `Creating`, `Running`, `Failed` |
| `gatewayURL` | string | External URL for gateway access |
| `clientCertSecretName` | string | Secret with client mTLS cert (for CI) |
| `observedGeneration` | int | Latest observed spec generation |
| `conditions` | []Condition | `Available`, `Progressing`, `Degraded`, `TLSReady`, `DatabaseReady` |
