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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	"github.com/aknochow/ogo/internal/openshellclient"
)

// fakeOpenShellServer is an in-process, in-memory stand-in for the real
// OpenShell gateway's workspace-membership RPCs.
type fakeOpenShellServer struct {
	openshellclient.UnimplementedOpenShellServer

	mu      sync.Mutex
	members map[string]map[string]openshellclient.WorkspaceRole // workspace -> subject -> role
	calls   []string

	// failRemoveSubject, when set, makes RemoveWorkspaceMember fail for that
	// exact subject only -- used to simulate a transient remote failure.
	failRemoveSubject string

	// failAddSubject, when set, makes AddWorkspaceMember fail for that exact
	// subject only when it is not already a member -- i.e. it only affects a
	// genuine add/re-add, never the AlreadyExists path an existing member
	// hits. Used to simulate the re-add half of a role change failing after
	// the old-role membership has already been removed.
	failAddSubject string
}

func newFakeOpenShellServer() *fakeOpenShellServer {
	return &fakeOpenShellServer{members: map[string]map[string]openshellclient.WorkspaceRole{}}
}

func (f *fakeOpenShellServer) setFailRemoveSubject(subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failRemoveSubject = subject
}

func (f *fakeOpenShellServer) setFailAddSubject(subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAddSubject = subject
}

// AddWorkspaceMember matches the real gateway's actual behavior (confirmed
// live against SNO): a second Add for the same (workspace, subject) fails
// with AlreadyExists rather than overwriting the role in place.
func (f *fakeOpenShellServer) AddWorkspaceMember(_ context.Context, req *openshellclient.AddWorkspaceMemberRequest) (*openshellclient.AddWorkspaceMemberResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("add:%s:%s:%s", req.Workspace, req.PrincipalSubject, req.Role))
	if f.members[req.Workspace] == nil {
		f.members[req.Workspace] = map[string]openshellclient.WorkspaceRole{}
	}
	if _, exists := f.members[req.Workspace][req.PrincipalSubject]; exists {
		return nil, grpcstatus.Error(codes.AlreadyExists, "member already exists in this workspace")
	}
	if f.failAddSubject != "" && req.PrincipalSubject == f.failAddSubject {
		return nil, grpcstatus.Error(codes.Unavailable, "simulated remote failure")
	}
	f.members[req.Workspace][req.PrincipalSubject] = req.Role
	return &openshellclient.AddWorkspaceMemberResponse{Member: &openshellclient.WorkspaceMember{
		PrincipalSubject: req.PrincipalSubject, Role: req.Role,
	}}, nil
}

func (f *fakeOpenShellServer) RemoveWorkspaceMember(_ context.Context, req *openshellclient.RemoveWorkspaceMemberRequest) (*openshellclient.RemoveWorkspaceMemberResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("remove:%s:%s", req.Workspace, req.PrincipalSubject))
	if f.failRemoveSubject != "" && req.PrincipalSubject == f.failRemoveSubject {
		return nil, grpcstatus.Error(codes.Unavailable, "simulated remote failure")
	}
	removed := false
	if members := f.members[req.Workspace]; members != nil {
		if _, ok := members[req.PrincipalSubject]; ok {
			delete(members, req.PrincipalSubject)
			removed = true
		}
	}
	return &openshellclient.RemoveWorkspaceMemberResponse{Removed: removed}, nil
}

func (f *fakeOpenShellServer) ListWorkspaceMembers(_ context.Context, req *openshellclient.ListWorkspaceMembersRequest) (*openshellclient.ListWorkspaceMembersResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	members := make([]*openshellclient.WorkspaceMember, 0, len(f.members[req.Workspace]))
	for subject, role := range f.members[req.Workspace] {
		members = append(members, &openshellclient.WorkspaceMember{PrincipalSubject: subject, Role: role})
	}
	return &openshellclient.ListWorkspaceMembersResponse{Members: members}, nil
}

// roleOf looks up a member's role in the "default" workspace -- the only
// one these tests exercise.
func (f *fakeOpenShellServer) roleOf(subject string) (openshellclient.WorkspaceRole, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	role, ok := f.members["default"][subject]
	return role, ok
}

