# Version pins for the NVIDIA OpenShell images this operator defaults to.
#
# This file is never built - it exists ONLY so Dependabot's `docker`
# ecosystem (which requires a file literally named "Dockerfile", not
# "Containerfile") can track new upstream releases and open a PR here.
# All real, buildable container definitions in this repo use the
# "Containerfile" naming convention (see Containerfile, Containerfile.auth-bridge)
# - this is a narrow, deliberate exception for Dependabot compatibility only.
#
# When Dependabot bumps a tag (and digest) below, CI
# (.github/workflows/upstream-sync.yml) syncs api/v1alpha1/openshellgateway_types.go's
# ImageTag kubebuilder default, regenerates CRD manifests, and updates the
# docs.toml/README.md OpenShell version badge in the same PR. Digests pin the
# exact image Scorecard/CI resolve; ImageTag remains the bare X.Y.Z tag.
FROM ghcr.io/nvidia/openshell/gateway:0.0.116@sha256:05cf77bbb022a739aed6f22daa0e7e164415f4ab273b5f84319e46d91eb8f645
FROM ghcr.io/nvidia/openshell/supervisor:0.0.116@sha256:c8c42aef16c200063e32cbf72e553e4ead027085427b555efafd95063ecead42
