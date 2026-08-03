---
type: Example
title: Claude Code + Vertex AI
description: Run Claude Code in sandboxes using Google Vertex AI for inference.
tags: [claude, vertex, gcp, inference]
---

# Claude Code with Vertex AI

> **Warning:** The Provider and Policy CRs below do not yet have any
> effect on the running gateway (see
> [Provider](../concepts/provider.md) and [Policy](../concepts/policy.md)).
> Following this example as written will not actually configure GCP
> credential refresh or allow network access to Vertex AI endpoints -
> the sandbox will run under the gateway's default policy, which permits
> no external network access. Tracked in
> [#51](https://github.com/aknochow/ogo/issues/51) and
> [#52](https://github.com/aknochow/ogo/issues/52).

This example configures OGO to run Claude Code in sandboxes using Google
Vertex AI for inference. The gateway manages GCP credential refresh - no
`gcloud` CLI needed in sandboxes.

## 1. Create a GCP service account

Create a service account with the `Vertex AI User` role:

```bash
gcloud iam service-accounts create openshell-vertex \
  --display-name="OpenShell Vertex AI"

gcloud projects add-iam-policy-binding YOUR_PROJECT \
  --member="serviceAccount:openshell-vertex@YOUR_PROJECT.iam.gserviceaccount.com" \
  --role="roles/aiplatform.user"

gcloud iam service-accounts keys create vertex-sa-key.json \
  --iam-account=openshell-vertex@YOUR_PROJECT.iam.gserviceaccount.com
```

## 2. Create the key Secret

```bash
oc create secret generic vertex-sa-key -n ogo \
  --from-file=key.json=vertex-sa-key.json
```

## 3. Create the Provider

```yaml
apiVersion: gateway.ogo.aknochow.io/v1alpha1
kind: OpenShellProvider
metadata:
  name: vertex-ai
  namespace: ogo
spec:
  providerType: google-vertex-ai
  credentials:
    GOOGLE_SERVICE_ACCOUNT_KEY:
      name: vertex-sa-key
      key: key.json
  config:
    project: your-gcp-project
    region: us-central1
```

The gateway exchanges the service account key for short-lived access tokens
and injects them into sandbox pods automatically.

## 4. Create the Policy

```yaml
apiVersion: gateway.ogo.aknochow.io/v1alpha1
kind: OpenShellPolicy
metadata:
  name: claude-code-vertex
  namespace: ogo
spec:
  policyName: claude-code-vertex
  filesystem:
    includeWorkdir: true
    readOnly: [/usr, /lib, /etc, /app, /proc/self, /dev/urandom]
    readWrite: [/sandbox, /tmp, /dev/null]
  network:
    vertex-ai:
      name: Google Vertex AI
      endpoints:
        - { host: "*.aiplatform.googleapis.com", port: 443, protocol: rest, access: read-write }
        - { host: aiplatform.googleapis.com, port: 443, protocol: rest, access: read-write }
        - { host: oauth2.googleapis.com, port: 443, protocol: rest, access: read-write }
      binaries:
        - { path: /usr/bin/claude }
        - { path: /usr/local/bin/claude }
    anthropic-telemetry:
      name: Anthropic Telemetry
      endpoints:
        - { host: statsig.anthropic.com, port: 443, protocol: rest, access: read-write }
        - { host: sentry.io, port: 443, protocol: rest, access: read-write }
      binaries:
        - { path: /usr/bin/claude }
        - { path: /usr/local/bin/claude }
  process:
    runAsUser: sandbox
    runAsGroup: sandbox
```

## 5. Connect

```bash
openshell sandbox create --gateway my-cluster
# Inside the sandbox, Claude Code picks up the Vertex credentials automatically:
claude
```

## See also

- [Provider concept](../concepts/provider.md)
- [Anthropic example](claude-code.md) - for direct Anthropic API access
