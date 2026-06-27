# OGO — Project Context

## Documentation — READ FIRST

Before asking the user about architecture, CRDs, configuration, or
deployment — check the docs. They have answers to most questions.

- **[docs/](docs/)** — full documentation (concepts, guides, reference, examples)
- **[docs/index.md](docs/index.md)** — navigation index
- **[README.md](README.md)** — same content as docs/index.md
- **[CLAUDE.md](CLAUDE.md)** — routes here

## What This Is

OGO (OpenShift Gateway Operator) deploys and manages NVIDIA OpenShell
Gateway instances on OpenShift. It automates TLS, RBAC, SCC, Routes,
Envoy Gateway ingress, and provides an auth-bridge for OpenShift SSO login.

## Versioning — CRITICAL

**Semver-calver hybrid.** Version is `0.1.0` with calver timestamp suffix.

- **NEVER bump the semver** unless the user explicitly says to
- Calver timestamp IS the natural bump: `TAG="0.1.0-$(date +%Y%m%d%H%M%S)"`
- Generate per-build, consistent across operator + auth-bridge + bundle + catalog

### Image Ownership — DO NOT CONFUSE

| Image | Owner | Tags |
|-------|-------|------|
| `quay.io/aknochow/ogo` | Us | Our calver |
| `quay.io/aknochow/ogo-auth-bridge` | Us | Our calver or `latest` |
| `ghcr.io/nvidia/openshell/gateway` | NVIDIA | Their tags (`latest`, `v0.0.68`) |
| `ghcr.io/nvidia/openshell/supervisor` | NVIDIA | Their tags |

**NEVER tag NVIDIA images with our calver.**

## Architecture

- **Cluster-scoped singleton** — one OpenShellGateway CR per cluster
- **API group** — `gateway.ogo.aknochow.io`
- **Single namespace** — operator and gateway run in `ogo`
- **Auth-bridge** — sidecar bridging OpenShift OAuth to OIDC JWT
- **Podman-first, OpenShift-first** throughout

## Key Files

- `api/v1alpha1/openshellgateway_types.go` — CRD types
- `internal/controller/openshellgateway_controller.go` — Main reconciler
- `internal/authbridge/` — Auth-bridge server, JWT, OpenShift OAuth
- `internal/gateway/config.go` — gateway.toml rendering
- `internal/pki/pki.go` — TLS and JWT key generation
- `internal/openshift/detect.go` — OpenShift and Gateway API detection

## Build & Test

```sh
make build           # Build binary
make test            # Unit + envtest tests
make manifests       # Regenerate CRDs and RBAC
make generate        # Regenerate deepcopy
```

See `CONTRIBUTING.md` for full build pipeline and deployment guide.
