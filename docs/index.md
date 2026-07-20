---
type: Index
title: OGO Documentation
description: OpenShell Gateway Operator - deploy and manage OpenShell on OpenShift.
---

# OGO - OpenShell Gateway Operator

> **Alpha software.** OGO and [NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell)
> are both in active development. APIs, CRDs, and behavior may change without
> notice. This project is not intended for production use.

OGO is an OpenShift operator that deploys and manages NVIDIA OpenShell
gateway instances. It handles TLS, authentication, ingress, and sandbox
lifecycle so you can run AI coding agents in isolated sandboxes on any
OpenShift cluster — cloud, on-prem, or local.

## Concepts

Core building blocks of an OGO deployment.

- [Why OpenShift](concepts/why-openshift.md) - security and platform features for AI agent workloads
- [Gateway](concepts/gateway.md) - the central gRPC server that manages sandboxes
- [Sandbox](concepts/sandbox.md) - isolated compute pods where agents run
- [Authentication](concepts/authentication.md) - how users and sandboxes authenticate
- [Provider](concepts/provider.md) - API credentials injected into sandboxes
- [Policy](concepts/policy.md) - network, filesystem, and process controls for sandboxes

## Guides

Step-by-step walkthroughs.

- [Quickstart](guides/quickstart.md) - deploy OGO on OpenShift in 10 minutes
- [Envoy Gateway](guides/envoy-gateway.md) - gRPC ingress with Let's Encrypt TLS
- [OpenShift SSO](guides/openshift-sso.md) - "Log in with OpenShift" for the CLI
- [Dev Spaces](guides/devspaces.md) - create sandboxes from Dev Spaces workspaces

## Reference

CRD specifications and field-level documentation.

- [OpenShellGateway](reference/openshellgateway.md) - gateway CRD reference
- [OpenShellProvider](reference/openshellprovider.md) - provider CRD reference
- [OpenShellPolicy](reference/openshellpolicy.md) - policy CRD reference

## Examples

Ready-to-use configurations.

- [Claude Code + Anthropic](examples/claude-code.md) - direct Anthropic API access
- [Claude Code + Vertex AI](examples/vertex-ai.md) - Google Vertex AI inference
- [Developer Policy](examples/developer-policy.md) - GitHub, PyPI, npm access

## Development

```bash
make build          # Build the binary
make test           # Run unit and integration tests
make manifests      # Regenerate CRDs and RBAC
make generate       # Regenerate deepcopy functions
```

See [CONTRIBUTING.md](../CONTRIBUTING.md) for development setup and guidelines.

## License

Copyright 2026. Licensed under the Apache License, Version 2.0.
