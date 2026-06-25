# OGO Development Guide

## Build & Test

```sh
make build              # Build binary
make test               # Unit + envtest integration tests
make manifests          # Regenerate CRDs and RBAC from markers
make generate           # Regenerate deepcopy functions
make image-build        # Build container image (podman)
make image-push         # Push to quay.io/aknochow/ogo
make deploy IMG=<img>   # Deploy to cluster via kustomize
make undeploy           # Remove from cluster
make install            # Install CRDs only
make uninstall          # Remove CRDs only
```

## Project Layout

- `api/v1alpha1/` — CRD type definitions with kubebuilder markers
- `internal/controller/` — Reconcilers for all three CRDs
- `internal/pki/` — TLS certificate and JWT key generation
- `internal/gateway/` — gateway.toml TOML rendering
- `internal/openshift/` — OpenShift API detection
- `config/` — Kustomize manifests (CRDs, RBAC, manager, samples)
- `scripts/` — Helper scripts (connect-gateway.sh)

## Key Design Decisions

- **Cluster-scoped singleton** — one OpenShellGateway per cluster
- **API group** — `gateway.ogo.io`
- **Single namespace** — operator and gateway run in `ogo` namespace
- **Podman-first** — all container operations use podman
- **OpenShift-first** — Route, SCC, security context handling
- **No ownerReferences** — cluster-scoped CR can't own namespaced resources; uses labels + finalizer
- **Shared CA** — server and client TLS certs use the same CA
- **cert-manager recommended** — Let's Encrypt for server TLS, self-signed for client mTLS

## Building for amd64 (from Apple Silicon)

Use the podzilla remote podman connection:

```sh
podman --connection podzilla build -f Containerfile -t quay.io/aknochow/ogo:v0.1.0 .
podman --connection podzilla push quay.io/aknochow/ogo:v0.1.0
```

## Testing on rdu

```sh
KUBECONFIG=~/.kube/rdu make deploy IMG=quay.io/aknochow/ogo:v0.1.0
KUBECONFIG=~/.kube/rdu oc get openshellgateways
KUBECONFIG=~/.kube/rdu oc logs -n ogo deployment/ogo-controller-manager
```
