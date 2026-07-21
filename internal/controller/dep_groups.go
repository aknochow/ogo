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
	"k8s.io/client-go/discovery"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/controller-runtime/pkg/client"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	"github.com/aknochow/ogo/internal/openshift"
)

const componentGroups = "openshift-groups"

var groupGVK = schema.GroupVersionKind{Group: "user.openshift.io", Version: "v1", Kind: "Group"}

type GroupsReconciler struct {
	client.Client
	DiscoveryClient discovery.DiscoveryInterface
}

func (g *GroupsReconciler) Name() string { return "OpenShiftGroupsReady" }

func (g *GroupsReconciler) Enabled(_ context.Context, gw *ogov1alpha1.OpenShellGateway) bool {
	if !openshift.HasGroupsAPI(g.DiscoveryClient) {
		return false
	}
	if gw.Spec.Auth.OpenShift.AutoCreateGroups != nil && !*gw.Spec.Auth.OpenShift.AutoCreateGroups {
		return false
	}
	return gw.Spec.Auth.OpenShift.UserGroup != ""
}

func (g *GroupsReconciler) Reconcile(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (metav1.Condition, error) {
	log := logf.FromContext(ctx)
	labels := ownershipLabels(componentGroups, gw)
	created := []string{}

	for _, groupName := range g.groupNames(gw) {
		if groupName == "" {
			continue
		}
		existing := &unstructured.Unstructured{}
		existing.SetGroupVersionKind(groupGVK)
		err := g.Get(ctx, types.NamespacedName{Name: groupName}, existing)
		if err == nil {
			continue
		}
		if !errors.IsNotFound(err) {
			return metav1.Condition{
				Type: ogov1alpha1.ConditionOpenShiftGroups, Status: metav1.ConditionFalse,
				Reason: "CheckFailed", Message: fmt.Sprintf("Failed to check group %s: %v", groupName, err),
			}, err
		}

		group := &unstructured.Unstructured{}
		group.SetGroupVersionKind(groupGVK)
		group.SetName(groupName)
		group.SetLabels(labels)
		group.Object["users"] = []interface{}{}
		if err := g.Create(ctx, group); err != nil {
			return metav1.Condition{
				Type: ogov1alpha1.ConditionOpenShiftGroups, Status: metav1.ConditionFalse,
				Reason: "CreateFailed", Message: fmt.Sprintf("Failed to create group %s: %v", groupName, err),
			}, err
		}
		log.Info("Created OpenShift group", "group", groupName)
		created = append(created, groupName)
	}

	msg := "Groups exist"
	reason := "PreExisting"
	if len(created) > 0 {
		msg = fmt.Sprintf("Created groups: %v", created)
		reason = "Created"
	}

	return metav1.Condition{
		Type: ogov1alpha1.ConditionOpenShiftGroups, Status: metav1.ConditionTrue,
		Reason: reason, Message: msg,
	}, nil
}

func (g *GroupsReconciler) Cleanup(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	log := logf.FromContext(ctx)
	for _, groupName := range g.groupNames(gw) {
		if groupName == "" {
			continue
		}
		existing := &unstructured.Unstructured{}
		existing.SetGroupVersionKind(groupGVK)
		if err := g.Get(ctx, types.NamespacedName{Name: groupName}, existing); err == nil {
			if isOwnedByOGO(existing.GetLabels()) {
				if err := g.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
					log.Error(err, "Failed to delete group", "group", groupName)
				}
			}
		}
	}
	return nil
}

func (g *GroupsReconciler) groupNames(gw *ogov1alpha1.OpenShellGateway) []string {
	return []string{gw.Spec.Auth.OpenShift.UserGroup, gw.Spec.Auth.OpenShift.AdminGroup}
}
