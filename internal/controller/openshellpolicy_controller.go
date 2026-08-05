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
	"fmt"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	"github.com/aknochow/ogo/internal/openshellclient"
)

const policyFinalizer = "ogo.aknochow.io/policy-cleanup"

// policySelfWatchPredicate re-enqueues every OpenShellPolicy CR only on
// Delete events. The only thing that ever needs another CR to re-evaluate
// itself is the active CR being deleted (so the next-oldest can be promoted
// without waiting for its own periodic reconcile) -- a newly created CR
// resolves its own active/superseded state via its own Reconcile call
// already. Reacting to Create/Update here would cause a continuous
// reconcile storm: every reconcile ends with a Status().Update() that bumps
// resourceVersion regardless of whether the content actually changed, which
// would re-trigger this same watch, re-enqueue every policy, and never
// settle into an idle steady state.
var policySelfWatchPredicate = predicate.Funcs{
	CreateFunc:  func(event.CreateEvent) bool { return false },
	UpdateFunc:  func(event.UpdateEvent) bool { return false },
	DeleteFunc:  func(event.DeleteEvent) bool { return true },
	GenericFunc: func(event.GenericEvent) bool { return false },
}

// OpenShellPolicyReconciler reconciles OpenShellPolicy objects. Unlike
// OpenShellProvider, the real gateway has no standalone named/reusable
// policy object -- only per-sandbox policy (which OGO doesn't manage) or a
// single gateway-wide global lock. OpenShellPolicy is therefore a singleton
// CRD, mirroring OpenShellGateway: only the oldest existing CR is ever the
// active gateway-global policy (see resolveActivePolicy); every other CR
// reports Superseded and never touches the gateway.
type OpenShellPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// connectClient constructs a client for the gateway's gRPC API and a func
	// to release it. See gatewayClient/dialConnectClient (openshell_grpc.go);
	// tests override it to point at an in-process fake server.
	connectClient gatewayConnectFunc
}

// resolveActivePolicy returns the oldest OpenShellPolicy CR across the
// cluster (namespace-scoped as a resource, but cluster-wide as a singleton,
// exactly like OpenShellGateway's own enforcement).
func resolveActivePolicy(ctx context.Context, c client.Client) (*ogov1alpha1.OpenShellPolicy, error) {
	policyList := &ogov1alpha1.OpenShellPolicyList{}
	if err := c.List(ctx, policyList); err != nil {
		return nil, err
	}
	if len(policyList.Items) == 0 {
		return nil, fmt.Errorf("no OpenShellPolicy found in the cluster")
	}
	items := make([]*ogov1alpha1.OpenShellPolicy, len(policyList.Items))
	for i := range policyList.Items {
		items[i] = &policyList.Items[i]
	}
	return oldestByCreationTimestamp(items), nil
}

// buildSandboxPolicy maps an OpenShellPolicySpec 1:1 onto the gateway's
// SandboxPolicy message.
func buildSandboxPolicy(spec ogov1alpha1.OpenShellPolicySpec) *openshellclient.SandboxPolicy {
	desired := &openshellclient.SandboxPolicy{}
	if spec.Filesystem != nil {
		desired.Filesystem = &openshellclient.FilesystemPolicy{
			IncludeWorkdir: spec.Filesystem.IncludeWorkdir,
			ReadOnly:       spec.Filesystem.ReadOnly,
			ReadWrite:      spec.Filesystem.ReadWrite,
		}
	}
	if spec.Process != nil {
		desired.Process = &openshellclient.ProcessPolicy{
			RunAsUser:  spec.Process.RunAsUser,
			RunAsGroup: spec.Process.RunAsGroup,
		}
	}
	if len(spec.Network) > 0 {
		desired.NetworkPolicies = make(map[string]*openshellclient.NetworkPolicyRule, len(spec.Network))
		for key, rule := range spec.Network {
			endpoints := make([]*openshellclient.NetworkEndpoint, 0, len(rule.Endpoints))
			for _, ep := range rule.Endpoints {
				endpoints = append(endpoints, &openshellclient.NetworkEndpoint{
					Host:        ep.Host,
					Port:        uint32(ep.Port),
					Protocol:    ep.Protocol,
					Enforcement: ep.Enforcement,
					Access:      ep.Access,
				})
			}
			binaries := make([]*openshellclient.NetworkBinary, 0, len(rule.Binaries))
			for _, b := range rule.Binaries {
				binaries = append(binaries, &openshellclient.NetworkBinary{Path: b.Path})
			}
			desired.NetworkPolicies[key] = &openshellclient.NetworkPolicyRule{
				Name:      rule.Name,
				Endpoints: endpoints,
				Binaries:  binaries,
			}
		}
	}
	return desired
}

