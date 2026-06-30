---
type: Concept
title: Authentication
description: How users and sandboxes authenticate to the OpenShell gateway - OpenShift SSO, external OIDC, mTLS, and K8s ServiceAccount bootstrap.
tags: [core, security, auth]
---

# Authentication

The OpenShell gateway supports multiple authentication methods organized
into two flows: **user authentication** (CLI to gateway) and **sandbox
bootstrap** (supervisor to gateway).

## User authentication

Users authenticate to create, list, and manage sandboxes.

### OpenShift SSO (recommended on OpenShift)

The OGO operator deploys an **auth-bridge** sidecar that translates
OpenShift OAuth tokens into standard OIDC JWTs. Users log in with their
OpenShift credentials via browser.

```bash
openshell gateway login my-cluster
# Browser opens → OpenShift login → JWT stored locally
```

See [OpenShift SSO guide](../guides/openshift-sso.md) for setup.

### External OIDC

On vanilla Kubernetes or when using an external identity provider
(Keycloak, Okta, Auth0), configure the gateway's OIDC settings to
point at your provider. The CLI handles the OIDC code flow.

### mTLS

For CI/CD pipelines and automation, the gateway supports client
certificate authentication. The operator generates a client mTLS
certificate in the `{gateway}-client-tls` Secret.

```bash
oc get secret openshell-client-tls -n ogo -o jsonpath='{.data.tls\.crt}' | base64 -d > client.crt
oc get secret openshell-client-tls -n ogo -o jsonpath='{.data.tls\.key}' | base64 -d > client.key
```

## Sandbox bootstrap

When a sandbox pod starts, the supervisor process inside it must
authenticate to the gateway to establish its relay session.

1. The pod has a projected Kubernetes ServiceAccount token
   (audience: `openshell-gateway`, TTL: 1 hour)
2. The supervisor calls `IssueSandboxToken` with this SA token
3. The gateway validates the token via `TokenReview`, looks up
   the pod, verifies the `openshell.io/sandbox-id` annotation
   and the Sandbox CRD ownerReference
4. The gateway mints a short-lived JWT for the sandbox
5. The supervisor uses this JWT for all subsequent calls

This flow is fully automatic - no user interaction required.

## Authentication chain

The gateway evaluates authenticators in order:

1. **K8s ServiceAccount** - scoped to `IssueSandboxToken` only
2. **Sandbox JWT** - gateway-minted tokens for sandbox relay
3. **OIDC** - user tokens from auth-bridge or external provider
4. **mTLS** - client certificate identity (when TLS is enabled)

The first authenticator that matches handles the request.

## Security considerations

- Tokens are RSA-signed (RS256) JWTs with configurable TTL (default 8h)
- The auth-bridge generates a new signing keypair on every restart,
  invalidating all existing tokens
- The `adminGroup` field maps an OpenShift group to the
  `openshell-admin` role for elevated access
- Sandbox tokens are scoped to a single sandbox ID and cannot
  be used to access other sandboxes

## See also

- [OpenShift SSO guide](../guides/openshift-sso.md)
- [OpenShellGateway CRD](../reference/openshellgateway.md) - `spec.auth` fields
- [Gateway concept](gateway.md) - architecture overview
