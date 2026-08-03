---
type: Example
title: Claude Code + Anthropic
description: Run Claude Code in sandboxes with direct Anthropic API access.
tags: [claude, anthropic, inference]
---

# Claude Code with Anthropic API

> **Warning:** The Provider and Policy CRs below do not yet have any
> effect on the running gateway (see
> [Provider](../concepts/provider.md) and [Policy](../concepts/policy.md)).
> Following this example as written will not actually inject the API key
> or allow network access to `api.anthropic.com`. The sandbox will run
> under the gateway's default policy, which permits no external network
> access. Tracked in [#51](https://github.com/aknochow/ogo/issues/51) and
> [#52](https://github.com/aknochow/ogo/issues/52).

This example configures OGO to run Claude Code in sandboxes with direct
access to the Anthropic API.

## 1. Create the API key Secret

```bash
oc create secret generic anthropic-secret -n ogo \
  --from-literal=api-key=sk-ant-api03-...
```

## 2. Create the Provider

```yaml
apiVersion: gateway.ogo.aknochow.io/v1alpha1
kind: OpenShellProvider
metadata:
  name: claude-code
  namespace: ogo
spec:
  providerType: claude-code
  credentials:
    ANTHROPIC_API_KEY:
      name: anthropic-secret
      key: api-key
```

The gateway injects `ANTHROPIC_API_KEY` into every sandbox pod.

## 3. Create the Policy

```yaml
apiVersion: gateway.ogo.aknochow.io/v1alpha1
kind: OpenShellPolicy
metadata:
  name: claude-code
  namespace: ogo
spec:
  policyName: claude-code
  filesystem:
    includeWorkdir: true
    readOnly: [/usr, /lib, /etc, /app, /proc/self, /dev/urandom]
    readWrite: [/sandbox, /tmp, /dev/null]
  network:
    anthropic:
      name: Anthropic API
      endpoints:
        - { host: api.anthropic.com, port: 443, protocol: rest, access: read-write }
        - { host: statsig.anthropic.com, port: 443, protocol: rest, access: read-write }
        - { host: sentry.io, port: 443, protocol: rest, access: read-write }
      binaries:
        - { path: /usr/bin/claude }
        - { path: /usr/local/bin/claude }
  process:
    runAsUser: sandbox
    runAsGroup: sandbox
```

## 4. Connect

```bash
openshell sandbox create --gateway my-cluster
# Inside the sandbox:
claude
```

## BYOK alternative

Skip the Provider and Policy CRs entirely. Users bring their own key:

```bash
openshell sandbox create --gateway my-cluster
# Inside the sandbox:
export ANTHROPIC_API_KEY=sk-ant-...
claude
```

This requires the sandbox policy to allow `api.anthropic.com` egress.

## See also

- [Provider concept](../concepts/provider.md)
- [Policy concept](../concepts/policy.md)
- [Vertex AI example](vertex-ai.md) - for Claude via Google Vertex
