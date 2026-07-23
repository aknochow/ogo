---
type: Guide
title: Quickstart
description: Deploy OGO on OpenShift and create your first sandbox in under 5 minutes.
tags: [getting-started]
---

# Quickstart

Deploy OGO on an OpenShift cluster. The operator handles everything:
Envoy Gateway, PostgreSQL, TLS, RBAC groups, and Routes.

## Prerequisites

- OpenShift 4.16+ cluster with `oc` CLI configured
- `openshell` CLI installed (`brew install nvidia/tap/openshell`)

## 1. Deploy the operator

```bash
make deploy IMG=quay.io/aknochow/ogo:v0.2.0
oc wait --for=condition=Available deployment/ogo-controller-manager -n ogo --timeout=120s
```

## 2. Create the gateway

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
    embedded: true
  tls:
    enabled: true
    certManager:
      enabled: true
      issuerName: letsencrypt
      issuerKind: ClusterIssuer
  route:
    hostname: openshell.apps.your-cluster.example.com
    gatewayAPI:
      enabled: true
  auth:
    openshift:
      userGroup: openshell-users
      adminGroup: openshell-admins
      autoCreateGroups: true
  logLevel: info
```

```bash
oc apply -f ogo-gateway.yaml
```

The operator will automatically:

- Install Envoy Gateway and configure SCCs
- Deploy an embedded dev PostgreSQL instance
- Create the `openshell-users` and `openshell-admins` groups
- Generate TLS certificates (self-signed, or via cert-manager if configured)
- Create OpenShift Routes for the gateway and auth-bridge
- Deploy the gateway with an auth-bridge sidecar for SSO

Watch progress:

```bash
oc get openshellgateway -w
# Wait for Phase: Running
```

## 3. Add yourself to the users group

```bash
oc adm groups add-users openshell-users $(oc whoami)
```

## 4. Connect

```bash
openshell gateway add https://openshell.apps.your-cluster.example.com \
  --name my-cluster \
  --oidc-issuer https://openshell-auth.apps.your-cluster.example.com

openshell sandbox create --gateway my-cluster
```

## Production considerations

The quickstart uses an embedded single-pod PostgreSQL that is **not suitable
for production**. For production deployments:

1. **Use an external database** — deploy CloudNativePG, Amazon RDS, or any
   PostgreSQL 14+ instance, then reference it with `spec.database.secretName`
   instead of `embedded: true`:

   ```yaml
   database:
     secretName: my-pg-secret  # Secret with key "uri"
   ```

2. **Use cert-manager for TLS** — install cert-manager with a Let's Encrypt
   ClusterIssuer for trusted certificates. The operator integrates with
   cert-manager when `spec.tls.certManager.enabled: true`.

3. **Pre-create groups** — for auditing, create groups and add members before
   deploying the gateway. Set `autoCreateGroups: false` if your groups already
   exist.

## Teardown

```bash
# Delete the gateway (triggers finalizer cleanup)
oc delete openshellgateway openshell

# Undeploy the operator, CRDs, and RBAC
make undeploy
```

## Next steps

- [Dev Spaces](devspaces.md) - create sandboxes from Dev Spaces workspaces
- [Envoy Gateway](envoy-gateway.md) - gRPC ingress architecture details
- [OpenShift SSO](openshift-sso.md) - authentication and user groups
- [Provider](../concepts/provider.md) - inject API keys into sandboxes
- [Policy](../concepts/policy.md) - control sandbox network and filesystem access
