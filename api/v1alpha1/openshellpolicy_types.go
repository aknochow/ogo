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

// OpenShellPolicySpec defines a sandbox policy template for the OpenShell Gateway.
type OpenShellPolicySpec struct {
	// PolicyName is the name of the policy in the gateway.
	PolicyName string `json:"policyName"`

	// Filesystem defines filesystem access policies.
	// +optional
	Filesystem *FilesystemPolicy `json:"filesystem,omitempty"`

	// Network defines network access policies keyed by policy name.
	// +optional
	Network map[string]NetworkPolicyRule `json:"network,omitempty"`

	// Process defines process execution policies.
	// +optional
	Process *ProcessPolicy `json:"process,omitempty"`
}

// FilesystemPolicy defines filesystem access boundaries for sandboxes.
type FilesystemPolicy struct {
	// IncludeWorkdir includes the sandbox working directory in the filesystem policy.
	IncludeWorkdir bool `json:"includeWorkdir,omitempty"`

	// ReadOnly paths accessible to the sandbox.
	ReadOnly []string `json:"readOnly,omitempty"`

	// ReadWrite paths accessible to the sandbox.
	ReadWrite []string `json:"readWrite,omitempty"`
}

// NetworkPolicyRule defines a named set of network access rules.
type NetworkPolicyRule struct {
	// Name is the display name for this network policy rule.
	Name string `json:"name"`

	// Endpoints defines allowed network endpoints.
	// +optional
	Endpoints []NetworkEndpoint `json:"endpoints,omitempty"`

	// Binaries defines which binaries can access these endpoints.
	// +optional
	Binaries []NetworkBinary `json:"binaries,omitempty"`
}

// NetworkEndpoint defines an allowed network destination.
type NetworkEndpoint struct {
	// Host is the hostname or glob pattern (e.g. "*.example.com").
	Host string `json:"host"`

	// Port is the destination port.
	Port int32 `json:"port"`

	// Protocol is the application protocol (rest, websocket, graphql, sql, or empty for L4).
	// +kubebuilder:validation:Enum=rest;websocket;graphql;sql;""
	Protocol string `json:"protocol,omitempty"`

	// Enforcement is the enforcement mode (enforce or audit).
	// +kubebuilder:default="enforce"
	// +kubebuilder:validation:Enum=enforce;audit
	Enforcement string `json:"enforcement,omitempty"`

	// Access is the access shorthand (read-only, read-write, full).
	// +kubebuilder:validation:Enum=read-only;read-write;full
	Access string `json:"access,omitempty"`
}

// NetworkBinary defines an allowed binary for network access.
type NetworkBinary struct {
	// Path is the absolute path to the binary.
	Path string `json:"path"`
}

// ProcessPolicy defines process execution constraints.
type ProcessPolicy struct {
	// RunAsUser is the user name or UID to run sandbox processes as.
	RunAsUser string `json:"runAsUser,omitempty"`

	// RunAsGroup is the group name or GID to run sandbox processes as.
	RunAsGroup string `json:"runAsGroup,omitempty"`
}

// OpenShellPolicyStatus defines the observed state of the policy.
type OpenShellPolicyStatus struct {
	// Phase is the current lifecycle phase.
	// +kubebuilder:validation:Enum=Pending;Synced;Failed
	Phase string `json:"phase,omitempty"`

	// ObservedGeneration is the latest generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Policy",type=string,JSONPath=`.spec.policyName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// OpenShellPolicy defines a sandbox policy template for the OpenShell Gateway.
type OpenShellPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenShellPolicySpec   `json:"spec,omitempty"`
	Status OpenShellPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OpenShellPolicyList contains a list of OpenShellPolicy.
type OpenShellPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenShellPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OpenShellPolicy{}, &OpenShellPolicyList{})
}
