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

package e2e

// These specs cover OpenShift OAuth/SSO and real cert-manager issuance —
// code paths MINC (MicroShift) and Kind cannot exercise at all, since
// neither has the oauth.openshift.io/user.openshift.io API group. They only
// run against a real OpenShift cluster (or CRC with the full preset), via:
//
//	KUBECONFIG=~/.kube/your-cluster E2E_REAL_CLUSTER_APPS_DOMAIN=apps.your-cluster.example.com \
//	  make test-e2e-real
//
// E2E_REAL_CLUSTER_APPS_DOMAIN is the cluster's wildcard apps domain (the
// same one behind any *.apps.<cluster> route). The actual test hostname is
// built per-run as ogo-e2e-<git-short-sha>.<domain> — the cert-manager
// specs perform real ACME issuance, and Let's Encrypt rate-limits repeat
// issuance for the same exact hostname, so reusing one fixed hostname
// across every local run/commit would burn through that limit fast.

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/aknochow/ogo/test/utils"
)

var realClusterAppsDomain = os.Getenv("E2E_REAL_CLUSTER_APPS_DOMAIN")

// keepClusterAfterSuite leaves the operator and a running gateway deployed
// after the suite finishes instead of tearing everything down — turning the
// target cluster (e.g. SNO) into a persistent staging environment running
// whatever build was just tested, promoted there ahead of a real cluster
// like RDU. Re-running test-e2e-real is idempotent against this leftover
// state (BeforeAll only ever applies/installs, never assumes a clean slate).
var keepClusterAfterSuite = os.Getenv("E2E_REAL_CLUSTER_KEEP") == "true"

// realClusterHostname is unique per commit (ogo-e2e-<short-sha>.<apps-domain>)
// so repeated e2e runs across commits don't collide on the same Let's
// Encrypt identifier and trip its per-hostname rate limit.
var realClusterHostname = buildRealClusterHostname()

func buildRealClusterHostname() string {
	if realClusterAppsDomain == "" {
		return ""
	}
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	sha, err := utils.Run(cmd)
	if err != nil {
		sha = "local"
	}
	return fmt.Sprintf("ogo-e2e-%s.%s", strings.TrimSpace(sha), realClusterAppsDomain)
}

// hasOpenShiftOAuthAPI reports whether the oauth.openshift.io API group is present —
// true on real OpenShift/CRC, false on MINC (MicroShift) and Kind.
func hasOpenShiftOAuthAPI() bool {
	cmd := exec.Command("kubectl", "get", "--raw", "/apis/oauth.openshift.io/v1")
	_, err := utils.Run(cmd)
	return err == nil
}

func hasGatewayClass(name string) bool {
	cmd := exec.Command("kubectl", "get", "gatewayclass", name)
	_, err := utils.Run(cmd)
	return err == nil
}

// stagingAuthBridgeImage derives the matching ogo-auth-bridge image from the
// controller image under test (e.g. quay.io/aknochow/ogo:baseline-abc123 ->
// quay.io/aknochow/ogo-auth-bridge:baseline-abc123), so the KEEP staging
// deployment actually exercises the auth-bridge fix being verified rather
// than falling back to the operator's hardcoded :latest default.
func stagingAuthBridgeImage(controllerImage string) string {
	repo, tag, ok := strings.Cut(controllerImage, ":")
	if !ok {
		return ""
	}
	slash := strings.LastIndex(repo, "/")
	if slash == -1 || repo[slash+1:] != "ogo" {
		return ""
	}
	return repo[:slash+1] + "ogo-auth-bridge:" + tag
}

func decodeBase64(s string) string {
	b, err := base64.StdEncoding.DecodeString(s)
	Expect(err).NotTo(HaveOccurred())
	return string(b)
}

