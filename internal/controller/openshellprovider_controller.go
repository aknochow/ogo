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
	"fmt"
	"maps"
	"slices"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	"github.com/aknochow/ogo/internal/openshellclient"
)

const providerFinalizer = "ogo.aknochow.io/provider-cleanup"

// OpenShellProviderReconciler reconciles OpenShellProvider objects by
// pushing their credentials/config to the OpenShell gateway via its
// CreateProvider/UpdateProvider/DeleteProvider gRPC RPCs.
type OpenShellProviderReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// connectClient constructs a client for the gateway's gRPC API and a func
	// to release it. See gatewayClient/dialConnectClient (openshell_grpc.go);
	// tests override it to point at an in-process fake server.
	connectClient gatewayConnectFunc
}

// gatewayProviderName derives the gateway-side Provider name deterministically
// from the CR's namespace and name. OpenShellProvider is namespace-scoped,
// but the gateway addresses Provider objects only by (workspace, name) --
// workspace is always defaultOpenShellWorkspace. Using the CR's bare name
// would let two CRs of the same name in different k8s namespaces collide on
// the gateway and silently overwrite each other's credentials via the
// AlreadyExists->Update fallback below.
func gatewayProviderName(p *ogov1alpha1.OpenShellProvider) string {
	return p.Namespace + "." + p.Name
}

// +kubebuilder:rbac:groups=gateway.ogo.aknochow.io,resources=openshellproviders,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.ogo.aknochow.io,resources=openshellproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.ogo.aknochow.io,resources=openshellproviders/finalizers,verbs=update

// Reconcile ensures the credentials/config described by an OpenShellProvider
// CR match the remote gateway state.
func (r *OpenShellProviderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	provider := &ogov1alpha1.OpenShellProvider{}
	if err := r.Get(ctx, req.NamespacedName, provider); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !provider.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, provider)
	}

	if !controllerutil.ContainsFinalizer(provider, providerFinalizer) {
		controllerutil.AddFinalizer(provider, providerFinalizer)
		if err := r.Update(ctx, provider); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	return r.reconcileSync(ctx, provider)
}

// reconcileSync resolves the gateway and the referenced Secrets, then
// reconciles the provider's credentials/config to match spec.
func (r *OpenShellProviderReconciler) reconcileSync(ctx context.Context, provider *ogov1alpha1.OpenShellProvider) (ctrl.Result, error) {
	gw, err := resolveGateway(ctx, r.Client)
	if err != nil {
		return r.setNotReady(ctx, provider, "GatewayNotFound", err)
	}

	desiredCredentials := make(map[string]string, len(provider.Spec.Credentials))
	for envVar, secretRef := range provider.Spec.Credentials {
		secret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{Name: secretRef.Name, Namespace: provider.Namespace}, secret)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return r.setNotReady(ctx, provider, "SecretNotFound",
					fmt.Errorf("secret %q for credential %q not found", secretRef.Name, envVar))
			}
			return ctrl.Result{}, err
		}
		value, ok := secret.Data[secretRef.Key]
		if !ok {
			return r.setNotReady(ctx, provider, "KeyNotFound",
				fmt.Errorf("key %q not found in Secret %q", secretRef.Key, secretRef.Name))
		}
		desiredCredentials[envVar] = string(value)
	}
	desiredConfig := maps.Clone(provider.Spec.Config)
	if desiredConfig == nil {
		desiredConfig = map[string]string{}
	}

	// The gateway's credentials/config maps are upsert-or-leave-untouched on
	// update: a key with a non-empty value is set, a key omitted entirely is
	// left unchanged server-side. A key removed from spec (or whose backing
	// Secret key disappeared) must be explicitly retracted with an
	// empty-string value here, or it silently persists on the gateway
	// forever. Track this against the *desired* key sets computed above --
	// status is only updated to those sets on success, further down.
	payloadCredentials := maps.Clone(desiredCredentials)
	for _, key := range provider.Status.ReconciledCredentialKeys {
		if _, ok := desiredCredentials[key]; !ok {
			payloadCredentials[key] = ""
		}
	}
	payloadConfig := maps.Clone(desiredConfig)
	for _, key := range provider.Status.ReconciledConfigKeys {
		if _, ok := desiredConfig[key]; !ok {
			payloadConfig[key] = ""
		}
	}

	rpcCtx, wsClient, closeFn, err := gatewayClient(ctx, r.Client, r.connectClient, gw)
	if err != nil {
		return r.setNotReady(ctx, provider, "GatewayUnreachable", fmt.Errorf("connecting to gateway: %w", err))
	}
	defer closeFn()

	name := gatewayProviderName(provider)
	desired := &openshellclient.Provider{
		Metadata:    &openshellclient.ObjectMeta{Name: name},
		Type:        provider.Spec.ProviderType,
		Credentials: payloadCredentials,
		Config:      payloadConfig,
	}

	_, err = wsClient.CreateProvider(rpcCtx, &openshellclient.CreateProviderRequest{
		Provider: desired, Workspace: defaultOpenShellWorkspace,
	})
	if err != nil {
		switch grpcstatus.Code(err) {
		case codes.AlreadyExists:
			_, err = wsClient.UpdateProvider(rpcCtx, &openshellclient.UpdateProviderRequest{
				Provider: desired, Workspace: defaultOpenShellWorkspace,
			})
			if err != nil {
				if grpcstatus.Code(err) == codes.InvalidArgument {
					return r.setNotReady(ctx, provider, "InvalidProviderSpec", err)
				}
				return r.setNotReady(ctx, provider, "GatewayUnreachable", fmt.Errorf("UpdateProvider: %w", err))
			}
		case codes.InvalidArgument:
			return r.setNotReady(ctx, provider, "InvalidProviderSpec", err)
		default:
			return r.setNotReady(ctx, provider, "GatewayUnreachable", fmt.Errorf("CreateProvider: %w", err))
		}
	}

	provider.Status.ReconciledCredentialKeys = slices.Sorted(maps.Keys(desiredCredentials))
	provider.Status.ReconciledConfigKeys = slices.Sorted(maps.Keys(desiredConfig))
	provider.Status.Phase = "Synced"
	provider.Status.ObservedGeneration = provider.Generation
	meta.SetStatusCondition(&provider.Status.Conditions, metav1.Condition{
		Type: "Synced", Status: metav1.ConditionTrue, Reason: "Ready",
		Message: fmt.Sprintf("Provider %q of type %q is synced to the gateway", provider.Name, provider.Spec.ProviderType),
	})
	return ctrl.Result{RequeueAfter: requeueInterval}, r.Status().Update(ctx, provider)
}

