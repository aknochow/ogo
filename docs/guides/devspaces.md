---
type: Guide
title: DevSpaces Integration
description: Create OpenShell sandboxes from Red Hat OpenShift DevSpaces workspaces using headless token exchange.
tags: [devspaces, authentication, headless]
---

# DevSpaces integration

Create OpenShell sandboxes from a DevSpaces workspace without a browser.
Works on the same cluster or across clusters.

## How it works

```
DevSpaces workspace
  │
  ├─ oc whoami -t              → OpenShift token (already authenticated)
  │
  ├─ POST /token/exchange      → auth-bridge exchanges for OpenShell JWT
  │   Authorization: Bearer <ocp-token>
  │
  └─ openshell sandbox create  → authenticated sandbox creation
```

The auth-bridge's `/token/exchange` endpoint accepts an OpenShift bearer
token and returns an OpenShell JWT. No browser redirect needed — the user
is already authenticated to OpenShift in DevSpaces.

## Prerequisites

- OGO deployed with OpenShift SSO enabled ([Quickstart](quickstart.md))
- User in the `openshell-users` group ([OpenShift SSO](openshift-sso.md))
- `openshell` CLI installed in the DevSpaces workspace
- `jq` available in the workspace

## Install the OpenShell CLI (if not in your workspace image)

If your DevSpaces workspace image does not include the `openshell` CLI,
install it manually:

```bash
curl -LsSf https://github.com/NVIDIA/OpenShell/releases/latest/download/openshell-x86_64-unknown-linux-musl.tar.gz \
  | tar xz -C ~/.local/bin/
openshell --version
```

To bake it into a custom workspace image, add to your Containerfile:

```dockerfile
RUN curl -LsSf https://github.com/NVIDIA/OpenShell/releases/latest/download/openshell-x86_64-unknown-linux-musl.tar.gz \
  | tar xz -C /usr/local/bin/
```

## Get your OpenShift user token

The token exchange requires your OpenShift **user** token, not a
ServiceAccount token. From inside a DevSpaces workspace, `oc login --web`
redirects to localhost which doesn't work. Instead, get your token from
the OpenShift console:

1. Open the OpenShift console for the gateway cluster
2. Click your username (top right) → **Copy login command** → **Display Token**
3. Copy the `oc login --token=sha256~... --server=...` command
4. Run it in the DevSpaces terminal

For same-cluster, this replaces the default DevSpaces SA identity with
your user identity. For cross-cluster, use a separate kubeconfig.

## Same-cluster setup

When DevSpaces and OGO are on the same OpenShift cluster.

### One-time setup

```bash
# Login as your user (get token from OpenShift console → Copy login command)
oc login --token=sha256~YOUR_TOKEN --server=https://api.YOUR-CLUSTER.example.com:6443

# Verify you're logged in as your user (not a ServiceAccount)
oc whoami
# Should show your username, not system:serviceaccount:...

# Trust the system CA bundle
export SSL_CERT_FILE=/etc/pki/tls/certs/ca-bundle.crt

# Exchange your OpenShift token for an OpenShell JWT
RESPONSE=$(curl -s -X POST \
  -H "Authorization: Bearer $(oc whoami -t)" \
  https://openshell-auth.apps.YOUR-CLUSTER.example.com/token/exchange)

# Verify the exchange succeeded
echo $RESPONSE | jq .

# Write the gateway config
GATEWAY_HOST="openshell.apps.YOUR-CLUSTER.example.com"
AUTH_HOST="openshell-auth.apps.YOUR-CLUSTER.example.com"

mkdir -p ~/.config/openshell/gateways/my-cluster
cat > ~/.config/openshell/gateways/my-cluster/metadata.json <<EOF
{
  "name": "my-cluster",
  "gateway_endpoint": "https://${GATEWAY_HOST}",
  "is_remote": true,
  "gateway_port": 0,
  "auth_mode": "oidc",
  "oidc_issuer": "https://${AUTH_HOST}",
  "oidc_client_id": "openshell-cli"
}
EOF
cat > ~/.config/openshell/gateways/my-cluster/oidc_token.json <<EOF
{
  "access_token": "$(echo $RESPONSE | jq -r .access_token)",
  "expires_at": $(echo $RESPONSE | jq -r .expires_at),
  "issuer": "https://${AUTH_HOST}",
  "client_id": "openshell-cli"
}
EOF
echo my-cluster > ~/.config/openshell/active_gateway
```

### Create a sandbox

```bash
openshell sandbox create
```

## Cross-cluster setup

When DevSpaces and OGO are on different OpenShift clusters. The gateway
cluster's Routes must be network-reachable from the DevSpaces pod.

### Prerequisites

- Network connectivity from DevSpaces to the gateway cluster's Routes
- An OpenShift account on the gateway cluster with `openshell-users` group
  membership

### Setup

