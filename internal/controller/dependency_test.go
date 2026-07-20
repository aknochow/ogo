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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
)

var _ = Describe("PostgreSQLReconciler", func() {
	ctx := context.Background()
	const ns = "pg-test"

	gw := func() *ogov1alpha1.OpenShellGateway {
		return &ogov1alpha1.OpenShellGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "test-gw"},
			Spec: ogov1alpha1.OpenShellGatewaySpec{
				Namespace: ns,
				Database:  ogov1alpha1.DatabaseSpec{Embedded: true},
			},
		}
	}

	BeforeEach(func() {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = k8sClient.Create(ctx, nsObj)
	})

	AfterEach(func() {
		for _, name := range []string{"test-gw-pg-password", "test-gw-pg-uri"} {
			_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}})
		}
		_ = k8sClient.Delete(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "test-gw-pg", Namespace: ns}})
		_ = k8sClient.Delete(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "test-gw-pg", Namespace: ns}})
		_ = k8sClient.Delete(ctx, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "test-gw-pg-data", Namespace: ns}})
	})

	It("should be enabled when embedded is true and secretName is empty", func() {
		r := &PostgreSQLReconciler{Client: k8sClient}
		Expect(r.Enabled(ctx, gw())).To(BeTrue())
	})

	It("should be disabled when secretName is set", func() {
		r := &PostgreSQLReconciler{Client: k8sClient}
		g := gw()
		g.Spec.Database.SecretName = "external-pg"
		Expect(r.Enabled(ctx, g)).To(BeFalse())
	})

	It("should be disabled when embedded is false", func() {
		r := &PostgreSQLReconciler{Client: k8sClient}
		g := gw()
		g.Spec.Database.Embedded = false
		Expect(r.Enabled(ctx, g)).To(BeFalse())
	})

	It("should create PG resources on reconcile", func() {
		r := &PostgreSQLReconciler{Client: k8sClient}
		condition, err := r.Reconcile(ctx, gw())
		Expect(err).NotTo(HaveOccurred())
		Expect(condition.Status).To(Equal(metav1.ConditionTrue))
		Expect(condition.Reason).To(Equal("EmbeddedProvisioned"))

		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-gw-pg-password", Namespace: ns}, secret)).To(Succeed())
		Expect(secret.Data["password"]).NotTo(BeEmpty())

		uriSecret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-gw-pg-uri", Namespace: ns}, uriSecret)).To(Succeed())
		Expect(string(uriSecret.Data["uri"])).To(ContainSubstring("postgresql://"))

		deploy := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-gw-pg", Namespace: ns}, deploy)).To(Succeed())
		Expect(deploy.Labels[labelProvisionedBy]).To(Equal(provisionedByOGO))

		svc := &corev1.Service{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "test-gw-pg", Namespace: ns}, svc)).To(Succeed())
	})

	It("should use custom storage size from embeddedConfig", func() {
		r := &PostgreSQLReconciler{Client: k8sClient}
		g := gw()
		g.Name = "custom-gw"
		g.Spec.Database.EmbeddedConfig = &ogov1alpha1.EmbeddedDatabaseConfig{StorageSize: "5Gi"}
		_, err := r.Reconcile(ctx, g)
		Expect(err).NotTo(HaveOccurred())

		pvc := &corev1.PersistentVolumeClaim{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "custom-gw-pg-data", Namespace: ns}, pvc)).To(Succeed())
		Expect(pvc.Spec.Resources.Requests.Storage().String()).To(Equal("5Gi"))

		Expect(r.Cleanup(ctx, g)).To(Succeed())
	})

	It("should be idempotent", func() {
		r := &PostgreSQLReconciler{Client: k8sClient}
		_, err := r.Reconcile(ctx, gw())
		Expect(err).NotTo(HaveOccurred())
		_, err = r.Reconcile(ctx, gw())
		Expect(err).NotTo(HaveOccurred())
	})

	It("should clean up only owned resources", func() {
		r := &PostgreSQLReconciler{Client: k8sClient}
		_, err := r.Reconcile(ctx, gw())
		Expect(err).NotTo(HaveOccurred())

		Expect(r.Cleanup(ctx, gw())).To(Succeed())

		deploy := &appsv1.Deployment{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: "test-gw-pg", Namespace: ns}, deploy)
		Expect(errors.IsNotFound(err)).To(BeTrue())

		svc := &corev1.Service{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: "test-gw-pg", Namespace: ns}, svc)
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})
})

