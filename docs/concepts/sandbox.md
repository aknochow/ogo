---
type: Concept
title: Sandbox
description: An isolated compute pod where AI agents and developer tools run with controlled access to external services.
tags: [core, architecture]
---

# Sandbox

A sandbox is an isolated Kubernetes pod that provides a compute environment
for AI coding agents (Claude Code, Cursor, Copilot) or interactive developer
sessions. Each sandbox gets its own filesystem, network namespace, and
security policies.

## Lifecycle

1. **Create** - the CLI calls `CreateSandbox`; the gateway creates a
   `Sandbox` CRD object (`agents.x-k8s.io/v1beta1`); the Sandbox controller
   creates the pod
2. **Bootstrap** - the supervisor sidecar starts, exchanges its K8s SA token
   for a gateway JWT via `IssueSandboxToken`, then connects the relay session
3. **Ready** - the sandbox accepts SSH connections from the CLI
4. **Delete** - the CLI calls `DeleteSandbox`; the gateway deletes the
   Sandbox CRD; the controller cleans up the pod and PVC

## Pod structure

```
sandbox pod
├── init: openshell-supervisor-install (copies supervisor binary)
├── init: workspace-init (initializes workspace PVC)
└── container: agent
    ├── entry: /opt/openshell/bin/openshell-sandbox
    ├── volume: openshell-sa-token (projected SA token)
    ├── volume: openshell-supervisor-bin (supervisor binary)
    └── volume: workspace (PVC, mounted at /sandbox)
```

The supervisor runs as a background process inside the agent container,
not as a separate sidecar container.

## Images

The default sandbox image is `ghcr.io/nvidia/openshell-community/sandboxes/base:latest`.
Override with `--from` on the CLI:

```bash
openshell sandbox create --from quay.io/myorg/my-sandbox-image:latest
```

Or set a cluster-wide default via `spec.sandbox.defaultImage` on the
[OpenShellGateway](../reference/openshellgateway.md) CRD.

## Workspace persistence

Each sandbox gets a PVC for the `/sandbox` workspace directory. The default
size is 2Gi (configurable via `spec.sandbox.workspaceStorageSize`). The PVC
persists across sandbox restarts but is deleted when the sandbox is deleted.

## See also

- [Policy](policy.md) - control what sandboxes can access
- [Provider](provider.md) - inject API credentials into sandboxes
