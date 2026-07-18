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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OpenShellGatewaySpec defines the desired state of the OpenShell Gateway.
type OpenShellGatewaySpec struct {
	// Namespace where the gateway Deployment and associated resources are created.
	// +kubebuilder:default="ogo"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Namespace string `json:"namespace,omitempty"`

	// Image is the gateway container image.
	// +kubebuilder:default="ghcr.io/nvidia/openshell/gateway"
	Image string `json:"image,omitempty"`

	// ImageTag overrides the default image tag for gateway and supervisor containers.
	ImageTag string `json:"imageTag,omitempty"`
	// TODO(aknochow): split into separate GatewayImageTag / SupervisorImageTag —
	// shared tag causes pull failures when overriding only the gateway image.

	// SupervisorImage is the supervisor container image sideloaded into sandbox pods.
	// +kubebuilder:default="ghcr.io/nvidia/openshell/supervisor"
	SupervisorImage string `json:"supervisorImage,omitempty"`

	// Replicas is the number of gateway pod replicas.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	Replicas *int32 `json:"replicas,omitempty"`

	// Database configures the PostgreSQL connection.
	Database DatabaseSpec `json:"database"`

	// Sandbox configures sandbox pod defaults.
	Sandbox SandboxSpec `json:"sandbox,omitempty"`

	// TLS configures TLS and mTLS.
	TLS TLSSpec `json:"tls,omitempty"`

	// Route configures the OpenShift Route for external access.
	Route RouteSpec `json:"route,omitempty"`

	// Auth configures authentication.
	Auth AuthSpec `json:"auth,omitempty"`

	// LogLevel sets the gateway log level.
	// +kubebuilder:default="info"
	// +kubebuilder:validation:Enum=trace;debug;info;warn;error
	LogLevel string `json:"logLevel,omitempty"`

	// Resources defines resource requests and limits for gateway pods.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// NetworkPolicy controls whether a NetworkPolicy restricting sandbox SSH is created.
	NetworkPolicy NetworkPolicySpec `json:"networkPolicy,omitempty"`
}

// DatabaseSpec configures the PostgreSQL database backend.
type DatabaseSpec struct {
	// SecretName references a Secret containing the PostgreSQL connection URI.
	// The Secret must have a key named "uri".
	// +kubebuilder:validation:MinLength=1
	SecretName string `json:"secretName"`
}

// SandboxSpec configures sandbox pod defaults.
type SandboxSpec struct {
	// Namespace where sandbox pods run. Defaults to the gateway namespace.
	Namespace string `json:"namespace,omitempty"`

	// DefaultImage is the default container image for sandbox pods.
	// +kubebuilder:default="ghcr.io/nvidia/openshell-community/sandboxes/base:latest"
	DefaultImage string `json:"defaultImage,omitempty"`

	// ImagePullPolicy for sandbox pod images.
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// WorkspaceStorageSize is the PVC size for sandbox workspace volumes (e.g. "2Gi", "10Gi").
	// +kubebuilder:default="2Gi"
	// +kubebuilder:validation:Pattern=`^[0-9]+(Ki|Mi|Gi|Ti)$`
	WorkspaceStorageSize string `json:"workspaceStorageSize,omitempty"`

	// RuntimeClassName is an optional RuntimeClass for sandbox pods (e.g. kata, gvisor).
	RuntimeClassName string `json:"runtimeClassName,omitempty"`

	// AppArmorProfile for sandbox pods.
	// +kubebuilder:default="Unconfined"
	AppArmorProfile string `json:"appArmorProfile,omitempty"`
}

// TLSSpec configures TLS certificate management.
type TLSSpec struct {
	// Enabled controls whether TLS is enabled.
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// CertManager configures cert-manager for server TLS certificates.
	// This is the recommended approach — uses your cluster's cert-manager
	// operator (e.g. with Let's Encrypt) for trusted server certificates.
	// Client mTLS certificates are always self-signed by the operator.
	CertManager CertManagerSpec `json:"certManager,omitempty"`

	// ServerCertSecretName references a pre-existing kubernetes.io/tls Secret for the server.
	// When set, the operator will not generate or request server certificates.
	ServerCertSecretName string `json:"serverCertSecretName,omitempty"`
}

// CertManagerSpec configures cert-manager integration for server TLS.
type CertManagerSpec struct {
	// Enabled uses cert-manager to issue server TLS certificates.
	Enabled bool `json:"enabled,omitempty"`

	// IssuerName is the name of the cert-manager ClusterIssuer or Issuer to use.
	// +kubebuilder:default="letsencrypt"
	IssuerName string `json:"issuerName,omitempty"`

	// IssuerKind is the kind of issuer (ClusterIssuer or Issuer).
	// +kubebuilder:default="ClusterIssuer"
	// +kubebuilder:validation:Enum=ClusterIssuer;Issuer
	IssuerKind string `json:"issuerKind,omitempty"`
}