var _ = Describe("EnvoyGatewayReconciler Enabled", func() {
	ctx := context.Background()

	It("should be disabled when gatewayAPI.enabled is false", func() {
		r := &EnvoyGatewayReconciler{
			Client:          k8sClient,
			DiscoveryClient: fake.NewSimpleClientset().Discovery(),
		}
		gw := &ogov1alpha1.OpenShellGateway{
			Spec: ogov1alpha1.OpenShellGatewaySpec{
				Route: ogov1alpha1.RouteSpec{
					GatewayAPI: ogov1alpha1.GatewayAPISpec{Enabled: ptr.To(false)},
				},
			},
		}
		Expect(r.Enabled(ctx, gw)).To(BeFalse())
	})

	It("should be disabled when installEnvoyGateway is false", func() {
		r := &EnvoyGatewayReconciler{
			Client:          k8sClient,
			DiscoveryClient: fake.NewSimpleClientset().Discovery(),
		}
		gw := &ogov1alpha1.OpenShellGateway{
			Spec: ogov1alpha1.OpenShellGatewaySpec{
				Route: ogov1alpha1.RouteSpec{
					GatewayAPI: ogov1alpha1.GatewayAPISpec{InstallEnvoyGateway: ptr.To(false)},
				},
			},
		}
		Expect(r.Enabled(ctx, gw)).To(BeFalse())
	})

	It("should be enabled when gatewayAPI.enabled is true", func() {
		r := &EnvoyGatewayReconciler{
			Client:          k8sClient,
			DiscoveryClient: fake.NewSimpleClientset().Discovery(),
		}
		gw := &ogov1alpha1.OpenShellGateway{
			Spec: ogov1alpha1.OpenShellGatewaySpec{
				Route: ogov1alpha1.RouteSpec{
					GatewayAPI: ogov1alpha1.GatewayAPISpec{Enabled: ptr.To(true)},
				},
			},
		}
		Expect(r.Enabled(ctx, gw)).To(BeTrue())
	})

	It("should be disabled when both nil and no Gateway API on cluster", func() {
		r := &EnvoyGatewayReconciler{
			Client:          k8sClient,
			DiscoveryClient: fake.NewSimpleClientset().Discovery(),
		}
		gw := &ogov1alpha1.OpenShellGateway{}
		Expect(r.Enabled(ctx, gw)).To(BeFalse())
	})
})

var _ = Describe("databaseSecretName", func() {
	It("should return secretName when set", func() {
		gw := &ogov1alpha1.OpenShellGateway{
			Spec: ogov1alpha1.OpenShellGatewaySpec{
				Database: ogov1alpha1.DatabaseSpec{SecretName: "my-secret"},
			},
		}
		Expect(databaseSecretName(gw)).To(Equal("my-secret"))
	})

	It("should return embedded name when embedded is true", func() {
		gw := &ogov1alpha1.OpenShellGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec: ogov1alpha1.OpenShellGatewaySpec{
				Database: ogov1alpha1.DatabaseSpec{Embedded: true},
			},
		}
		Expect(databaseSecretName(gw)).To(Equal("test-pg-uri"))
	})

	It("should return empty when neither is set", func() {
		gw := &ogov1alpha1.OpenShellGateway{
			Spec: ogov1alpha1.OpenShellGatewaySpec{
				Database: ogov1alpha1.DatabaseSpec{},
			},
		}
		Expect(databaseSecretName(gw)).To(BeEmpty())
	})

	It("should prefer secretName over embedded", func() {
		gw := &ogov1alpha1.OpenShellGateway{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
			Spec: ogov1alpha1.OpenShellGatewaySpec{
				Database: ogov1alpha1.DatabaseSpec{
					Embedded:   true,
					SecretName: "explicit",
				},
			},
		}
		Expect(databaseSecretName(gw)).To(Equal("explicit"))
	})
})