```bash
export SSL_CERT_FILE=/etc/pki/tls/certs/ca-bundle.crt

# Login to the gateway cluster with a separate kubeconfig
# Get the token from the gateway cluster's OpenShift console → Copy login command
# Note: --insecure-skip-tls-verify is needed when the remote cluster's API CA
# is not in the DevSpaces pod's trust store. For production, add the CA instead:
#   cp remote-ca.crt /etc/pki/ca-trust/source/anchors/ && update-ca-trust
KUBECONFIG=~/.kube/remote-cluster oc login \
  --token=sha256~YOUR_TOKEN \
  --server=https://api.REMOTE-CLUSTER.example.com:6443 \
  --insecure-skip-tls-verify

# Exchange the remote cluster's token
REMOTE_TOKEN=$(KUBECONFIG=~/.kube/remote-cluster oc whoami -t)
RESPONSE=$(curl -s -X POST \
  -H "Authorization: Bearer $REMOTE_TOKEN" \
  https://openshell-auth.apps.REMOTE-CLUSTER.example.com/token/exchange)

# Write the gateway config (same as above, with the remote cluster's hostnames)
GATEWAY_HOST="openshell.apps.REMOTE-CLUSTER.example.com"
AUTH_HOST="openshell-auth.apps.REMOTE-CLUSTER.example.com"

mkdir -p ~/.config/openshell/gateways/remote
cat > ~/.config/openshell/gateways/remote/metadata.json <<EOF
{
  "name": "remote",
  "gateway_endpoint": "https://${GATEWAY_HOST}",
  "is_remote": true,
  "gateway_port": 0,
  "auth_mode": "oidc",
  "oidc_issuer": "https://${AUTH_HOST}",
  "oidc_client_id": "openshell-cli"
}
EOF
cat > ~/.config/openshell/gateways/remote/oidc_token.json <<EOF
{
  "access_token": "$(echo $RESPONSE | jq -r .access_token)",
  "expires_at": $(echo $RESPONSE | jq -r .expires_at),
  "issuer": "https://${AUTH_HOST}",
  "client_id": "openshell-cli"
}
EOF

# Create a sandbox on the remote cluster
openshell sandbox create --gateway remote
```

## Token refresh

The OpenShell JWT expires after the configured `tokenTTL` (default 8 hours).
To refresh, re-run the token exchange:

```bash
RESPONSE=$(curl -s -X POST \
  -H "Authorization: Bearer $(oc whoami -t)" \
  https://openshell-auth.apps.YOUR-CLUSTER.example.com/token/exchange)

cat > ~/.config/openshell/gateways/my-cluster/oidc_token.json <<EOF
{
  "access_token": "$(echo $RESPONSE | jq -r .access_token)",
  "expires_at": $(echo $RESPONSE | jq -r .expires_at),
  "issuer": "https://openshell-auth.apps.YOUR-CLUSTER.example.com",
  "client_id": "openshell-cli"
}
EOF
```

## Troubleshooting

### "missing Bearer token"

The `Authorization` header is missing or not using the `Bearer` scheme.
Check that `oc whoami -t` returns a token:

```bash
oc whoami -t
# If empty, re-login with a token from the OpenShift console:
oc login --token=sha256~YOUR_TOKEN --server=https://api.YOUR-CLUSTER.example.com:6443
```

### "user system:serviceaccount:... is not a member of group"

You're using a ServiceAccount token instead of your user token.
DevSpaces pods default to the workspace SA. Get your user token from
the OpenShift console (**Copy login command**) and re-login:

```bash
oc login --token=sha256~YOUR_TOKEN --server=https://api.YOUR-CLUSTER.example.com:6443
oc whoami
# Should show your username, not system:serviceaccount:...
```

### "user is not a member of group"

Your OpenShift user is not in the `openshell-users` group. Ask your
cluster admin:

```bash
oc adm groups add-users openshell-users your-username
```

### "invalid OpenShift token"

The token is expired or from a different cluster than the auth-bridge.
For cross-cluster, make sure you use a token from the **gateway cluster**,
not the DevSpaces cluster.

### TLS certificate errors

Set `SSL_CERT_FILE` to use the system CA bundle instead of the CLI's
built-in bundle:

```bash
export SSL_CERT_FILE=/etc/pki/tls/certs/ca-bundle.crt
```

Add this to your `~/.bashrc` in the DevSpaces workspace for persistence.

### curl hangs on token exchange

The auth-bridge Route is not reachable from the DevSpaces pod. For
cross-cluster, verify network connectivity:

```bash
curl --connect-timeout 5 https://openshell-auth.apps.REMOTE-CLUSTER.example.com/healthz
```

## See also

- [Quickstart](quickstart.md) - deploy OGO
- [OpenShift SSO](openshift-sso.md) - user groups and token management
- [OpenShellGateway CRD](../reference/openshellgateway.md) - CR reference
