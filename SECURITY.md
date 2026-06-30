# Security Policy

## Reporting Vulnerabilities

If you discover a security vulnerability in OGO, please report it
responsibly by opening a [GitHub issue](https://github.com/aknochow/ogo/issues)
with the label `security`.

For sensitive reports, email aknochow@redhat.com directly.

## Scope

OGO manages AI agent sandboxes on OpenShift. Security-relevant areas include:
- Auth-bridge (OAuth token handling, JWT minting)
- Sandbox pod security (SCC, network policies)
- TLS certificate management
- Credential injection (provider Secrets)

## Supported Versions

Only the latest version on `main` is supported. OGO is alpha software.