// RouteSpec configures the OpenShift Route for external gRPC access.
type RouteSpec struct {
	// Enabled creates an OpenShift Route. Auto-detected on OpenShift when nil.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Hostname is the custom hostname for the Route (e.g. openshell.apps.example.com).
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`
	Hostname string `json:"hostname,omitempty"`

	// GatewayAPI configures Kubernetes Gateway API ingress (Gateway + GRPCRoute).
	// When enabled, the operator creates Gateway API resources and routes
	// through an Envoy Gateway proxy instead of directly via OpenShift Routes.
	GatewayAPI GatewayAPISpec `json:"gatewayAPI,omitempty"`
}

// GatewayAPISpec configures Kubernetes Gateway API ingress.
type GatewayAPISpec struct {
	// Enabled creates a Gateway and GRPCRoute for ingress.
	// Auto-detected when nil: enabled if Gateway API CRDs are installed.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// GatewayClassName is the name of the GatewayClass to use.
	// +kubebuilder:default="eg"
	// +kubebuilder:validation:MaxLength=253
	GatewayClassName string `json:"gatewayClassName,omitempty"`
}

// AuthSpec configures gateway authentication.
type AuthSpec struct {
	// AllowUnauthenticated enables unauthenticated access (dev mode only, not recommended).
	// +kubebuilder:default=false
	AllowUnauthenticated bool `json:"allowUnauthenticated,omitempty"`

	// OpenShift configures authentication via OpenShift's built-in OAuth server.
	// This is the default and recommended auth method on OpenShift.
	// The operator deploys an auth-bridge sidecar that translates OpenShift
	// OAuth tokens to standard OIDC JWTs for the gateway.
	OpenShift OpenShiftAuth `json:"openshift,omitempty"`
}

// OpenShiftAuth configures the auth-bridge for OpenShift OAuth integration.
type OpenShiftAuth struct {
	// Enabled activates OpenShift OAuth authentication.
	// Auto-detected on OpenShift when nil.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// UserGroup is the OpenShift group required for gateway access via SSO.
	// Only members of this group can authenticate. Users not in this group
	// are rejected with 403 at login. Does not affect mTLS access.
	// +kubebuilder:validation:MinLength=1
	UserGroup string `json:"userGroup"`

	// AdminGroup is the OpenShift group that maps to the openshell-admin role.
	// Users in this group get admin access to the gateway.
	AdminGroup string `json:"adminGroup,omitempty"`

	// TokenTTL is the duration (e.g. "8h", "24h") that OIDC tokens are valid.
	// +kubebuilder:default="8h"
	// +kubebuilder:validation:Pattern=`^[0-9]+(h|m|s)$`
	TokenTTL string `json:"tokenTTL,omitempty"`
}

// NetworkPolicySpec controls NetworkPolicy creation.
type NetworkPolicySpec struct {
	// Enabled creates a NetworkPolicy restricting SSH (port 2222) on sandbox pods to gateway pods only.
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`
}

// OpenShellGatewayStatus defines the observed state of the OpenShell Gateway.
type OpenShellGatewayStatus struct {
	// Phase is the current lifecycle phase of the gateway.
	// +kubebuilder:validation:Enum=Pending;Creating;Running;Failed
	Phase string `json:"phase,omitempty"`

	// GatewayURL is the external URL for accessing the gateway.
	GatewayURL string `json:"gatewayURL,omitempty"`

	// ClientCertSecretName is the name of the Secret containing the client mTLS certificate.
	// CI systems can extract this to authenticate with the gateway.
	ClientCertSecretName string `json:"clientCertSecretName,omitempty"`

	// ObservedGeneration is the latest generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the gateway's state.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types for OpenShellGateway.
const (
	ConditionAvailable     = "Available"
	ConditionProgressing   = "Progressing"
	ConditionDegraded      = "Degraded"
	ConditionSandboxCRD    = "SandboxCRDReady"
	ConditionTLSReady      = "TLSReady"
	ConditionDatabaseReady = "DatabaseReady"
)

// Phase values for OpenShellGateway.
const (
	PhasePending  = "Pending"
	PhaseCreating = "Creating"
	PhaseRunning  = "Running"
	PhaseFailed   = "Failed"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.gatewayURL`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// OpenShellGateway is a cluster-scoped singleton that manages an OpenShell Gateway instance.
type OpenShellGateway struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenShellGatewaySpec   `json:"spec,omitempty"`
	Status OpenShellGatewayStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OpenShellGatewayList contains a list of OpenShellGateway.
type OpenShellGatewayList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenShellGateway `json:"items"`
}