// +kubebuilder:rbac:groups=gateway.ogo.aknochow.io,resources=openshellpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.ogo.aknochow.io,resources=openshellpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.ogo.aknochow.io,resources=openshellpolicies/finalizers,verbs=update

// Reconcile ensures the active OpenShellPolicy CR's spec matches the
// gateway's global policy, and that non-active CRs never touch it.
func (r *OpenShellPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	policy := &ogov1alpha1.OpenShellPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !policy.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, policy)
	}

	if !controllerutil.ContainsFinalizer(policy, policyFinalizer) {
		controllerutil.AddFinalizer(policy, policyFinalizer)
		if err := r.Update(ctx, policy); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	return r.reconcileSync(ctx, policy)
}

func (r *OpenShellPolicyReconciler) reconcileSync(ctx context.Context, policy *ogov1alpha1.OpenShellPolicy) (ctrl.Result, error) {
	active, err := resolveActivePolicy(ctx, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	if active.UID != policy.UID {
		meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
			Type: "Synced", Status: metav1.ConditionFalse, Reason: "AnotherPolicyActive",
			Message: fmt.Sprintf("OpenShellPolicy %s/%s is the active gateway policy; this resource has no effect until it is deleted", active.Namespace, active.Name),
		})
		policy.Status.Phase = phaseSuperseded
		policy.Status.ObservedGeneration = policy.Generation
		return ctrl.Result{RequeueAfter: requeueInterval}, r.Status().Update(ctx, policy)
	}

	gw, err := resolveGateway(ctx, r.Client)
	if err != nil {
		return r.setNotReady(ctx, policy, "GatewayNotFound", err)
	}

	rpcCtx, wsClient, closeFn, err := gatewayClient(ctx, r.Client, r.connectClient, gw)
	if err != nil {
		return r.setNotReady(ctx, policy, "GatewayUnreachable", fmt.Errorf("connecting to gateway: %w", err))
	}
	defer closeFn()

	_, err = wsClient.UpdateConfig(rpcCtx, &openshellclient.UpdateConfigRequest{
		Policy:    buildSandboxPolicy(policy.Spec),
		Global:    true,
		Workspace: defaultOpenShellWorkspace,
	})
	if err != nil {
		if grpcstatus.Code(err) == codes.InvalidArgument {
			return r.setNotReady(ctx, policy, "InvalidPolicySpec", err)
		}
		return r.setNotReady(ctx, policy, "GatewayUnreachable", fmt.Errorf("UpdateConfig: %w", err))
	}

	policy.Status.AppliedToGateway = true
	policy.Status.Phase = "Synced"
	policy.Status.ObservedGeneration = policy.Generation
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type: "Synced", Status: metav1.ConditionTrue, Reason: "Ready",
		Message: fmt.Sprintf("OpenShellPolicy %q is the active gateway-global policy", policy.Spec.PolicyName),
	})
	return ctrl.Result{RequeueAfter: requeueInterval}, r.Status().Update(ctx, policy)
}

// hasSuccessorPolicy reports whether any OpenShellPolicy CR other than
// excludeUID currently exists. Kubernetes List still returns an object with
// a DeletionTimestamp set until its last finalizer is removed, so the
// currently-deleting CR itself must be excluded explicitly.
func hasSuccessorPolicy(ctx context.Context, c client.Client, excludeUID types.UID) (bool, error) {
	policyList := &ogov1alpha1.OpenShellPolicyList{}
	if err := c.List(ctx, policyList); err != nil {
		return false, err
	}
	for _, p := range policyList.Items {
		if p.UID != excludeUID {
			return true, nil
		}
	}
	return false, nil
}

