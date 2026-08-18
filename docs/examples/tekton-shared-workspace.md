---
type: Example
title: Tekton Shared Workspace
description: A Tekton Task where non-sandboxed steps prepare and finalize a workspace PVC, and a sandboxed step runs an AI agent against the same PVC.
tags: [sandbox, tekton, ci-cd, pvc]
---

# Tekton shared workspace

This example demonstrates the pattern from
[Shared volumes](../guides/shared-volumes.md): a pipeline orchestrator
manages a workspace PVC across steps, where a non-sandboxed step prepares
the workspace, a **sandboxed** step runs an AI agent against it, and a
final non-sandboxed step processes results — all sharing one PVC.

## Key point: the sandboxed step is a separate pod

Unlike a normal Tekton step (a container in the Task's own pod), the
"sandboxed step" below doesn't run the agent directly. It runs the
`openshell` CLI, which calls the gateway to create a **separate** Sandbox
pod elsewhere in the sandbox namespace, waits for it to finish (via
`--no-keep` and a trailing command), and exits. Both pods — the Tekton
Task pod and the sandbox pod — need to mount the same PVC, which is
exactly the scenario the
[RWO scheduling section](../guides/shared-volumes.md#rwo-scheduling) of the
shared-volumes guide covers. Use `ReadWriteMany` storage for this PVC if
your cluster offers it.

## Task

```yaml
apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: agent-shared-workspace
spec:
  workspaces:
    - name: shared-workspace
  steps:
    - name: prepare
      image: alpine/git
      script: |
        #!/bin/sh
        set -eu
        git clone https://github.com/example/repo.git $(workspaces.shared-workspace.path)/repo

    - name: run-agent-sandbox
      # A custom image with the openshell CLI installed and the gateway's
      # mTLS client cert already registered (see Authentication in
      # docs/concepts/authentication.md) - baking this in avoids repeating
      # `openshell gateway add` setup on every pipeline run.
      image: quay.io/myorg/openshell-cli:latest
      script: |
        #!/bin/sh
        set -eu
        openshell sandbox create \
          --gateway my-gateway \
          --no-keep \
          --driver-config-json '{
            "kubernetes": {
              "volumes": [
                {"name": "shared", "persistent_volume_claim": {"claim_name": "'"$(context.taskRun.name)"'-shared-workspace"}}
              ],
              "containers": {
                "agent": {
                  "volume_mounts": [
                    {"name": "shared", "mount_path": "/workspace"}
                  ]
                }
              }
            }
          }' \
          -- sh -c 'cd /workspace/repo && claude "fix the failing tests"'

    - name: push-results
      image: alpine/git
      script: |
        #!/bin/sh
        set -eu
        cd $(workspaces.shared-workspace.path)/repo
        git push origin HEAD:agent-fix-$(context.taskRun.name)
```

## PipelineRun binding

```yaml
apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: agent-fix-run
spec:
  taskRunTemplate:
    serviceAccountName: ci-pipeline
  workspaces:
    - name: shared-workspace
      persistentVolumeClaim:
        claimName: agent-fix-run-shared-workspace
```

The PVC referenced here (`agent-fix-run-shared-workspace`) must:

- Already exist (or be created by a preceding `VolumeClaimTemplate`/step) —
  it's an **existing** PVC from the sandbox's point of view, per
  [Shared volumes](../guides/shared-volumes.md)
- Live in the same namespace as `spec.sandbox.namespace` on the
  `OpenShellGateway` CR, so the sandbox pod created in `run-agent-sandbox`
  can mount it
- Use `ReadWriteMany` storage if available, to avoid the RWO
  same-node-scheduling constraint between the Tekton pod and the sandbox
  pod

The `ci-pipeline` ServiceAccount is the same one granted workspace
membership in the
[`OpenShellWorkspaceMember` sample](https://github.com/aknochow/ogo/tree/main/config/samples) —
see [Shared volumes](../guides/shared-volumes.md#authentication-for-non-interactive-callers)
for the two supported non-interactive auth paths.

## See also

- [Shared volumes](../guides/shared-volumes.md)
- [Sandbox](../concepts/sandbox.md)
- [Workspace Membership](../concepts/workspace-membership.md)
