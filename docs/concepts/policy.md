---
type: Concept
title: Policy
description: Policies control what sandboxes can access — network endpoints, filesystem paths, and process identity.
tags: [core, security]
---

# Policy

Policies define the security boundary for sandbox pods. The `OpenShellPolicy`
CRD configures three dimensions of control:

## Network

Controls which external hosts and ports the sandbox can reach. Each rule
specifies:

- **Endpoints** — hostname (supports globs like `*.googleapis.com`), port,
  protocol (`rest`, `websocket`, `graphql`, `sql`), and access level
  (`read-only`, `read-write`, `full`)
- **Binaries** — which executables are allowed to use these endpoints
- **Enforcement** — `enforce` (block violations) or `audit` (log only)

The supervisor enforces network policies at the process level using eBPF-based
interception. Traffic from unlisted binaries or to unlisted endpoints is blocked.

## Filesystem

Controls which paths the sandbox can read and write:

- `includeWorkdir: true` — the sandbox working directory (`/sandbox`) is
  automatically read-write
- `readOnly` — paths accessible for reading (e.g., `/usr`, `/etc`)
- `readWrite` — paths accessible for writing (e.g., `/tmp`, `/dev/null`)

Enforced via Landlock LSM on supported kernels.

## Process

Controls the identity of processes inside the sandbox:

- `runAsUser` — the user name or UID (default: `sandbox`)
- `runAsGroup` — the group name or GID (default: `sandbox`)

## Default policy

When no `OpenShellPolicy` is configured, the gateway applies a restrictive
default:

- Filesystem: workdir + `/usr`, `/lib`, `/etc`, `/proc`, `/dev/urandom`
  read-only; `/sandbox`, `/tmp`, `/dev/null` read-write
- Network: **no external access** (empty network policy)
- Process: `sandbox:sandbox`

This means sandboxes cannot reach any external API by default — you must
create a policy to allow it.

## See also

- [OpenShellPolicy CRD](../reference/openshellpolicy.md)
- [Developer Policy example](../examples/developer-policy.md)
- [Provider](provider.md) — providers define which endpoints need access
