# Version pins for the NVIDIA OpenShell images this operator defaults to.
#
# This file is never built - it exists ONLY so Dependabot's `docker`
# ecosystem (which requires a file literally named "Dockerfile", not
# "Containerfile") can track new upstream releases and open a PR here.
# All real, buildable container definitions in this repo use the
# "Containerfile" naming convention (see Containerfile, Containerfile.auth-bridge)
# - this is a narrow, deliberate exception for Dependabot compatibility only.
#
# When Dependabot bumps a tag below, CI (.github/workflows/upstream-sync.yml)
# syncs api/v1alpha1/openshellgateway_types.go's ImageTag kubebuilder default,
# regenerates CRD manifests, and updates the docs.toml/README.md OpenShell
# version badge in the same PR.
FROM ghcr.io/nvidia/openshell/gateway:0.0.92
FROM ghcr.io/nvidia/openshell/supervisor:0.0.92
