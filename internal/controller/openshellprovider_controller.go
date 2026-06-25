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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
)

// OpenShellProviderReconciler reconciles OpenShellProvider objects.
// v0.1: resolves credentials from Secrets and reports status.
// v0.2: syncs providers to the gateway via gRPC.
type OpenShellProviderReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=gateway.ogo.io,resources=openshellproviders,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.ogo.io,resources=openshellproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.ogo.io,resources=openshellproviders/finalizers,verbs=update

func (r *OpenShellProviderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	provider := &ogov1alpha1.OpenShellProvider{}
	if err := r.Get(ctx, req.NamespacedName, provider); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Resolve the singleton gateway
	gwList := &ogov1alpha1.OpenShellGatewayList{}
	if err := r.List(ctx, gwList); err != nil {
		return ctrl.Result{}, err
	}
	if len(gwList.Items) == 0 {
		r.setProviderCondition(provider, "Synced", metav1.ConditionFalse,
			"NoGateway", "No OpenShellGateway found in the cluster")
		provider.Status.Phase = "Pending"
		return ctrl.Result{}, r.Status().Update(ctx, provider)
	}

	// Validate credential references
	for envVar, secretRef := range provider.Spec.Credentials {
		secret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      secretRef.Name,
			Namespace: req.Namespace,
		}, secret)
		if err != nil {
			if apierrors.IsNotFound(err) {
				r.setProviderCondition(provider, "Synced", metav1.ConditionFalse,
					"SecretNotFound", fmt.Sprintf("Secret %q for credential %q not found", secretRef.Name, envVar))
				provider.Status.Phase = "Failed"
				return ctrl.Result{}, r.Status().Update(ctx, provider)
			}
			return ctrl.Result{}, err
		}
		if _, ok := secret.Data[secretRef.Key]; !ok {
			r.setProviderCondition(provider, "Synced", metav1.ConditionFalse,
				"KeyNotFound", fmt.Sprintf("Key %q not found in Secret %q", secretRef.Key, secretRef.Name))
			provider.Status.Phase = "Failed"
			return ctrl.Result{}, r.Status().Update(ctx, provider)
		}
	}

	log.Info("Provider credentials validated", "provider", provider.Spec.ProviderType,
		"credentials", len(provider.Spec.Credentials))

	// v0.2: sync to gateway via gRPC CreateProvider/UpdateProvider

	r.setProviderCondition(provider, "Synced", metav1.ConditionTrue,
		"Ready", "Provider credentials validated")
	provider.Status.Phase = "Synced"
	provider.Status.ObservedGeneration = provider.Generation

	return ctrl.Result{}, r.Status().Update(ctx, provider)
}

func (r *OpenShellProviderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ogov1alpha1.OpenShellProvider{}).
		Watches(&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findProvidersForSecret),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Named("openshellprovider").
		Complete(r)
}

func (r *OpenShellProviderReconciler) findProvidersForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	providers := &ogov1alpha1.OpenShellProviderList{}
	if err := r.List(ctx, providers, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, p := range providers.Items {
		for _, ref := range p.Spec.Credentials {
			if ref.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      p.Name,
						Namespace: p.Namespace,
					},
				})
				break
			}
		}
	}
	return requests
}

func (r *OpenShellProviderReconciler) setProviderCondition(provider *ogov1alpha1.OpenShellProvider, condType string, status metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
	for i, c := range provider.Status.Conditions {
		if c.Type == condType {
			if c.Status != status {
				provider.Status.Conditions[i] = condition
			}
			return
		}
	}
	provider.Status.Conditions = append(provider.Status.Conditions, condition)
}
