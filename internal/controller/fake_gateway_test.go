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

	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	"github.com/aknochow/ogo/internal/openshellclient"
)

// fakeOpenShellServer is an in-process, in-memory stand-in for the real
// OpenShell gateway's workspace-membership, provider, and global-policy
// RPCs -- shared by OpenShellWorkspaceMember, OpenShellProvider, and
// OpenShellPolicy controller tests, since all three talk to the same real
// gRPC service.
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

	providers map[string]*openshellclient.Provider // keyed by gateway-side name

	// failDeleteProviderName, when set, makes DeleteProvider fail for that
	// exact name with FailedPrecondition -- simulates the real gateway's
	// sandbox-attached delete block.
	failDeleteProviderName string

	globalPolicy *openshellclient.SandboxPolicy // nil = unset

	// failGlobalPolicyInvalid, when true, makes a global UpdateConfig fail
	// with InvalidArgument -- simulates a real policy-content rejection.
	failGlobalPolicyInvalid bool
}

func newFakeOpenShellServer() *fakeOpenShellServer {
	return &fakeOpenShellServer{
		members:   map[string]map[string]openshellclient.WorkspaceRole{},
		providers: map[string]*openshellclient.Provider{},
	}
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

func (f *fakeOpenShellServer) setFailDeleteProviderName(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failDeleteProviderName = name
}

func (f *fakeOpenShellServer) setFailGlobalPolicyInvalid(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failGlobalPolicyInvalid = fail
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

func cloneStringMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// mergeStringMap replicates the real gateway's Provider.credentials/.config
// update semantics (provider.rs's merge_map, confirmed against the pinned
// v0.0.96 source): a key with a non-empty incoming value is upserted, a key
// with an empty-string incoming value is deleted, a key absent from
// incoming is left untouched.
func mergeStringMap(existing, incoming map[string]string) map[string]string {
	result := cloneStringMap(existing)
	for k, v := range incoming {
		if v == "" {
			delete(result, k)
		} else {
			result[k] = v
		}
	}
	return result
}

// CreateProvider matches the real gateway: WriteCondition::MustCreate, so a
// second Create for the same name fails with AlreadyExists.
func (f *fakeOpenShellServer) CreateProvider(_ context.Context, req *openshellclient.CreateProviderRequest) (*openshellclient.ProviderResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := req.Provider.Metadata.Name
	f.calls = append(f.calls, fmt.Sprintf("createProvider:%s", name))
	if _, exists := f.providers[name]; exists {
		return nil, grpcstatus.Error(codes.AlreadyExists, "provider already exists")
	}
	stored := &openshellclient.Provider{
		Metadata:    &openshellclient.ObjectMeta{Name: name},
		Type:        req.Provider.Type,
		Credentials: cloneStringMap(req.Provider.Credentials),
		Config:      cloneStringMap(req.Provider.Config),
	}
	f.providers[name] = stored
	return &openshellclient.ProviderResponse{Provider: stored}, nil
}

// UpdateProvider matches the real gateway: not_found on a missing provider,
// type is immutable once set, and credentials/config merge rather than
// replace (see mergeStringMap).
func (f *fakeOpenShellServer) UpdateProvider(_ context.Context, req *openshellclient.UpdateProviderRequest) (*openshellclient.ProviderResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := req.Provider.Metadata.Name
	f.calls = append(f.calls, fmt.Sprintf("updateProvider:%s", name))
	existing, ok := f.providers[name]
	if !ok {
		return nil, grpcstatus.Error(codes.NotFound, "provider not found")
	}
	incomingType := req.Provider.Type
	if incomingType != "" && incomingType != existing.Type {
		return nil, grpcstatus.Error(codes.InvalidArgument, "provider type cannot be changed; delete and recreate the provider")
	}
	updated := &openshellclient.Provider{
		Metadata:    existing.Metadata,
		Type:        existing.Type,
		Credentials: mergeStringMap(existing.Credentials, req.Provider.Credentials),
		Config:      mergeStringMap(existing.Config, req.Provider.Config),
	}
	f.providers[name] = updated
	return &openshellclient.ProviderResponse{Provider: updated}, nil
}

// DeleteProvider matches the real gateway: idempotent (deleted:false on
// not-found, no error), but blockable via failDeleteProviderName to
// simulate the real sandbox-attached FailedPrecondition.
func (f *fakeOpenShellServer) DeleteProvider(_ context.Context, req *openshellclient.DeleteProviderRequest) (*openshellclient.DeleteProviderResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("deleteProvider:%s", req.Name))
	if f.failDeleteProviderName != "" && req.Name == f.failDeleteProviderName {
		return nil, grpcstatus.Error(codes.FailedPrecondition, "provider is still attached to a sandbox")
	}
	if _, ok := f.providers[req.Name]; !ok {
		return &openshellclient.DeleteProviderResponse{Deleted: false}, nil
	}
	delete(f.providers, req.Name)
	return &openshellclient.DeleteProviderResponse{Deleted: true}, nil
}

// providerOf returns the stored provider by gateway-side name.
func (f *fakeOpenShellServer) providerOf(name string) (*openshellclient.Provider, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.providers[name]
	return p, ok
}

// UpdateConfig implements only the two shapes OGO's controllers ever send:
// setting the gateway-global policy (global:true, policy set) and deleting
// it (global:true, delete_setting:true, setting_key:"policy") -- matching
// the exact request shapes confirmed against the pinned v0.0.96 CLI/server
// source.
func (f *fakeOpenShellServer) UpdateConfig(_ context.Context, req *openshellclient.UpdateConfigRequest) (*openshellclient.UpdateConfigResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.DeleteSetting && req.Global && req.SettingKey == "policy" {
		f.calls = append(f.calls, "deleteGlobalPolicy")
		deleted := f.globalPolicy != nil
		f.globalPolicy = nil
		return &openshellclient.UpdateConfigResponse{Deleted: deleted}, nil
	}
	if req.Global {
		f.calls = append(f.calls, "setGlobalPolicy")
		if f.failGlobalPolicyInvalid {
			return nil, grpcstatus.Error(codes.InvalidArgument, "simulated invalid policy")
		}
		f.globalPolicy = req.Policy
		return &openshellclient.UpdateConfigResponse{Version: 1}, nil
	}
	return nil, grpcstatus.Error(codes.Unimplemented, "sandbox-scoped UpdateConfig not implemented in fake server")
}

// globalPolicySnapshot returns the currently stored global policy, or nil if
// unset.
func (f *fakeOpenShellServer) globalPolicySnapshot() *openshellclient.SandboxPolicy {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.globalPolicy
}

// startFakeGateway starts fakeOpenShellServer on an in-memory bufconn
// listener and returns a connectClient func wiring a reconciler to it,
// regardless of which OpenShellGateway CR is passed in.
func startFakeGateway(server *fakeOpenShellServer) (connectFn gatewayConnectFunc, stop func()) {
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

func findReadyCondition(conditions []metav1.Condition) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == "Ready" {
			return &conditions[i]
		}
	}
	return nil
}

// findSyncedCondition is findReadyCondition's counterpart for
// OpenShellProvider/OpenShellPolicy, whose condition Type is "Synced"
// (their own pre-existing convention, unchanged by this fix).
func findSyncedCondition(conditions []metav1.Condition) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == "Synced" {
			return &conditions[i]
		}
	}
	return nil
}
