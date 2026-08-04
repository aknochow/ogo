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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	"github.com/aknochow/ogo/internal/authbridge"
	"github.com/aknochow/ogo/internal/openshellclient"
)

const (
	workspaceMemberFinalizer = "ogo.aknochow.io/workspace-member-cleanup"

	// Mirrors gateway.toml's hardcoded admin_role (internal/gateway/config.go)
	// and auth-bridge's own roleAdmin constant -- there's no shared exported
	// constant for this across packages today.
	workspaceAdminRole = "openshell-admin"

	// Matches auth-bridge's own default (cmd/auth-bridge/main.go
	// envOrDefault("AUTH_BRIDGE_AUDIENCE", "openshell-cli")); the controller
	// never overrides it on the auth-bridge deployment today.
	workspaceOIDCAudience = "openshell-cli"

	// adminTokenTTL is short-lived on purpose: minted fresh per reconcile,
	// never persisted, used for a single gRPC call.
	adminTokenTTL = 60 * time.Second
)

// OpenShellWorkspaceMemberReconciler reconciles an OpenShellWorkspaceMember object.
type OpenShellWorkspaceMemberReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// connectClient constructs a client for the gateway's workspace-membership
	// gRPC API and a func to release it. Defaults to dialConnectClient (a real
	// in-cluster dial); tests override it to point at an in-process fake
	// server without needing a real gateway or network.
	connectClient func(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (openshellclient.OpenShellClient, func(), error)
}

// connect returns the reconciler's connectClient, defaulting to a real
// in-cluster gRPC dial when unset (the zero value, i.e. outside of tests).
func (r *OpenShellWorkspaceMemberReconciler) connect(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (openshellclient.OpenShellClient, func(), error) {
	if r.connectClient != nil {
		return r.connectClient(ctx, gw)
	}
	return r.dialConnectClient(ctx, gw)
}

// +kubebuilder:rbac:groups=gateway.ogo.aknochow.io,resources=openshellworkspacemembers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.ogo.aknochow.io,resources=openshellworkspacemembers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.ogo.aknochow.io,resources=openshellworkspacemembers/finalizers,verbs=update

func (r *OpenShellWorkspaceMemberReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	wm := &ogov1alpha1.OpenShellWorkspaceMember{}
	if err := r.Get(ctx, req.NamespacedName, wm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !wm.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, wm)
	}

	if !controllerutil.ContainsFinalizer(wm, workspaceMemberFinalizer) {
		controllerutil.AddFinalizer(wm, workspaceMemberFinalizer)
		if err := r.Update(ctx, wm); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	gw, err := r.resolveGateway(ctx)
	if err != nil {
		return r.setNotReady(ctx, wm, "GatewayNotFound", err)
	}

	saNamespace := wm.Spec.ServiceAccountRef.Namespace
	if saNamespace == "" {
		saNamespace = wm.Namespace
	}
	sa := &corev1.ServiceAccount{}
	err = r.Get(ctx, types.NamespacedName{Name: wm.Spec.ServiceAccountRef.Name, Namespace: saNamespace}, sa)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	identityFound := err == nil

	if !identityFound {
		if wm.Status.ReconciledSubject != "" {
			if rmErr := r.removeMember(ctx, gw, wm.Spec.Workspace, wm.Status.ReconciledSubject); rmErr != nil {
				log.Error(rmErr, "failed to remove stale membership for a missing identity")
			} else {
				wm.Status.ReconciledSubject = ""
			}
		}
		return r.setNotReady(ctx, wm, "IdentityNotFound",
			fmt.Errorf("ServiceAccount %s/%s not found", saNamespace, wm.Spec.ServiceAccountRef.Name))
	}

	currentSubject := string(sa.UID)
	if wm.Status.ReconciledSubject != "" && wm.Status.ReconciledSubject != currentSubject {
		// The ServiceAccount was deleted and recreated (a new UID) since our
		// last successful reconcile. Remove the stale membership first so a
		// recreated identity never silently inherits the old one's access.
		if err := r.removeMember(ctx, gw, wm.Spec.Workspace, wm.Status.ReconciledSubject); err != nil {
			log.Error(err, "failed to remove stale membership for a recreated identity",
				"oldSubject", wm.Status.ReconciledSubject, "newSubject", currentSubject)
		}
	}

	role := workspaceRoleFromSpec(wm.Spec.Role)
	if err := r.addMember(ctx, gw, wm.Spec.Workspace, currentSubject, role); err != nil {
		return r.setNotReady(ctx, wm, "GatewayUnreachable", err)
	}

	wm.Status.ReconciledSubject = currentSubject
	wm.Status.Phase = "Synced"
	wm.Status.ObservedGeneration = wm.Generation
	meta.SetStatusCondition(&wm.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue, Reason: "Synced",
		Message: fmt.Sprintf("%s is a %s member of workspace %q", wm.Spec.ServiceAccountRef.Name, wm.Spec.Role, wm.Spec.Workspace),
	})
	return ctrl.Result{RequeueAfter: requeueInterval}, r.Status().Update(ctx, wm)
}

