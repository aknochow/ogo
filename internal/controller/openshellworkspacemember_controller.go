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
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
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

	// gatewayGRPCPort is the gateway's in-cluster gRPC listener port.
	gatewayGRPCPort = 8080
)

// errMembershipRemoved signals that addMember's role-change path removed the
// old-role membership but the subsequent re-add failed, leaving the subject
// with no workspace membership at all. reconcileSync checks for this via
// errors.Is to clear status.ReconciledSubject, so the next reconcile treats
// the subject as unreconciled and re-adds from scratch instead of reporting
// a stale "last known good" subject that no longer holds any membership.
var errMembershipRemoved = errors.New("workspace membership removed but re-add failed")

// OpenShellWorkspaceMemberReconciler reconciles OpenShellWorkspaceMember
// objects by managing workspace membership in the OpenShell gateway via its
// gRPC API. It mints short-lived admin JWTs to authenticate, tracks the
// ServiceAccount UID in status to detect identity recreation, and uses a
// finalizer to clean up remote membership on CR deletion.
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

// Reconcile ensures the workspace membership described by an
// OpenShellWorkspaceMember CR matches the remote gateway state.
func (r *OpenShellWorkspaceMemberReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	return r.reconcileSync(ctx, wm)
}

// reconcileSync resolves the gateway and the target ServiceAccount, then
// reconciles workspace membership to match spec.
func (r *OpenShellWorkspaceMemberReconciler) reconcileSync(ctx context.Context, wm *ogov1alpha1.OpenShellWorkspaceMember) (ctrl.Result, error) {
	gw, err := r.resolveGateway(ctx)
	if err != nil {
		return r.setNotReady(ctx, wm, "GatewayNotFound", err)
	}

	// The referenced ServiceAccount is always looked up in this CR's own
	// namespace -- deliberately no cross-namespace override, see
	// ServiceAccountReference's doc comment.
	sa := &corev1.ServiceAccount{}
	err = r.Get(ctx, types.NamespacedName{Name: wm.Spec.ServiceAccountRef.Name, Namespace: wm.Namespace}, sa)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	identityFound := err == nil

	if !identityFound {
		if wm.Status.ReconciledSubject != "" {
			if rmErr := r.removeMember(ctx, gw, wm.Spec.Workspace, wm.Status.ReconciledSubject); rmErr != nil {
				// The real problem here is the failed remote cleanup, not
				// the (already-established) missing ServiceAccount -- report
				// it as such so status doesn't mask a still-active stale
				// membership behind an IdentityNotFound reason.
				return r.setNotReady(ctx, wm, "GatewayUnreachable",
					fmt.Errorf("removing stale membership for a deleted identity: %w", rmErr))
			}
			wm.Status.ReconciledSubject = ""
		}
		return r.setNotReady(ctx, wm, "IdentityNotFound",
			fmt.Errorf("ServiceAccount %s/%s not found", wm.Namespace, wm.Spec.ServiceAccountRef.Name))
	}

	if sa.UID == "" {
		return r.setNotReady(ctx, wm, "IdentityNotFound",
			fmt.Errorf("ServiceAccount %s/%s has no uid", wm.Namespace, wm.Spec.ServiceAccountRef.Name))
	}
	currentSubject := string(sa.UID)

	// From here on a remote call is always needed (at minimum the addMember
	// below), so it's safe to connect once and share it -- avoids minting a
	// second admin JWT and opening a second connection for the recreation
	// case, which otherwise calls both removeMember and addMember in the
	// same reconcile.
	rpcCtx, wsClient, closeFn, err := r.workspaceClient(ctx, gw)
	if err != nil {
		return r.setNotReady(ctx, wm, "GatewayUnreachable", fmt.Errorf("connecting to gateway: %w", err))
	}
	defer closeFn()

	if wm.Status.ReconciledSubject != "" && wm.Status.ReconciledSubject != currentSubject {
		// The ServiceAccount was deleted and recreated (a new UID) since our
		// last successful reconcile. Remove the stale membership first so a
		// recreated identity never silently inherits the old one's access.
		// Don't proceed to grant the new identity if this fails -- that
		// would leave both the stale and new memberships active at once.
		if err := removeMemberWithClient(rpcCtx, wsClient, wm.Spec.Workspace, wm.Status.ReconciledSubject); err != nil {
			return r.setNotReady(ctx, wm, "GatewayUnreachable",
				fmt.Errorf("removing stale membership for recreated identity: %w", err))
		}
		wm.Status.ReconciledSubject = ""
	}

	role := workspaceRoleFromSpec(wm.Spec.Role)
	if err := addMemberWithClient(rpcCtx, wsClient, wm.Spec.Workspace, currentSubject, role); err != nil {
		if errors.Is(err, errMembershipRemoved) {
			// The subject's old-role membership is already gone remotely --
			// don't let status keep claiming currentSubject is reconciled
			// when it currently holds no membership at all.
			wm.Status.ReconciledSubject = ""
		}
		return r.setNotReady(ctx, wm, "GatewayUnreachable", err)
	}

	wm.Status.ReconciledSubject = currentSubject
	wm.Status.Phase = "Synced"
	wm.Status.ObservedGeneration = wm.Generation
	meta.SetStatusCondition(&wm.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue, Reason: "Synced",
		Message: fmt.Sprintf("%s is a %s member of workspace %q", wm.Spec.ServiceAccountRef.Name, wm.Spec.Role, wm.Spec.Workspace),
	})
	// requeueInterval is the shared periodic requeue cadence defined in
	// openshellgateway_controller.go (this file's sibling in the same package).
	return ctrl.Result{RequeueAfter: requeueInterval}, r.Status().Update(ctx, wm)
}

