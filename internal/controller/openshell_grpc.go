/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	"github.com/aknochow/ogo/internal/authbridge"
	"github.com/aknochow/ogo/internal/openshellclient"
)

// Shared by every controller that calls the gateway's gRPC API
// (OpenShellWorkspaceMember, OpenShellProvider, OpenShellPolicy).
const (
	// Mirrors gateway.toml's hardcoded admin_role (internal/gateway/config.go)
	// and auth-bridge's own roleAdmin constant -- there's no shared exported
	// constant for this across packages today. The same role satisfies every
	// gateway RPC these controllers call: workspace-admin, provider-admin, and
	// platform-admin (global policy) checks all resolve to the identical
	// configured admin_role string server-side.
	gatewayAdminRole = "openshell-admin"

	// Matches auth-bridge's own default (cmd/auth-bridge/main.go
	// envOrDefault("AUTH_BRIDGE_AUDIENCE", "openshell-cli")); the controller
	// never overrides it on the auth-bridge deployment today.
	gatewayOIDCAudience = "openshell-cli"

	// adminTokenTTL is short-lived on purpose: minted fresh per reconcile,
	// never persisted, used for a single gRPC call.
	adminTokenTTL = 60 * time.Second

	// gatewayGRPCPort is the gateway's in-cluster gRPC listener port.
	gatewayGRPCPort = 8080

	// defaultOpenShellWorkspace is the gateway's own default workspace,
	// auto-seeded server-side at gateway startup. OGO is a single-tenant,
	// single-workspace deployment today -- every gRPC call that takes a
	// workspace argument uses this constant.
	defaultOpenShellWorkspace = "default"

	// gatewayRetryInterval is the shared backoff used when a gateway gRPC
	// call fails transiently (unreachable, temporarily blocked) across the
	// controllers that talk to it.
	gatewayRetryInterval = 30 * time.Second
)

// gatewayConnectFunc constructs a client for the gateway's gRPC API and a
// func to release it. Each reconciler that talks to the gateway declares its
// own field of this type, defaulting to dialConnectClient (a real in-cluster
// dial) when nil; tests override it to point at an in-process fake server
// without needing a real gateway or network.
type gatewayConnectFunc func(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (openshellclient.OpenShellClient, func(), error)

// resolveGateway returns the cluster's singleton OpenShellGateway, using the
// same oldest-wins tie-break OpenShellGatewayReconciler itself enforces, so
// this stays consistent if a second (rejected) Gateway CR ever transiently
// exists.
func resolveGateway(ctx context.Context, c client.Client) (*ogov1alpha1.OpenShellGateway, error) {
	gwList := &ogov1alpha1.OpenShellGatewayList{}
	if err := c.List(ctx, gwList); err != nil {
		return nil, err
	}
	if len(gwList.Items) == 0 {
		return nil, fmt.Errorf("no OpenShellGateway found in the cluster")
	}
	items := make([]*ogov1alpha1.OpenShellGateway, len(gwList.Items))
	for i := range gwList.Items {
		items[i] = &gwList.Items[i]
	}
	return oldestByCreationTimestamp(items), nil
}

// gatewayClient mints a short-lived admin JWT using the same RSA key
// auth-bridge signs with, connects to the gateway's gRPC API (a real
// in-cluster dial via dialConnectClient when connect is nil, or a test
// double via connect), and returns a context carrying the token as Bearer
// auth metadata. The caller must call the returned func to release the
// connection.
func gatewayClient(ctx context.Context, c client.Client, connect gatewayConnectFunc, gw *ogov1alpha1.OpenShellGateway) (context.Context, openshellclient.OpenShellClient, func(), error) {
	token, err := mintAdminToken(ctx, c, gw)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("minting admin token: %w", err)
	}

	dial := connect
	if dial == nil {
		dial = func(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (openshellclient.OpenShellClient, func(), error) {
			return dialConnectClient(ctx, c, gw)
		}
	}
	wsClient, closeFn, err := dial(ctx, gw)
	if err != nil {
		return nil, nil, nil, err
	}

	rpcCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	return rpcCtx, wsClient, closeFn, nil
}

// dialConnectClient is the real, default gatewayConnectFunc implementation:
// dials the gateway's in-cluster gRPC endpoint, plaintext or mTLS matching
// the gateway's own listener configuration.
func dialConnectClient(ctx context.Context, c client.Client, gw *ogov1alpha1.OpenShellGateway) (openshellclient.OpenShellClient, func(), error) {
	creds, err := dialCredentials(ctx, c, gw)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving gRPC credentials: %w", err)
	}

	target := fmt.Sprintf("%s.%s.svc.cluster.local:%d", gw.Name, gatewayNamespace(gw), gatewayGRPCPort)
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, nil, fmt.Errorf("dialing gateway gRPC endpoint %s: %w", target, err)
	}

	return openshellclient.NewOpenShellClient(conn), func() { _ = conn.Close() }, nil
}

