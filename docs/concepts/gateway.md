---
type: Concept
title: Gateway
description: The OpenShell gateway is a gRPC server that manages sandbox lifecycle, authentication, and relay sessions.
tags: [core, architecture]
---

# Gateway

The gateway is the central component of an OGO deployment. It runs as a
Kubernetes Deployment in the `ogo` namespace and serves the OpenShell gRPC
API on port 8080.

## What it does

- **Sandbox lifecycle** - creates, lists, and deletes sandbox pods via the
  Kubernetes Sandbox CRD (`agents.x-k8s.io/v1beta1`)
- **Authentication** - validates OIDC tokens (from the [auth-bridge](../guides/openshift-sso.md)
  or external providers) and Kubernetes ServiceAccount tokens (for supervisor bootstrap)
- **Relay sessions** - proxies SSH connections between the CLI and sandbox
  supervisors over gRPC streams
- **Provider injection** - delivers API credentials to sandbox pods at startup
- **Policy enforcement** - pushes network, filesystem, and process policies to
  sandbox supervisors

## Architecture

```
CLI (openshell) ──gRPC──▶ Envoy Gateway ──▶ Gateway Pod ──▶ Sandbox Pod
                              (TLS)          (plaintext)    (supervisor)
```

On OpenShift with [Envoy Gateway](../guides/envoy-gateway.md):
1. The CLI connects to the external hostname over HTTPS (Let's Encrypt)
2. The OpenShift Router does TLS passthrough to the Envoy proxy
3. Envoy terminates TLS and forwards plaintext HTTP/2 to the gateway Service
4. The gateway pod handles the gRPC request

## Gateway image

The gateway binary is built and published by NVIDIA at
`ghcr.io/nvidia/openshell/gateway`. OGO deploys this image - the operator
does not build or modify the gateway binary.

The `spec.image` and `spec.imageTag` fields on the
[OpenShellGateway](../reference/openshellgateway.md) CRD control which
image version is deployed.

## Configuration

The operator renders a `gateway.toml` configuration file and mounts it into
the gateway pod. The TOML includes:

- Bind addresses and ports
- Database connection (PostgreSQL)
- OIDC issuer configuration (auth-bridge or external)
- JWT signing key paths
- Kubernetes driver settings (namespace, supervisor image, SA token TTL)

The operator manages this file - do not edit the ConfigMap directly.

## See also

- [OpenShellGateway CRD](../reference/openshellgateway.md)
- [Quickstart](../guides/quickstart.md)