func (r *OpenShellWorkspaceMemberReconciler) reconcileDelete(ctx context.Context, wm *ogov1alpha1.OpenShellWorkspaceMember) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(wm, workspaceMemberFinalizer) {
		return ctrl.Result{}, nil
	}

	if wm.Status.ReconciledSubject != "" {
		gw, err := r.resolveGateway(ctx)
		if err != nil {
			log.Error(err, "no gateway found during cleanup; removing finalizer without a remote cleanup call")
		} else if err := r.removeMember(ctx, gw, wm.Spec.Workspace, wm.Status.ReconciledSubject); err != nil {
			log.Error(err, "failed to remove workspace membership on delete, will retry")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
	}

	controllerutil.RemoveFinalizer(wm, workspaceMemberFinalizer)
	return ctrl.Result{}, r.Update(ctx, wm)
}

func (r *OpenShellWorkspaceMemberReconciler) setNotReady(ctx context.Context, wm *ogov1alpha1.OpenShellWorkspaceMember, reason string, cause error) (ctrl.Result, error) {
	meta.SetStatusCondition(&wm.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: reason, Message: cause.Error(),
	})
	wm.Status.Phase = "Failed"
	if err := r.Status().Update(ctx, wm); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// resolveGateway returns the cluster's singleton OpenShellGateway, mirroring
// OpenShellGatewayReconciler's own oldest-wins tie-break so this stays
// consistent if a second (rejected) Gateway CR ever transiently exists.
func (r *OpenShellWorkspaceMemberReconciler) resolveGateway(ctx context.Context) (*ogov1alpha1.OpenShellGateway, error) {
	gwList := &ogov1alpha1.OpenShellGatewayList{}
	if err := r.List(ctx, gwList); err != nil {
		return nil, err
	}
	if len(gwList.Items) == 0 {
		return nil, fmt.Errorf("no OpenShellGateway found in the cluster")
	}
	oldest := gwList.Items[0]
	for _, item := range gwList.Items[1:] {
		if item.CreationTimestamp.Before(&oldest.CreationTimestamp) {
			oldest = item
		}
	}
	return &oldest, nil
}

func workspaceRoleFromSpec(role string) openshellclient.WorkspaceRole {
	if role == "admin" {
		return openshellclient.WorkspaceRole_WORKSPACE_ROLE_ADMIN
	}
	return openshellclient.WorkspaceRole_WORKSPACE_ROLE_USER
}

func (r *OpenShellWorkspaceMemberReconciler) addMember(ctx context.Context, gw *ogov1alpha1.OpenShellGateway, workspace, subject string, role openshellclient.WorkspaceRole) error {
	rpcCtx, wsClient, closeFn, err := r.workspaceClient(ctx, gw)
	if err != nil {
		return err
	}
	defer closeFn()

	_, err = wsClient.AddWorkspaceMember(rpcCtx, &openshellclient.AddWorkspaceMemberRequest{
		Workspace:        workspace,
		PrincipalSubject: subject,
		Role:             role,
	})
	if err != nil {
		return fmt.Errorf("AddWorkspaceMember: %w", err)
	}
	return nil
}

