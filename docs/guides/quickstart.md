---
type: Guide
title: Quickstart
description: Deploy OGO on OpenShift and create your first sandbox in 10 minutes.
tags: [getting-started]
---

# Quickstart

Deploy OGO on an OpenShift cluster with PostgreSQL and the OpenShell gateway.

## Prerequisites

- OpenShift 4.16+ cluster with `oc` CLI configured
- `openshell` CLI installed (`brew install nvidia/tap/openshell`)

## Choose a deployment path

| Path | Auth | TLS | Prerequisites |
|------|------|-----|---------------|
| **[With Envoy Gateway](#with-envoy-gateway)** | OpenShift SSO (browser login) | Let's Encrypt via cert-manager | cert-manager, Envoy Gateway, Helm |
| **[Without Envoy Gateway](#without-envoy-gateway)** | mTLS (client certificates) | Self-signed (operator-managed) | None |

Envoy Gateway is required for OpenShift SSO because the OpenShell gateway
needs the OIDC issuer on the same hostname as the gateway endpoint. Without
Envoy, use mTLS client certificates or `port-forward` for access.

---

## With Envoy Gateway

This path gives you browser-based SSO login and Let's Encrypt TLS.

### 1. Install cert-manager

If not already installed:

```bash
oc apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.17.2/cert-manager.yaml
oc wait --for=condition=Available deployment/cert-manager -n cert-manager --timeout=120s
```

Create a ClusterIssuer for Let's Encrypt (requires DNS challenge for wildcard
certs, or HTTP challenge for single-host certs):

```bash
cat <<EOF | oc apply -f -
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: your-email@example.com
    privateKeySecretRef:
      name: letsencrypt-key
    solvers:
    - http01:
        ingress: {}
EOF
```

### 2. Install Envoy Gateway

The certgen pre-install hook runs as uid 65534, outside OpenShift's allowed
UID range. Pre-create the namespace and service account, grant SCC, then
install with the UID overridden:

```bash
# 1. Pre-create namespace and certgen service account
oc create namespace envoy-gateway-system
oc create sa eg-gateway-helm-certgen -n envoy-gateway-system
oc adm policy add-scc-to-user anyuid -z eg-gateway-helm-certgen -n envoy-gateway-system

# 2. Install — override certgen UID so OpenShift assigns from the namespace range
helm install eg oci://docker.io/envoyproxy/gateway-helm \
  --version v1.3.2 -n envoy-gateway-system --skip-crds \
  --set-json 'certgen.job.securityContext.runAsUser=null' \
  --set-json 'certgen.job.securityContext.runAsGroup=null' \
  --set-json 'certgen.job.securityContext.seccompProfile=null'

# 3. Grant privileged SCC to the main controller service account
oc adm policy add-scc-to-user privileged -z envoy-gateway -n envoy-gateway-system
```

Create the GatewayClass:

```bash
cat <<EOF | oc apply -f -
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: eg
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
EOF
```

Verify Envoy Gateway is ready:

```bash
oc get gatewayclasses
# Should show: eg   ACCEPTED   True
```

### 3. Deploy the operator

```bash
make deploy IMG=quay.io/aknochow/ogo:main
oc wait --for=condition=Available deployment/ogo-controller-manager -n ogo --timeout=120s
```

### 4. Set up PostgreSQL

```bash
oc create deployment ogo-pg -n ogo --image=docker.io/library/postgres:16
oc set env deployment/ogo-pg -n ogo \
  POSTGRES_USER=openshell POSTGRES_PASSWORD=openshell POSTGRES_DB=openshell
oc expose deployment/ogo-pg -n ogo --port=5432
oc adm policy add-scc-to-user anyuid -z default -n ogo
oc create secret generic ogo-pg -n ogo \
  --from-literal=uri='postgresql://openshell:openshell@ogo-pg.ogo.svc:5432/openshell'
```

### 5. Create user groups

```bash
oc adm groups new openshell-users
oc adm groups add-users openshell-users your-username
oc adm groups new openshell-admins
oc adm groups add-users openshell-admins your-username
```

### 6. Create the Gateway

Replace `your-cluster.example.com` with your cluster's apps domain:

```yaml
# ogo-gateway.yaml
apiVersion: gateway.ogo.aknochow.io/v1alpha1
kind: OpenShellGateway
metadata:
  name: openshell
spec:
  namespace: ogo
  replicas: 1
  database:
    secretName: ogo-pg
  sandbox:
    defaultImage: ghcr.io/nvidia/openshell-community/sandboxes/base:latest
    workspaceStorageSize: "2Gi"
    appArmorProfile: "Unconfined"
  tls:
    enabled: false
    certManager:
      issuerName: letsencrypt
      issuerKind: ClusterIssuer
  route:
    hostname: openshell.apps.your-cluster.example.com
    gatewayAPI:
      gatewayClassName: eg
  auth:
    openshift:
      userGroup: openshell-users
      adminGroup: openshell-admins
      tokenTTL: "8h"
  logLevel: info
  networkPolicy:
    enabled: true
```

```bash
oc apply -f ogo-gateway.yaml
```

Wait for the Envoy proxy to start. When Gateway API detects Envoy, it
creates a dynamic ServiceAccount for the proxy. Grant it the privileged SCC:

```bash
# Find the Envoy proxy SA (created after the Gateway resource is reconciled)
oc get sa -n envoy-gateway-system | grep envoy-ogo
# Grant it privileged SCC
oc adm policy add-scc-to-user privileged -z <envoy-sa-name> -n envoy-gateway-system
```

Verify the gateway is running:

```bash
oc get openshellgateway
# Should show: openshell   Running   https://openshell.apps.your-cluster.example.com:443
```

### 7. Connect

```bash
openshell gateway add https://openshell.apps.your-cluster.example.com \
  --name my-cluster \
  --oidc-issuer https://openshell-auth.apps.your-cluster.example.com

openshell sandbox create --gateway my-cluster
```

---

## Without Envoy Gateway

This path uses self-signed TLS with a passthrough OpenShift Route. No
external prerequisites. Authentication uses mTLS client certificates.

### 1. Deploy the operator

```bash
make deploy IMG=quay.io/aknochow/ogo:main
oc wait --for=condition=Available deployment/ogo-controller-manager -n ogo --timeout=120s
```

### 2. Set up PostgreSQL

```bash
oc create deployment ogo-pg -n ogo --image=docker.io/library/postgres:16
oc set env deployment/ogo-pg -n ogo \
  POSTGRES_USER=openshell POSTGRES_PASSWORD=openshell POSTGRES_DB=openshell
oc expose deployment/ogo-pg -n ogo --port=5432
oc adm policy add-scc-to-user anyuid -z default -n ogo
oc create secret generic ogo-pg -n ogo \
  --from-literal=uri='postgresql://openshell:openshell@ogo-pg.ogo.svc:5432/openshell'
```

### 3. Create the Gateway

```yaml
# ogo-gateway.yaml
apiVersion: gateway.ogo.aknochow.io/v1alpha1
kind: OpenShellGateway
metadata:
  name: openshell
spec:
  namespace: ogo
  replicas: 1
  database:
    secretName: ogo-pg
  sandbox:
    defaultImage: ghcr.io/nvidia/openshell-community/sandboxes/base:latest
    workspaceStorageSize: "2Gi"
  tls:
    enabled: true
  route:
    hostname: openshell.apps.your-cluster.example.com
    gatewayAPI:
      enabled: false
  auth:
    openshift:
      enabled: false
      userGroup: openshell-users
  logLevel: info
  networkPolicy:
    enabled: false
```

```bash
oc apply -f ogo-gateway.yaml
```

Verify:

```bash
oc get openshellgateway
oc get route -n ogo
# Should show a passthrough Route
```

### 4. Connect

```bash
openshell gateway add https://openshell.apps.your-cluster.example.com \
  --name my-cluster --gateway-insecure

openshell sandbox create --gateway my-cluster
```

The `--gateway-insecure` flag is needed because the self-signed TLS
certificate is not trusted by the CLI. For production, use the Envoy
Gateway path with Let's Encrypt certificates.

---

## Teardown

To completely remove OGO from the cluster:

```bash
# 1. Delete the gateway CR (triggers finalizer cleanup of all resources)
oc delete openshellgateway openshell

# 2. Delete the database
oc delete deployment ogo-pg -n ogo
oc delete svc ogo-pg -n ogo
oc delete secret ogo-pg -n ogo

# 3. Undeploy the operator, CRDs, and RBAC
make undeploy

# 4. Clean up cluster-scoped resources
oc delete oauthclient openshell 2>/dev/null
oc delete groups openshell-users openshell-admins 2>/dev/null

# 5. Delete the namespace
oc delete ns ogo

# 6. (If using Envoy Gateway) Remove Envoy Gateway
helm uninstall eg -n envoy-gateway-system
oc delete ns envoy-gateway-system
oc delete gatewayclass eg
```

## Next steps

- [DevSpaces](devspaces.md) - create sandboxes from DevSpaces workspaces
- [Envoy Gateway](envoy-gateway.md) - gRPC ingress architecture details
- [OpenShift SSO](openshift-sso.md) - authentication and user groups
- [Provider](../concepts/provider.md) - inject API keys into sandboxes
- [Policy](../concepts/policy.md) - control sandbox network and filesystem access
