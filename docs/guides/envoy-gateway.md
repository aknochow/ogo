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

- cert-manager with a `ClusterIssuer` named `letsencrypt`
- DNS resolving the route hostname to the cluster

Envoy Gateway itself is **not** a prerequisite — OGO installs and configures
it automatically (CRDs, controller, a `GatewayClass` named `eg`, and the
SCCs it needs) when a CR sets `route.gatewayAPI.enabled: true` and no
`GatewayClass` already exists. See [Quickstart](quickstart.md#with-envoy-gateway).

### Bring your own Envoy Gateway instead

If you already run Envoy Gateway on this cluster for other workloads,
install it yourself and create its `GatewayClass` before creating the OGO
CR — OGO detects an existing `GatewayClass` and won't install its own. See
[Quickstart's Advanced section](quickstart.md#advanced-bring-your-own-envoy-gateway)
for the full manual install (including the `--skip-crds` scoping and
`EnvoyProxy` ClusterIP configuration needed on OpenShift).

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
| `GatewayClass/eg` | (cluster-scoped) | Only if none exists yet (auto-install) |
| `EnvoyProxy/openshift-clusterip` | envoy-gateway-system | Only if auto-installed - configures the proxy Service as `ClusterIP` |

## Troubleshooting

### Gateway shows `Programmed: False`

With the default auto-install (`ClusterIP` Envoy Service), this shouldn't
happen — there's no cloud LB address to wait for, and traffic flows through
the OpenShift Route regardless. If you see it anyway, check the Envoy
Gateway controller logs (`oc logs -n envoy-gateway-system
deployment/envoy-gateway`) for the actual reason the proxy Service/
Deployment failed to provision — don't assume it's benign.

If you're on the [manual "bring your own Envoy Gateway"](#bring-your-own-envoy-gateway-instead)
path *without* the `EnvoyProxy` ClusterIP configuration, this is expected on
bare-metal/SNO clusters: the Envoy Service defaults to `LoadBalancer` type
with no cloud LB provider to assign an address. Traffic still flows through
the OpenShift Route in that case. On managed clusters (ROSA/OSD) with a
LoadBalancer quota, the same default can instead fail outright rather than
just sitting unprogrammed — see the `EnvoyProxy` ClusterIP config in the
Advanced quickstart section to avoid depending on a cloud LB entirely.

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
