# OGO — OpenShift OpenShell Gateway Operator

An OpenShift-first Kubernetes operator that deploys and manages
[NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell) Gateway instances.

OpenShell provides safe, policy-enforced sandboxed environments for autonomous
AI agents. OGO automates the deployment lifecycle on OpenShift — PKI generation,
SCC bindings, Route creation, RBAC, and status reporting — so you can run a
persistent gateway that CI jobs and developers connect to remotely.

## Prerequisites

- OpenShift 4.18+ (or CRC with OpenShift preset for local development)
- Podman 4.0+
- `oc` CLI
- `operator-sdk` CLI
- PostgreSQL instance (the gateway requires an external database)
- `agents.x-k8s.io` Sandbox CRD installed on the cluster

## Quick Start

### Build and push the operator image

```sh
make image-build image-push IMG=quay.io/aknochow/ogo:v0.1.0
```

### Install the CRDs

```sh
make install
```

### Deploy the operator

```sh
make deploy IMG=quay.io/aknochow/ogo:v0.1.0
```

### Create a gateway instance

```sh
oc apply -f config/samples/ogo_v1alpha1_openshellgateway.yaml
```

### Check status

```sh
oc get openshellgateways
```

### Connect remotely

```sh
# Extract client certificates
oc get secret openshell-client-tls -n openshell -o jsonpath='{.data.tls\.crt}' | base64 -d > client.crt
oc get secret openshell-client-tls -n openshell -o jsonpath='{.data.tls\.key}' | base64 -d > client.key
oc get secret openshell-client-tls -n openshell -o jsonpath='{.data.ca\.crt}' | base64 -d > ca.crt

# Register the remote gateway
openshell gateway add https://openshell.apps.<cluster-domain> \
  --name my-cluster --tls-cert client.crt --tls-key client.key --tls-ca ca.crt

# Create a sandbox on the remote cluster
openshell sandbox create --gateway my-cluster -- claude
```

## Local Development

This project uses **Podman Desktop** with the built-in **CRC extension** for
local OpenShift development. Install Podman Desktop, enable the CRC extension
(OpenShift preset), and you have a full OpenShift cluster for testing.

### Run the operator locally (against your current cluster)

```sh
make run
```

### Run tests

```sh
make test
```

## CRDs

| CRD | Scope | Description |
|-----|-------|-------------|
| `OpenShellGateway` | Cluster | Singleton — deploys and manages the OpenShell Gateway |
| `OpenShellProvider` | Namespaced | Credential bundles for AI providers (Anthropic, GitHub, etc.) |
| `OpenShellPolicy` | Namespaced | Sandbox policy templates (network, filesystem, process) |

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0.
