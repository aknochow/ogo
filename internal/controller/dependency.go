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

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
)

const (
	labelProvisionedBy = "ogo.aknochow.io/provisioned-by"
	labelComponent     = "ogo.aknochow.io/component"
	labelGatewayName   = "ogo.aknochow.io/gateway-name"
	provisionedByOGO   = "ogo"
)

type DependencyReconciler interface {
	Name() string
	Enabled(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) bool
	Reconcile(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (metav1.Condition, error)
	Cleanup(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error
}

func ownershipLabels(component string, gw *ogov1alpha1.OpenShellGateway) map[string]string {
	return map[string]string{
		labelProvisionedBy: provisionedByOGO,
		labelComponent:     component,
		labelGatewayName:   gw.Name,
		labelManagedBy:     managedByValue,
	}
}

func isOwnedByOGO(labels map[string]string) bool {
	return labels != nil && labels[labelProvisionedBy] == provisionedByOGO
}