func mintAdminToken(ctx context.Context, c client.Client, gw *ogov1alpha1.OpenShellGateway) (string, error) {
	secret := &corev1.Secret{}
	name := types.NamespacedName{Name: gw.Name + "-auth-bridge-keys", Namespace: gatewayNamespace(gw)}
	if err := c.Get(ctx, name, secret); err != nil {
		return "", fmt.Errorf("reading auth-bridge signing key: %w", err)
	}

	signer, err := authbridge.NewJWTSignerFromPEM(secret.Data["signing.pem"], string(secret.Data["kid"]))
	if err != nil {
		return "", fmt.Errorf("loading auth-bridge signing key: %w", err)
	}

	return signer.MintToken(
		authBridgeInternalURL(gw), gatewayOIDCAudience,
		"ogo-controller", "ogo-controller", "",
		[]string{gatewayAdminRole}, adminTokenTTL,
	)
}

// dialCredentials mirrors the gateway pod's own listener: plaintext when
// spec.tls.enabled is explicitly false, or mTLS using the same shared
// self-signed CA/client cert the operator already generates for internal
// clients when TLS is enabled. TLS.Enabled defaults to true at the CRD
// schema level (+kubebuilder:default=true), and nil is treated the same way
// here -- matching every other TLS.Enabled check in this package
// (openshellgateway_controller.go), where nil means "not explicitly
// disabled" rather than "disabled". Plaintext is therefore only ever a
// deliberate, explicit choice, never a silent fallback for an unset field.
// The TLS branch has not been live-verified against a tls.enabled=true
// deployment -- both real clusters run with it explicitly disabled today.
func dialCredentials(ctx context.Context, c client.Client, gw *ogov1alpha1.OpenShellGateway) (credentials.TransportCredentials, error) {
	tlsEnabled := gw.Spec.TLS.Enabled == nil || *gw.Spec.TLS.Enabled
	if !tlsEnabled {
		logf.FromContext(ctx).Info("dialing OpenShell gateway gRPC endpoint over plaintext; the openshell-admin JWT minted for this call will be transmitted in cleartext on the pod network -- set spec.tls.enabled to encrypt this channel")
		return insecure.NewCredentials(), nil
	}

	secret := &corev1.Secret{}
	name := types.NamespacedName{Name: gw.Name + "-client-tls", Namespace: gatewayNamespace(gw)}
	if err := c.Get(ctx, name, secret); err != nil {
		return nil, fmt.Errorf("reading client TLS secret: %w", err)
	}

	cert, err := tls.X509KeyPair(secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey])
	if err != nil {
		return nil, fmt.Errorf("parsing client TLS keypair: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(secret.Data["ca.crt"]) {
		return nil, fmt.Errorf("client TLS secret %q has no usable ca.crt", name.Name)
	}

	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   fmt.Sprintf("%s.%s.svc.cluster.local", gw.Name, gatewayNamespace(gw)),
		MinVersion:   tls.VersionTLS13,
	}), nil
}
