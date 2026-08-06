---
type: CRD Reference
title: OpenShellWorkspaceMember
description: Namespaced CRD that grants a workload ServiceAccount identity membership in an OpenShell workspace.
resource: gateway.ogo.aknochow.io/v1alpha1
tags: [crd, authorization, workspace]
---

# OpenShellWorkspaceMember

**API Group:** `gateway.ogo.aknochow.io`
**Version:** `v1alpha1`
**Scope:** Namespaced

Grants a Kubernetes ServiceAccount workspace membership on the OpenShell
gateway — the authorization layer that sits below authentication. A
ServiceAccount can hold a perfectly valid OIDC token (see
[Authentication](../concepts/authentication.md)) and still get
`PERMISSION_DENIED` on every sandbox operation if it isn't a member of the
workspace it's trying to use. `OpenShellWorkspaceMember` reconciles that
membership declaratively instead of requiring a manual grant.

## Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `workspace` | string | yes | The OpenShell workspace name to grant membership in |
| `serviceAccountRef.name` | string | yes | Name of the ServiceAccount, in the **same namespace** as this CR (no cross-namespace references — see [Security](#security)) |
| `role` | enum | | `user` (default) or `admin` |

## Status

| Field | Type | Description |
|-------|------|-------------|
| `phase` | enum | `Pending`, `Synced`, `Failed` |
| `observedGeneration` | int | Latest observed spec generation |
| `conditions` | []Condition | Sync status conditions (`Ready` type; `Reason` includes `Synced`, `GatewayNotFound`, `GatewayUnreachable`, `IdentityNotFound`) |
| `reconciledSubject` | string | The ServiceAccount UID last successfully granted membership |

## Recreation handling

`reconciledSubject` tracks the ServiceAccount's UID, not just its name. If
the referenced ServiceAccount is deleted and recreated (a new UID), the
controller detects the mismatch, removes the stale membership tied to the
old UID, and grants the new identity — a recreated ServiceAccount never
silently inherits a prior identity's access.

## Security

`serviceAccountRef` deliberately has no namespace field: it always resolves
within the CR's own namespace. Allowing a cross-namespace reference would
let anyone with create access to this resource in namespace A grant
workspace membership to an identity in namespace B, bypassing that
namespace's own RBAC boundary.

## Examples

See [config/samples/](https://github.com/aknochow/ogo/tree/main/config/samples)
for ready-to-use `OpenShellWorkspaceMember` CRs.
