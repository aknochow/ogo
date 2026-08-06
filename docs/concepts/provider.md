---
type: Concept
title: Provider
description: Providers inject API credentials into sandboxes so agents can access external services like LLM APIs and source control.
tags: [core, credentials]
---

# Provider

A provider represents an external service that sandboxes need access to -
an LLM inference API, a source control platform, or a cloud service. The
`OpenShellProvider` CRD tells the operator which credentials to inject and
how to refresh them.

## How it works

1. Admin creates an `OpenShellProvider` CR referencing a Kubernetes Secret
2. The operator syncs the provider configuration to the gateway
3. When a sandbox starts, the gateway injects the credential as environment
   variables into the sandbox pod
4. The agent (Claude Code, etc.) reads the env var and authenticates

## Credential models

| Model | How it works | Best for |
|-------|-------------|----------|
| **Shared key** | One API key for all sandboxes | Small teams, dev/test |
| **BYOK** | Users set their own env vars in the sandbox | No admin setup needed |
| **Token refresh** | Gateway exchanges a service account key for short-lived tokens | Production with Vertex AI, GCP |
| **SPIFFE/WIF** | Per-sandbox identity via Workload Identity Federation | Enterprise with audit requirements |

## Built-in provider profiles

The OpenShell gateway ships with built-in profiles that define the endpoints,
binaries, and credential shapes for common services:

- `claude-code` - Anthropic API (`api.anthropic.com`)
- `google-vertex-ai` - Vertex AI with service account token refresh
- `github` - GitHub API and git operations
- `google-cloud` - GCP APIs
- `aws-bedrock` - AWS Bedrock (bridge-fronted)

Set `spec.providerType` to one of these names. The gateway handles endpoint
allowlisting and credential injection automatically.

## See also

- [OpenShellProvider CRD](../reference/openshellprovider.md)
- [Claude Code example](../examples/claude-code.md)
- [Vertex AI example](../examples/vertex-ai.md)
- [Policy](policy.md) - network policies must allow the provider endpoints