// reconcileDelete removes the provider from the gateway before allowing the
// finalizer to be dropped and the CR to be deleted.
func (r *OpenShellProviderReconciler) reconcileDelete(ctx context.Context, provider *ogov1alpha1.OpenShellProvider) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(provider, providerFinalizer) {
		return ctrl.Result{}, nil
	}

	gw, err := resolveGateway(ctx, r.Client)
	if err != nil {
		// Don't drop the finalizer here -- that would permanently orphan the
		// remote provider if the gateway is only temporarily unreachable.
		log.Error(err, "no gateway found during cleanup, will retry")
		return ctrl.Result{RequeueAfter: gatewayRetryInterval}, nil
	}

	rpcCtx, wsClient, closeFn, err := gatewayClient(ctx, r.Client, r.connectClient, gw)
	if err != nil {
		log.Error(err, "failed to connect to gateway during cleanup, will retry")
		return ctrl.Result{RequeueAfter: gatewayRetryInterval}, nil
	}
	defer closeFn()

	_, err = wsClient.DeleteProvider(rpcCtx, &openshellclient.DeleteProviderRequest{
		Name: gatewayProviderName(provider), Workspace: defaultOpenShellWorkspace,
	})
	if err != nil {
		if grpcstatus.Code(err) == codes.FailedPrecondition {
			// The provider is still attached to one or more sandboxes -- the
			// gateway refuses to delete it. Don't drop the finalizer; this
			// needs the sandbox(es) detached first, not a retry of the same
			// call.
			meta.SetStatusCondition(&provider.Status.Conditions, metav1.Condition{
				Type: "Synced", Status: metav1.ConditionFalse, Reason: "DeletionBlocked",
				Message: err.Error(),
			})
			provider.Status.Phase = phaseFailed
			if updErr := r.Status().Update(ctx, provider); updErr != nil {
				return ctrl.Result{}, updErr
			}
			return ctrl.Result{RequeueAfter: gatewayRetryInterval}, nil
		}
		log.Error(err, "failed to delete provider on gateway, will retry")
		return ctrl.Result{RequeueAfter: gatewayRetryInterval}, nil
	}

	controllerutil.RemoveFinalizer(provider, providerFinalizer)
	return ctrl.Result{}, r.Update(ctx, provider)
}

// setNotReady records a Synced=False condition and a Failed phase. For
// GatewayUnreachable specifically, the underlying cause is logged
// server-side only; the CR's status gets a generic message instead.
func (r *OpenShellProviderReconciler) setNotReady(ctx context.Context, provider *ogov1alpha1.OpenShellProvider, reason string, cause error) (ctrl.Result, error) {
	message := cause.Error()
	if reason == "GatewayUnreachable" {
		logf.FromContext(ctx).Error(cause, "gateway unreachable while reconciling provider")
		message = "failed to reach the OpenShell gateway; see operator logs for details"
	}
	meta.SetStatusCondition(&provider.Status.Conditions, metav1.Condition{
		Type: "Synced", Status: metav1.ConditionFalse, Reason: reason, Message: message,
	})
	provider.Status.Phase = phaseFailed
	provider.Status.ObservedGeneration = provider.Generation
	if err := r.Status().Update(ctx, provider); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: gatewayRetryInterval}, nil
}

func (r *OpenShellProviderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ogov1alpha1.OpenShellProvider{}).
		Watches(&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findProvidersForSecret),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(&ogov1alpha1.OpenShellGateway{},
			handler.EnqueueRequestsFromMapFunc(r.findProvidersForGateway),
		).
		Named("openshellprovider").
		Complete(r)
}

func (r *OpenShellProviderReconciler) findProvidersForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	providers := &ogov1alpha1.OpenShellProviderList{}
	if err := r.List(ctx, providers, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(providers.Items))
	for _, p := range providers.Items {
		for _, ref := range p.Spec.Credentials {
			if ref.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: p.Name, Namespace: p.Namespace},
				})
				break
			}
		}
	}
	return requests
}

func (r *OpenShellProviderReconciler) findProvidersForGateway(ctx context.Context, _ client.Object) []reconcile.Request {
	providers := &ogov1alpha1.OpenShellProviderList{}
	if err := r.List(ctx, providers); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(providers.Items))
	for _, p := range providers.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: p.Name, Namespace: p.Namespace},
		})
	}
	return requests
}