// reconcileDelete removes the subject's remote workspace membership before
// allowing the finalizer to be dropped and the CR to be deleted.
func (r *OpenShellWorkspaceMemberReconciler) reconcileDelete(ctx context.Context, wm *ogov1alpha1.OpenShellWorkspaceMember) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(wm, workspaceMemberFinalizer) {
		return ctrl.Result{}, nil
	}

	if wm.Status.ReconciledSubject != "" {
		gw, err := r.resolveGateway(ctx)
		if err != nil {
			// Don't drop the finalizer here -- that would permanently orphan
			// the remote membership if the gateway is only temporarily
			// unreachable. Matches OpenShellGatewayReconciler's own
			// reconcileDelete, which also retries indefinitely rather than
			// giving up on external cleanup.
			log.Error(err, "no gateway found during cleanup, will retry")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		if err := r.removeMember(ctx, gw, wm.Spec.Workspace, wm.Status.ReconciledSubject); err != nil {
			log.Error(err, "failed to remove workspace membership on delete, will retry")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
	}

	controllerutil.RemoveFinalizer(wm, workspaceMemberFinalizer)
	return ctrl.Result{}, r.Update(ctx, wm)
}

// setNotReady records a Ready=False condition and a Failed phase. For
// GatewayUnreachable specifically, the underlying cause (which can include
// details from reading/parsing the auth-bridge signing key Secret) is logged
// server-side only; the CR's status -- visible to anyone with read access to
// it -- gets a generic message instead.
func (r *OpenShellWorkspaceMemberReconciler) setNotReady(ctx context.Context, wm *ogov1alpha1.OpenShellWorkspaceMember, reason string, cause error) (ctrl.Result, error) {
	message := cause.Error()
	if reason == "GatewayUnreachable" {
		logf.FromContext(ctx).Error(cause, "gateway unreachable while reconciling workspace membership")
		message = "failed to reach the OpenShell gateway; see operator logs for details"
	}
	meta.SetStatusCondition(&wm.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: reason, Message: message,
	})
	wm.Status.Phase = "Failed"
	wm.Status.ObservedGeneration = wm.Generation
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

// addMemberWithClient reconciles subject's membership to exactly (workspace,
// role) over an already-open connection -- every reconcileSync call site
// shares one connection (see reconcileSync), so there is no bare
// no-shared-connection variant of this.
// AddWorkspaceMember itself is not idempotent -- the gateway rejects a
// second call for the same (workspace, subject) with AlreadyExists, which a
// controller reconcile loop will always eventually trigger. On AlreadyExists,
// look up the existing role: if it already matches, this is a no-op; if the
// CR's spec.role changed, remove and re-add to pick up the new role (there
// is no UpdateWorkspaceMember RPC).
func addMemberWithClient(rpcCtx context.Context, wsClient openshellclient.OpenShellClient, workspace, subject string, role openshellclient.WorkspaceRole) error {
	_, err := wsClient.AddWorkspaceMember(rpcCtx, &openshellclient.AddWorkspaceMemberRequest{
		Workspace:        workspace,
		PrincipalSubject: subject,
		Role:             role,
	})
	if err == nil {
		return nil
	}
	if grpcstatus.Code(err) != codes.AlreadyExists {
		return fmt.Errorf("AddWorkspaceMember: %w", err)
	}

	existingRole, err := findMemberRole(rpcCtx, wsClient, workspace, subject)
	if err != nil {
		return fmt.Errorf("looking up existing member after AlreadyExists: %w", err)
	}
	if existingRole == role {
		return nil
	}

	if _, err := wsClient.RemoveWorkspaceMember(rpcCtx, &openshellclient.RemoveWorkspaceMemberRequest{
		Workspace: workspace, PrincipalSubject: subject,
	}); err != nil {
		return fmt.Errorf("RemoveWorkspaceMember (role change): %w", err)
	}
	if _, err := wsClient.AddWorkspaceMember(rpcCtx, &openshellclient.AddWorkspaceMemberRequest{
		Workspace: workspace, PrincipalSubject: subject, Role: role,
	}); err != nil {
		return fmt.Errorf("%w: AddWorkspaceMember (role change): %v", errMembershipRemoved, err)
	}
	return nil
}

// findMemberRole scans ListWorkspaceMembers for subject's current role. The
// RPC has no per-subject filter, so this fetches up to the gateway's own
// per-workspace member cap (1000, see upstream's MAX_WORKSPACE_MEMBERS) and
// searches client-side.
func findMemberRole(ctx context.Context, wsClient openshellclient.OpenShellClient, workspace, subject string) (openshellclient.WorkspaceRole, error) {
	resp, err := wsClient.ListWorkspaceMembers(ctx, &openshellclient.ListWorkspaceMembersRequest{Workspace: workspace, Limit: 1000})
	if err != nil {
		return openshellclient.WorkspaceRole_WORKSPACE_ROLE_UNSPECIFIED, fmt.Errorf("ListWorkspaceMembers: %w", err)
	}
	for _, m := range resp.Members {
		if m.PrincipalSubject == subject {
			return m.Role, nil
		}
	}
	return openshellclient.WorkspaceRole_WORKSPACE_ROLE_UNSPECIFIED, fmt.Errorf("member %q not found in workspace %q despite AlreadyExists", subject, workspace)
}

// removeMember removes subject's membership over a fresh connection. See
// removeMemberWithClient for callers that already have a connection open.
func (r *OpenShellWorkspaceMemberReconciler) removeMember(ctx context.Context, gw *ogov1alpha1.OpenShellGateway, workspace, subject string) error {
	rpcCtx, wsClient, closeFn, err := r.workspaceClient(ctx, gw)
	if err != nil {
		return err
	}
	defer closeFn()
	return removeMemberWithClient(rpcCtx, wsClient, workspace, subject)
}

func removeMemberWithClient(rpcCtx context.Context, wsClient openshellclient.OpenShellClient, workspace, subject string) error {
	_, err := wsClient.RemoveWorkspaceMember(rpcCtx, &openshellclient.RemoveWorkspaceMemberRequest{
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

	target := fmt.Sprintf("%s.%s.svc.cluster.local:%d", gw.Name, gatewayNamespace(gw), gatewayGRPCPort)
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
func (r *OpenShellWorkspaceMemberReconciler) dialCredentials(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (credentials.TransportCredentials, error) {
	tlsEnabled := gw.Spec.TLS.Enabled == nil || *gw.Spec.TLS.Enabled
	if !tlsEnabled {
		logf.FromContext(ctx).Info("dialing OpenShell gateway gRPC endpoint over plaintext; the openshell-admin JWT minted for this call will be transmitted in cleartext on the pod network -- set spec.tls.enabled to encrypt this channel")
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
		MinVersion:   tls.VersionTLS13,
	}), nil
}

// SetupWithManager registers the controller with the manager.
func (r *OpenShellWorkspaceMemberReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ogov1alpha1.OpenShellWorkspaceMember{}).
		Named("openshellworkspacemember").
		Complete(r)
}
