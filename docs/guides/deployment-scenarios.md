---
type: Guide
title: Deployment Scenarios
description: Numbered reference of every OGO deployment scenario - pick your type, jump to the guide.
tags: [getting-started, reference]
---

# Deployment Scenarios

OGO can be deployed and accessed in several distinct shapes, depending on
cluster type, ingress, and authentication. This page is a quick-reference
index — each numbered type is a real, encountered scenario, not a
hypothetical combination. Find your type, then follow the link to the
full guide.

Use this page the way you'd use a support triage doc: "you're looking for
Type 4, check that out."

## Which type am I?

```
Do you need OpenShift SSO / OIDC login for the openshell CLI?
│
├─ No (mTLS, unauthenticated, or CI/automation) ──────────────┐
│                                                              │
└─ Yes ──────────────────────────────────────────────────────┐│
                                                               ││
Is this a local single-node test cluster (MINC/Kind/CRC),     ││
or a real/shared OpenShift cluster?                           ││
                                                               ││
   Local, no SSO needed        → Type 1                       ││
   Local, SSO needed           → Type 2                       ││
   Real cluster, no SSO        → Type 3                       ││
   Real cluster, with SSO      → Type 4                       ││
                                                               ▼▼
Are you connecting from a Dev Spaces workspace instead of a
developer laptop?
   Same cluster, unauthenticated route  → Type 5
   Same cluster, authenticated route    → Type 6
   Different cluster than Dev Spaces    → Type 7
```

## Summary table

