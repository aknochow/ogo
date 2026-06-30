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
	"fmt"
	"strings"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
)

// RenderGatewayTOML generates gateway.toml from the CR spec.
// oidcIssuer is the auth-bridge URL; pass empty to skip the OIDC section.
func RenderGatewayTOML(gw *ogov1alpha1.OpenShellGateway, sandboxNS string, oidcIssuer ...string) string {
	var b strings.Builder

	tlsEnabled := gw.Spec.TLS.Enabled == nil || *gw.Spec.TLS.Enabled

	b.WriteString("[openshell]\nversion = 1\n\n")

	b.WriteString("[openshell.gateway]\n")
	b.WriteString("bind_address          = \"0.0.0.0:8080\"\n")
	b.WriteString("health_bind_address   = \"0.0.0.0:8081\"\n")
	b.WriteString("metrics_bind_address  = \"0.0.0.0:9090\"\n")
	fmt.Fprintf(&b, "log_level             = %q\n", logLevel(gw))
	fmt.Fprintf(&b, "sandbox_namespace     = %q\n", sandboxNS)
	fmt.Fprintf(&b, "default_image         = %q\n", defaultImage(gw))
	fmt.Fprintf(&b, "supervisor_image      = %q\n", supervisorImage(gw))
	b.WriteString("enable_loopback_service_http = true\n")

	if !tlsEnabled {
		b.WriteString("disable_tls            = true\n")
	} else {
		fmt.Fprintf(&b, "client_tls_secret_name = %q\n", gw.Name+"-client-tls")
	}

	if tlsEnabled {
		b.WriteString("\n[openshell.gateway.tls]\n")
		b.WriteString("cert_path             = \"/etc/openshell-tls/server/tls.crt\"\n")
		b.WriteString("key_path              = \"/etc/openshell-tls/server/tls.key\"\n")
		b.WriteString("client_ca_path        = \"/etc/openshell-tls/client-ca/ca.crt\"\n")
	}

	if gw.Spec.Auth.AllowUnauthenticated {
		b.WriteString("\n[openshell.gateway.auth]\n")
		b.WriteString("allow_unauthenticated_users = true\n")
	}

	if len(oidcIssuer) > 0 && oidcIssuer[0] != "" {
		b.WriteString("\n[openshell.gateway.oidc]\n")
		fmt.Fprintf(&b, "issuer        = %q\n", oidcIssuer[0])
		b.WriteString("audience      = \"openshell-cli\"\n")
		b.WriteString("jwks_ttl_secs = 300\n")
		b.WriteString("roles_claim   = \"realm_access.roles\"\n")
		b.WriteString("admin_role    = \"openshell-admin\"\n")
		b.WriteString("user_role     = \"openshell-user\"\n")
	}

	b.WriteString("\n[openshell.gateway.gateway_jwt]\n")
	b.WriteString("signing_key_path = \"/etc/openshell-jwt/signing.pem\"\n")
	b.WriteString("public_key_path  = \"/etc/openshell-jwt/public.pem\"\n")
	b.WriteString("kid_path         = \"/etc/openshell-jwt/kid\"\n")
	fmt.Fprintf(&b, "gateway_id       = %q\n", gw.Name)
	b.WriteString("ttl_secs         = 3600\n")

	scheme := "https"
	if !tlsEnabled {
		scheme = "http"
	}

	b.WriteString("\n[openshell.drivers.kubernetes]\n")
	gwNS := gw.Spec.Namespace
	if gwNS == "" {
		gwNS = "ogo"
	}
	fmt.Fprintf(&b, "grpc_endpoint                = %q\n",
		fmt.Sprintf("%s://%s.%s.svc.cluster.local:8080", scheme, gw.Name, gwNS))
	fmt.Fprintf(&b, "service_account_name         = %q\n", gw.Name+"-sandbox")
	b.WriteString("supervisor_sideload_method   = \"init-container\"\n")
	b.WriteString("sa_token_ttl_secs            = 3600\n")

	if gw.Spec.Sandbox.ImagePullPolicy != "" {
		fmt.Fprintf(&b, "image_pull_policy            = %q\n", string(gw.Spec.Sandbox.ImagePullPolicy))
	}
	if gw.Spec.Sandbox.WorkspaceStorageSize != "" {
		fmt.Fprintf(&b, "workspace_default_storage_size = %q\n", gw.Spec.Sandbox.WorkspaceStorageSize)
	}
	if gw.Spec.Sandbox.RuntimeClassName != "" {
		fmt.Fprintf(&b, "default_runtime_class_name   = %q\n", gw.Spec.Sandbox.RuntimeClassName)
	}
	if gw.Spec.Sandbox.AppArmorProfile != "" {
		fmt.Fprintf(&b, "app_armor_profile            = %q\n", gw.Spec.Sandbox.AppArmorProfile)
	}

	return b.String()
}

func logLevel(gw *ogov1alpha1.OpenShellGateway) string {
	if gw.Spec.LogLevel != "" {
		return gw.Spec.LogLevel
	}
	return "info"
}

func defaultImage(gw *ogov1alpha1.OpenShellGateway) string {
	if gw.Spec.Sandbox.DefaultImage != "" {
		return gw.Spec.Sandbox.DefaultImage
	}
	return "ghcr.io/nvidia/openshell-community/sandboxes/base:latest"
}

func supervisorImage(gw *ogov1alpha1.OpenShellGateway) string {
	img := gw.Spec.SupervisorImage
	if img == "" {
		img = "ghcr.io/nvidia/openshell/supervisor"
	}
	if gw.Spec.ImageTag != "" && !strings.Contains(img, ":") {
		img = img + ":" + gw.Spec.ImageTag
	}
	return img
}
