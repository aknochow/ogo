---
type: Guide
title: Envoy Gateway
description: Set up gRPC ingress with Envoy Gateway for proper HTTP/2 + TLS termination with Let's Encrypt certificates.
tags: [networking, tls, envoy]
---

# Envoy Gateway ingress

OpenShift Routes with edge TLS termination don't support HTTP/2, which gRPC
requires. Envoy Gateway provides proper gRPC ingress with Let's Encrypt TLS
termination.

## How it works

```
Client ──HTTPS──▶ OpenShift Router ──passthrough──▶ Envoy Proxy ──h2c──▶ Gateway
                                                    (TLS termination)
```

1. The operator creates a **Gateway** and **GRPCRoute** (Kubernetes Gateway API)
2. Envoy Gateway provisions an Envoy proxy Deployment + Service
3. The operator creates a **passthrough Route** pointing to the Envoy Service
4. cert-manager issues a Let's Encrypt certificate for the Gateway hostname
5. Envoy terminates TLS and forwards plaintext HTTP/2 to the gateway pod

## Prerequisites

- [Envoy Gateway](https://gateway.envoyproxy.io/) installed on the cluster
- A `GatewayClass` named `eg` (Envoy Gateway's default)
- cert-manager with a `ClusterIssuer` named `letsencrypt`
- DNS resolving the route hostname to the cluster

### Install Envoy Gateway on OpenShift

```bash
helm install eg oci://docker.io/envoyproxy/gateway-helm \
  --version v1.3.2 -n envoy-gateway-system --create-namespace --skip-crds
```

Grant privileged SCC to Envoy ServiceAccounts:

```bash
oc adm policy add-scc-to-user privileged -z envoy-gateway -n envoy-gateway-system
```

## Configuration

The operator auto-detects Gateway API CRDs. When present, it creates Gateway
API resources instead of a direct OpenShift Route.

Set the hostname in the CR:

```yaml
spec:
  tls:
    enabled: false          # gateway pod doesn't need TLS - Envoy handles it
    certManager:
      enabled: true         # cert-manager issues the Let's Encrypt cert
      issuerName: letsencrypt
      issuerKind: ClusterIssuer
  route:
    hostname: openshell.apps.your-cluster.example.com
```

The operator will create:

| Resource | Namespace | Purpose |
|----------|-----------|---------|
| `Gateway/openshell` | ogo | HTTPS listener on port 443 |
| `GRPCRoute/openshell` | ogo | Routes to the gateway Service |
| `Certificate/openshell-gateway-tls` | ogo | Let's Encrypt cert |
| `Route/openshell-gw` | envoy-gateway-system | Passthrough to Envoy proxy |

## Troubleshooting

### Gateway shows `Programmed: False`

This is normal on bare-metal / SNO clusters. The Envoy Service is `LoadBalancer`
type but there's no cloud LB provider to assign an IP. Traffic flows through
the OpenShift Route instead.

### `filter_chain_not_found` in Envoy logs

The Envoy proxy receives connections but can't match a filter chain. If
`requested_server_name` is null, it's a health check (harmless). If it has
a hostname, check that the cert SAN matches.

### Empty HAProxy backend (no server entries)

The Route's `targetPort` must be `10443` (the Envoy container port), not
`443` (the Service port). The Envoy convention is container port = 10000 +
listener port. Check with:

```bash
oc rsh -n openshift-ingress deployment/router-default \
  cat /var/lib/haproxy/conf/haproxy.config | grep -A10 'be_tcp:envoy'
```

## See also

- [Gateway concept](../concepts/gateway.md)
- [OpenShellGateway CRD](../reference/openshellgateway.md)
