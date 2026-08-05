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
	"time"

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

var _ = Describe("OpenShellPolicy Controller", func() {
	const (
		polGWName    = "policy-test-gw"
		polNamespace = "ogo-policy-test"
	)

	ctx := context.Background()

	var (
		fakeServer *fakeOpenShellServer
		stopServer func()
		gw         *ogov1alpha1.OpenShellGateway
	)

	reconciler := func() *OpenShellPolicyReconciler {
		connectFn, stop := startFakeGateway(fakeServer)
		stopServer = stop
		return &OpenShellPolicyReconciler{
			Client:        k8sClient,
			Scheme:        k8sClient.Scheme(),
			connectClient: connectFn,
		}
	}

	BeforeEach(func() {
		fakeServer = newFakeOpenShellServer()

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: polNamespace}}
		_ = k8sClient.Create(ctx, ns)

		gw = &ogov1alpha1.OpenShellGateway{
			ObjectMeta: metav1.ObjectMeta{Name: polGWName},
			Spec: ogov1alpha1.OpenShellGatewaySpec{
				Namespace: polNamespace,
				Database:  ogov1alpha1.DatabaseSpec{SecretName: "policy-test-pg-uri"},
				Auth: ogov1alpha1.AuthSpec{
					OpenShift: ogov1alpha1.OpenShiftAuth{UserGroup: "policy-test-users"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, gw)).To(Succeed())

		secret := generateAuthBridgeKeysSecret(polGWName+"-auth-bridge-keys", polNamespace)
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
	})

	AfterEach(func() {
		if stopServer != nil {
			stopServer()
		}

		// OpenShellPolicyReconciler adds a real finalizer; a bare Delete()
		// on a spec's own CR (via `defer`) without a follow-up Reconcile()
		// leaves it stuck with a DeletionTimestamp but never actually
		// removed from etcd. Since resolveActivePolicy lists across the
		// whole envtest cluster, a leftover CR from one spec would corrupt
		// the oldest-wins resolution for every later spec. Force-clear any
		// finalizers directly so every spec starts from a clean slate,
		// regardless of whether that spec exercised the real deletion path
		// itself.
		policies := &ogov1alpha1.OpenShellPolicyList{}
		if err := k8sClient.List(ctx, policies, client.InNamespace(polNamespace)); err == nil {
			for i := range policies.Items {
				p := &policies.Items[i]
				if len(p.Finalizers) > 0 {
					p.Finalizers = nil
					_ = k8sClient.Update(ctx, p)
				}
				_ = k8sClient.Delete(ctx, p)
			}
		}

		_ = k8sClient.Delete(ctx, &ogov1alpha1.OpenShellGateway{ObjectMeta: metav1.ObjectMeta{Name: polGWName}})
		_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: polGWName + "-auth-bridge-keys", Namespace: polNamespace}})
	})

	newPolicy := func(name string, network map[string]ogov1alpha1.NetworkPolicyRule) *ogov1alpha1.OpenShellPolicy {
		return &ogov1alpha1.OpenShellPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: polNamespace},
			Spec: ogov1alpha1.OpenShellPolicySpec{
				PolicyName: name,
				Filesystem: &ogov1alpha1.FilesystemPolicy{IncludeWorkdir: true, ReadOnly: []string{"/usr"}},
				Network:    network,
				Process:    &ogov1alpha1.ProcessPolicy{RunAsUser: "sandbox"},
			},
		}
	}

	It("pushes the sole policy as the gateway-global policy", func() {
		policy := newPolicy("baseline", map[string]ogov1alpha1.NetworkPolicyRule{
			"api": {Name: "api", Endpoints: []ogov1alpha1.NetworkEndpoint{
				{Host: "api.example.com", Port: 443, Protocol: "rest", Enforcement: "enforce", Access: "read-only"},
			}},
		})
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, policy) }()

		r := reconciler()
		key := types.NamespacedName{Name: policy.Name, Namespace: polNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key}) // adds finalizer
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		applied := fakeServer.globalPolicySnapshot()
		Expect(applied).NotTo(BeNil())
		Expect(applied.Filesystem.IncludeWorkdir).To(BeTrue())
		Expect(applied.Filesystem.ReadOnly).To(ConsistOf("/usr"))
		Expect(applied.Process.RunAsUser).To(Equal("sandbox"))
		Expect(applied.NetworkPolicies).To(HaveKey("api"))
		Expect(applied.NetworkPolicies["api"].Endpoints[0].Host).To(Equal("api.example.com"))
		Expect(applied.NetworkPolicies["api"].Endpoints[0].Port).To(Equal(uint32(443)))

		Expect(k8sClient.Get(ctx, key, policy)).To(Succeed())
		Expect(policy.Status.Phase).To(Equal("Synced"))
		Expect(policy.Status.AppliedToGateway).To(BeTrue())
		ready := findSyncedCondition(policy.Status.Conditions)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))
	})

	It("supersedes a second, newer policy CR and never touches the gateway for it", func() {
		first := newPolicy("first", nil)
		Expect(k8sClient.Create(ctx, first)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, first) }()

		r := reconciler()
		firstKey := types.NamespacedName{Name: first.Name, Namespace: polNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: firstKey})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: firstKey})
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeServer.globalPolicySnapshot()).NotTo(BeNil())

		// metav1.CreationTimestamp has only second-level granularity.
		// Without a real gap, both CRs can land in the same second, and
		// oldestByCreationTimestamp's tie-break then falls back to List's
		// return order (not creation order) -- making "which one is
		// active" depend on an implementation detail instead of genuine
		// chronological precedence.
		time.Sleep(1100 * time.Millisecond)

		second := newPolicy("second", nil)
		Expect(k8sClient.Create(ctx, second)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, second) }()
		secondKey := types.NamespacedName{Name: second.Name, Namespace: polNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: secondKey})
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: secondKey})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, secondKey, second)).To(Succeed())
		Expect(second.Status.Phase).To(Equal(phaseSuperseded))
		Expect(second.Status.AppliedToGateway).To(BeFalse())
		ready := findSyncedCondition(second.Status.Conditions)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Reason).To(Equal("AnotherPolicyActive"))

		Expect(fakeServer.callLog()).To(ConsistOf("setGlobalPolicy"), "the superseded CR must never call UpdateConfig")
	})

	It("pushes drift when the active policy's spec.network changes", func() {
		policy := newPolicy("drifting", map[string]ogov1alpha1.NetworkPolicyRule{
			"api": {Name: "api", Endpoints: []ogov1alpha1.NetworkEndpoint{{Host: "old.example.com", Port: 443}}},
		})
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, policy) }()

		r := reconciler()
		key := types.NamespacedName{Name: policy.Name, Namespace: polNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, policy)).To(Succeed())
		policy.Spec.Network = map[string]ogov1alpha1.NetworkPolicyRule{
			"api": {Name: "api", Endpoints: []ogov1alpha1.NetworkEndpoint{{Host: "new.example.com", Port: 443}}},
		}
		Expect(k8sClient.Update(ctx, policy)).To(Succeed())

		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		applied := fakeServer.globalPolicySnapshot()
		Expect(applied).NotTo(BeNil())
		Expect(applied.NetworkPolicies["api"].Endpoints[0].Host).To(Equal("new.example.com"))
	})

	It("retracts the global policy via the finalizer when the active CR is deleted", func() {
		policy := newPolicy("active-delete", nil)
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())

		r := reconciler()
		key := types.NamespacedName{Name: policy.Name, Namespace: polNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeServer.globalPolicySnapshot()).NotTo(BeNil())

		Expect(k8sClient.Delete(ctx, policy)).To(Succeed())
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(fakeServer.globalPolicySnapshot()).To(BeNil())
		Expect(fakeServer.callLog()).To(ContainElement("deleteGlobalPolicy"))

		err = k8sClient.Get(ctx, key, &ogov1alpha1.OpenShellPolicy{})
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("deletes a superseded CR without ever touching the gateway", func() {
		active := newPolicy("stays-active", nil)
		Expect(k8sClient.Create(ctx, active)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, active) }()

		r := reconciler()
		activeKey := types.NamespacedName{Name: active.Name, Namespace: polNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: activeKey})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: activeKey})
		Expect(err).NotTo(HaveOccurred())

		time.Sleep(1100 * time.Millisecond) // see comment above on tied CreationTimestamps

		superseded := newPolicy("gets-deleted", nil)
		Expect(k8sClient.Create(ctx, superseded)).To(Succeed())
		supersededKey := types.NamespacedName{Name: superseded.Name, Namespace: polNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: supersededKey})
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: supersededKey})
		Expect(err).NotTo(HaveOccurred())

		callsBeforeDelete := len(fakeServer.callLog())

		Expect(k8sClient.Delete(ctx, superseded)).To(Succeed())
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: supersededKey})
		Expect(err).NotTo(HaveOccurred())

		Expect(fakeServer.callLog()).To(HaveLen(callsBeforeDelete), "deleting a CR that was never applied to the gateway must make zero gateway calls")
		Expect(fakeServer.globalPolicySnapshot()).NotTo(BeNil(), "the still-active policy must remain untouched")

		err = k8sClient.Get(ctx, supersededKey, &ogov1alpha1.OpenShellPolicy{})
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("hands off to the next-oldest policy when the active CR is deleted", func() {
		older := newPolicy("older", map[string]ogov1alpha1.NetworkPolicyRule{
			"a": {Name: "a", Endpoints: []ogov1alpha1.NetworkEndpoint{{Host: "older.example.com", Port: 1}}},
		})
		Expect(k8sClient.Create(ctx, older)).To(Succeed())

		r := reconciler()
		olderKey := types.NamespacedName{Name: older.Name, Namespace: polNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: olderKey})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: olderKey})
		Expect(err).NotTo(HaveOccurred())

		time.Sleep(1100 * time.Millisecond) // see comment above on tied CreationTimestamps

		newer := newPolicy("newer", map[string]ogov1alpha1.NetworkPolicyRule{
			"b": {Name: "b", Endpoints: []ogov1alpha1.NetworkEndpoint{{Host: "newer.example.com", Port: 1}}},
		})
		Expect(k8sClient.Create(ctx, newer)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, newer) }()
		newerKey := types.NamespacedName{Name: newer.Name, Namespace: polNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: newerKey})
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: newerKey})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, newerKey, newer)).To(Succeed())
		Expect(newer.Status.Phase).To(Equal(phaseSuperseded))

		Expect(k8sClient.Delete(ctx, older)).To(Succeed())
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: olderKey})
		Expect(err).NotTo(HaveOccurred())

		// The gateway must never be left with no global policy at all during
		// the handoff: since a successor CR exists, older's own deletion
		// must skip the delete-global-policy call entirely and let newer's
		// reconcile (below) replace the policy directly.
		Expect(fakeServer.callLog()).NotTo(ContainElement("deleteGlobalPolicy"))
		Expect(fakeServer.globalPolicySnapshot()).NotTo(BeNil(), "the gateway must still hold older's policy until the successor overwrites it")

		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: newerKey})
		Expect(err).NotTo(HaveOccurred())

		applied := fakeServer.globalPolicySnapshot()
		Expect(applied).NotTo(BeNil())
		Expect(applied.NetworkPolicies).To(HaveKey("b"))

		Expect(k8sClient.Get(ctx, newerKey, newer)).To(Succeed())
		Expect(newer.Status.Phase).To(Equal("Synced"))
		Expect(newer.Status.AppliedToGateway).To(BeTrue())
	})

	It("reports InvalidPolicySpec when the gateway rejects the policy content", func() {
		policy := newPolicy("rejected", nil)
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, policy) }()

		fakeServer.setFailGlobalPolicyInvalid(true)

		r := reconciler()
		key := types.NamespacedName{Name: policy.Name, Namespace: polNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, policy)).To(Succeed())
		Expect(policy.Status.Phase).To(Equal(phaseFailed))
		Expect(policy.Status.AppliedToGateway).To(BeFalse())
		ready := findSyncedCondition(policy.Status.Conditions)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Reason).To(Equal("InvalidPolicySpec"))
	})

	It("reports GatewayNotFound without dialing when no OpenShellGateway exists", func() {
		Expect(k8sClient.Delete(ctx, gw)).To(Succeed())

		policy := newPolicy("no-gateway", nil)
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, policy) }()

		r := reconciler()
		key := types.NamespacedName{Name: policy.Name, Namespace: polNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, policy)).To(Succeed())
		ready := findSyncedCondition(policy.Status.Conditions)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Reason).To(Equal("GatewayNotFound"))
		Expect(fakeServer.callLog()).To(BeEmpty())
	})
})
