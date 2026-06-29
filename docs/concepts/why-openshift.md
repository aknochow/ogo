---
type: Concept
title: Why OpenShift
description: Why OGO targets OpenShift — the security and platform features that make it safe to run untrusted AI agent workloads.
tags: [architecture, security, openshift]
---

# Why OpenShift

AI coding agents run arbitrary code in sandboxes. They install packages,
modify files, execute shell commands, and make network requests — all
autonomously. This is an inherently adversarial workload that requires
defense-in-depth security controls.

OpenShift provides these controls as platform features. OGO integrates
with them directly rather than reimplementing them.

## Security Context Constraints (SCC)

OpenShift's SCC system enforces pod-level security policies that vanilla
Kubernetes Pod Security Admission cannot match:

- Force sandbox pods to run as non-root with specific UID ranges
- Drop all Linux capabilities except the minimum required
- Apply seccomp profiles that restrict system calls
- Prevent privilege escalation even if the agent code attempts it

OGO configures SCC bindings for sandbox ServiceAccounts automatically.

## SELinux

OpenShift enforces SELinux in enforcing mode by default. Even if an
agent achieves a container escape, SELinux's mandatory access control
prevents access to host resources, other containers, and sensitive
kernel interfaces.

This is a layer of protection that does not exist on most vanilla
Kubernetes distributions.

## Network policy enforcement

OpenShift's OVN-Kubernetes CNI enforces NetworkPolicies at the kernel
level using eBPF and Open vSwitch. The OpenShell supervisor's network
policies (which control per-binary egress to specific hosts and ports)
are enforced in addition to Kubernetes NetworkPolicies.

On vanilla Kubernetes, NetworkPolicy enforcement depends on the CNI
plugin installed — and many CNIs don't enforce them at all.

## Integrated OAuth

OpenShift includes a built-in OAuth server integrated with the cluster's
identity provider (LDAP, OIDC, GitHub, etc.). OGO's auth-bridge
translates OpenShift OAuth tokens into OIDC JWTs, giving users
"Log in with OpenShift" without deploying a separate identity provider.

On vanilla Kubernetes, you would need to deploy and manage Keycloak,
Dex, or another OIDC provider separately.

## cert-manager operator

OpenShift's OperatorHub includes cert-manager as a supported operator.
OGO uses cert-manager to issue Let's Encrypt certificates for the
Envoy Gateway TLS listener, with automatic renewal.

## Operator Lifecycle Manager

OLM provides managed operator installs with:
- Console integration (Installed Operators page, CRD forms)
- Automatic upgrades via catalog polling
- RBAC and ServiceAccount management
- Bundle validation and scorecard testing

## Audit logging

OpenShift's API server audit logging captures every Kubernetes API call
made by sandbox pods, the gateway, and the operator. This provides a
complete audit trail for compliance and incident response.

## What OGO uses from OpenShift

| Feature | How OGO uses it |
|---------|----------------|
| SCC | Privileged SCC for sandbox SA (container-in-container) |
| Routes | TLS passthrough to Envoy Gateway, edge TLS for auth-bridge |
| OAuthClient | Auth-bridge SSO registration |
| OAuth Server | User authentication for the CLI |
| cert-manager | Let's Encrypt certs for Envoy Gateway |
| OLM | Operator install and console integration |

## See also

- [Authentication](authentication.md) — how SSO works with the auth-bridge
- [Policy](policy.md) — sandbox network and filesystem controls
- [Envoy Gateway guide](../guides/envoy-gateway.md) — TLS termination
