---
type: Guide
title: OpenShift SSO
description: Authenticate to the OpenShell gateway with your OpenShift credentials using the auth-bridge.
tags: [authentication, openshift]
---

# OpenShift SSO

OGO includes an auth-bridge sidecar that translates OpenShift OAuth tokens
into standard OIDC JWTs. This lets users log in to the OpenShell gateway
with their OpenShift credentials — no external identity provider needed.

## How it works

```
Browser ──▶ OpenShift OAuth ──▶ auth-bridge ──▶ JWT
                                    │
CLI ◀── stores JWT ◀────────────────┘
```

1. User runs `openshell gateway login`
2. Browser opens the auth-bridge's `/authorize` endpoint
3. Auth-bridge redirects to OpenShift OAuth login
4. User authenticates with OpenShift credentials
5. OAuth redirects back to auth-bridge with an authorization code
6. Auth-bridge exchanges the code for an OpenShift token
7. Auth-bridge calls the OpenShift UserInfo API to get identity
8. Auth-bridge mints a short-lived JWT with the user's identity
9. CLI receives and stores the JWT

## Configuration

Auth-bridge is enabled by default on OpenShift. Control it via the CR:

```yaml
spec:
  auth:
    openshift:
      enabled: true         # default on OpenShift, false on vanilla K8s
      adminGroup: my-admins  # OpenShift group for admin role
      tokenTTL: "8h"        # JWT lifetime
```

## Token lifetime

The `tokenTTL` field controls how long the JWT is valid. After expiry, the
CLI prompts for re-authentication. The default is 8 hours.

Tokens are invalidated immediately if the gateway pod restarts — the
auth-bridge generates a new Ed25519 signing keypair on startup, so tokens
signed by the previous keypair are rejected.

## OAuthClient

The operator creates an OpenShift `OAuthClient` CR named `openshell` with
the auth-bridge route as the redirect URI. The client secret is stored in
the `openshell-oauth-client` Secret.

If you delete and recreate the `OAuthClient`, also delete the
`openshell-oauth-client` Secret to avoid a client secret mismatch.

## Admin role

Users in the OpenShift group specified by `spec.auth.openshift.adminGroup`
are granted the `openshell-admin` role in the gateway. This role allows
managing other users' sandboxes and viewing all sandbox logs.

## See also

- [OpenShellGateway CRD](../reference/openshellgateway.md)
- [Quickstart](quickstart.md)
