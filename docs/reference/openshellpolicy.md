---
type: CRD Reference
title: OpenShellPolicy
description: Namespaced CRD that defines network, filesystem, and process policies for sandbox pods.
resource: gateway.ogo.aknochow.io/v1alpha1
tags: [crd, policy, security]
---

# OpenShellPolicy

**API Group:** `gateway.ogo.aknochow.io`
**Version:** `v1alpha1`
**Scope:** Namespaced (in the gateway namespace)

## Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `policyName` | string | yes | Name of the policy in the gateway |
| `filesystem` | FilesystemPolicy | | Filesystem access controls |
| `network` | map[string]NetworkPolicyRule | | Network access rules keyed by name |
| `process` | ProcessPolicy | | Process identity controls |

### FilesystemPolicy

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `includeWorkdir` | bool | `false` | Auto-include the working directory as read-write |
| `readOnly` | []string | | Read-only path allowlist |
| `readWrite` | []string | | Read-write path allowlist |

### NetworkPolicyRule

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Display name for this rule |
| `endpoints` | []NetworkEndpoint | Allowed network endpoints |
| `binaries` | []NetworkBinary | Binaries allowed to use these endpoints |

### NetworkEndpoint

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `host` | string | | Hostname or glob (e.g., `*.googleapis.com`) |
| `port` | int | | Destination port |
| `protocol` | enum | | `rest`, `websocket`, `graphql`, `sql`, or empty (L4) |
| `enforcement` | enum | `enforce` | `enforce` (block) or `audit` (log only) |
| `access` | enum | | `read-only`, `read-write`, `full` |

### NetworkBinary

| Field | Type | Description |
|-------|------|-------------|
| `path` | string | Absolute path to the allowed binary |

### ProcessPolicy

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `runAsUser` | string | `sandbox` | User name or UID |
| `runAsGroup` | string | `sandbox` | Group name or GID |

## Status

| Field | Type | Description |
|-------|------|-------------|
| `phase` | enum | `Pending`, `Synced`, `Failed` |
| `observedGeneration` | int | Latest observed spec generation |
| `conditions` | []Condition | Sync status conditions |

## Default policy

When no policy is configured, sandboxes get a restrictive default:

- **Filesystem:** workdir + system paths read-only, `/sandbox` + `/tmp` read-write
- **Network:** no external access (all egress blocked)
- **Process:** `sandbox:sandbox`

## Examples

See [config/samples/](https://github.com/aknochow/ogo/tree/main/config/samples)
for ready-to-use policy CRs.