// reconcileDelete retracts this CR's policy from the gateway, but only if it
// was ever actually applied there (status.AppliedToGateway) -- a superseded
// CR that was never the active singleton must never touch gateway state.
//
// If another OpenShellPolicy CR exists, the retraction is skipped entirely:
// that CR's own reconcile (triggered by the self-watch) will push a full
// replacement policy via UpdateConfig, which is a full replace, not a merge.
// Retracting first would leave the gateway with no global policy at all
// (falling back to the restrictive default) for the window between this
// CR's finalizer running and the successor's reconcile catching up. Skipping
// the retraction instead means the deleted CR's policy content simply stays
// live, unowned, until the successor overwrites it -- a strictly safer
// transition with no gap.
func (r *OpenShellPolicyReconciler) reconcileDelete(ctx context.Context, policy *ogov1alpha1.OpenShellPolicy) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(policy, policyFinalizer) {
		return ctrl.Result{}, nil
	}

	if policy.Status.AppliedToGateway {
		hasSuccessor, err := hasSuccessorPolicy(ctx, r.Client, policy.UID)
		if err != nil {
			return ctrl.Result{}, err
		}

		if !hasSuccessor {
			gw, err := resolveGateway(ctx, r.Client)
			if err != nil {
				log.Error(err, "no gateway found during cleanup, will retry")
				return ctrl.Result{RequeueAfter: gatewayRetryInterval}, nil
			}

			rpcCtx, wsClient, closeFn, err := gatewayClient(ctx, r.Client, r.connectClient, gw)
			if err != nil {
				log.Error(err, "failed to connect to gateway during cleanup, will retry")
				return ctrl.Result{RequeueAfter: gatewayRetryInterval}, nil
			}
			defer closeFn()

			_, err = wsClient.UpdateConfig(rpcCtx, &openshellclient.UpdateConfigRequest{
				SettingKey:    "policy",
				DeleteSetting: true,
				Global:        true,
				Workspace:     defaultOpenShellWorkspace,
			})
			if err != nil {
				log.Error(err, "failed to delete global policy on gateway, will retry")
				return ctrl.Result{RequeueAfter: gatewayRetryInterval}, nil
			}
		}
	}

	// No need to persist status.AppliedToGateway = false here: this CR is
	// about to be fully deleted the moment its finalizer is gone (the plain
	// Update below can't write .status through the status subresource
	// anyway), and if the finalizer removal below fails for any reason, the
	// next reconcile re-reads AppliedToGateway fresh from etcd and redoes
	// this logic from scratch. Matches OpenShellWorkspaceMemberReconciler's
	// own reconcileDelete, which doesn't bother clearing status either.
	controllerutil.RemoveFinalizer(policy, policyFinalizer)
	return ctrl.Result{}, r.Update(ctx, policy)
}

// setNotReady records a Synced=False condition and a Failed phase. For
// GatewayUnreachable and InvalidPolicySpec, the underlying cause -- a raw
// gateway gRPC error, which can echo back gateway-internal detail -- is
// logged server-side only; the CR's status, readable by anyone with get
// access to it, gets a generic message instead.
func (r *OpenShellPolicyReconciler) setNotReady(ctx context.Context, policy *ogov1alpha1.OpenShellPolicy, reason string, cause error) (ctrl.Result, error) {
	message := cause.Error()
	switch reason {
	case "GatewayUnreachable":
		logf.FromContext(ctx).Error(cause, "gateway unreachable while reconciling policy")
		message = "failed to reach the OpenShell gateway; see operator logs for details"
	case "InvalidPolicySpec":
		logf.FromContext(ctx).Error(cause, "gateway rejected policy spec")
		message = "the gateway rejected this policy's spec; see operator logs for details"
	}
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type: "Synced", Status: metav1.ConditionFalse, Reason: reason, Message: message,
	})
	policy.Status.Phase = phaseFailed
	policy.Status.ObservedGeneration = policy.Generation
	if err := r.Status().Update(ctx, policy); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: gatewayRetryInterval}, nil
}

func (r *OpenShellPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ogov1alpha1.OpenShellPolicy{}).
		// findAllPolicies ignores which type triggered it and just requeues
		// every existing OpenShellPolicy CR, so the same handler serves
		// both watches below. Both are filtered: every reconcile ends with a
		// Status().Update() that bumps resourceVersion regardless of
		// whether the content actually changed, so an unfiltered watch on
		// either type would fire again on that update, re-enqueue every
		// policy, and never settle into an idle steady state.
		//
		// Gateway: only a real spec change (a gateway newly created, or its
		// config changed) can affect whether a Policy CR that was stuck on
		// GatewayNotFound can now proceed -- the gateway's own periodic
		// status-only refresh never matters here.
		Watches(&ogov1alpha1.OpenShellGateway{},
			handler.EnqueueRequestsFromMapFunc(r.findAllPolicies),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		// Self-watch, filtered to Delete only -- see policySelfWatchPredicate.
		Watches(&ogov1alpha1.OpenShellPolicy{},
			handler.EnqueueRequestsFromMapFunc(r.findAllPolicies),
			builder.WithPredicates(policySelfWatchPredicate),
		).
		Named("openshellpolicy").
		Complete(r)
}

func (r *OpenShellPolicyReconciler) findAllPolicies(ctx context.Context, _ client.Object) []reconcile.Request {
	policies := &ogov1alpha1.OpenShellPolicyList{}
	if err := r.List(ctx, policies); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(policies.Items))
	for _, p := range policies.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: p.Name, Namespace: p.Namespace},
		})
	}
	return requests
}
