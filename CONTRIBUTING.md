# Contributing to OGO

OGO is in early alpha. Contributions are welcome.

## Development

```bash
make build    # Build the operator binary
make test     # Run unit and integration tests
make lint     # Run golangci-lint
```

See [AGENTS.md](AGENTS.md) for project structure, versioning rules, and
image ownership. See [docs/](docs/) for full documentation.

## Upstream OpenShell version bumps

The `ImageTag` default (`api/v1alpha1/openshellgateway_types.go`) pins the
NVIDIA `gateway`/`supervisor` images to a specific tested version - it does
not float on `:latest`. New upstream releases arrive as **Dependabot PRs**
against `hack/openshell-pins/Dockerfile` (a pin-only file, never built - see
its header comment), not manual edits. When Dependabot opens one,
`.github/workflows/upstream-sync.yml` automatically syncs the `ImageTag`
default, regenerates CRD manifests, and updates the docs version badge onto
that same PR, so normal CI tests the new pin before anyone merges it. Merging
a dependency-only bump (no other code changes) auto-tags a `vX.Y.Z-N`
release - see `AGENTS.md`'s tag strategy table.

## Testing against a real cluster

`make test` and the CI e2e suite (MINC) cover most changes, but MINC
(MicroShift) — and Kind — have no OpenShift OAuth/SSO support, so neither
can exercise SSO-, `OAuthClient`-, or real-cert-manager-issuance-related
code paths. Changes touching `internal/authbridge/`, TLS/cert-manager
reconciliation, or `route.gatewayAPI` should also be validated against a
real OpenShift cluster with cert-manager and SSO configured before merging:

```bash
KUBECONFIG=~/.kube/your-cluster \
  E2E_REAL_CLUSTER_APPS_DOMAIN=apps.your-cluster.example.com \
  IMG=quay.io/aknochow/ogo:main \
  make test-e2e-real
```

`test-e2e-real` never builds locally — set `IMG` to an image already pushed
to a registry the target cluster can pull (`:main` to validate against
current `main`, or your own image built for the cluster's architecture via
podzilla and pushed, if testing local changes). A local build on Apple
Silicon produces an arm64 image that can't run on a real cluster's amd64
nodes anyway.

This is a hard requirement for changes in those areas at this stage of the
project — MINC's blind spots have caused real regressions to land in
reviewed, CI-green PRs before.

### Promoting a build to staging

Add `E2E_REAL_CLUSTER_KEEP=true` to leave the operator and a running
gateway deployed after the suite finishes, instead of tearing everything
down. This turns the target cluster into a persistent staging environment
running whatever build was just verified — re-running `test-e2e-real` is
idempotent against that leftover state, so promoting a new build is just
running it again with a new `IMG`. SNO serves this role for RDU: verify a
build there before it goes to RDU.

## Reporting Issues

Open a [GitHub issue](https://github.com/aknochow/ogo/issues) with:
- Steps to reproduce
- Expected vs actual behavior
- OpenShift version and OGO version

## Commits

Use [Conventional Commits](https://www.conventionalcommits.org/) format:

```
feat: add sandbox timeout configuration
fix: correct TLS cert rotation on renewal
docs: update quickstart for Envoy Gateway
refactor: extract PKI helpers to internal/pki
```

Valid types: `feat`, `fix`, `docs`, `refactor`, `perf`, `test`, `chore`, `ci`, `build`, `revert`, `style`

Sign off all commits (`git commit -s`).

## Pull Requests

1. Fork the repo and create a feature branch
2. Run `make build test lint` before submitting
3. Keep PRs focused on one change
4. Use conventional commit messages (changelogs are generated automatically)
5. Never include credentials, token names, robot accounts, or internal
   infrastructure details in PR descriptions, commits, or comments
