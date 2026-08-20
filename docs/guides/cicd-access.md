---
type: Guide
title: CI/CD Pipeline Access
description: Grant a CI/CD runner (AAP, GitLab CI, Tekton, Jenkins, or anything else) non-interactive access to create and manage sandboxes.
tags: [ci-cd, authentication, workspace-membership, automation]
---

# CI/CD pipeline access

This guide covers granting a CI/CD pipeline non-interactive access to an
OGO-managed gateway — for example, a job that spins up a sandbox, runs an
AI agent inside it, and tears it down as part of a larger pipeline.

Two supported paths, in order of preference:

1. **ServiceAccount + `OpenShellWorkspaceMember`** (recommended) — a
   dedicated, revocable identity per pipeline, authorized declaratively.
2. **mTLS** — simpler to set up, but a single shared client certificate
   grants access to anyone who has it; no per-identity revocation or audit
   trail. Reasonable for a quick start or a low-stakes pipeline; see
   [mTLS](../concepts/authentication.md#mtls) in the Authentication concept
   doc.

The rest of this guide covers path 1.

## 1. Create a dedicated ServiceAccount

`OpenShellWorkspaceMember`'s `serviceAccountRef` only resolves within its
own namespace (see [Security](../reference/openshellworkspacemember.md#security))
— the ServiceAccount **must live in the same namespace as the gateway**,
not in your CI runner's own namespace.

```bash
oc create serviceaccount ci-pipeline -n ogo
```

Use a dedicated ServiceAccount per pipeline rather than reusing a shared or
default one. A shared identity can't be individually revoked or audited,
and over-grants access to every pipeline using it.

## 2. Grant workspace membership

```yaml
apiVersion: gateway.ogo.aknochow.io/v1alpha1
kind: OpenShellWorkspaceMember
metadata:
  name: ci-pipeline
  namespace: ogo
spec:
  workspace: default
  serviceAccountRef:
    name: ci-pipeline
  role: user
```

```bash
oc apply -f openshellworkspacemember-ci-pipeline.yaml
oc get openshellworkspacemember ci-pipeline -n ogo -w
# wait for phase: Synced
```

See [Workspace Membership](../concepts/workspace-membership.md) for the
full concept — this is the authorization layer; a valid token alone
doesn't grant sandbox access without it.

## 3. Get the ServiceAccount's token into the pipeline

How you do this depends on whether your CI platform can run the pipeline's
pod **as** this ServiceAccount directly:

### Option A — the runner can bind to the ServiceAccount (preferred)

Some platforms let you run a job's pod under an arbitrary existing
ServiceAccount in a chosen namespace — for example, Tekton's
`taskRunTemplate.serviceAccountName`, or a Kubernetes-executor-based
runner configured with a target namespace and service account. When this
is possible, point the job at the `ci-pipeline` ServiceAccount created
above, running in the `ogo` namespace. Kubernetes automatically projects
that ServiceAccount's token into the pod at the standard path
(`/var/run/secrets/kubernetes.io/serviceaccount/token`) — no secret
management needed, and the token is short-lived and automatically
rotated.

### Option B — store a durable token as a CI/CD variable

If your platform can't run the job as an arbitrary ServiceAccount (for
example, its execution environment always runs under its own fixed
identity, or automatic token projection is disabled and can't be
overridden), generate a token for the ServiceAccount once and store it as
a protected, masked CI/CD variable instead:

```bash
oc create token ci-pipeline -n ogo --duration=8760h
```

Store the output as a protected/masked variable in your CI platform
(e.g. a GitLab CI/CD variable, a Jenkins credential). Treat it like any
other long-lived credential: masked in logs, restricted to protected
branches/pipelines, and rotated periodically
(`oc create token ci-pipeline -n ogo --duration=8760h` again to reissue).

## 4. Exchange the token and configure the CLI

Whichever option above got you a Kubernetes ServiceAccount token, the
pipeline step itself is the same — exchange it for an OpenShell JWT via
auth-bridge's `/token/exchange` endpoint, then write the CLI's config
files directly (the same technique used for
[Dev Spaces "with auth"](devspaces.md#same-cluster-setup-with-auth)):

```bash
SA_TOKEN=$(cat /var/run/secrets/kubernetes.io/serviceaccount/token)  # Option A
# or: SA_TOKEN="$CI_SERVICEACCOUNT_TOKEN"                            # Option B, from your CI variable

RESPONSE=$(curl -sf -X POST \
  -H "Authorization: Bearer ${SA_TOKEN}" \
  https://openshell-auth.apps.YOUR-CLUSTER.example.com/token/exchange)

GATEWAY_HOST="openshell.apps.YOUR-CLUSTER.example.com"
AUTH_HOST="openshell-auth.apps.YOUR-CLUSTER.example.com"

mkdir -p ~/.config/openshell/gateways/ci
cat > ~/.config/openshell/gateways/ci/metadata.json <<EOF
{
  "name": "ci",
  "gateway_endpoint": "https://${GATEWAY_HOST}",
  "is_remote": true,
  "gateway_port": 0,
  "auth_mode": "oidc",
  "oidc_issuer": "https://${AUTH_HOST}",
  "oidc_client_id": "openshell-cli"
}
EOF
cat > ~/.config/openshell/gateways/ci/oidc_token.json <<EOF
{
  "access_token": "$(echo "$RESPONSE" | jq -r .access_token)",
  "expires_at": $(echo "$RESPONSE" | jq -r .expires_at),
  "issuer": "https://${AUTH_HOST}",
  "client_id": "openshell-cli"
}
EOF
echo ci > ~/.config/openshell/active_gateway

openshell sandbox create --no-keep -- your-command-here
```

If a pipeline step's token is close to its expiry (default JWT TTL is 8h,
see [Authentication](../concepts/authentication.md#security-considerations)),
re-run the exchange rather than reusing a cached one — pipeline jobs are
typically short-lived enough that this is a non-issue, but a long-running
job should re-exchange partway through rather than assume the token
outlives the job.

## Same-cluster networking

If your CI runner's pods run on the **same cluster** as the OGO-managed
gateway, you can talk to the gateway's in-cluster Service directly instead
of routing through the public Route — skips the router hop entirely. Check
whether the gateway's direct in-cluster listener has TLS enabled
(`spec.tls.enabled` on the `OpenShellGateway` CR) before choosing `http://`
vs `https://`; sending a TLS handshake to a plaintext-only listener (or
vice versa) fails immediately with a protocol-level error, not a helpful
one.

## See also

- [Authentication](../concepts/authentication.md)
- [Workspace Membership](../concepts/workspace-membership.md)
- [OpenShellWorkspaceMember reference](../reference/openshellworkspacemember.md)
- [Shared Volumes](shared-volumes.md) — mounting existing PVCs into
  sandboxes created from a pipeline
- [Tekton Shared Workspace example](../examples/tekton-shared-workspace.md)
