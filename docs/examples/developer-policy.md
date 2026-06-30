---
type: Example
title: Developer Policy
description: Network policy allowing GitHub, PyPI, and npm access for general development workflows.
tags: [policy, github, developer]
---

# Developer policy

This example configures a sandbox policy for general development workflows:
git operations, package installation, and GitHub API access.

Combine this with a [provider-specific policy](claude-code.md) for the
inference endpoint access.

## Provider

```yaml
apiVersion: gateway.ogo.aknochow.io/v1alpha1
kind: OpenShellProvider
metadata:
  name: github
  namespace: ogo
spec:
  providerType: github
  credentials:
    GITHUB_TOKEN:
      name: github-token
      key: token
```

Create the token Secret:

```bash
oc create secret generic github-token -n ogo \
  --from-literal=token=ghp_...
```

## Policy

```yaml
apiVersion: gateway.ogo.aknochow.io/v1alpha1
kind: OpenShellPolicy
metadata:
  name: developer
  namespace: ogo
spec:
  policyName: developer
  filesystem:
    includeWorkdir: true
    readOnly: [/usr, /lib, /etc, /app, /proc/self, /dev/urandom, /var/log]
    readWrite: [/sandbox, /tmp, /dev/null]
  network:
    github:
      name: GitHub
      endpoints:
        - { host: api.github.com, port: 443, protocol: rest, access: read-only }
        - { host: api.github.com, port: 443, protocol: graphql, access: read-only }
        - { host: github.com, port: 443, protocol: rest, access: read-only }
      binaries:
        - { path: /usr/bin/gh }
        - { path: /usr/local/bin/gh }
        - { path: /usr/bin/git }
        - { path: /usr/local/bin/git }
    pypi:
      name: PyPI
      endpoints:
        - { host: pypi.org, port: 443 }
        - { host: files.pythonhosted.org, port: 443 }
      binaries:
        - { path: /usr/bin/python3 }
        - { path: /usr/bin/pip }
        - { path: /usr/local/bin/pip }
    npm:
      name: npm Registry
      endpoints:
        - { host: registry.npmjs.org, port: 443 }
      binaries:
        - { path: /usr/bin/npm }
        - { path: /usr/local/bin/npm }
        - { path: /usr/bin/node }
        - { path: /usr/local/bin/node }
  process:
    runAsUser: sandbox
    runAsGroup: sandbox
```

## Notes

- GitHub endpoints default to **read-only** - agents can read code and
  issues but can't push or create PRs without an explicit policy upgrade
- PyPI and npm access is scoped to the package manager binaries only -
  arbitrary `curl` to these hosts is blocked
- Combine with a [Claude Code](claude-code.md) or [Vertex AI](vertex-ai.md)
  policy for full agent capability

## See also

- [Policy concept](../concepts/policy.md)
- [OpenShellPolicy CRD](../reference/openshellpolicy.md)
