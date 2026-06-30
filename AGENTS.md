# OGO - Project Context

## Documentation - READ FIRST

Before asking the user about architecture, CRDs, configuration, or
deployment - check the docs. They have answers to most questions.

- **[docs/](docs/)** - full documentation (concepts, guides, reference, examples)
- **[docs/index.md](docs/index.md)** - navigation index
- **[README.md](README.md)** - same content as docs/index.md
- **[CLAUDE.md](CLAUDE.md)** - routes here

## What This Is

OGO (OpenShift Gateway Operator) deploys and manages NVIDIA OpenShell
Gateway instances on OpenShift. It automates TLS, RBAC, SCC, Routes,
Envoy Gateway ingress, and provides an auth-bridge for OpenShift SSO login.

## Versioning - CRITICAL

**Semantic versioning.** Base version is `0.1.0` in the Makefile.

- **NEVER bump the semver** unless the user explicitly says to
- Git tags drive releases: `git tag v0.1.0 && git push origin v0.1.0`
- VERSION in Makefile stays as base — CI overrides from the git tag
- No calver dev tags — `:main` is the rolling dev build

### Tag Strategy

| Event | Image tags |
|-------|------------|
| Push to main | `:main` |
| Release (`v0.1.0`) | `:v0.1.0` + `:latest` |
| Pre-release (`v0.2.0-rc1`) | `:v0.2.0-rc1` |

### Image Ownership - DO NOT CONFUSE

| Image | Owner | Tags |
|-------|-------|------|
| `quay.io/aknochow/ogo` | Us | Our semver |
| `quay.io/aknochow/ogo-auth-bridge` | Us | Our semver |
| `ghcr.io/nvidia/openshell/gateway` | NVIDIA | Their tags (`latest`, `v0.0.73`) |
| `ghcr.io/nvidia/openshell/supervisor` | NVIDIA | Their tags |

**NEVER tag NVIDIA images with our tags.**

## Architecture

- **Cluster-scoped singleton** - one OpenShellGateway CR per cluster
- **API group** - `gateway.ogo.aknochow.io`
- **Single namespace** - operator and gateway run in `ogo`
- **Auth-bridge** - sidecar bridging OpenShift OAuth to OIDC JWT
- **Podman-first, OpenShift-first** throughout

## Key Files

- `api/v1alpha1/openshellgateway_types.go` - CRD types
- `internal/controller/openshellgateway_controller.go` - Main reconciler
- `internal/authbridge/` - Auth-bridge server, JWT, OpenShift OAuth
- `internal/gateway/config.go` - gateway.toml rendering
- `internal/pki/pki.go` - TLS and JWT key generation
- `internal/openshift/detect.go` - OpenShift and Gateway API detection

## Public Repo - CRITICAL

This is a **public repository**. Never include in commits, PR descriptions,
issues, or any public-facing content:

- Credentials, tokens, passwords, or API keys
- Robot account names or service account identifiers
- Internal hostnames, IPs, or infrastructure details
- Registry auth configuration or secret names
- Any information that could aid an attacker

If a workflow requires secrets, reference them generically
(e.g., "configure registry credentials in repo settings") without
naming the specific account or value.

## Build & Test

```sh
make build           # Build binary
make test            # Unit + envtest tests
make manifests       # Regenerate CRDs and RBAC
make generate        # Regenerate deepcopy
```

See the [docs/](docs/) directory for full documentation and deployment guides.
