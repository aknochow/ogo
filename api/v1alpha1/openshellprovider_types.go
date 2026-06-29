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

// OpenShellProviderSpec defines credential bundles for the OpenShell Gateway.
type OpenShellProviderSpec struct {
	// ProviderType is the canonical provider profile slug (e.g. "claude-code", "github", "nvidia").
	// +kubebuilder:validation:MinLength=1
	ProviderType string `json:"providerType"`

	// Credentials maps environment variable names to Secret key references.
	// Each entry injects the Secret value as the named env var into sandbox pods.
	// +optional
	Credentials map[string]SecretKeyRef `json:"credentials,omitempty"`

	// Config holds non-secret provider configuration key-value pairs.
	// +optional
	Config map[string]string `json:"config,omitempty"`
}

// SecretKeyRef references a key within a Secret.
type SecretKeyRef struct {
	// Name of the Secret.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Key within the Secret data.
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// OpenShellProviderStatus defines the observed state of the provider.
type OpenShellProviderStatus struct {
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
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.providerType`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// OpenShellProvider defines a credential bundle for the OpenShell Gateway.
type OpenShellProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenShellProviderSpec   `json:"spec,omitempty"`
	Status OpenShellProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OpenShellProviderList contains a list of OpenShellProvider.
type OpenShellProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenShellProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OpenShellProvider{}, &OpenShellProviderList{})
}
