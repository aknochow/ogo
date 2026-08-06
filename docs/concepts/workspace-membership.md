---
type: Concept
title: Workspace Membership
description: Workspace membership is the authorization layer that decides which authenticated identities can use a given OpenShell workspace.
tags: [core, security, authorization]
---

# Workspace Membership

Authentication proves *who* is calling the gateway (see
[Authentication](authentication.md)). Workspace membership decides *what
that identity is allowed to do* once authenticated — a separate
authorization layer keyed on the caller's JWT `sub` claim. A ServiceAccount
or user can have a perfectly valid token and still get `PERMISSION_DENIED`
on every sandbox operation if it isn't a member of the workspace it's
targeting.

The `OpenShellWorkspaceMember` CRD reconciles this membership declaratively,
so headless workload identities (CI pipelines, controllers, other
operators) don't need a manual, one-off grant to use the gateway.

## How it works

1. Admin creates an `OpenShellWorkspaceMember` CR referencing a
   ServiceAccount in the same namespace, a target workspace, and a role
2. The operator resolves the ServiceAccount's real UID and grants
   membership on the gateway using that UID as the principal subject
3. When that ServiceAccount later exchanges its Kubernetes token for an
   OpenShell JWT (see [Authentication](authentication.md)), the JWT's
   `sub` claim matches the granted membership and gateway calls succeed
4. If the ServiceAccount is deleted and recreated, its UID changes — the
   controller notices, retracts the stale membership, and grants the new
   identity, so access is never silently inherited across a recreation

## Roles

| Role | Grants |
|------|--------|
| `user` (default) | Create, list, and manage sandboxes in the workspace |
| `admin` | `user` permissions plus workspace-level administration |

## Why this needed its own CRD

The real gateway has no concept of "workspace membership for a Kubernetes
identity" out of the box — only a gRPC API to add/remove named principals
per workspace. Before this CRD existed, granting a headless identity access
meant a hand-run `grpcurl` call with a manually-minted admin token: it
worked, but it was a one-off that the system would never redo on its own
(e.g. after the ServiceAccount's UID changed). `OpenShellWorkspaceMember`
turns that manual step into normal, continuously-reconciled cluster state.

## See also

- [OpenShellWorkspaceMember CRD](../reference/openshellworkspacemember.md)
- [Authentication](authentication.md) - how identities get a token in the first place
- [Gateway](gateway.md) - architecture overview
