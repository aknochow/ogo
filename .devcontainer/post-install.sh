#!/bin/bash
set -ex

# OGO development environment setup
# Podman-first, OpenShift-first

OS=$(go env GOOS)
ARCH=$(go env GOARCH)

# Install OpenShift CLI (oc includes kubectl) — latest stable
curl -sL "https://mirror.openshift.com/pub/openshift-v4/clients/ocp/stable/openshift-client-linux.tar.gz" \
  | tar xzf - -C /usr/local/bin oc kubectl

# Install operator-sdk — latest
OPERATOR_SDK_VERSION=$(curl -s https://api.github.com/repos/operator-framework/operator-sdk/releases/latest | grep tag_name | cut -d '"' -f 4)
curl -sLo /usr/local/bin/operator-sdk \
  "https://github.com/operator-framework/operator-sdk/releases/download/${OPERATOR_SDK_VERSION}/operator-sdk_${OS}_${ARCH}"
chmod +x /usr/local/bin/operator-sdk

# Install opm (OLM package manager) — latest
OPM_VERSION=$(curl -s https://api.github.com/repos/operator-framework/operator-registry/releases/latest | grep tag_name | cut -d '"' -f 4)
curl -sLo /usr/local/bin/opm \
  "https://github.com/operator-framework/operator-registry/releases/download/${OPM_VERSION}/${OS}-${ARCH}-opm"
chmod +x /usr/local/bin/opm

# Verify
echo "=== Tool Versions ==="
oc version --client
operator-sdk version
opm version
go version