var _ = Describe("RealCluster", Ordered, func() {
	skipReason := ""

	BeforeAll(func() {
		if !hasOpenShiftOAuthAPI() {
			skipReason = "requires a real OpenShift cluster with oauth.openshift.io (not MINC/Kind)"
			Skip(skipReason)
		}
		if realClusterAppsDomain == "" {
			skipReason = "set E2E_REAL_CLUSTER_APPS_DOMAIN to the target cluster's wildcard apps domain " +
				"(cert-manager specs perform real ACME issuance)"
			Skip(skipReason)
		}

		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace, "--dry-run=client", "-o", "yaml")
		nsYaml, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		cmd = exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(nsYaml)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	AfterAll(func() {
		if skipReason != "" {
			return
		}
		if keepClusterAfterSuite {
			By("leaving the operator deployed and applying a staging gateway CR (E2E_REAL_CLUSTER_KEEP=true)")
			authBridgeLine := ""
			if img := stagingAuthBridgeImage(projectImage); img != "" {
				authBridgeLine = fmt.Sprintf("  authBridgeImage: %s\n", img)
			}
			// Mirrors RDU's actual production shape: Gateway API/Envoy fronting
			// a public cert-manager (Let's Encrypt) cert, gateway pod itself
			// plaintext internally. This is the whole point of the per-run
			// unique hostname — a real, browser-trusted cert per staged build,
			// not the self-signed direct-Route fallback.
			cr := fmt.Sprintf(`
apiVersion: gateway.ogo.aknochow.io/v1alpha1
kind: OpenShellGateway
metadata:
  name: openshell
spec:
  namespace: %s
  database:
    embedded: true
  auth:
    openshift:
      userGroup: openshell-users
      adminGroup: openshell-admins
  tls:
    enabled: false
    certManager:
      enabled: true
      issuerName: letsencrypt
      issuerKind: ClusterIssuer
  route:
    hostname: %s
    gatewayAPI:
      enabled: true
%s`, namespace, realClusterHostname, authBridgeLine)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(cr)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply staging gateway CR")
			return
		}

		By("cleaning up gateway CR if still present")
		cmd := exec.Command("kubectl", "delete", "openshellgateways", "openshell",
			"--timeout=60s", "--ignore-not-found=true")
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace, "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		if skipReason != "" {
			return
		}
		By("deleting the gateway CR between specs")
		cmd := exec.Command("kubectl", "delete", "openshellgateways", "openshell",
			"--timeout=60s", "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "openshellgateways", "openshell")
			_, err := utils.Run(cmd)
			g.Expect(err).To(HaveOccurred(), "gateway CR should be gone before the next spec")
		}, 90*time.Second).Should(Succeed())
	})

	It("keeps the gateway pod's own TLS listener self-signed when cert-manager is enabled without Gateway API", func() {
		By("applying a CR with cert-manager enabled and Gateway API disabled")
		cr := fmt.Sprintf(`
apiVersion: gateway.ogo.aknochow.io/v1alpha1
kind: OpenShellGateway
metadata:
  name: openshell
spec:
  namespace: %s
  database:
    embedded: true
  auth:
    openshift:
      userGroup: openshell-e2e-users
  tls:
    enabled: true
    certManager:
      enabled: true
      issuerName: letsencrypt
      issuerKind: ClusterIssuer
  route:
    hostname: %s
    gatewayAPI:
      enabled: false
`, namespace, realClusterHostname)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(cr)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply gateway CR")

		By("waiting for the server-tls secret to exist")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "secret", "openshell-server-tls", "-n", namespace)
			_, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
		}, 2*time.Minute).Should(Succeed())

		By("verifying the server-tls secret is self-signed, not cert-manager-issued")
		// Regression test: a cert-manager (Let's Encrypt) cert here breaks supervisor
		// mTLS, since sandboxes connect via internal service DNS, which no public CA
		// can issue for.
		cmd = exec.Command("sh", "-c", fmt.Sprintf(
			"kubectl get secret openshell-server-tls -n %s -o jsonpath='{.data.tls\\.crt}' | "+
				"base64 -d | openssl x509 -noout -issuer", namespace))
		out, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring("CN=openshell-ca"))
		Expect(out).NotTo(ContainSubstring("Let's Encrypt"))
	})

	It("issues a separate cert-manager Certificate for the Gateway API listener "+
		"without touching the pod's self-signed cert", func() {
		if !hasGatewayClass("eg") {
			Skip("requires Envoy Gateway installed with a GatewayClass named 'eg'")
		}

		By("applying a CR with cert-manager and Gateway API both enabled")
		cr := fmt.Sprintf(`
apiVersion: gateway.ogo.aknochow.io/v1alpha1
kind: OpenShellGateway
metadata:
  name: openshell
spec:
  namespace: %s
  database:
    embedded: true
  auth:
    openshift:
      userGroup: openshell-e2e-users
  tls:
    enabled: false
    certManager:
      enabled: true
      issuerName: letsencrypt
      issuerKind: ClusterIssuer
  route:
    hostname: %s
    gatewayAPI:
      enabled: true
`, namespace, realClusterHostname)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(cr)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply gateway CR")

		By("waiting for the cert-manager Certificate to be Ready")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "certificate", "openshell-gateway-tls", "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("True"), "cert-manager Certificate should issue successfully")
		}, 3*time.Minute).Should(Succeed())
	})

	It("keeps the OAuthClient secret in sync when the CR is deleted and recreated", func() {
		cr := fmt.Sprintf(`
apiVersion: gateway.ogo.aknochow.io/v1alpha1
kind: OpenShellGateway
metadata:
  name: openshell
spec:
  namespace: %s
  database:
    embedded: true
  auth:
    openshift:
      userGroup: openshell-e2e-users
  route:
    hostname: %s
    gatewayAPI:
      enabled: false
`, namespace, realClusterHostname)
		applyCR := func() {
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(cr)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply gateway CR")
		}

		By("applying the CR the first time")
		applyCR()
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "secret", "openshell-oauth-client", "-n", namespace)
			_, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
		}, 2*time.Minute).Should(Succeed())

		By("deleting and recreating the CR, simulating a redeploy onto existing cluster state")
		cmd := exec.Command("kubectl", "delete", "openshellgateways", "openshell", "--timeout=60s")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		applyCR()

		By("verifying the OAuthClient secret matches the freshly generated namespace secret")
		// Regression test: reconcileOAuthClient currently no-ops if the cluster-scoped
		// OAuthClient already exists, leaving it out of sync with a freshly regenerated
		// namespace Secret — this is expected to FAIL until that function is fixed to
		// reconcile the secret on every pass, not just on first creation.
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "secret", "openshell-oauth-client", "-n", namespace,
				"-o", "jsonpath={.data.secret}")
			nsSecretB64, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())

			cmd = exec.Command("kubectl", "get", "oauthclient", "openshell", "-o", "jsonpath={.secret}")
			oauthClientSecret, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())

			g.Expect(decodeBase64(nsSecretB64)).To(Equal(oauthClientSecret))
		}, 2*time.Minute).Should(Succeed())
	})

	It("accepts a CR with database.secretName omitted entirely", func() {
		// Regression test for the CEL rule needing has(self.secretName) before
		// size(self.secretName) — omitting the field (not setting it to "") used
		// to throw "no such key: secretName" on every reconcile.
		cr := fmt.Sprintf(`
apiVersion: gateway.ogo.aknochow.io/v1alpha1
kind: OpenShellGateway
metadata:
  name: openshell
spec:
  namespace: %s
  database:
    embedded: true
  auth:
    openshift:
      userGroup: openshell-e2e-users
  route:
    hostname: %s
    gatewayAPI:
      enabled: false
`, namespace, realClusterHostname)
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(cr)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
	})
})
