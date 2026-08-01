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

## Controller Reconcile Logic

- [ ] Every early `return` inside `Reconcile()` either reaches `updateStatus()`
      or has an explicit comment justifying why it skips it — an early return
      that skips `updateStatus()` leaves `Phase`/`Degraded` stuck at whatever
      a *previous* failed pass last set, even once a later pass resolves
      cleanly (found live on SNO: removing a CR's `hostname` correctly set
      `EnvoyRouteReady: HostnameMissing`, but a hasty fix returned early and
      left `Phase: Failed`/`Degraded: True` permanently stuck from an earlier
      failure, because `updateStatus()` — the only thing that resets them —
      never ran again)
- [ ] A "waiting on user input/config" condition (e.g. a required field left
      unset) does not escalate to `Phase: Failed` / `Degraded: True` via
      `setDegraded()` — that framing implies a transient technical failure,
      not an incomplete config the user is actively filling in. Surface it
      via a specific condition (e.g. `Reason: HostnameMissing`) instead
- [ ] This class of bug is easy to introduce and hard to catch via code
      review or unit tests alone (envtest's fake clients often don't
      exercise the exact `isOCP`/`useGWAPI` gating these paths depend on) —
      verify any new early-return path live against a real cluster:
      trigger the condition, confirm `Phase`/`Degraded` reflect it, resolve
      the condition, confirm they reset back to healthy

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
