---
type: Guide
title: Shared Volumes
description: Mount existing PVCs, ConfigMaps, and Secrets into OGO-managed sandboxes for CI/CD pipelines and shared datasets.
tags: [sandbox, storage, pvc, ci-cd]
---

# Shared volumes

OGO-managed sandboxes normally get one auto-provisioned PVC for the
`/sandbox` workspace directory (see [Sandbox](../concepts/sandbox.md)). This
guide covers mounting an **additional, pre-existing** PVC (or ConfigMap or
Secret) into a sandbox pod — for sharing a workspace across pipeline steps,
serving a read-only dataset to multiple sandboxes, or injecting
configuration that shouldn't live in the sandbox image.

## Is this a CRD feature or a passthrough?

OGO's operator never mediates individual sandbox creation requests — it
only provisions the `<gateway>-sandbox` ServiceAccount, RBAC, and namespace
scaffolding that CI callers and the CLI use to talk to the gateway
directly. OGO's own code contains no Go types or proto definitions for the
`agents.x-k8s.io` `Sandbox` CRD's spec; a sandbox is created and owned
entirely by the OpenShell gateway binary in response to a `CreateSandbox`
call, independent of OGO's reconcile loop. So the existing-PVC-mount
capability upstream added ([NVIDIA/OpenShell#2034](https://github.com/NVIDIA/OpenShell/pull/2034))
is available to any OGO-managed gateway today, with **no OGO CRD or
controller changes required** — it's a matter of passing the right
request, not configuring the operator.

Confirm the mechanism yourself:

```bash
openshell sandbox create --help
```

Look for `--driver-config-json`, documented as an "Experimental
driver-keyed JSON object for driver-specific sandbox settings" whose
"[v]alidation behavior is not yet finalized." For the Kubernetes driver,
this accepts `kubernetes.volumes` (standard pod
[`Volume`](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#volume-v1-core)
entries) and `kubernetes.containers.agent.volume_mounts` (standard
[`VolumeMount`](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#volumemount-v1-core)
entries), per the upstream issue. Since the flag is marked experimental,
verify the exact JSON shape against your installed OpenShell CLI's own
`--help` output before relying on it in a pipeline — this guide documents
the field paths and OGO-specific deployment constraints, not a stable
upstream schema OGO controls.

Requires the gateway's OpenShell version to be `>= v0.0.82` (the version
`driver_config.kubernetes.volumes`/`volume_mounts` first shipped in).
Check your gateway's pinned version:

```bash
oc get openshellgateway -o jsonpath='{.items[0].spec.imageTag}'
```

## Usage

```bash
openshell sandbox create \
  --driver-config-json '{
    "kubernetes": {
      "volumes": [
        {"name": "shared", "persistent_volume_claim": {"claim_name": "my-existing-pvc"}}
      ],
      "containers": {
        "agent": {
          "volume_mounts": [
            {"name": "shared", "mount_path": "/data"}
          ]
        }
      }
    }
  }' \
  -- ls /data
```

This is additive: the sandbox's own auto-provisioned workspace PVC still
gets mounted at `/sandbox` as usual. `/data` in this example is a second,
independent mount pointing at a PVC that already exists before the sandbox
was created.

## Deployment constraints

### Namespace alignment

The sandbox pod and the PVC must be in the **same namespace** — a Sandbox
spec references a PVC by name only, with no cross-namespace mount
mechanism. OGO's `spec.sandbox.namespace` (default: the gateway's own
namespace) controls where sandbox pods are created. If your workload that
needs to share a PVC with sandboxes runs in a different namespace than
`spec.sandbox.namespace`, either:

- Move the workload into the sandbox namespace, or
- Point `spec.sandbox.namespace` at the workload's namespace.

Cross-namespace PVC access isn't supported by the underlying mechanism, so
there's no OGO-side workaround for this — the
[recommended approach](../reference/openshellgateway.md) is one gateway per
team, with the sandbox namespace matching the team's workload namespace,
rather than trying to share PVCs across namespace boundaries.

### Authentication for non-interactive callers

A CI pipeline step or controller creating sandboxes non-interactively needs
a supported auth path — the same two options documented in
[Authentication](../concepts/authentication.md):

- **mTLS** — works today, no extra setup. Extract the gateway's client
  certificate and use it with `openshell gateway add ... --local` or
  `--remote` (see the mTLS section of the Authentication doc).
- **ServiceAccount identity** — as of
  [`OpenShellWorkspaceMember`](../concepts/workspace-membership.md)
  (shipped v0.3.0), a Kubernetes ServiceAccount in the same namespace as
  the gateway can be granted workspace membership declaratively, letting a
  CI pipeline authenticate as its own ServiceAccount instead of
  distributing a shared mTLS client certificate. See the
  [sample CR](https://github.com/aknochow/ogo/tree/main/config/samples) for
  a `ci-pipeline`-style grant.

### RWO scheduling

This is the constraint most likely to bite a shared-workspace CI/CD
pattern, and it's a Kubernetes volume-binding property, not something OGO
or OpenShell control:

- A `ReadWriteOnce` (RWO) PVC can be mounted by multiple pods
  **concurrently only if those pods land on the same node**. A
  `ReadWriteOnce` claim restricts attachment to one node at a time — not
  one pod — but Kubernetes doesn't guarantee co-location unless you tell it
  to.
- The common failure mode: a CI system (e.g. Tekton) holds a workspace PVC
  mounted in its own pod for the full duration of a multi-step Task, and a
  sandbox pod created mid-task also needs that PVC. If the scheduler places
  the sandbox pod on a different node, it gets stuck `Pending` with a
  `FailedAttachVolume`/multi-attach error — nothing about the sandbox
  itself is broken, the volume just can't attach to two nodes
  simultaneously.
- **Prefer a `ReadWriteMany` (RWX) storage class** for any PVC that will be
  concurrently mounted by a CI pod and a sandbox pod, if your cluster's
  storage provider offers one (e.g. NFS-backed, CephFS, or a cloud
  provider's RWX-capable class).
- If only RWO storage is available, either avoid true concurrency (don't
  create the sandbox until the CI pod has released the mount, and vice
  versa — awkward with Tekton's per-Task pod-lifetime volume mounting), or
  pin both the CI pod and the sandbox pod to the same node with matching
  `nodeSelector`/affinity rules. Neither is as simple as just switching to
  RWX if you have the option.

## See also

- [Tekton shared-workspace example](../examples/tekton-shared-workspace.md)
- [Sandbox](../concepts/sandbox.md)
- [Workspace Membership](../concepts/workspace-membership.md)
- [Authentication](../concepts/authentication.md)
