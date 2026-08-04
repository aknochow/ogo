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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ServiceAccountReference identifies a workload identity by ServiceAccount name and namespace.
type ServiceAccountReference struct {
	// Name of the ServiceAccount.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace of the ServiceAccount. Defaults to the OpenShellWorkspaceMember's own namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// OpenShellWorkspaceMemberSpec defines the intended OpenShell workspace membership for a workload identity.
type OpenShellWorkspaceMemberSpec struct {
	// Workspace is the OpenShell workspace name to grant membership in.
	// +kubebuilder:validation:MinLength=1
	Workspace string `json:"workspace"`

	// ServiceAccountRef identifies the workload identity to reconcile membership for.
	ServiceAccountRef ServiceAccountReference `json:"serviceAccountRef"`

	// Role is the workspace-scoped role to grant.
	// +kubebuilder:validation:Enum=user;admin
	// +kubebuilder:default=user
	Role string `json:"role,omitempty"`
}

// OpenShellWorkspaceMemberStatus defines the observed state of the workspace membership.
type OpenShellWorkspaceMemberStatus struct {
	// Phase is the current lifecycle phase.
	// +kubebuilder:validation:Enum=Pending;Synced;Failed
	Phase string `json:"phase,omitempty"`

	// ObservedGeneration is the latest generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ReconciledSubject is the ServiceAccount UID last successfully added as
	// the workspace member's principal subject. Used to detect ServiceAccount
	// recreation (a UID change) so stale membership can be removed instead of
	// silently transferring access to the replacement identity.
	ReconciledSubject string `json:"reconciledSubject,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Workspace",type=string,JSONPath=`.spec.workspace`
// +kubebuilder:printcolumn:name="ServiceAccount",type=string,JSONPath=`.spec.serviceAccountRef.name`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.role`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// OpenShellWorkspaceMember reconciles OpenShell workspace membership for a workload identity.
type OpenShellWorkspaceMember struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenShellWorkspaceMemberSpec   `json:"spec,omitempty"`
	Status OpenShellWorkspaceMemberStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OpenShellWorkspaceMemberList contains a list of OpenShellWorkspaceMember.
type OpenShellWorkspaceMemberList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenShellWorkspaceMember `json:"items"`
}
