---
type: Index
title: OGO Documentation
description: OpenShell Gateway Operator — deploy and manage OpenShell on OpenShift.
---

# OGO — OpenShell Gateway Operator

> **Alpha software.** OGO and [NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell)
> are both in active development. APIs, CRDs, and behavior may change without
> notice. This project is not intended for production use.

OGO is an OpenShift operator that deploys and manages NVIDIA OpenShell
gateway instances. It handles TLS, authentication, ingress, and sandbox
lifecycle so you can run AI coding agents in isolated cloud sandboxes.

## Why OpenShift?

AI coding agents run arbitrary code in sandboxes — that's an inherently
adversarial workload. OpenShift provides the security stack that makes
this safe:

- **Security Context Constraints (SCC)** — enforce non-root execution,
  drop dangerous capabilities, and apply seccomp profiles on sandbox pods
- **SELinux** — mandatory access control that agent code cannot bypass,
  even with container escapes
- **OVN-Kubernetes network policy** — kernel-level egress enforcement for
  sandbox network policies, not bypassable from userspace
- **Integrated OAuth** — user identity tied to your organization's SSO,
  not self-managed tokens
- **cert-manager operator** — managed TLS lifecycle with Let's Encrypt
- **Audit logging** — full audit trail of sandbox API activity
- **Operator Lifecycle Manager** — managed operator installs with
  console integration

Vanilla Kubernetes provides none of these out of the box. Running untrusted
agent workloads without these controls is not recommended.

See [Why OpenShift](docs/concepts/why-openshift.md) for details.

## Concepts

Core building blocks of an OGO deployment.

- [Gateway](docs/concepts/gateway.md) — the central gRPC server that manages sandboxes
- [Sandbox](docs/concepts/sandbox.md) — isolated compute pods where agents run
- [Authentication](docs/concepts/authentication.md) — how users and sandboxes authenticate
- [Provider](docs/concepts/provider.md) — API credentials injected into sandboxes
- [Policy](docs/concepts/policy.md) — network, filesystem, and process controls for sandboxes

## Guides

Step-by-step walkthroughs.

- [Quickstart](docs/guides/quickstart.md) — deploy OGO on OpenShift in 10 minutes
- [Envoy Gateway](docs/guides/envoy-gateway.md) — gRPC ingress with Let's Encrypt TLS
- [OpenShift SSO](docs/guides/openshift-sso.md) — "Log in with OpenShift" for the CLI

## Reference

CRD specifications and field-level documentation.

- [OpenShellGateway](docs/reference/openshellgateway.md) — gateway CRD reference
- [OpenShellProvider](docs/reference/openshellprovider.md) — provider CRD reference
- [OpenShellPolicy](docs/reference/openshellpolicy.md) — policy CRD reference

## Examples

Ready-to-use configurations.

- [Claude Code + Anthropic](docs/examples/claude-code.md) — direct Anthropic API access
- [Claude Code + Vertex AI](docs/examples/vertex-ai.md) — Google Vertex AI inference
- [Developer Policy](docs/examples/developer-policy.md) — GitHub, PyPI, npm access

## Development

```bash
make build          # Build the binary
make test           # Run unit and integration tests
make manifests      # Regenerate CRDs and RBAC
make generate       # Regenerate deepcopy functions
```

See [CLAUDE.md](CLAUDE.md) for versioning rules, image ownership, and build pipeline.
See [AGENTS.md](AGENTS.md) for AI agent instructions.

## License

Copyright 2026. Licensed under the Apache License, Version 2.0.
