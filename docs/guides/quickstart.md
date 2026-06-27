---
type: Guide
title: Quickstart
description: Deploy OGO on OpenShift and create your first sandbox in 10 minutes.
tags: [getting-started]
---

# Quickstart

Deploy OGO on an OpenShift cluster with PostgreSQL, auth-bridge, and the
OpenShell gateway.

## Prerequisites

- OpenShift 4.16+ cluster with `oc` CLI configured
- cert-manager operator installed (for TLS certificates)
- `openshell` CLI installed (`brew install nvidia/tap/openshell`)

## 1. Install CRDs

```bash
KUBECONFIG=~/.kube/your-cluster make install
```

## 2. Deploy the operator

```bash
TAG="0.1.0-$(date +%Y%m%d%H%M%S)"
podman build -f Containerfile -t quay.io/yourorg/ogo:$TAG .
podman push quay.io/yourorg/ogo:$TAG
KUBECONFIG=~/.kube/your-cluster make deploy IMG=quay.io/yourorg/ogo:$TAG
```

## 3. Set up PostgreSQL

```bash
oc create deployment ogo-pg -n ogo --image=docker.io/library/postgres:16
oc set env deployment/ogo-pg -n ogo \
  POSTGRES_USER=openshell POSTGRES_PASSWORD=openshell POSTGRES_DB=openshell
oc expose deployment/ogo-pg -n ogo --port=5432
oc create secret generic ogo-pg -n ogo \
  --from-literal=uri='postgresql://openshell:openshell@ogo-pg.ogo.svc:5432/openshell'
```

On OpenShift, grant the `anyuid` SCC:

```bash
oc adm policy add-scc-to-user anyuid -z default -n ogo
```

## 4. Create the Gateway

```bash
oc apply -f config/samples/ogo_v1alpha1_openshellgateway.yaml
```

Edit the sample to set your `route.hostname` before applying.

## 5. Connect

```bash
openshell gateway add https://openshell.apps.your-cluster.example.com sno
openshell gateway login sno
openshell sandbox create --gateway sno
```

## Next steps

- [Envoy Gateway](envoy-gateway.md) — eliminate `--gateway-insecure` with proper TLS
- [OpenShift SSO](openshift-sso.md) — understand the auth-bridge
- [Provider](../concepts/provider.md) — inject API keys into sandboxes
