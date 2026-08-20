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

// Command smoke-test-config writes a representative gateway.toml (plus the
// JWT signing keys it references) for
// .github/workflows/upstream-smoke-test.yml. It calls the exact same
// gateway.RenderGatewayTOML and pki.GenerateJWTKeys functions OGO's
// controller uses in production, so the config stays representative
// automatically as those evolve, instead of hand-maintaining a static TOML
// fixture that could silently drift from what OGO actually generates.
//
// TLS and OIDC are disabled here so the result can be handed straight to a
// bare gateway binary in CI with no real certs, no OIDC issuer, and no real
// Kubernetes cluster behind it -- the smoke test only needs to confirm the
// candidate gateway version still accepts the config shape OGO produces,
// not to run a full deployment.
//
// Usage: smoke-test-config <output-dir>
// Writes <output-dir>/gateway.toml and <output-dir>/jwt/{signing,public}.pem
// + kid, at the exact paths RenderGatewayTOML hardcodes
// (/etc/openshell-jwt/...) so they can be bind-mounted straight into the
// gateway container at that path.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	"github.com/aknochow/ogo/internal/gateway"
	"github.com/aknochow/ogo/internal/pki"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: smoke-test-config <output-dir>")
		os.Exit(1)
	}
	outDir := os.Args[1]

	tlsEnabled := false
	gw := &ogov1alpha1.OpenShellGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "smoke-test"},
		Spec: ogov1alpha1.OpenShellGatewaySpec{
			Namespace: "ogo",
			Auth: ogov1alpha1.AuthSpec{
				AllowUnauthenticated: true,
			},
			TLS: ogov1alpha1.TLSSpec{
				Enabled: &tlsEnabled,
			},
			Sandbox: ogov1alpha1.SandboxSpec{
				// Populate every optional field RenderGatewayTOML
				// conditionally includes, to maximize the config
				// surface a future gateway version could reject --
				// this is the exact bug class (v0.0.88 rejecting
				// enable_bind_mounts) this smoke test exists to catch.
				ImagePullPolicy:      corev1.PullIfNotPresent,
				WorkspaceStorageSize: "2Gi",
				RuntimeClassName:     "kata",
				AppArmorProfile:      "Unconfined",
			},
		},
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir output dir:", err)
		os.Exit(1)
	}

	toml := gateway.RenderGatewayTOML(gw, "ogo")
	if err := os.WriteFile(filepath.Join(outDir, "gateway.toml"), []byte(toml), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write gateway.toml:", err)
		os.Exit(1)
	}

	keys, err := pki.GenerateJWTKeys()
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate JWT keys:", err)
		os.Exit(1)
	}
	jwtDir := filepath.Join(outDir, "jwt")
	if err := os.MkdirAll(jwtDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir jwt dir:", err)
		os.Exit(1)
	}
	writes := []struct {
		name    string
		content []byte
		perm    os.FileMode
	}{
		{"signing.pem", keys.SigningKey, 0o600}, // private key
		{"public.pem", keys.PublicKey, 0o644},
		{"kid", []byte(keys.KID), 0o644},
	}
	for _, w := range writes {
		if err := os.WriteFile(filepath.Join(jwtDir, w.name), w.content, w.perm); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", w.name, err)
			os.Exit(1)
		}
	}

	fmt.Printf("Wrote %s (gateway.toml + jwt/{signing,public}.pem,kid)\n", outDir)
}