func (f *fakeOpenShellServer) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// startFakeGateway starts fakeOpenShellServer on an in-memory bufconn
// listener and returns a connectClient func wiring the reconciler to it,
// regardless of which OpenShellGateway CR is passed in.
func startFakeGateway(server *fakeOpenShellServer) (connectFn func(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (openshellclient.OpenShellClient, func(), error), stop func()) {
	const bufSize = 1024 * 1024
	lis := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer()
	openshellclient.RegisterOpenShellServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(lis) }()

	connectFn = func(ctx context.Context, _ *ogov1alpha1.OpenShellGateway) (openshellclient.OpenShellClient, func(), error) {
		conn, err := grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			return nil, nil, err
		}
		return openshellclient.NewOpenShellClient(conn), func() { _ = conn.Close() }, nil
	}
	stop = grpcServer.Stop
	return connectFn, stop
}

// generateAuthBridgeKeysSecret returns a Secret shaped like the real
// <gateway>-auth-bridge-keys Secret the OpenShellGateway controller manages.
func generateAuthBridgeKeysSecret(name, namespace string) *corev1.Secret {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	Expect(err).NotTo(HaveOccurred())
	privBytes, err := x509.MarshalPKCS8PrivateKey(key)
	Expect(err).NotTo(HaveOccurred())
	signingPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privBytes})

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       map[string][]byte{"signing.pem": signingPEM, "kid": []byte("test-kid")},
	}
}

