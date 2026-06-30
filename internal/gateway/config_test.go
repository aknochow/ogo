/*
Copyright 2026 Adam Knochowski.

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

package gateway

import (
	"strings"
	"testing"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestRenderGatewayTOML_Defaults(t *testing.T) {
	gw := &ogov1alpha1.OpenShellGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "openshell"},
		Spec: ogov1alpha1.OpenShellGatewaySpec{
			Namespace: "openshell",
			Database:  ogov1alpha1.DatabaseSpec{SecretName: "pg-uri"},
		},
	}

	toml := RenderGatewayTOML(gw, "openshell")

	checks := []string{
		`[openshell]`,
		`version = 1`,
		`bind_address          = "0.0.0.0:8080"`,
		`health_bind_address   = "0.0.0.0:8081"`,
		`metrics_bind_address  = "0.0.0.0:9090"`,
		`log_level             = "info"`,
		`sandbox_namespace     = "openshell"`,
		`default_image         = "ghcr.io/nvidia/openshell-community/sandboxes/base:latest"`,
		`supervisor_image      = "ghcr.io/nvidia/openshell/supervisor"`,
		`[openshell.gateway.tls]`,
		`cert_path             = "/etc/openshell-tls/server/tls.crt"`,
		`[openshell.gateway.gateway_jwt]`,
		`gateway_id       = "openshell"`,
		`[openshell.drivers.kubernetes]`,
		`grpc_endpoint                = "https://openshell.openshell.svc.cluster.local:8080"`,
		`service_account_name         = "openshell-sandbox"`,
		`supervisor_sideload_method   = "init-container"`,
	}

	for _, check := range checks {
		if !strings.Contains(toml, check) {
			t.Errorf("TOML missing expected content: %q", check)
		}
	}

	if strings.Contains(toml, "disable_tls") {
		t.Error("TLS should be enabled by default, but found disable_tls")
	}
	if strings.Contains(toml, "allow_unauthenticated") {
		t.Error("Auth should require authentication by default")
	}
}

func TestRenderGatewayTOML_TLSDisabled(t *testing.T) {
	gw := &ogov1alpha1.OpenShellGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "openshell"},
		Spec: ogov1alpha1.OpenShellGatewaySpec{
			Namespace: "openshell",
			Database:  ogov1alpha1.DatabaseSpec{SecretName: "pg-uri"},
			TLS:       ogov1alpha1.TLSSpec{Enabled: ptr.To(false)},
			Auth:      ogov1alpha1.AuthSpec{AllowUnauthenticated: true},
		},
	}

	toml := RenderGatewayTOML(gw, "openshell")

	if !strings.Contains(toml, `disable_tls            = true`) {
		t.Error("Expected disable_tls = true when TLS disabled")
	}
	if strings.Contains(toml, "[openshell.gateway.tls]") {
		t.Error("TLS section should not be present when TLS disabled")
	}
	if !strings.Contains(toml, `allow_unauthenticated_users = true`) {
		t.Error("Expected allow_unauthenticated_users = true")
	}
	if !strings.Contains(toml, `"http://openshell.openshell.svc.cluster.local:8080"`) {
		t.Error("Expected http:// scheme when TLS disabled")
	}
}

func TestRenderGatewayTOML_CustomSandbox(t *testing.T) {
	gw := &ogov1alpha1.OpenShellGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "openshell"},
		Spec: ogov1alpha1.OpenShellGatewaySpec{
			Namespace: "openshell",
			Database:  ogov1alpha1.DatabaseSpec{SecretName: "pg-uri"},
			Sandbox: ogov1alpha1.SandboxSpec{
				DefaultImage:         "custom-image:v1",
				WorkspaceStorageSize: "10Gi",
				RuntimeClassName:     "kata",
				AppArmorProfile:      "RuntimeDefault",
			},
		},
	}

	toml := RenderGatewayTOML(gw, "sandbox-ns")

	if !strings.Contains(toml, `sandbox_namespace     = "sandbox-ns"`) {
		t.Error("Expected custom sandbox namespace")
	}
	if !strings.Contains(toml, `default_image         = "custom-image:v1"`) {
		t.Error("Expected custom sandbox image")
	}
	if !strings.Contains(toml, `workspace_default_storage_size = "10Gi"`) {
		t.Error("Expected custom workspace size")
	}
	if !strings.Contains(toml, `default_runtime_class_name   = "kata"`) {
		t.Error("Expected custom runtime class")
	}
	if !strings.Contains(toml, `app_armor_profile            = "RuntimeDefault"`) {
		t.Error("Expected custom apparmor profile")
	}
}

func TestRenderGatewayTOML_WithOIDC(t *testing.T) {
	gw := &ogov1alpha1.OpenShellGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "openshell"},
		Spec: ogov1alpha1.OpenShellGatewaySpec{
			Namespace: "ogo",
			Database:  ogov1alpha1.DatabaseSpec{SecretName: "pg-uri"},
			TLS:       ogov1alpha1.TLSSpec{Enabled: ptr.To(false)},
		},
	}

	toml := RenderGatewayTOML(gw, "ogo", "http://localhost:8085")

	if !strings.Contains(toml, `[openshell.gateway.oidc]`) {
		t.Error("Expected OIDC section")
	}
	if !strings.Contains(toml, `issuer        = "http://localhost:8085"`) {
		t.Error("Expected OIDC issuer")
	}
	if !strings.Contains(toml, `audience      = "openshell-cli"`) {
		t.Error("Expected OIDC audience")
	}
}