| Type | Scenario | Auth | TLS | Cluster | Guide |
|------|----------|------|-----|---------|-------|
| **1** | Local quick test | mTLS or unauthenticated | Self-signed | MINC / Kind | [Without Envoy Gateway](quickstart.md#without-envoy-gateway) |
| **2** | Local full-featured test | OpenShift SSO | Self-signed or Let's Encrypt | CRC (OpenShift preset) | [With Envoy Gateway](quickstart.md#with-envoy-gateway) + [real-cluster testing](../../CONTRIBUTING.md#testing-against-a-real-cluster) |
| **3** | Real cluster, direct Route | mTLS | Self-signed | Any real OpenShift | [Without Envoy Gateway](quickstart.md#without-envoy-gateway) |
| **4** | Real cluster, Envoy Gateway | OpenShift SSO (browser) | Let's Encrypt via cert-manager | Any real OpenShift | [With Envoy Gateway](quickstart.md#with-envoy-gateway) |
| **5** | Dev Spaces, same cluster, no auth | None (`allowUnauthenticated`) | N/A (cluster-internal) | Same as Dev Spaces | [Same-cluster setup (simple)](devspaces.md#same-cluster-setup-simple) |
| **6** | Dev Spaces, same cluster, with auth | Token exchange (OpenShift token → JWT) | Let's Encrypt (external Route) | Same as Dev Spaces | [Same-cluster setup (with auth)](devspaces.md#same-cluster-setup-with-auth) |
| **7** | Dev Spaces, cross-cluster | Token exchange (OpenShift token → JWT) | Let's Encrypt (external Route) | Different from Dev Spaces | [Cross-cluster setup](devspaces.md#cross-cluster-setup) |

## Type 1 — Local quick test (MINC / Kind)

Fastest iteration loop for developing the operator itself. No Envoy
Gateway, no cert-manager, no OpenShift OAuth.

- **Auth**: mTLS client certs, or `allow_unauthenticated_users` for pure
  local iteration
- **TLS**: operator-managed self-signed
- **Cluster**: MINC (MicroShift) or Kind, driven by `make test-e2e` /
  `make deploy` against a local kubeconfig

**Cannot test**: OpenShift SSO, `OAuthClient` reconciliation, real
cert-manager/Let's Encrypt issuance. MINC and Kind do not have the
`oauth.openshift.io`/`user.openshift.io` API groups at all — see
[Testing against a real cluster](../../CONTRIBUTING.md#testing-against-a-real-cluster)
for why this matters and when you need Type 2 or Type 3/4 instead.

→ Follow [Without Envoy Gateway](quickstart.md#without-envoy-gateway).

## Type 2 — Local full-featured test (CRC with OpenShift preset)

CodeReady Containers with the full OpenShift preset (not the
OKD/MicroShift-style preset) has the complete API surface, including
`oauth.openshift.io`, so it's the only *local* option that can exercise
SSO- and OAuthClient-related code before pushing to a shared cluster.
Heavier than Type 1 (~8GB RAM) — use it when you're specifically
validating auth-bridge, TLS, or Gateway API changes, not for routine
iteration.

Run the real-cluster e2e suite against it the same way you would against
a shared cluster:

```bash
KUBECONFIG=~/.kube/crc make test-e2e-real
```

See [Testing against a real cluster](../../CONTRIBUTING.md#testing-against-a-real-cluster)
for the full requirement and rationale — this is a hard requirement for
changes touching `internal/authbridge/`, TLS/cert-manager reconciliation,
or `route.gatewayAPI`.

→ Follow [With Envoy Gateway](quickstart.md#with-envoy-gateway) for the
CR shape, substituting CRC's kubeconfig and apps domain.

## Type 3 — Real cluster, direct Route (no Envoy Gateway)

The simplest production-capable shape. An OpenShift Route passthroughs
TLS directly to the gateway pod, which terminates it with its own
self-signed certificate. No cert-manager, no Envoy Gateway, no Helm
dependency.

- **Auth**: mTLS client certificates (`spec.auth.openshift.enabled: false`)
- **TLS**: operator-managed self-signed, `--gateway-insecure` on the CLI
- **Use when**: you don't need browser-based SSO, or don't want the
  Envoy Gateway/cert-manager dependency

→ Follow [Without Envoy Gateway](quickstart.md#without-envoy-gateway).

## Type 4 — Real cluster, Envoy Gateway + OpenShift SSO

The full production shape (this is what RDU runs). Envoy Gateway fronts
the gateway pod over the Gateway API, cert-manager issues a real Let's
Encrypt certificate for the public hostname, and users log in via
OpenShift SSO through the auth-bridge.

- **Auth**: OpenShift SSO (browser login), backed by an external identity
  provider (e.g. Red Hat SSO) configured on the cluster's OAuth config
- **TLS**: Let's Encrypt via cert-manager on the Gateway API listener.
  The gateway pod's *own* listener stays self-signed regardless — see
  [Gateway concept](../concepts/gateway.md) and
  [Authentication concept](../concepts/authentication.md) for why these
  are two independent termination points, not a contradiction
- **Prerequisites**: cert-manager, Envoy Gateway, Helm

→ Follow [With Envoy Gateway](quickstart.md#with-envoy-gateway), then
[OpenShift SSO](openshift-sso.md) for user group setup, then
[Envoy Gateway](envoy-gateway.md) for ingress architecture details and
troubleshooting.

## Type 5 — Dev Spaces, same cluster, unauthenticated

A Dev Spaces workspace on the *same* cluster as OGO, talking to the
cluster-internal gateway Service directly. No token exchange, no
external Route round-trip. Requires
`spec.auth.allowUnauthenticated: true` — dev/test clusters only, since
this disables authentication for all cluster-internal traffic.

→ Follow [Same-cluster setup (simple)](devspaces.md#same-cluster-setup-simple).

## Type 6 — Dev Spaces, same cluster, authenticated

Same cluster as Type 5, but `allowUnauthenticated` is off (shared or
multi-tenant clusters). The workspace exchanges its OpenShift user token
for an OpenShell JWT via the auth-bridge's `/token/exchange` endpoint,
then talks to the gateway over the external Route like any other client.

→ Follow [Same-cluster setup (with auth)](devspaces.md#same-cluster-setup-with-auth).

## Type 7 — Dev Spaces, cross-cluster

Dev Spaces and OGO run on *different* OpenShift clusters. Same token
exchange as Type 6, but against the remote cluster's auth-bridge, with a
separate kubeconfig to obtain the remote user token. Requires network
connectivity from the Dev Spaces pod to the gateway cluster's Routes.

→ Follow [Cross-cluster setup](devspaces.md#cross-cluster-setup).

## See also

- [Quickstart](quickstart.md) - the two base deployment paths in full detail
- [Dev Spaces](devspaces.md) - all three Dev Spaces sub-scenarios in full detail
- [OpenShift SSO](openshift-sso.md) - user groups, token lifetime, troubleshooting
- [Envoy Gateway](envoy-gateway.md) - ingress architecture and troubleshooting
- [CONTRIBUTING.md](../../CONTRIBUTING.md#testing-against-a-real-cluster) - why Types 1/2 differ in test coverage
