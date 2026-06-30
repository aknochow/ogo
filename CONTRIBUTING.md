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