func (r *OpenShellWorkspaceMemberReconciler) removeMember(ctx context.Context, gw *ogov1alpha1.OpenShellGateway, workspace, subject string) error {
	rpcCtx, wsClient, closeFn, err := r.workspaceClient(ctx, gw)
	if err != nil {
		return err
	}
	defer closeFn()

	_, err = wsClient.RemoveWorkspaceMember(rpcCtx, &openshellclient.RemoveWorkspaceMemberRequest{
		Workspace:        workspace,
		PrincipalSubject: subject,
	})
	if err != nil {
		return fmt.Errorf("RemoveWorkspaceMember: %w", err)
	}
	return nil
}

// workspaceClient mints a short-lived admin JWT using the same RSA key
// auth-bridge signs with, connects to the gateway's workspace-membership API
// (a real in-cluster gRPC dial by default, or a test double via
// connectClient), and returns a context carrying the token as Bearer auth
// metadata. The caller must call the returned func to release the connection.
func (r *OpenShellWorkspaceMemberReconciler) workspaceClient(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (context.Context, openshellclient.OpenShellClient, func(), error) {
	token, err := r.mintAdminToken(ctx, gw)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("minting admin token: %w", err)
	}

	wsClient, closeFn, err := r.connect(ctx, gw)
	if err != nil {
		return nil, nil, nil, err
	}

	rpcCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	return rpcCtx, wsClient, closeFn, nil
}

// dialConnectClient is the real, default connectClient implementation: dials
// the gateway's in-cluster gRPC endpoint, plaintext or mTLS matching the
// gateway's own listener configuration.
func (r *OpenShellWorkspaceMemberReconciler) dialConnectClient(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (openshellclient.OpenShellClient, func(), error) {
	creds, err := r.dialCredentials(ctx, gw)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving gRPC credentials: %w", err)
	}

	target := fmt.Sprintf("%s.%s.svc.cluster.local:8080", gw.Name, gatewayNamespace(gw))
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, nil, fmt.Errorf("dialing gateway gRPC endpoint %s: %w", target, err)
	}

	return openshellclient.NewOpenShellClient(conn), func() { _ = conn.Close() }, nil
}

func (r *OpenShellWorkspaceMemberReconciler) mintAdminToken(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (string, error) {
	secret := &corev1.Secret{}
	name := types.NamespacedName{Name: gw.Name + "-auth-bridge-keys", Namespace: gatewayNamespace(gw)}
	if err := r.Get(ctx, name, secret); err != nil {
		return "", fmt.Errorf("reading auth-bridge signing key: %w", err)
	}

	signer, err := authbridge.NewJWTSignerFromPEM(secret.Data["signing.pem"], string(secret.Data["kid"]))
	if err != nil {
		return "", fmt.Errorf("loading auth-bridge signing key: %w", err)
	}

	return signer.MintToken(
		authBridgeInternalURL(gw), workspaceOIDCAudience,
		"ogo-controller", "ogo-controller", "",
		[]string{workspaceAdminRole}, adminTokenTTL,
	)
}

// dialCredentials mirrors the gateway pod's own listener: plaintext when
// spec.tls.enabled is false (the default, and the only shape exercised on
// SNO/RDU today), or mTLS using the same shared self-signed CA/client cert
// the operator already generates for internal clients when TLS is enabled.
// The TLS branch has not been live-verified against a tls.enabled=true
// deployment -- both real clusters run plaintext today.
func (r *OpenShellWorkspaceMemberReconciler) dialCredentials(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (credentials.TransportCredentials, error) {
	tlsEnabled := gw.Spec.TLS.Enabled != nil && *gw.Spec.TLS.Enabled
	if !tlsEnabled {
		return insecure.NewCredentials(), nil
	}

	secret := &corev1.Secret{}
	name := types.NamespacedName{Name: gw.Name + "-client-tls", Namespace: gatewayNamespace(gw)}
	if err := r.Get(ctx, name, secret); err != nil {
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
	}), nil
}

func (r *OpenShellWorkspaceMemberReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ogov1alpha1.OpenShellWorkspaceMember{}).
		Named("openshellworkspacemember").
		Complete(r)
}
