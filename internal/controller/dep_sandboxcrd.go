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

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"bytes"

	"sigs.k8s.io/controller-runtime/pkg/client"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	"github.com/aknochow/ogo/internal/manifests/sandbox"
)

const componentSandboxCRD = "sandbox-crd"

var sandboxCRDGVK = schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}

type SandboxCRDReconciler struct {
	client.Client
}

func (s *SandboxCRDReconciler) Name() string { return "SandboxCRDReady" }

func (s *SandboxCRDReconciler) Enabled(_ context.Context, _ *ogov1alpha1.OpenShellGateway) bool {
	return true
}

func (s *SandboxCRDReconciler) Reconcile(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (metav1.Condition, error) {
	log := logf.FromContext(ctx)

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(sandboxCRDGVK)
	err := s.Get(ctx, types.NamespacedName{Name: "sandboxes.agents.x-k8s.io"}, existing)
	if err == nil {
		return metav1.Condition{
			Type: ogov1alpha1.ConditionSandboxCRD, Status: metav1.ConditionTrue,
			Reason: "Exists", Message: "Sandbox CRD already installed",
		}, nil
	}
	if !errors.IsNotFound(err) && !isNoKindMatch(err) {
		return metav1.Condition{
			Type: ogov1alpha1.ConditionSandboxCRD, Status: metav1.ConditionFalse,
			Reason: "CheckFailed", Message: fmt.Sprintf("Failed to check Sandbox CRD: %v", err),
		}, err
	}

	obj := &unstructured.Unstructured{}
	if err := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(sandbox.CRD), len(sandbox.CRD)).Decode(obj); err != nil {
		return metav1.Condition{
			Type: ogov1alpha1.ConditionSandboxCRD, Status: metav1.ConditionFalse,
			Reason: "DecodeFailed", Message: fmt.Sprintf("Failed to decode Sandbox CRD: %v", err),
		}, err
	}

	labels := ownershipLabels(componentSandboxCRD, gw)
	existingLabels := obj.GetLabels()
	if existingLabels == nil {
		existingLabels = make(map[string]string)
	}
	for k, v := range labels {
		existingLabels[k] = v
	}
	obj.SetLabels(existingLabels)

	if err := s.Create(ctx, obj); err != nil {
		if errors.IsAlreadyExists(err) {
			return metav1.Condition{
				Type: ogov1alpha1.ConditionSandboxCRD, Status: metav1.ConditionTrue,
				Reason: "Exists", Message: "Sandbox CRD already installed",
			}, nil
		}
		return metav1.Condition{
			Type: ogov1alpha1.ConditionSandboxCRD, Status: metav1.ConditionFalse,
			Reason: "InstallFailed", Message: fmt.Sprintf("Failed to install Sandbox CRD: %v", err),
		}, err
	}

	log.Info("Installed Sandbox CRD (agents.x-k8s.io/sandboxes)")
	return metav1.Condition{
		Type: ogov1alpha1.ConditionSandboxCRD, Status: metav1.ConditionTrue,
		Reason: "Installed", Message: "Sandbox CRD installed by OGO",
	}, nil
}

func (s *SandboxCRDReconciler) Cleanup(ctx context.Context, _ *ogov1alpha1.OpenShellGateway) error {
	logf.FromContext(ctx).Info("Sandbox CRD cleanup skipped — shared cluster resource")
	return nil
}
