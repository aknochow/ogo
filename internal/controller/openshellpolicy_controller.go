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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
)

type OpenShellPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=gateway.ogo.aknochow.io,resources=openshellpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.ogo.aknochow.io,resources=openshellpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.ogo.aknochow.io,resources=openshellpolicies/finalizers,verbs=update

func (r *OpenShellPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	policy := &ogov1alpha1.OpenShellPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	gwList := &ogov1alpha1.OpenShellGatewayList{}
	if err := r.List(ctx, gwList); err != nil {
		return ctrl.Result{}, err
	}
	if len(gwList.Items) == 0 {
		meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
			Type: "Synced", Status: metav1.ConditionFalse,
			Reason: "NoGateway", Message: "No OpenShellGateway found in the cluster",
		})
		policy.Status.Phase = "Pending"
		return ctrl.Result{}, r.Status().Update(ctx, policy)
	}

	if policy.Spec.PolicyName == "" {
		meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
			Type: "Synced", Status: metav1.ConditionFalse,
			Reason: "InvalidSpec", Message: "policyName is required",
		})
		policy.Status.Phase = phaseFailed
		return ctrl.Result{}, r.Status().Update(ctx, policy)
	}

	endpointCount := 0
	for _, rule := range policy.Spec.Network {
		endpointCount += len(rule.Endpoints)
	}

	log.Info("Policy validated", "policy", policy.Spec.PolicyName,
		"network_rules", len(policy.Spec.Network), "endpoints", endpointCount)

	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type: "Synced", Status: metav1.ConditionTrue,
		Reason: "Ready", Message: "Policy validated",
	})
	policy.Status.Phase = "Synced"
	policy.Status.ObservedGeneration = policy.Generation

	return ctrl.Result{}, r.Status().Update(ctx, policy)
}

func (r *OpenShellPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ogov1alpha1.OpenShellPolicy{}).
		Watches(&ogov1alpha1.OpenShellGateway{},
			handler.EnqueueRequestsFromMapFunc(r.findPoliciesForGateway),
		).
		Named("openshellpolicy").
		Complete(r)
}

func (r *OpenShellPolicyReconciler) findPoliciesForGateway(ctx context.Context, _ client.Object) []reconcile.Request {
	policies := &ogov1alpha1.OpenShellPolicyList{}
	if err := r.List(ctx, policies); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(policies.Items))
	for _, p := range policies.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: p.Name, Namespace: p.Namespace},
		})
	}
	return requests
}
