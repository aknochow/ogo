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
      enabled: true              # default on OpenShift, false on vanilla K8s
      userGroup: openshell-users  # required — only members can authenticate
      adminGroup: openshell-admins # OpenShift group for admin role
      tokenTTL: "8h"             # JWT lifetime
```

## User group (required)

The `userGroup` field specifies the OpenShift group required for SSO access.
Only members of this group can authenticate via the browser login flow.
Users not in the group are rejected with a 403 error at login.

Set up the group and add users:

```bash
oc adm groups new openshell-users
oc adm groups add-users openshell-users alice bob
```

This check does not affect mTLS authentication (used by CI/automation)
or the internal sandbox bootstrap (K8s ServiceAccount tokens).

## Troubleshooting

### "access denied: you are not a member of the required OpenShift group"

Your OpenShift user is not in the group specified by `spec.auth.openshift.userGroup`.
Ask your cluster admin to add you:

```bash
oc adm groups add-users <group-name> <your-username>
```

Check your current groups:

```bash
oc get users <your-username> -o jsonpath='{.groups}'
```

### Emergency token revocation

To immediately invalidate all active tokens (e.g., a user who should not
have access obtained a valid token):

```bash
oc delete secret openshell-auth-bridge-keys -n ogo
oc delete pod -n ogo -l app.kubernetes.io/name=openshell
```

The operator generates new signing keys and the gateway restarts with
fresh JWKS. All existing tokens become invalid within ~30 seconds.
Every user must re-login, and users removed from the `userGroup` will
be blocked.

For less disruptive revocation, reduce `spec.auth.openshift.tokenTTL`
to a shorter duration (e.g., `"30m"`). Revoked users lose access when
their token expires naturally.

### "authentication failed" after gateway restart

The OAuthClient secret may be out of sync. The admin should delete both
and let the operator recreate them:

```bash
oc delete secret openshell-oauth-client -n ogo
oc delete oauthclient openshell
# Wait 60s for the operator to reconcile, then restart the gateway pod
oc delete pod -n ogo -l app.kubernetes.io/name=openshell
```

## Token lifetime

The `tokenTTL` field controls how long the JWT is valid. After expiry, the
CLI prompts for re-authentication. The default is 8 hours.

The auth-bridge Ed25519 signing keypair is persisted in a Kubernetes Secret
(`openshell-auth-bridge-keys`). Tokens survive pod restarts. The operator
creates the keypair once; subsequent pod restarts load the same keys.

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
