#!/bin/bash
set -euo pipefail

# Connect to a remote OpenShell Gateway deployed by OGO.
# Extracts client mTLS certificates from the cluster and registers
# the gateway with the local openshell CLI.
#
# Usage:
#   ./scripts/connect-gateway.sh [gateway-name] [namespace] [kubeconfig]
#
# Examples:
#   ./scripts/connect-gateway.sh                          # defaults: openshell / openshell / current context
#   ./scripts/connect-gateway.sh rdu openshell ~/.kube/rdu

GATEWAY_NAME="${1:-openshell}"
NAMESPACE="${2:-openshell}"
KUBECONFIG_PATH="${3:-}"

KUBECTL="oc"
if [ -n "$KUBECONFIG_PATH" ]; then
  export KUBECONFIG="$KUBECONFIG_PATH"
fi

command -v "$KUBECTL" >/dev/null 2>&1 || { echo "Error: oc not found"; exit 1; }
command -v openshell >/dev/null 2>&1 || { echo "Error: openshell CLI not found"; exit 1; }

CERT_DIR=$(mktemp -d)
trap 'rm -rf "$CERT_DIR"' EXIT

echo "Extracting client certificates from ${NAMESPACE}/${GATEWAY_NAME}-client-tls..."
$KUBECTL get secret "${GATEWAY_NAME}-client-tls" -n "$NAMESPACE" -o jsonpath='{.data.tls\.crt}' | base64 -d > "$CERT_DIR/tls.crt"
$KUBECTL get secret "${GATEWAY_NAME}-client-tls" -n "$NAMESPACE" -o jsonpath='{.data.tls\.key}' | base64 -d > "$CERT_DIR/tls.key"
$KUBECTL get secret "${GATEWAY_NAME}-client-tls" -n "$NAMESPACE" -o jsonpath='{.data.ca\.crt}' | base64 -d > "$CERT_DIR/ca.crt"

GATEWAY_URL=$($KUBECTL get openshellgateway "$GATEWAY_NAME" -o jsonpath='{.status.gatewayURL}' 2>/dev/null || true)
if [ -z "$GATEWAY_URL" ]; then
  ROUTE_HOST=$($KUBECTL get route "$GATEWAY_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.host}' 2>/dev/null || true)
  if [ -n "$ROUTE_HOST" ]; then
    GATEWAY_URL="https://${ROUTE_HOST}"
  else
    echo "Error: could not determine gateway URL from CR status or Route"
    exit 1
  fi
fi

# Remember current active gateway so we can restore it
PREVIOUS_GW=$(openshell gateway list 2>/dev/null | grep '^\*' | awk '{print $2}' || true)

echo "Registering gateway '${GATEWAY_NAME}' at ${GATEWAY_URL}..."
openshell gateway add "$GATEWAY_URL" \
  --name "$GATEWAY_NAME" \
  --tls-cert "$CERT_DIR/tls.crt" \
  --tls-key "$CERT_DIR/tls.key" \
  --tls-ca "$CERT_DIR/ca.crt"

# Restore previous active gateway (gateway add sets the new one as active)
if [ -n "$PREVIOUS_GW" ] && [ "$PREVIOUS_GW" != "$GATEWAY_NAME" ]; then
  openshell gateway select "$PREVIOUS_GW" >/dev/null 2>&1 || true
  echo "Active gateway restored to '${PREVIOUS_GW}'"
fi

echo ""
echo "Connected. Use --gateway ${GATEWAY_NAME} to target this gateway:"
echo "  openshell sandbox list --gateway ${GATEWAY_NAME}"
echo "  openshell sandbox create --gateway ${GATEWAY_NAME} -- claude"
