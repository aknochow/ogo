---
type: CRD Reference
title: OpenShellProvider
description: Namespaced CRD that configures API credentials for sandbox pods.
resource: gateway.ogo.aknochow.io/v1alpha1
tags: [crd, provider, credentials]
---

# OpenShellProvider

**API Group:** `gateway.ogo.aknochow.io`
**Version:** `v1alpha1`
**Scope:** Namespaced (in the gateway namespace)

## Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `providerType` | string | yes | Provider profile slug (e.g., `claude-code`, `google-vertex-ai`, `github`) |
| `credentials` | map[string]SecretKeyRef | | Env var name → Secret key reference |
| `config` | map[string]string | | Non-secret provider configuration |

### SecretKeyRef

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Name of the Kubernetes Secret |
| `key` | string | Key within the Secret data |

## Status

| Field | Type | Description |
|-------|------|-------------|
| `phase` | enum | `Pending`, `Synced`, `Failed` |
| `observedGeneration` | int | Latest observed spec generation |
| `conditions` | []Condition | Sync status conditions |

## Provider types

| Type | Category | Credentials | Description |
|------|----------|-------------|-------------|
| `claude-code` | agent | `ANTHROPIC_API_KEY` | Anthropic API for Claude Code |
| `google-vertex-ai` | inference | `GOOGLE_SERVICE_ACCOUNT_KEY` | Vertex AI with token refresh |
| `github` | source_control | `GITHUB_TOKEN` | GitHub API and git operations |
| `google-cloud` | inference | Service account or ADC | GCP APIs |
| `aws-bedrock` | inference | AWS credentials | AWS Bedrock (bridge-fronted) |

## Examples

See [config/samples/](https://github.com/aknochow/ogo/tree/main/config/samples)
for ready-to-use provider CRs.
