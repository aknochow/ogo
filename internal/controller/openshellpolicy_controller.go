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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
)

// OpenShellPolicyReconciler reconciles OpenShellPolicy objects.
// v0.1: validates the policy spec and reports status.
// v0.2: syncs policies to the gateway via gRPC.
type OpenShellPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ogo.ogo.io,resources=openshellpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ogo.ogo.io,resources=openshellpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ogo.ogo.io,resources=openshellpolicies/finalizers,verbs=update

func (r *OpenShellPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	policy := &ogov1alpha1.OpenShellPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Resolve the singleton gateway
	gwList := &ogov1alpha1.OpenShellGatewayList{}
	if err := r.List(ctx, gwList); err != nil {
		return ctrl.Result{}, err
	}
	if len(gwList.Items) == 0 {
		r.setPolicyCondition(policy, "Synced", metav1.ConditionFalse,
			"NoGateway", "No OpenShellGateway found in the cluster")
		policy.Status.Phase = "Pending"
		return ctrl.Result{}, r.Status().Update(ctx, policy)
	}

	// Validate policy spec
	if policy.Spec.PolicyName == "" {
		r.setPolicyCondition(policy, "Synced", metav1.ConditionFalse,
			"InvalidSpec", "policyName is required")
		policy.Status.Phase = "Failed"
		return ctrl.Result{}, r.Status().Update(ctx, policy)
	}

	endpointCount := 0
	for _, rule := range policy.Spec.Network {
		endpointCount += len(rule.Endpoints)
	}

	log.Info("Policy validated", "policy", policy.Spec.PolicyName,
		"network_rules", len(policy.Spec.Network), "endpoints", endpointCount)

	// v0.2: sync to gateway via gRPC UpdateConfig/policy APIs

	r.setPolicyCondition(policy, "Synced", metav1.ConditionTrue,
		"Ready", "Policy validated")
	policy.Status.Phase = "Synced"
	policy.Status.ObservedGeneration = policy.Generation

	return ctrl.Result{}, r.Status().Update(ctx, policy)
}

func (r *OpenShellPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ogov1alpha1.OpenShellPolicy{}).
		Named("openshellpolicy").
		Complete(r)
}

func (r *OpenShellPolicyReconciler) setPolicyCondition(policy *ogov1alpha1.OpenShellPolicy, condType string, status metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
	for i, c := range policy.Status.Conditions {
		if c.Type == condType {
			if c.Status != status {
				policy.Status.Conditions[i] = condition
			}
			return
		}
	}
	policy.Status.Conditions = append(policy.Status.Conditions, condition)
}
