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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/controller-runtime/pkg/client"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	"github.com/aknochow/ogo/internal/openshift"
)

const (
	componentEnvoyProxySCC = "envoy-proxy-scc"
	envoyGatewaySystemNS   = "envoy-gateway-system"
)

type EnvoyProxySCCReconciler struct {
	client.Client
	DiscoveryClient discovery.DiscoveryInterface
}

func (e *EnvoyProxySCCReconciler) Name() string { return "EnvoyProxySCCReady" }

func (e *EnvoyProxySCCReconciler) Enabled(_ context.Context, gw *ogov1alpha1.OpenShellGateway) bool {
	if !openshift.IsOpenShift(e.DiscoveryClient) {
		return false
	}
	if gw.Spec.Route.GatewayAPI.Enabled != nil && !*gw.Spec.Route.GatewayAPI.Enabled {
		return false
	}
	if gw.Spec.Route.GatewayAPI.Enabled == nil {
		return openshift.HasGatewayAPI(e.DiscoveryClient)
	}
	return true
}

func (e *EnvoyProxySCCReconciler) Reconcile(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (metav1.Condition, error) {
	log := logf.FromContext(ctx)
	labels := ownershipLabels(componentEnvoyProxySCC, gw)

	saList := &corev1.ServiceAccountList{}
	if err := e.List(ctx, saList, client.InNamespace(envoyGatewaySystemNS)); err != nil {
		return metav1.Condition{
			Type: ogov1alpha1.ConditionEnvoyProxySCCReady, Status: metav1.ConditionFalse,
			Reason: "ListFailed", Message: fmt.Sprintf("Failed to list ServiceAccounts: %v", err),
		}, err
	}

	granted := 0
	for i := range saList.Items {
		sa := &saList.Items[i]
		if sa.Name == "default" || sa.Name == "envoy-gateway" || sa.Name == "envoy-gateway-certgen" {
			continue
		}

		bindingName := "envoy-proxy-" + sa.Name + "-privileged"
		existing := &unstructured.Unstructured{}
		existing.SetGroupVersionKind(clusterRoleBindingGVK)
		if err := e.Get(ctx, types.NamespacedName{Name: bindingName}, existing); err == nil {
			granted++
			continue
		}

		crb := &unstructured.Unstructured{}
		crb.SetGroupVersionKind(clusterRoleBindingGVK)
		crb.SetName(bindingName)
		crb.SetLabels(labels)
		crb.Object["roleRef"] = map[string]interface{}{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "ClusterRole",
			"name":     "system:openshift:scc:privileged",
		}
		crb.Object["subjects"] = []interface{}{
			map[string]interface{}{
				"kind":      "ServiceAccount",
				"name":      sa.Name,
				"namespace": envoyGatewaySystemNS,
			},
		}
		if err := e.Create(ctx, crb); err != nil && !errors.IsAlreadyExists(err) {
			return metav1.Condition{
				Type: ogov1alpha1.ConditionEnvoyProxySCCReady, Status: metav1.ConditionFalse,
				Reason: "GrantFailed", Message: fmt.Sprintf("Failed to grant SCC to %s: %v", sa.Name, err),
			}, err
		}
		log.Info("Granted privileged SCC to Envoy proxy SA", "sa", sa.Name)
		granted++
	}

	msg := "No Envoy proxy ServiceAccounts found yet"
	if granted > 0 {
		msg = fmt.Sprintf("Granted privileged SCC to %d Envoy proxy ServiceAccount(s)", granted)
	}
	return metav1.Condition{
		Type: ogov1alpha1.ConditionEnvoyProxySCCReady, Status: metav1.ConditionTrue,
		Reason: "Granted", Message: msg,
	}, nil
}

func (e *EnvoyProxySCCReconciler) Cleanup(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	log := logf.FromContext(ctx)
	labels := ownershipLabels(componentEnvoyProxySCC, gw)

	crbList := &unstructured.UnstructuredList{}
	crbList.SetGroupVersionKind(clusterRoleBindingGVK)
	if err := e.List(ctx, crbList, client.MatchingLabels(labels)); err != nil {
		log.Error(err, "Failed to list SCC bindings for cleanup")
		return nil
	}

	for i := range crbList.Items {
		crb := &crbList.Items[i]
		if isOwnedByOGO(crb.GetLabels()) {
			if err := e.Delete(ctx, crb); err != nil && !errors.IsNotFound(err) {
				log.Error(err, "Failed to delete SCC binding", "name", crb.GetName())
			}
		}
	}
	return nil
}