var _ = Describe("OpenShellWorkspaceMember Controller", func() {
	const (
		wsGWName    = "wsmember-test-gw"
		wsNamespace = "ogo-test"
		wsWorkspace = "default"
	)

	ctx := context.Background()

	var (
		fakeServer *fakeOpenShellServer
		stopServer func()
		gw         *ogov1alpha1.OpenShellGateway
	)

	reconciler := func() *OpenShellWorkspaceMemberReconciler {
		connectFn, stop := startFakeGateway(fakeServer)
		stopServer = stop
		return &OpenShellWorkspaceMemberReconciler{
			Client:        k8sClient,
			Scheme:        k8sClient.Scheme(),
			connectClient: connectFn,
		}
	}

	BeforeEach(func() {
		fakeServer = newFakeOpenShellServer()

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: wsNamespace}}
		_ = k8sClient.Create(ctx, ns)

		gw = &ogov1alpha1.OpenShellGateway{
			ObjectMeta: metav1.ObjectMeta{Name: wsGWName},
			Spec: ogov1alpha1.OpenShellGatewaySpec{
				Namespace: wsNamespace,
				Database:  ogov1alpha1.DatabaseSpec{SecretName: "wsmember-test-pg-uri"},
				Auth: ogov1alpha1.AuthSpec{
					OpenShift: ogov1alpha1.OpenShiftAuth{UserGroup: "wsmember-test-users"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, gw)).To(Succeed())

		secret := generateAuthBridgeKeysSecret(wsGWName+"-auth-bridge-keys", wsNamespace)
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
	})

	AfterEach(func() {
		if stopServer != nil {
			stopServer()
		}
		_ = k8sClient.Delete(ctx, &ogov1alpha1.OpenShellGateway{ObjectMeta: metav1.ObjectMeta{Name: wsGWName}})
		_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: wsGWName + "-auth-bridge-keys", Namespace: wsNamespace}})
	})

	createServiceAccount := func(name string) *corev1.ServiceAccount {
		sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: wsNamespace}}
		Expect(k8sClient.Create(ctx, sa)).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: wsNamespace}, sa)).To(Succeed())
		return sa
	}

	It("adds the ServiceAccount's UID as a workspace member", func() {
		sa := createServiceAccount("wsmember-sa-1")
		wm := &ogov1alpha1.OpenShellWorkspaceMember{
			ObjectMeta: metav1.ObjectMeta{Name: "wm-1", Namespace: wsNamespace},
			Spec: ogov1alpha1.OpenShellWorkspaceMemberSpec{
				Workspace:         wsWorkspace,
				ServiceAccountRef: ogov1alpha1.ServiceAccountReference{Name: sa.Name},
				Role:              "user",
			},
		}
		Expect(k8sClient.Create(ctx, wm)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, wm) }()

		r := reconciler()
		key := types.NamespacedName{Name: wm.Name, Namespace: wsNamespace}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key}) // adds finalizer
		Expect(err).NotTo(HaveOccurred())
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key}) // does the real work
		Expect(err).NotTo(HaveOccurred())

		role, ok := fakeServer.roleOf(string(sa.UID))
		Expect(ok).To(BeTrue())
		Expect(role).To(Equal(openshellclient.WorkspaceRole_WORKSPACE_ROLE_USER))

		Expect(k8sClient.Get(ctx, key, wm)).To(Succeed())
		Expect(wm.Status.Phase).To(Equal("Synced"))
		Expect(wm.Status.ReconciledSubject).To(Equal(string(sa.UID)))
		ready := findReadyCondition(wm.Status.Conditions)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))
	})

	It("tolerates re-reconciling an already-correct membership (AlreadyExists)", func() {
		// The real gateway rejects a second AddWorkspaceMember for the same
		// (workspace, subject) with AlreadyExists (confirmed live on SNO) --
		// a reconcile loop will always eventually hit this on an unchanged
		// object. This must not surface as a Failed/Ready=False cycle.
		sa := createServiceAccount("wsmember-sa-5")
		wm := &ogov1alpha1.OpenShellWorkspaceMember{
			ObjectMeta: metav1.ObjectMeta{Name: "wm-5", Namespace: wsNamespace},
			Spec: ogov1alpha1.OpenShellWorkspaceMemberSpec{
				Workspace:         wsWorkspace,
				ServiceAccountRef: ogov1alpha1.ServiceAccountReference{Name: sa.Name},
				Role:              "user",
			},
		}
		Expect(k8sClient.Create(ctx, wm)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, wm) }()

		r := reconciler()
		key := types.NamespacedName{Name: wm.Name, Namespace: wsNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key}) // adds finalizer
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		// A third reconcile of the same, unchanged object -- this is where
		// AddWorkspaceMember hits AlreadyExists.
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, wm)).To(Succeed())
		Expect(wm.Status.Phase).To(Equal("Synced"))
		ready := findReadyCondition(wm.Status.Conditions)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))

		role, ok := fakeServer.roleOf(string(sa.UID))
		Expect(ok).To(BeTrue())
		Expect(role).To(Equal(openshellclient.WorkspaceRole_WORKSPACE_ROLE_USER))
	})

	It("grants admin role when spec.role is admin", func() {
		sa := createServiceAccount("wsmember-sa-admin")
		wm := &ogov1alpha1.OpenShellWorkspaceMember{
			ObjectMeta: metav1.ObjectMeta{Name: "wm-admin", Namespace: wsNamespace},
			Spec: ogov1alpha1.OpenShellWorkspaceMemberSpec{
				Workspace:         wsWorkspace,
				ServiceAccountRef: ogov1alpha1.ServiceAccountReference{Name: sa.Name},
				Role:              "admin",
			},
		}
		Expect(k8sClient.Create(ctx, wm)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, wm) }()

		r := reconciler()
		key := types.NamespacedName{Name: wm.Name, Namespace: wsNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		role, ok := fakeServer.roleOf(string(sa.UID))
		Expect(ok).To(BeTrue())
		Expect(role).To(Equal(openshellclient.WorkspaceRole_WORKSPACE_ROLE_ADMIN))
	})

	It("treats an unset spec.tls.enabled as TLS-required, not as plaintext", func() {
		// Regression test: dialCredentials must default a nil TLS.Enabled the
		// same way every other TLS.Enabled check in this package does (nil
		// means "not explicitly disabled", not "disabled"). Exercise the real
		// dialCredentials directly since the fake gRPC server bypasses it.
		gwNoTLS := &ogov1alpha1.OpenShellGateway{
			Spec: ogov1alpha1.OpenShellGatewaySpec{Namespace: wsNamespace},
		}
		_, err := dialCredentials(ctx, k8sClient, gwNoTLS)
		// With TLS.Enabled nil, dialCredentials must attempt the TLS path (and
		// fail here since no client-TLS secret exists in this test) rather
		// than silently falling back to plaintext insecure credentials.
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("client TLS secret"))
	})

	It("clears ReconciledSubject when a role-change re-add fails after the old membership was removed", func() {
		// Regression test: addMember's role-change path first removes the
		// old-role membership, then re-adds with the new role. If that
		// re-add fails, the subject is left with no membership at all --
		// status must not go on claiming currentSubject is reconciled.
		sa := createServiceAccount("wsmember-sa-8")
		uid := string(sa.UID)

		wm := &ogov1alpha1.OpenShellWorkspaceMember{
			ObjectMeta: metav1.ObjectMeta{Name: "wm-8", Namespace: wsNamespace},
			Spec: ogov1alpha1.OpenShellWorkspaceMemberSpec{
				Workspace:         wsWorkspace,
				ServiceAccountRef: ogov1alpha1.ServiceAccountReference{Name: sa.Name},
				Role:              "user",
			},
		}
		Expect(k8sClient.Create(ctx, wm)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, wm) }()

		r := reconciler()
		key := types.NamespacedName{Name: wm.Name, Namespace: wsNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key}) // adds finalizer
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		role, ok := fakeServer.roleOf(uid)
		Expect(ok).To(BeTrue())
		Expect(role).To(Equal(openshellclient.WorkspaceRole_WORKSPACE_ROLE_USER))

		// Change the role and make the re-add half of the remove+re-add
		// sequence fail, simulating a transient error between the two calls.
		Expect(k8sClient.Get(ctx, key, wm)).To(Succeed())
		wm.Spec.Role = "admin"
		Expect(k8sClient.Update(ctx, wm)).To(Succeed())
		fakeServer.setFailAddSubject(uid)

		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		_, stillPresent := fakeServer.roleOf(uid)
		Expect(stillPresent).To(BeFalse(), "the old-role membership was removed and the re-add failed, so no membership should remain")

		Expect(k8sClient.Get(ctx, key, wm)).To(Succeed())
		Expect(wm.Status.Phase).To(Equal("Failed"))
		Expect(wm.Status.ReconciledSubject).To(BeEmpty(), "status must not claim a subject that no longer holds any workspace membership")
		ready := findReadyCondition(wm.Status.Conditions)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Reason).To(Equal("GatewayUnreachable"))

		// Clear the injected failure -- the next reconcile should self-heal
		// and re-add cleanly with the new role.
		fakeServer.setFailAddSubject("")
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		role, ok = fakeServer.roleOf(uid)
		Expect(ok).To(BeTrue())
		Expect(role).To(Equal(openshellclient.WorkspaceRole_WORKSPACE_ROLE_ADMIN))

		Expect(k8sClient.Get(ctx, key, wm)).To(Succeed())
		Expect(wm.Status.Phase).To(Equal("Synced"))
		Expect(wm.Status.ReconciledSubject).To(Equal(uid))
	})

	It("removes stale membership and adds new membership when the ServiceAccount is recreated", func() {
		sa := createServiceAccount("wsmember-sa-2")
		oldUID := string(sa.UID)

		wm := &ogov1alpha1.OpenShellWorkspaceMember{
			ObjectMeta: metav1.ObjectMeta{Name: "wm-2", Namespace: wsNamespace},
			Spec: ogov1alpha1.OpenShellWorkspaceMemberSpec{
				Workspace:         wsWorkspace,
				ServiceAccountRef: ogov1alpha1.ServiceAccountReference{Name: sa.Name},
			},
		}
		Expect(k8sClient.Create(ctx, wm)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, wm) }()

		r := reconciler()
		key := types.NamespacedName{Name: wm.Name, Namespace: wsNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		_, ok := fakeServer.roleOf(oldUID)
		Expect(ok).To(BeTrue())

		Expect(k8sClient.Delete(ctx, sa)).To(Succeed())
		sa = createServiceAccount("wsmember-sa-2")
		newUID := string(sa.UID)
		Expect(newUID).NotTo(Equal(oldUID))

		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		_, oldStillPresent := fakeServer.roleOf(oldUID)
		Expect(oldStillPresent).To(BeFalse(), "stale membership for the recreated identity's old UID should have been removed")
		_, newPresent := fakeServer.roleOf(newUID)
		Expect(newPresent).To(BeTrue())

		Expect(k8sClient.Get(ctx, key, wm)).To(Succeed())
		Expect(wm.Status.ReconciledSubject).To(Equal(newUID))
	})

	It("does not grant the new identity when removing stale membership fails", func() {
		sa := createServiceAccount("wsmember-sa-6")
		oldUID := string(sa.UID)

		wm := &ogov1alpha1.OpenShellWorkspaceMember{
			ObjectMeta: metav1.ObjectMeta{Name: "wm-6", Namespace: wsNamespace},
			Spec: ogov1alpha1.OpenShellWorkspaceMemberSpec{
				Workspace:         wsWorkspace,
				ServiceAccountRef: ogov1alpha1.ServiceAccountReference{Name: sa.Name},
			},
		}
		Expect(k8sClient.Create(ctx, wm)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, wm) }()

		r := reconciler()
		key := types.NamespacedName{Name: wm.Name, Namespace: wsNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Delete(ctx, sa)).To(Succeed())
		sa = createServiceAccount("wsmember-sa-6")
		newUID := string(sa.UID)

		fakeServer.setFailRemoveSubject(oldUID)
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		_, oldStillPresent := fakeServer.roleOf(oldUID)
		Expect(oldStillPresent).To(BeTrue(), "old membership should be untouched since its removal failed")
		_, newPresent := fakeServer.roleOf(newUID)
		Expect(newPresent).To(BeFalse(), "the new identity must not be granted access while stale cleanup is unresolved")

		Expect(k8sClient.Get(ctx, key, wm)).To(Succeed())
		Expect(wm.Status.Phase).To(Equal("Failed"))
		ready := findReadyCondition(wm.Status.Conditions)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Reason).To(Equal("GatewayUnreachable"))
	})

	It("sanitizes the GatewayUnreachable condition message instead of leaking the underlying cause", func() {
		sa := createServiceAccount("wsmember-sa-7")
		wm := &ogov1alpha1.OpenShellWorkspaceMember{
			ObjectMeta: metav1.ObjectMeta{Name: "wm-7", Namespace: wsNamespace},
			Spec: ogov1alpha1.OpenShellWorkspaceMemberSpec{
				Workspace:         wsWorkspace,
				ServiceAccountRef: ogov1alpha1.ServiceAccountReference{Name: sa.Name},
			},
		}
		Expect(k8sClient.Create(ctx, wm)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, wm) }()

		// Remove the auth-bridge signing key Secret this BeforeEach created,
		// so mintAdminToken fails with a detailed, Secret-referencing error.
		Expect(k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: wsGWName + "-auth-bridge-keys", Namespace: wsNamespace},
		})).To(Succeed())

		r := reconciler()
		key := types.NamespacedName{Name: wm.Name, Namespace: wsNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, key, wm)).To(Succeed())
		ready := findReadyCondition(wm.Status.Conditions)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Reason).To(Equal("GatewayUnreachable"))
		Expect(ready.Message).NotTo(ContainSubstring("auth-bridge-keys"))
		Expect(ready.Message).NotTo(ContainSubstring("Secret"))
		Expect(ready.Message).To(Equal("failed to reach the OpenShell gateway; see operator logs for details"))
	})

	It("removes membership and reports IdentityNotFound when the ServiceAccount is deleted", func() {
		sa := createServiceAccount("wsmember-sa-3")
		uid := string(sa.UID)

		wm := &ogov1alpha1.OpenShellWorkspaceMember{
			ObjectMeta: metav1.ObjectMeta{Name: "wm-3", Namespace: wsNamespace},
			Spec: ogov1alpha1.OpenShellWorkspaceMemberSpec{
				Workspace:         wsWorkspace,
				ServiceAccountRef: ogov1alpha1.ServiceAccountReference{Name: sa.Name},
			},
		}
		Expect(k8sClient.Create(ctx, wm)).To(Succeed())
		defer func() { _ = k8sClient.Delete(ctx, wm) }()

		r := reconciler()
		key := types.NamespacedName{Name: wm.Name, Namespace: wsNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		_, ok := fakeServer.roleOf(uid)
		Expect(ok).To(BeTrue())

		Expect(k8sClient.Delete(ctx, sa)).To(Succeed())
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		_, stillPresent := fakeServer.roleOf(uid)
		Expect(stillPresent).To(BeFalse())

		Expect(k8sClient.Get(ctx, key, wm)).To(Succeed())
		Expect(wm.Status.Phase).To(Equal("Failed"))
		Expect(wm.Status.ReconciledSubject).To(BeEmpty())
		ready := findReadyCondition(wm.Status.Conditions)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal("IdentityNotFound"))
	})

	It("removes membership via the finalizer before the CR is deleted", func() {
		sa := createServiceAccount("wsmember-sa-4")
		uid := string(sa.UID)

		wm := &ogov1alpha1.OpenShellWorkspaceMember{
			ObjectMeta: metav1.ObjectMeta{Name: "wm-4", Namespace: wsNamespace},
			Spec: ogov1alpha1.OpenShellWorkspaceMemberSpec{
				Workspace:         wsWorkspace,
				ServiceAccountRef: ogov1alpha1.ServiceAccountReference{Name: sa.Name},
			},
		}
		Expect(k8sClient.Create(ctx, wm)).To(Succeed())

		r := reconciler()
		key := types.NamespacedName{Name: wm.Name, Namespace: wsNamespace}
		_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		_, ok := fakeServer.roleOf(uid)
		Expect(ok).To(BeTrue())

		Expect(k8sClient.Delete(ctx, wm)).To(Succeed())
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		_, stillPresent := fakeServer.roleOf(uid)
		Expect(stillPresent).To(BeFalse())
		Expect(fakeServer.callLog()).To(ContainElement(fmt.Sprintf("remove:%s:%s", wsWorkspace, uid)))

		err = k8sClient.Get(ctx, key, &ogov1alpha1.OpenShellWorkspaceMember{})
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})
})

func findReadyCondition(conditions []metav1.Condition) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == "Ready" {
			return &conditions[i]
		}
	}
	return nil
}
