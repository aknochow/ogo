# Pre-Commit Checklist

Verify before pushing to main or opening a PR.

## Build

- [ ] `make build` passes
- [ ] `make test` passes (all packages)
- [ ] `make lint` passes (zero findings)
- [ ] `make generate manifests` if CRD types changed

## Secrets and Credentials

- [ ] No API keys, tokens, or passwords in code or docs (use `...` placeholders)
- [ ] No personal kubeconfig paths (`~/.kube/sno`, `~/.kube/rdu`)
- [ ] No internal registry references (`registry.access.redhat.com/hi/`)
- [ ] Sample CRs use generic values, not real credentials
- [ ] `git diff --cached` reviewed for accidental secret inclusion

## Documentation

- [ ] README.md and docs/index.md are in sync (same content, adjusted paths)
- [ ] New CRD fields added to `docs/reference/` tables
- [ ] Sample CRs updated in `config/samples/` if spec changed
- [ ] CSV alm-examples updated if samples changed
- [ ] No personal cluster names (`sno`, `rdu`, `podzilla`) in docs
- [ ] No CLAUDE.md/AGENTS.md references in user-facing docs

## Images

- [ ] Containerfiles use public base images (not internal registries)
- [ ] Version tag follows calver: `0.1.0-YYYYMMDDHHMMSS`
- [ ] No NVIDIA images tagged with our calver
- [ ] auth-bridge image tag matches operator expectations

## Code Quality

- [ ] Apache 2.0 license header on all Go source files
- [ ] No scaffold TODO comments in committed code
- [ ] Error returns checked (errcheck lint)
- [ ] No import shadowing (revive lint)
- [ ] Constants extracted for repeated strings (goconst lint)

## OLM

- [ ] `make bundle` regenerated if operator image or CRDs changed
- [ ] Bundle validated: `operator-sdk bundle validate ./bundle`
- [ ] CSV description and alm-examples current
- [ ] Catalog FBC regenerated if bundle changed

## Security

- [ ] `.gitignore` covers: `.env`, `*.pem`, `*.key`, `kubeconfig`, `.devcontainer/`
- [ ] `.dockerignore` excludes: `.git/`, `.env`, secrets
- [ ] Auth-bridge uses RS256 (not EdDSA) for NVIDIA gateway compatibility
- [ ] User group gate configured (userGroup is required when SSO enabled)
- [ ] No `allowUnauthenticated: true` in production samples
