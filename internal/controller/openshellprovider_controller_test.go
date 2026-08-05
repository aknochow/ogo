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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
)

var _ = Describe("OpenShellProvider Controller", func() {
	const (
		provGWName    = "provider-test-gw"
		provNamespace = "ogo-provider-test"
	)

	ctx := context.Background()

	var (
		fakeServer *fakeOpenShellServer
		stopServer func()
		gw         *ogov1alpha1.OpenShellGateway
	)

	reconciler := func() *OpenShellProviderReconciler {
		connectFn, stop := startFakeGateway(fakeServer)
		stopServer = stop
		return &OpenShellProviderReconciler{
			Client:        k8sClient,
			Scheme:        k8sClient.Scheme(),
			connectClient: connectFn,
		}
	}

	BeforeEach(func() {
		fakeServer = newFakeOpenShellServer()

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: provNamespace}}
		_ = k8sClient.Create(ctx, ns)

		gw = &ogov1alpha1.OpenShellGateway{
			ObjectMeta: metav1.ObjectMeta{Name: provGWName},
			Spec: ogov1alpha1.OpenShellGatewaySpec{
				Namespace: provNamespace,
				Database:  ogov1alpha1.DatabaseSpec{SecretName: "provider-test-pg-uri"},
				Auth: ogov1alpha1.AuthSpec{
					OpenShift: ogov1alpha1.OpenShiftAuth{UserGroup: "provider-test-users"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, gw)).To(Succeed())

		secret := generateAuthBridgeKeysSecret(provGWName+"-auth-bridge-keys", provNamespace)
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
	})

	AfterEach(func() {
		if stopServer != nil {
			stopServer()
		}

		// OpenShellProviderReconciler adds a real finalizer; a bare
		// Delete() on a spec's own CR (via `defer`) without a follow-up
		// Reconcile() leaves it stuck with a DeletionTimestamp but never
		// actually removed from etcd. Force-clear any finalizers directly
		// so every spec starts from a clean slate.
		providers := &ogov1alpha1.OpenShellProviderList{}
		if err := k8sClient.List(ctx, providers, client.InNamespace(provNamespace)); err == nil {
			for i := range providers.Items {
				p := &providers.Items[i]
				if len(p.Finalizers) > 0 {
					p.Finalizers = nil
					_ = k8sClient.Update(ctx, p)
				}
				_ = k8sClient.Delete(ctx, p)
			}
		}

		_ = k8sClient.Delete(ctx, &ogov1alpha1.OpenShellGateway{ObjectMeta: metav1.ObjectMeta{Name: provGWName}})
		_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: provGWName + "-auth-bridge-keys", Namespace: provNamespace}})
	})

	createCredentialSecret := func(name, key, value string) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: provNamespace},
			Data:       map[string][]byte{key: []byte(value)},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
	}

	It("creates the provider on the gateway with the namespaced name", func() {
		createCredentialSecret("openai-creds", "api-key", "sk-test-123")

		provider := &ogov1alpha1.OpenShellProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "openai", Namespace: provNamespace},
			Spec: ogov1alpha1.OpenShellProviderSpec{
				ProviderType: "openai",
				Credentials: map[string]ogov1alpha1.SecretKeyRef{
					"OPENAI_API_KEY": {Name: "openai-creds", Key: "api-key"},
				},
				Config: map[string]string{"region": "us-east-1"},
			},
		}
		Expect(k8sClient.Create(ctx, provider)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, provider) }()

		r := reconciler()
		key := types.NamespacedName{Name: provider.Name, Namespace: provNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key}) // adds finalizer
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		stored, ok := fakeServer.providerOf(gatewayProviderName(provider))
		Expect(ok).To(BeTrue())
		Expect(stored.Type).To(Equal("openai"))
		Expect(stored.Credentials).To(Equal(map[string]string{"OPENAI_API_KEY": "sk-test-123"}))
		Expect(stored.Config).To(Equal(map[string]string{"region": "us-east-1"}))

		Expect(k8sClient.Get(ctx, key, provider)).To(Succeed())
		Expect(provider.Status.Phase).To(Equal("Synced"))
		ready := findSyncedCondition(provider.Status.Conditions)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))
	})

	It("tolerates re-reconciling an already-correct provider (AlreadyExists)", func() {
		provider := &ogov1alpha1.OpenShellProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "nvidia", Namespace: provNamespace},
			Spec:       ogov1alpha1.OpenShellProviderSpec{ProviderType: "nvidia"},
		}
		Expect(k8sClient.Create(ctx, provider)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, provider) }()

		r := reconciler()
		key := types.NamespacedName{Name: provider.Name, Namespace: provNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		// Third reconcile of the same, unchanged object -- CreateProvider
		// hits AlreadyExists and must fall back to UpdateProvider cleanly.
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, provider)).To(Succeed())
		Expect(provider.Status.Phase).To(Equal("Synced"))
		_, ok := fakeServer.providerOf(gatewayProviderName(provider))
		Expect(ok).To(BeTrue())
	})

	It("pushes drift when spec.config changes after initial sync", func() {
		provider := &ogov1alpha1.OpenShellProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "github", Namespace: provNamespace},
			Spec: ogov1alpha1.OpenShellProviderSpec{
				ProviderType: "github",
				Config:       map[string]string{"org": "before"},
			},
		}
		Expect(k8sClient.Create(ctx, provider)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, provider) }()

		r := reconciler()
		key := types.NamespacedName{Name: provider.Name, Namespace: provNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, provider)).To(Succeed())
		provider.Spec.Config["org"] = "after"
		Expect(k8sClient.Update(ctx, provider)).To(Succeed())

		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		stored, ok := fakeServer.providerOf(gatewayProviderName(provider))
		Expect(ok).To(BeTrue())
		Expect(stored.Config).To(Equal(map[string]string{"org": "after"}))
	})

	It("retracts a credential key removed from spec instead of leaving it stale", func() {
		createCredentialSecret("multi-creds", "a", "value-a")

		provider := &ogov1alpha1.OpenShellProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: provNamespace},
			Spec: ogov1alpha1.OpenShellProviderSpec{
				ProviderType: "custom",
				Credentials: map[string]ogov1alpha1.SecretKeyRef{
					"KEY_A": {Name: "multi-creds", Key: "a"},
				},
				Config: map[string]string{"keep": "yes", "drop": "yes"},
			},
		}
		Expect(k8sClient.Create(ctx, provider)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, provider) }()

		r := reconciler()
		key := types.NamespacedName{Name: provider.Name, Namespace: provNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		stored, ok := fakeServer.providerOf(gatewayProviderName(provider))
		Expect(ok).To(BeTrue())
		Expect(stored.Credentials).To(HaveKey("KEY_A"))
		Expect(stored.Config).To(HaveKey("drop"))

		// Shrink both maps -- the removed keys must be explicitly retracted,
		// not just left alone (the gateway's merge semantics treat an
		// omitted key as "leave unchanged", not "delete").
		Expect(k8sClient.Get(ctx, key, provider)).To(Succeed())
		provider.Spec.Credentials = map[string]ogov1alpha1.SecretKeyRef{}
		provider.Spec.Config = map[string]string{"keep": "yes"}
		Expect(k8sClient.Update(ctx, provider)).To(Succeed())

		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		stored, ok = fakeServer.providerOf(gatewayProviderName(provider))
		Expect(ok).To(BeTrue())
		Expect(stored.Credentials).NotTo(HaveKey("KEY_A"), "removed credential key must be actively retracted, not left stale")
		Expect(stored.Config).To(HaveKey("keep"))
		Expect(stored.Config).NotTo(HaveKey("drop"), "removed config key must be actively retracted, not left stale")

		Expect(k8sClient.Get(ctx, key, provider)).To(Succeed())
		Expect(provider.Status.ReconciledCredentialKeys).To(BeEmpty())
		Expect(provider.Status.ReconciledConfigKeys).To(ConsistOf("keep"))
	})

	It("rejects a providerType change instead of silently updating it", func() {
		provider := &ogov1alpha1.OpenShellProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "immutable-type", Namespace: provNamespace},
			Spec:       ogov1alpha1.OpenShellProviderSpec{ProviderType: "claude-code"},
		}
		Expect(k8sClient.Create(ctx, provider)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, provider) }()

		r := reconciler()
		key := types.NamespacedName{Name: provider.Name, Namespace: provNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, provider)).To(Succeed())
		provider.Spec.ProviderType = "codex"
		Expect(k8sClient.Update(ctx, provider)).To(Succeed())

		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, provider)).To(Succeed())
		Expect(provider.Status.Phase).To(Equal(phaseFailed))
		ready := findSyncedCondition(provider.Status.Conditions)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Reason).To(Equal("InvalidProviderSpec"))

		stored, ok := fakeServer.providerOf(gatewayProviderName(provider))
		Expect(ok).To(BeTrue())
		Expect(stored.Type).To(Equal("claude-code"), "the gateway-side type must be untouched by the rejected update")
	})

	It("removes the provider via the finalizer before the CR is deleted", func() {
		provider := &ogov1alpha1.OpenShellProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "deletable", Namespace: provNamespace},
			Spec:       ogov1alpha1.OpenShellProviderSpec{ProviderType: "custom"},
		}
		Expect(k8sClient.Create(ctx, provider)).To(Succeed())

		r := reconciler()
		key := types.NamespacedName{Name: provider.Name, Namespace: provNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		_, ok := fakeServer.providerOf(gatewayProviderName(provider))
		Expect(ok).To(BeTrue())

		Expect(k8sClient.Delete(ctx, provider)).To(Succeed())
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		_, stillPresent := fakeServer.providerOf(gatewayProviderName(provider))
		Expect(stillPresent).To(BeFalse())

		err = k8sClient.Get(ctx, key, &ogov1alpha1.OpenShellProvider{})
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("does not drop the finalizer while the provider is still attached to a sandbox", func() {
		provider := &ogov1alpha1.OpenShellProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "in-use", Namespace: provNamespace},
			Spec:       ogov1alpha1.OpenShellProviderSpec{ProviderType: "custom"},
		}
		Expect(k8sClient.Create(ctx, provider)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, provider) }()

		r := reconciler()
		key := types.NamespacedName{Name: provider.Name, Namespace: provNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		fakeServer.setFailDeleteProviderName(gatewayProviderName(provider))
		Expect(k8sClient.Delete(ctx, provider)).To(Succeed())
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, provider)).To(Succeed())
		Expect(provider.Status.Phase).To(Equal(phaseFailed))
		ready := findSyncedCondition(provider.Status.Conditions)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Reason).To(Equal("DeletionBlocked"))
		_, stillPresent := fakeServer.providerOf(gatewayProviderName(provider))
		Expect(stillPresent).To(BeTrue())

		fakeServer.setFailDeleteProviderName("")
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		_, stillPresent = fakeServer.providerOf(gatewayProviderName(provider))
		Expect(stillPresent).To(BeFalse())
		err = k8sClient.Get(ctx, key, &ogov1alpha1.OpenShellProvider{})
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("reports GatewayNotFound without dialing when no OpenShellGateway exists", func() {
		Expect(k8sClient.Delete(ctx, gw)).To(Succeed())

		provider := &ogov1alpha1.OpenShellProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "no-gateway", Namespace: provNamespace},
			Spec:       ogov1alpha1.OpenShellProviderSpec{ProviderType: "custom"},
		}
		Expect(k8sClient.Create(ctx, provider)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, provider) }()

		r := reconciler()
		key := types.NamespacedName{Name: provider.Name, Namespace: provNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, provider)).To(Succeed())
		ready := findSyncedCondition(provider.Status.Conditions)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Reason).To(Equal("GatewayNotFound"))
		Expect(fakeServer.callLog()).To(BeEmpty())
	})

	It("reports SecretNotFound and KeyNotFound for invalid credential references", func() {
		provider := &ogov1alpha1.OpenShellProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-secret-ref", Namespace: provNamespace},
			Spec: ogov1alpha1.OpenShellProviderSpec{
				ProviderType: "custom",
				Credentials: map[string]ogov1alpha1.SecretKeyRef{
					"MISSING": {Name: "does-not-exist", Key: "k"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, provider)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, provider) }()

		r := reconciler()
		key := types.NamespacedName{Name: provider.Name, Namespace: provNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, provider)).To(Succeed())
		ready := findSyncedCondition(provider.Status.Conditions)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Reason).To(Equal("SecretNotFound"))

		createCredentialSecret("wrong-key-secret", "actual-key", "v")
		Expect(k8sClient.Get(ctx, key, provider)).To(Succeed())
		provider.Spec.Credentials = map[string]ogov1alpha1.SecretKeyRef{
			"MISSING": {Name: "wrong-key-secret", Key: "nonexistent-key"},
		}
		Expect(k8sClient.Update(ctx, provider)).To(Succeed())

		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, provider)).To(Succeed())
		ready = findSyncedCondition(provider.Status.Conditions)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Reason).To(Equal("KeyNotFound"))
	})

	It("deterministically reports the alphabetically-first broken credential ref", func() {
		// Regression test: credential refs are validated in sorted key
		// order specifically so that when multiple are broken at once,
		// which one is reported doesn't depend on Go's randomized map
		// iteration order.
		provider := &ogov1alpha1.OpenShellProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "multi-broken", Namespace: provNamespace},
			Spec: ogov1alpha1.OpenShellProviderSpec{
				ProviderType: "custom",
				Credentials: map[string]ogov1alpha1.SecretKeyRef{
					"ZZZ_KEY": {Name: "does-not-exist-z", Key: "k"},
					"AAA_KEY": {Name: "does-not-exist-a", Key: "k"},
					"MMM_KEY": {Name: "does-not-exist-m", Key: "k"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, provider)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, provider) }()

		r := reconciler()
		key := types.NamespacedName{Name: provider.Name, Namespace: provNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})

		for i := 0; i < 5; i++ {
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, key, provider)).To(Succeed())
			ready := findSyncedCondition(provider.Status.Conditions)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Reason).To(Equal("SecretNotFound"))
			Expect(ready.Message).To(ContainSubstring("AAA_KEY"), "AAA_KEY sorts first and must always be the one reported")
		}
	})
})
