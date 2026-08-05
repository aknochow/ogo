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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	"github.com/aknochow/ogo/internal/openshellclient"
)

const policyFinalizer = "ogo.aknochow.io/policy-cleanup"

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
func buildSandboxPolicy(spec *ogov1alpha1.OpenShellPolicySpec) *openshellclient.SandboxPolicy {
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
		Policy:    buildSandboxPolicy(&policy.Spec),
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

// reconcileDelete retracts this CR's policy from the gateway, but only if it
// was ever actually applied there (status.AppliedToGateway) -- a superseded
// CR that was never the active singleton must never touch gateway state.
func (r *OpenShellPolicyReconciler) reconcileDelete(ctx context.Context, policy *ogov1alpha1.OpenShellPolicy) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(policy, policyFinalizer) {
		return ctrl.Result{}, nil
	}

	if policy.Status.AppliedToGateway {
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
		policy.Status.AppliedToGateway = false
	}

	controllerutil.RemoveFinalizer(policy, policyFinalizer)
	return ctrl.Result{}, r.Update(ctx, policy)
}

// setNotReady records a Synced=False condition and a Failed phase. For
// GatewayUnreachable specifically, the underlying cause is logged
// server-side only; the CR's status gets a generic message instead.
func (r *OpenShellPolicyReconciler) setNotReady(ctx context.Context, policy *ogov1alpha1.OpenShellPolicy, reason string, cause error) (ctrl.Result, error) {
	message := cause.Error()
	if reason == "GatewayUnreachable" {
		logf.FromContext(ctx).Error(cause, "gateway unreachable while reconciling policy")
		message = "failed to reach the OpenShell gateway; see operator logs for details"
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
		Watches(&ogov1alpha1.OpenShellGateway{},
			handler.EnqueueRequestsFromMapFunc(r.findPoliciesForGateway),
		).
		// Self-watch so deleting the active CR promotes the next-oldest
		// surviving one promptly, instead of waiting up to requeueInterval
		// for its own periodic reconcile to notice.
		Watches(&ogov1alpha1.OpenShellPolicy{},
			handler.EnqueueRequestsFromMapFunc(r.findAllPolicies),
		).
		Named("openshellpolicy").
		Complete(r)
}

func (r *OpenShellPolicyReconciler) findPoliciesForGateway(ctx context.Context, _ client.Object) []reconcile.Request {
	return r.findAllPolicies(ctx, nil)
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
