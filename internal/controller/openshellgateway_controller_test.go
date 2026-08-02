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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
)

var _ = Describe("OpenShellGateway Controller", func() {
	const gwName = "openshell"

	ctx := context.Background()
	gwKey := types.NamespacedName{Name: gwName}

	BeforeEach(func() {
		// Ensure namespace exists
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ogo-test"}}
		_ = k8sClient.Create(ctx, ns)

		// Ensure database Secret exists
		dbSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pg-uri", Namespace: "ogo-test"},
			Data:       map[string][]byte{"uri": []byte("postgresql://test:test@localhost:5432/test")},
		}
		_ = k8sClient.Create(ctx, dbSecret)

		gw := &ogov1alpha1.OpenShellGateway{}
		err := k8sClient.Get(ctx, gwKey, gw)
		if err != nil && errors.IsNotFound(err) {
			resource := &ogov1alpha1.OpenShellGateway{
				ObjectMeta: metav1.ObjectMeta{Name: gwName},
				Spec: ogov1alpha1.OpenShellGatewaySpec{
					Namespace: "ogo-test",
					Database:  ogov1alpha1.DatabaseSpec{SecretName: "test-pg-uri"},
					Auth: ogov1alpha1.AuthSpec{
						OpenShift: ogov1alpha1.OpenShiftAuth{UserGroup: "test-users"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		}
	})

	AfterEach(func() {
		gw := &ogov1alpha1.OpenShellGateway{}
		if err := k8sClient.Get(ctx, gwKey, gw); err == nil {
			gw.Finalizers = nil
			_ = k8sClient.Update(ctx, gw)
			_ = k8sClient.Delete(ctx, gw)
		}
		// Clean up cluster-scoped resources
		_ = k8sClient.Delete(ctx, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: gwName + "-node-reader"}})
		_ = k8sClient.Delete(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: gwName + "-node-reader"}})
	})

	reconciler := func() *OpenShellGatewayReconciler {
		return &OpenShellGatewayReconciler{
			Client:          k8sClient,
			Scheme:          k8sClient.Scheme(),
			DiscoveryClient: fake.NewSimpleClientset().Discovery(),
		}
	}

	It("should add a finalizer on first reconcile", func() {
		r := reconciler()
		result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).ToNot(BeZero())

		gw := &ogov1alpha1.OpenShellGateway{}
		Expect(k8sClient.Get(ctx, gwKey, gw)).To(Succeed())
		Expect(gw.Finalizers).To(ContainElement(finalizerName))
	})

	It("should create the gateway namespace", func() {
		r := reconciler()
		// First reconcile adds finalizer
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		// Second reconcile creates resources
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		Expect(err).NotTo(HaveOccurred())

		ns := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "ogo-test"}, ns)).To(Succeed())
		Expect(ns.Labels[labelManagedBy]).To(Equal("ogo"))
	})

	It("should create service accounts", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		gwSA := &corev1.ServiceAccount{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName, Namespace: "ogo-test"}, gwSA)).To(Succeed())

		sandboxSA := &corev1.ServiceAccount{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName + "-sandbox", Namespace: "ogo-test"}, sandboxSA)).To(Succeed())
	})

	It("should create RBAC resources", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		cr := &rbacv1.ClusterRole{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName + "-node-reader"}, cr)).To(Succeed())
		// tokenreviews:create, nodes:get/list/watch, and user.openshift.io
		// groups:get (added so auth-bridge can look up Group CR membership
		// for ServiceAccount identities — see checkGroupMemberships).
		Expect(cr.Rules).To(HaveLen(3))

		crb := &rbacv1.ClusterRoleBinding{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName + "-node-reader"}, crb)).To(Succeed())
		Expect(crb.Subjects[0].Name).To(Equal(gwName))

		role := &rbacv1.Role{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName + "-sandbox", Namespace: "ogo-test"}, role)).To(Succeed())

		rb := &rbacv1.RoleBinding{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName + "-sandbox", Namespace: "ogo-test"}, rb)).To(Succeed())
	})

	It("should generate TLS secrets with shared CA", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		serverTLS := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName + "-server-tls", Namespace: "ogo-test"}, serverTLS)).To(Succeed())
		Expect(serverTLS.Data).To(HaveKey("tls.crt"))
		Expect(serverTLS.Data).To(HaveKey("tls.key"))
		Expect(serverTLS.Data).To(HaveKey("ca.crt"))

		clientTLS := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName + "-client-tls", Namespace: "ogo-test"}, clientTLS)).To(Succeed())
		Expect(clientTLS.Data).To(HaveKey("tls.crt"))
		Expect(clientTLS.Data).To(HaveKey("tls.key"))
		Expect(clientTLS.Data).To(HaveKey("ca.crt"))

		Expect(serverTLS.Data["ca.crt"]).To(Equal(clientTLS.Data["ca.crt"]))
	})

	It("should generate JWT keys", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		jwtKeys := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName + "-jwt-keys", Namespace: "ogo-test"}, jwtKeys)).To(Succeed())
		Expect(jwtKeys.Data).To(HaveKey("signing.pem"))
		Expect(jwtKeys.Data).To(HaveKey("public.pem"))
		Expect(jwtKeys.Data).To(HaveKey("kid"))
	})

	It("should create a ConfigMap with gateway.toml", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName + "-config", Namespace: "ogo-test"}, cm)).To(Succeed())
		Expect(cm.Data).To(HaveKey("gateway.toml"))
		Expect(cm.Data["gateway.toml"]).To(ContainSubstring("[openshell]"))
		Expect(cm.Data["gateway.toml"]).To(ContainSubstring("sandbox_namespace"))
	})

	It("should create a Deployment", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		deploy := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName, Namespace: "ogo-test"}, deploy)).To(Succeed())
		Expect(*deploy.Spec.Replicas).To(Equal(int32(1)))
		Expect(deploy.Spec.Template.Spec.Containers).To(HaveLen(1))
		Expect(deploy.Spec.Template.Spec.Containers[0].Name).To(Equal("openshell-gateway"))
		Expect(deploy.Spec.Template.Annotations).To(HaveKey("ogo.aknochow.io/config-hash"))
	})

	It("should not report Available while the Envoy Route isn't ready, even with pods Ready", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		deploy := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName, Namespace: "ogo-test"}, deploy)).To(Succeed())
		deploy.Status.Replicas = 1
		deploy.Status.ReadyReplicas = 1
		Expect(k8sClient.Status().Update(ctx, deploy)).To(Succeed())

		gw := &ogov1alpha1.OpenShellGateway{}
		Expect(k8sClient.Get(ctx, gwKey, gw)).To(Succeed())
		// Simulates the auto-install path mid-flight: the gateway pod is
		// Ready, but Envoy Gateway hasn't provisioned the proxy Service the
		// Route bridges to yet — the exact state that used to report
		// Available=True/Phase=Running despite there being no working
		// ingress path at all.
		meta.SetStatusCondition(&gw.Status.Conditions, metav1.Condition{
			Type: ogov1alpha1.ConditionEnvoyRouteReady, Status: metav1.ConditionFalse,
			Reason: "ProxyServiceNotFound", Message: "waiting for proxy service",
		})
		Expect(k8sClient.Status().Update(ctx, gw)).To(Succeed())

		Expect(r.updateStatus(ctx, gw)).To(Succeed())

		updated := &ogov1alpha1.OpenShellGateway{}
		Expect(k8sClient.Get(ctx, gwKey, updated)).To(Succeed())
		Expect(updated.Status.Phase).To(Equal(ogov1alpha1.PhaseCreating))
		available := meta.FindStatusCondition(updated.Status.Conditions, ogov1alpha1.ConditionAvailable)
		Expect(available).NotTo(BeNil())
		Expect(available.Status).To(Equal(metav1.ConditionFalse))
		Expect(available.Reason).To(Equal("EnvoyRouteNotReady"))
	})

	It("should create a Service", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName, Namespace: "ogo-test"}, svc)).To(Succeed())
		Expect(svc.Spec.Ports).To(HaveLen(2))
	})

	It("should expose the auth bridge over TLS", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		gw := &ogov1alpha1.OpenShellGateway{}
		Expect(k8sClient.Get(ctx, gwKey, gw)).To(Succeed())
		gw.Spec.Auth.OpenShift.Enabled = ptr.To(true)
		Expect(k8sClient.Update(ctx, gw)).To(Succeed())
		Expect(r.reconcileAuthBridgeCA(ctx, gw)).To(Succeed())
		Expect(r.reconcileDeployment(ctx, gw)).To(Succeed())
		Expect(r.reconcileService(ctx, gw)).To(Succeed())

		deploy := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName, Namespace: "ogo-test"}, deploy)).To(Succeed())
		var authBridge *corev1.Container
		for i := range deploy.Spec.Template.Spec.Containers {
			if deploy.Spec.Template.Spec.Containers[i].Name == "auth-bridge" {
				authBridge = &deploy.Spec.Template.Spec.Containers[i]
				break
			}
		}
		Expect(authBridge).NotTo(BeNil())
		Expect(authBridge.Ports).To(ContainElement(corev1.ContainerPort{
			Name: "auth-tls", ContainerPort: 8443, Protocol: corev1.ProtocolTCP,
		}))
		Expect(authBridge.ReadinessProbe.HTTPGet.Scheme).To(Equal(corev1.URISchemeHTTPS))
		Expect(authBridge.VolumeMounts).To(ContainElement(corev1.VolumeMount{
			Name: "tls-cert", MountPath: "/etc/auth-bridge-tls", ReadOnly: true,
		}))
		env := map[string]string{}
		for _, variable := range authBridge.Env {
			env[variable.Name] = variable.Value
		}
		Expect(env).To(HaveKeyWithValue("AUTH_BRIDGE_LISTEN", "127.0.0.1:8085"))
		Expect(env).To(HaveKeyWithValue("AUTH_BRIDGE_TLS_LISTEN", ":8443"))
		Expect(env).To(HaveKeyWithValue("AUTH_BRIDGE_TLS_CERT", "/etc/auth-bridge-tls/tls.crt"))
		Expect(env).To(HaveKeyWithValue("AUTH_BRIDGE_TLS_KEY", "/etc/auth-bridge-tls/tls.key"))

		ca := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName + "-auth-ca", Namespace: "ogo-test"}, ca)).To(Succeed())
		Expect(ca.Data).To(HaveLen(1))
		Expect(ca.Data["ca.crt"]).NotTo(BeEmpty())
		ca.BinaryData = map[string][]byte{"tls.key": []byte("drift")}
		Expect(k8sClient.Update(ctx, ca)).To(Succeed())
		Expect(r.reconcileAuthBridgeCA(ctx, gw)).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName + "-auth-ca", Namespace: "ogo-test"}, ca)).To(Succeed())
		Expect(ca.BinaryData).To(BeEmpty())

		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName, Namespace: "ogo-test"}, svc)).To(Succeed())
		var authPort *corev1.ServicePort
		for i := range svc.Spec.Ports {
			if svc.Spec.Ports[i].Name == "auth" {
				authPort = &svc.Spec.Ports[i]
				break
			}
		}
		Expect(authPort).NotTo(BeNil())
		Expect(authPort.TargetPort).To(Equal(intstr.FromString("auth-tls")))

		gw.Spec.TLS.Enabled = ptr.To(false)
		Expect(r.reconcileAuthBridgeCA(ctx, gw)).To(Succeed())
		Expect(errors.IsNotFound(k8sClient.Get(ctx,
			types.NamespacedName{Name: gwName + "-auth-ca", Namespace: "ogo-test"}, &corev1.ConfigMap{}))).To(BeTrue())
	})

	It("should set status URL from route hostname", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		gw := &ogov1alpha1.OpenShellGateway{}
		Expect(k8sClient.Get(ctx, gwKey, gw)).To(Succeed())
		// Default: no hostname set, should use internal service URL
		Expect(gw.Status.GatewayURL).To(ContainSubstring("svc.cluster.local:8080"))
	})

	It("should set status URL from route hostname when configured", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		gw := &ogov1alpha1.OpenShellGateway{}
		Expect(k8sClient.Get(ctx, gwKey, gw)).To(Succeed())
		gw.Spec.Route.Hostname = "openshell.apps.example.com"
		Expect(k8sClient.Update(ctx, gw)).To(Succeed())

		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		Expect(k8sClient.Get(ctx, gwKey, gw)).To(Succeed())
		Expect(gw.Status.GatewayURL).To(Equal("https://openshell.apps.example.com:443"))
	})

	It("should include auth port in service when OpenShift auth enabled", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName, Namespace: "ogo-test"}, svc)).To(Succeed())
		portNames := make([]string, 0, len(svc.Spec.Ports))
		for _, p := range svc.Spec.Ports {
			portNames = append(portNames, p.Name)
		}
		Expect(portNames).To(ContainElement("grpc"))
		Expect(portNames).To(ContainElement("metrics"))
	})

	It("should include allow_unauthenticated in config when enabled", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		gw := &ogov1alpha1.OpenShellGateway{}
		Expect(k8sClient.Get(ctx, gwKey, gw)).To(Succeed())
		gw.Spec.Auth.AllowUnauthenticated = true
		Expect(k8sClient.Update(ctx, gw)).To(Succeed())

		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName + "-config", Namespace: "ogo-test"}, cm)).To(Succeed())
		Expect(cm.Data["gateway.toml"]).To(ContainSubstring("allow_unauthenticated_users = true"))
	})

	It("should not include allow_unauthenticated by default", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName + "-config", Namespace: "ogo-test"}, cm)).To(Succeed())
		Expect(cm.Data["gateway.toml"]).NotTo(ContainSubstring("allow_unauthenticated"))
	})

	It("should create deployment with correct container image", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		deploy := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName, Namespace: "ogo-test"}, deploy)).To(Succeed())
		container := deploy.Spec.Template.Spec.Containers[0]
		Expect(container.Image).To(ContainSubstring("openshell/gateway"))
	})

	It("should mount TLS volumes when TLS enabled", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		deploy := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName, Namespace: "ogo-test"}, deploy)).To(Succeed())
		volumeNames := make([]string, 0, len(deploy.Spec.Template.Spec.Volumes))
		for _, v := range deploy.Spec.Template.Spec.Volumes {
			volumeNames = append(volumeNames, v.Name)
		}
		Expect(volumeNames).To(ContainElement("tls-cert"))
		Expect(volumeNames).To(ContainElement("tls-client-ca"))
		Expect(volumeNames).To(ContainElement("gateway-config"))
		Expect(volumeNames).To(ContainElement("sandbox-jwt"))
	})

	It("should create network policy when enabled", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		gw := &ogov1alpha1.OpenShellGateway{}
		Expect(k8sClient.Get(ctx, gwKey, gw)).To(Succeed())
		gw.Spec.NetworkPolicy.Enabled = ptr.To(true)
		Expect(k8sClient.Update(ctx, gw)).To(Succeed())

		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		np := &networkingv1.NetworkPolicy{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: gwName + "-sandbox-ssh", Namespace: "ogo-test"}, np)).To(Succeed())
	})

	It("should not create network policy when explicitly disabled", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		gw := &ogov1alpha1.OpenShellGateway{}
		Expect(k8sClient.Get(ctx, gwKey, gw)).To(Succeed())
		gw.Spec.NetworkPolicy.Enabled = ptr.To(false)
		Expect(k8sClient.Update(ctx, gw)).To(Succeed())

		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		np := &networkingv1.NetworkPolicy{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: gwName + "-sandbox-ssh", Namespace: "ogo-test"}, np)
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("should set status conditions after reconcile", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		gw := &ogov1alpha1.OpenShellGateway{}
		Expect(k8sClient.Get(ctx, gwKey, gw)).To(Succeed())
		Expect(gw.Status.Conditions).NotTo(BeEmpty())

		conditionTypes := make([]string, 0, len(gw.Status.Conditions))
		for _, c := range gw.Status.Conditions {
			conditionTypes = append(conditionTypes, c.Type)
		}
		Expect(conditionTypes).To(ContainElement(ogov1alpha1.ConditionAvailable))
		Expect(conditionTypes).To(ContainElement(ogov1alpha1.ConditionProgressing))
		Expect(conditionTypes).To(ContainElement(ogov1alpha1.ConditionDegraded))
	})

	It("should set client cert secret name in status", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		gw := &ogov1alpha1.OpenShellGateway{}
		Expect(k8sClient.Get(ctx, gwKey, gw)).To(Succeed())
		Expect(gw.Status.ClientCertSecretName).To(Equal(gwName + "-client-tls"))
	})

	It("should clean up on deletion", func() {
		r := reconciler()
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})

		gw := &ogov1alpha1.OpenShellGateway{}
		Expect(k8sClient.Get(ctx, gwKey, gw)).To(Succeed())
		ca := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Name: gwName + "-auth-ca", Namespace: "ogo-test", Labels: gatewayLabels(gw),
		}}
		Expect(k8sClient.Create(ctx, ca)).To(Succeed())
		Expect(k8sClient.Delete(ctx, gw)).To(Succeed())

		// Reconcile handles the finalizer
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: gwKey})
		Expect(err).NotTo(HaveOccurred())

		cr := &rbacv1.ClusterRole{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: gwName + "-node-reader"}, cr)
		Expect(errors.IsNotFound(err)).To(BeTrue())
		err = k8sClient.Get(ctx, types.NamespacedName{Name: gwName + "-auth-ca", Namespace: "ogo-test"}, &corev1.ConfigMap{})
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})
})
