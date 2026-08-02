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
	"crypto/sha256"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/discovery"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	"github.com/aknochow/ogo/internal/gateway"
	"github.com/aknochow/ogo/internal/openshift"
	"github.com/aknochow/ogo/internal/pki"
)

const (
	finalizerName    = "ogo.aknochow.io/gateway-cleanup"
	labelManagedBy   = "app.kubernetes.io/managed-by"
	labelInstance    = "app.kubernetes.io/instance"
	labelName        = "app.kubernetes.io/name"
	labelPartOf      = "app.kubernetes.io/part-of"
	requeueInterval  = 60 * time.Second
	managedByValue   = "ogo"
	defaultNamespace = "ogo"
	phaseFailed      = "Failed"

	// reasonHostnameMissing is set by both reconcileGatewayAPI and
	// reconcileEnvoyRoute (each independently checks route.hostname, since
	// one runs regardless of isOCP and the other only on OpenShift), and
	// checked again at the reconcileEnvoyRoute call site to avoid
	// escalating this specific reason to Phase: Failed. A shared constant
	// keeps those three sites from silently drifting apart.
	reasonHostnameMissing = "HostnameMissing"
)

type OpenShellGatewayReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	DiscoveryClient discovery.DiscoveryInterface
}

// +kubebuilder:rbac:groups=gateway.ogo.aknochow.io,resources=openshellgateways,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.ogo.aknochow.io,resources=openshellgateways/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.ogo.aknochow.io,resources=openshellgateways/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services;serviceaccounts;configmaps;secrets;namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
// +kubebuilder:rbac:groups=agents.x-k8s.io,resources=sandboxes;sandboxes/status,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings;roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes/custom-host,verbs=create;patch
// +kubebuilder:rbac:groups=security.openshift.io,resources=securitycontextconstraints,verbs=use
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=oauth.openshift.io,resources=oauthclients,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=user.openshift.io,resources=groups,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways;grpcroutes;gatewayclasses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.envoyproxy.io,resources=backendtrafficpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create;update;patch

func (r *OpenShellGatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	gw := &ogov1alpha1.OpenShellGateway{}
	if err := r.Get(ctx, req.NamespacedName, gw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Singleton enforcement — fail the newer gateways, not the oldest
	gwList := &ogov1alpha1.OpenShellGatewayList{}
	if err := r.List(ctx, gwList); err != nil {
		return ctrl.Result{}, err
	}
	if len(gwList.Items) > 1 {
		oldest := gwList.Items[0]
		for _, item := range gwList.Items[1:] {
			if item.CreationTimestamp.Before(&oldest.CreationTimestamp) {
				oldest = item
			}
		}
		if gw.Name != oldest.Name {
			meta.SetStatusCondition(&gw.Status.Conditions, metav1.Condition{
				Type: ogov1alpha1.ConditionDegraded, Status: metav1.ConditionTrue,
				Reason: "MultipleGateways", Message: fmt.Sprintf("Only one OpenShellGateway is allowed per cluster; %q is the active instance", oldest.Name),
			})
			gw.Status.Phase = ogov1alpha1.PhaseFailed
			return ctrl.Result{}, r.Status().Update(ctx, gw)
		}
	}

	if !gw.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, gw)
	}

	if !controllerutil.ContainsFinalizer(gw, finalizerName) {
		controllerutil.AddFinalizer(gw, finalizerName)
		if err := r.Update(ctx, gw); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	isOCP := openshift.IsOpenShift(r.DiscoveryClient)
	hasGWAPI := openshift.HasGatewayAPI(r.DiscoveryClient)
	useGWAPI := gatewayAPIEnabled(gw, hasGWAPI)
	ns := gatewayNamespace(gw)
	sandboxNS := sandboxNamespace(gw)

	log.Info("Reconciling OpenShellGateway", "namespace", ns, "sandbox_namespace", sandboxNS, "openshift", isOCP, "gatewayAPI", useGWAPI)

	// Phase 1: Auto-provision dependencies
	for _, dep := range r.dependencies() {
		if !dep.Enabled(ctx, gw) {
			continue
		}
		condition, err := dep.Reconcile(ctx, gw)
		meta.SetStatusCondition(&gw.Status.Conditions, condition)
		if err != nil {
			log.Error(err, "Dependency reconcile failed", "dependency", dep.Name())
			return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setDegraded(ctx, gw, dep.Name(), err)
		}
	}

	// Validate prerequisites before creating resources
	if err := r.validateDatabaseSecret(ctx, gw); err != nil {
		log.Error(err, "Database secret validation failed")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setDegraded(ctx, gw, "DatabaseSecret", err)
	}

	// Phase 2: Core gateway resources
	steps := []struct {
		name string
		fn   func(context.Context, *ogov1alpha1.OpenShellGateway) error
	}{
		{"Namespace", r.reconcileNamespace},
		{"GatewayServiceAccount", r.reconcileGatewayServiceAccount},
		{"SandboxServiceAccount", r.reconcileSandboxServiceAccount},
		{"ClusterRole", r.reconcileClusterRole},
		{"ClusterRoleBinding", r.reconcileClusterRoleBinding},
		{"Role", r.reconcileRole},
		{"RoleBinding", r.reconcileRoleBinding},
		{"TLS", r.reconcileTLS},
		{"AuthBridgeCA", r.reconcileAuthBridgeCA},
		{"JWTKeys", r.reconcileJWTKeys},
		{"AuthBridgeKeys", r.reconcileAuthBridgeKeys},
		{"ConfigMap", r.reconcileConfigMap},
		{"Deployment", r.reconcileDeployment},
		{"Service", r.reconcileService},
		{"NetworkPolicy", r.reconcileNetworkPolicy},
	}

	for _, step := range steps {
		if err := step.fn(ctx, gw); err != nil {
			log.Error(err, "Reconcile step failed", "step", step.name)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setDegraded(ctx, gw, step.name, err)
		}
	}

	if useGWAPI {
		if err := r.reconcileGatewayAPI(ctx, gw); err != nil {
			log.Error(err, "Failed to reconcile Gateway API resources")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setDegraded(ctx, gw, "GatewayAPI", err)
		}
	}

	if isOCP {
		if useGWAPI {
			condition, err := r.reconcileEnvoyRoute(ctx, gw)
			if condition.Type != "" {
				meta.SetStatusCondition(&gw.Status.Conditions, condition)
			}
			// reconcileEnvoyRoute returns nil for the reasonHostnameMissing
			// case (an incomplete config waiting on the user, not a
			// reconcile failure - the condition set above already surfaces
			// exactly what is missing), so any non-nil error here is a
			// genuine failure. No special-casing needed at this call site.
			if err != nil {
				log.Error(err, "Failed to reconcile Envoy Route")
				return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setDegraded(ctx, gw, "EnvoyRoute", err)
			}
		} else {
			if err := r.reconcileRoute(ctx, gw); err != nil {
				log.Error(err, "Failed to reconcile Route")
				return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setDegraded(ctx, gw, "Route", err)
			}
		}
		if err := r.reconcileSCCBinding(ctx, gw); err != nil {
			log.Error(err, "Failed to reconcile SCC binding")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setDegraded(ctx, gw, "SCCBinding", err)
		}
		if authBridgeEnabled(gw, isOCP) {
			if err := r.reconcileAuthBridgeRoute(ctx, gw); err != nil {
				log.Error(err, "Failed to reconcile auth-bridge Route")
				return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setDegraded(ctx, gw, "AuthBridgeRoute", err)
			}
			if err := r.reconcileOAuthClient(ctx, gw); err != nil {
				log.Error(err, "Failed to reconcile OAuthClient")
				return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setDegraded(ctx, gw, "OAuthClient", err)
			}
		}
	}

	return ctrl.Result{RequeueAfter: requeueInterval}, r.updateStatus(ctx, gw)
}

// --- Deletion ---

func (r *OpenShellGatewayReconciler) reconcileDelete(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(gw, finalizerName) {
		return ctrl.Result{}, nil
	}

	log.Info("Cleaning up gateway resources")

	// Clean up dependencies in reverse order
	deps := r.dependencies()
	for i := len(deps) - 1; i >= 0; i-- {
		if err := deps[i].Cleanup(ctx, gw); err != nil {
			log.Error(err, "Failed to clean up dependency", "name", deps[i].Name())
		}
	}

	ns := gatewayNamespace(gw)
	sandboxNS := sandboxNamespace(gw)

	clusterResources := []client.Object{
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-node-reader"}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-node-reader"}},
	}

	if openshift.IsOpenShift(r.DiscoveryClient) {
		clusterResources = append(clusterResources,
			&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-sandbox-scc-privileged"}})
	}

	{ // Gateway API cleanup — attempt unconditionally; NotFound is expected if CRDs absent
		for _, gvk := range []schema.GroupVersionKind{
			{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"},
			{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "GRPCRoute"},
		} {
			obj := &unstructured.Unstructured{}
			obj.SetGroupVersionKind(gvk)
			obj.SetName(gw.Name)
			obj.SetNamespace(ns)
			if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
				log.Error(err, "Failed to delete Gateway API resource", "kind", gvk.Kind)
			}
		}
		btpObj := &unstructured.Unstructured{}
		btpObj.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.envoyproxy.io", Version: "v1alpha1", Kind: "BackendTrafficPolicy"})
		btpObj.SetName(gw.Name + "-timeout")
		btpObj.SetNamespace(ns)
		if err := r.Delete(ctx, btpObj); err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete BackendTrafficPolicy")
		}
		svcList := &corev1.ServiceList{}
		if err := r.List(ctx, svcList, client.MatchingLabels{
			"gateway.envoyproxy.io/owning-gateway-name":      gw.Name,
			"gateway.envoyproxy.io/owning-gateway-namespace": ns,
		}); err == nil && len(svcList.Items) > 0 {
			envoyRoute := &unstructured.Unstructured{}
			envoyRoute.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
			envoyRoute.SetName(gw.Name + "-gw")
			envoyRoute.SetNamespace(svcList.Items[0].Namespace)
			if err := r.Delete(ctx, envoyRoute); err != nil && !apierrors.IsNotFound(err) {
				log.Error(err, "Failed to delete Envoy Route")
			}
		}
	}

	var cleanupErrors []error
	for _, obj := range clusterResources {
		if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete cluster resource", "resource", obj.GetName())
			cleanupErrors = append(cleanupErrors, err)
		}
	}

	if sandboxNS != ns {
		crossNSResources := []client.Object{
			&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-sandbox", Namespace: sandboxNS}},
			&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-sandbox", Namespace: sandboxNS}},
			&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-sandbox", Namespace: sandboxNS}},
			&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-sandbox-ssh", Namespace: sandboxNS}},
		}
		for _, obj := range crossNSResources {
			if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
				log.Error(err, "Failed to delete cross-namespace resource", "resource", obj.GetName())
				cleanupErrors = append(cleanupErrors, err)
			}
		}
	}

	authCA := &corev1.ConfigMap{}
	authCAKey := types.NamespacedName{Name: gw.Name + "-auth-ca", Namespace: ns}
	if err := r.Get(ctx, authCAKey, authCA); err == nil {
		if authCA.Labels[labelManagedBy] == managedByValue && authCA.Labels[labelInstance] == gw.Name {
			if err := r.Delete(ctx, authCA); err != nil && !apierrors.IsNotFound(err) {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
	} else if !apierrors.IsNotFound(err) {
		cleanupErrors = append(cleanupErrors, err)
	}

	if len(cleanupErrors) > 0 {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, fmt.Errorf("cleanup incomplete: %d errors", len(cleanupErrors))
	}

	controllerutil.RemoveFinalizer(gw, finalizerName)
	return ctrl.Result{}, r.Update(ctx, gw)
}

// --- Validation ---

func (r *OpenShellGatewayReconciler) validateDatabaseSecret(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	ns := gatewayNamespace(gw)
	secretName := databaseSecretName(gw)
	if secretName == "" {
		return fmt.Errorf("either spec.database.secretName or spec.database.embedded must be set")
	}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ns}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("database secret %q not found in namespace %q", secretName, ns)
		}
		return fmt.Errorf("checking database secret: %w", err)
	}
	if _, ok := secret.Data["uri"]; !ok {
		return fmt.Errorf("database secret %q missing required key \"uri\"", secretName)
	}
	return nil
}

func databaseSecretName(gw *ogov1alpha1.OpenShellGateway) string {
	if gw.Spec.Database.SecretName != "" {
		return gw.Spec.Database.SecretName
	}
	if gw.Spec.Database.Embedded {
		return gw.Name + "-pg-uri"
	}
	return ""
}

// --- Namespace ---

func (r *OpenShellGatewayReconciler) reconcileNamespace(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	for _, nsName := range uniqueNamespaces(gw) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, ns, func() error {
			if ns.Labels == nil {
				ns.Labels = map[string]string{}
			}
			ns.Labels[labelManagedBy] = managedByValue
			return nil
		})
		if err != nil {
			return fmt.Errorf("ensuring namespace %s: %w", nsName, err)
		}
	}
	return nil
}

// --- ServiceAccounts ---

func (r *OpenShellGatewayReconciler) reconcileGatewayServiceAccount(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name:      gw.Name,
		Namespace: gatewayNamespace(gw),
	}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		sa.Labels = gatewayLabels(gw)
		return nil
	})
	return err
}

func (r *OpenShellGatewayReconciler) reconcileSandboxServiceAccount(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name:      gw.Name + "-sandbox",
		Namespace: sandboxNamespace(gw),
	}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		sa.Labels = gatewayLabels(gw)
		return nil
	})
	return err
}

// --- RBAC ---

func (r *OpenShellGatewayReconciler) reconcileClusterRole(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	cr := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-node-reader"}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cr, func() error {
		cr.Labels = gatewayLabels(gw)
		cr.Rules = []rbacv1.PolicyRule{
			{APIGroups: []string{"authentication.k8s.io"}, Resources: []string{"tokenreviews"}, Verbs: []string{"create"}},
			{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"get", "list", "watch"}},
			// auth-bridge looks up Group CR membership directly for ServiceAccount
			// identities, since the users/~ self-lookup API never returns their
			// custom Group memberships (see checkGroupMemberships in authbridge/openshift.go).
			{APIGroups: []string{"user.openshift.io"}, Resources: []string{"groups"}, Verbs: []string{"get"}},
		}
		return nil
	})
	return err
}

func (r *OpenShellGatewayReconciler) reconcileClusterRoleBinding(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	crb := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-node-reader"}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, crb, func() error {
		crb.Labels = gatewayLabels(gw)
		crb.RoleRef = rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: gw.Name + "-node-reader"}
		crb.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: gw.Name, Namespace: gatewayNamespace(gw)}}
		return nil
	})
	return err
}

func (r *OpenShellGatewayReconciler) reconcileRole(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-sandbox", Namespace: sandboxNamespace(gw)}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
		role.Labels = gatewayLabels(gw)
		role.Rules = []rbacv1.PolicyRule{
			{APIGroups: []string{"agents.x-k8s.io"}, Resources: []string{"sandboxes", "sandboxes/status"}, Verbs: []string{"create", "delete", "get", "list", "patch", "update", "watch"}},
			{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}},
		}
		return nil
	})
	return err
}

func (r *OpenShellGatewayReconciler) reconcileRoleBinding(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	rb := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-sandbox", Namespace: sandboxNamespace(gw)}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, rb, func() error {
		rb.Labels = gatewayLabels(gw)
		rb.RoleRef = rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: gw.Name + "-sandbox"}
		rb.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: gw.Name, Namespace: gatewayNamespace(gw)}}
		return nil
	})
	return err
}

// --- TLS ---

func (r *OpenShellGatewayReconciler) reconcileTLS(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	if gw.Spec.TLS.Enabled != nil && !*gw.Spec.TLS.Enabled {
		return nil
	}

	if gw.Spec.TLS.ServerCertSecretName != "" {
		return nil
	}

	// Always generate self-signed certs for internal mTLS (client certs,
	// CA) — the gateway pod's own listener never presents a publicly
	// trusted cert, since it also has to be trusted by the self-signed
	// client CA for supervisor mTLS. When cert-manager is enabled,
	// separately issue a public cert for the Gateway API listener only
	// (see reconcileGatewayAPI / reconcileGatewayTLSCert).
	if err := r.reconcileSelfSignedTLS(ctx, gw); err != nil {
		return err
	}

	if gw.Spec.TLS.CertManager.Enabled && gw.Spec.Route.Hostname != "" {
		certSecretName := gw.Name + "-gateway-tls"
		if err := r.reconcileGatewayTLSCert(ctx, gw, certSecretName, gw.Spec.Route.Hostname); err != nil {
			return fmt.Errorf("reconciling cert-manager TLS certificate: %w", err)
		}
	}

	return nil
}

func (r *OpenShellGatewayReconciler) reconcileSelfSignedTLS(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	ns := gatewayNamespace(gw)
	serverSecretName := gw.Name + "-server-tls"
	clientSecretName := gw.Name + "-client-tls"
	sans := computeServerSANs(gw)
	sansHash := pki.HashSANs(sans)

	serverSecret := &corev1.Secret{}
	serverErr := r.Get(ctx, types.NamespacedName{Name: serverSecretName, Namespace: ns}, serverSecret)
	if serverErr != nil && !apierrors.IsNotFound(serverErr) {
		return fmt.Errorf("checking server TLS secret: %w", serverErr)
	}
	clientSecret := &corev1.Secret{}
	clientErr := r.Get(ctx, types.NamespacedName{Name: clientSecretName, Namespace: ns}, clientSecret)
	if clientErr != nil && !apierrors.IsNotFound(clientErr) {
		return fmt.Errorf("checking client TLS secret: %w", clientErr)
	}

	if serverErr == nil && clientErr == nil {
		if serverSecret.Annotations != nil && serverSecret.Annotations["ogo.aknochow.io/pki-sans-hash"] == sansHash {
			return nil
		}
	}

	bundle, err := pki.GeneratePKI(sans)
	if err != nil {
		return fmt.Errorf("generating PKI: %w", err)
	}

	server := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: serverSecretName, Namespace: ns}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, server, func() error {
		server.Labels = gatewayLabels(gw)
		if server.Annotations == nil {
			server.Annotations = map[string]string{}
		}
		server.Annotations["ogo.aknochow.io/pki-sans-hash"] = sansHash
		server.Type = corev1.SecretTypeTLS
		server.Data = map[string][]byte{"tls.crt": bundle.ServerCert, "tls.key": bundle.ServerKey, "ca.crt": bundle.CACert}
		return nil
	}); err != nil {
		return fmt.Errorf("creating server TLS secret: %w", err)
	}

	clientTLSSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: clientSecretName, Namespace: ns}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, clientTLSSecret, func() error {
		clientTLSSecret.Labels = gatewayLabels(gw)
		clientTLSSecret.Type = corev1.SecretTypeTLS
		clientTLSSecret.Data = map[string][]byte{"tls.crt": bundle.ClientCert, "tls.key": bundle.ClientKey, "ca.crt": bundle.CACert}
		return nil
	}); err != nil {
		return fmt.Errorf("creating client TLS secret: %w", err)
	}

	return nil
}

func (r *OpenShellGatewayReconciler) reconcileAuthBridgeCA(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	name := gw.Name + "-auth-ca"
	namespace := gatewayNamespace(gw)
	configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	tlsEnabled := gw.Spec.TLS.Enabled == nil || *gw.Spec.TLS.Enabled
	if !tlsEnabled || !authBridgeEnabled(gw, openshift.IsOpenShift(r.DiscoveryClient)) {
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, configMap); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		if configMap.Labels[labelManagedBy] == managedByValue && configMap.Labels[labelInstance] == gw.Name {
			return r.Delete(ctx, configMap)
		}
		return nil
	}

	destinationCA, err := r.serverTLSCA(ctx, gw)
	if err != nil {
		return err
	}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, configMap, func() error {
		if !configMap.CreationTimestamp.IsZero() &&
			(configMap.Labels[labelManagedBy] != managedByValue || configMap.Labels[labelInstance] != gw.Name) {
			return fmt.Errorf("existing ConfigMap %s/%s is not managed by OGO", namespace, name)
		}
		configMap.Labels = gatewayLabels(gw)
		configMap.Data = map[string]string{"ca.crt": string(destinationCA)}
		configMap.BinaryData = nil
		return nil
	})
	return err
}

func (r *OpenShellGatewayReconciler) serverTLSCA(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) ([]byte, error) {
	secretName := gw.Name + "-server-tls"
	if gw.Spec.TLS.ServerCertSecretName != "" {
		secretName = gw.Spec.TLS.ServerCertSecretName
	}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: gatewayNamespace(gw)}, secret); err != nil {
		return nil, fmt.Errorf("reading server TLS CA: %w", err)
	}
	destinationCA := secret.Data["ca.crt"]
	if len(destinationCA) == 0 {
		destinationCA = secret.Data[corev1.TLSCertKey]
	}
	if len(destinationCA) == 0 {
		return nil, fmt.Errorf("server TLS secret %q has neither ca.crt nor tls.crt", secretName)
	}
	return destinationCA, nil
}

// --- JWT Keys ---

func (r *OpenShellGatewayReconciler) reconcileJWTKeys(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	return r.ensureEd25519KeySecret(ctx, gw, gw.Name+"-jwt-keys")
}

func (r *OpenShellGatewayReconciler) reconcileAuthBridgeKeys(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	ns := gatewayNamespace(gw)
	secretName := gw.Name + "-auth-bridge-keys"
	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ns}, existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("checking auth-bridge keys: %w", err)
	}

	keys, err := pki.GenerateRSAKeys()
	if err != nil {
		return fmt.Errorf("generating auth-bridge RSA keys: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns, Labels: gatewayLabels(gw)},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"signing.pem": keys.SigningKey, "public.pem": keys.PublicKey, "kid": []byte(keys.KID)},
	}
	return r.Create(ctx, secret)
}

func (r *OpenShellGatewayReconciler) ensureEd25519KeySecret(ctx context.Context, gw *ogov1alpha1.OpenShellGateway, secretName string) error {
	ns := gatewayNamespace(gw)
	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ns}, existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("checking key secret %s: %w", secretName, err)
	}

	keys, err := pki.GenerateJWTKeys()
	if err != nil {
		return fmt.Errorf("generating keys for %s: %w", secretName, err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns, Labels: gatewayLabels(gw)},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"signing.pem": keys.SigningKey, "public.pem": keys.PublicKey, "kid": []byte(keys.KID)},
	}
	return r.Create(ctx, secret)
}

// --- ConfigMap ---

func (r *OpenShellGatewayReconciler) reconcileConfigMap(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	ns := gatewayNamespace(gw)
	isOCP := openshift.IsOpenShift(r.DiscoveryClient)
	var oidcIssuer string
	if authBridgeEnabled(gw, isOCP) {
		oidcIssuer = authBridgeInternalURL(gw)
	}
	toml := gateway.RenderGatewayTOML(gw, sandboxNamespace(gw), oidcIssuer)

	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-config", Namespace: ns}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = gatewayLabels(gw)
		cm.Data = map[string]string{"gateway.toml": toml}
		return nil
	})
	return err
}

// --- Deployment ---

func (r *OpenShellGatewayReconciler) reconcileDeployment(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	ns := gatewayNamespace(gw)
	isOCP := openshift.IsOpenShift(r.DiscoveryClient)
	tlsEnabled := gw.Spec.TLS.Enabled == nil || *gw.Spec.TLS.Enabled

	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: gw.Name, Namespace: ns}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		replicas := int32(1)
		if gw.Spec.Replicas != nil {
			replicas = *gw.Spec.Replicas
		}
		deploy.Spec.Replicas = &replicas

		labels := gatewayLabels(gw)
		deploy.Labels = labels
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: selectorLabels(gw)}

		var oidcIssuer string
		if authBridgeEnabled(gw, isOCP) {
			oidcIssuer = authBridgeInternalURL(gw)
		}
		configHash := computeConfigHash(gateway.RenderGatewayTOML(gw, sandboxNamespace(gw), oidcIssuer))

		image := gw.Spec.Image
		if image == "" {
			image = "ghcr.io/nvidia/openshell/gateway"
		}
		if gw.Spec.ImageTag != "" {
			image = image + ":" + gw.Spec.ImageTag
		}

		container := corev1.Container{
			Name:  "openshell-gateway",
			Image: image,
			Args:  []string{"--config", "/etc/openshell/gateway.toml"},
			Env: []corev1.EnvVar{{
				Name: "OPENSHELL_DB_URL",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: databaseSecretName(gw)},
						Key:                  "uri",
					},
				},
			}},
			Ports: []corev1.ContainerPort{
				{Name: "grpc", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
				{Name: "health", ContainerPort: 8081, Protocol: corev1.ProtocolTCP},
				{Name: "metrics", ContainerPort: 9090, Protocol: corev1.ProtocolTCP},
			},
			StartupProbe: &corev1.Probe{
				ProbeHandler:  corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromString("health")}},
				PeriodSeconds: 2, FailureThreshold: 30,
			},
			LivenessProbe: &corev1.Probe{
				ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromString("health")}},
				InitialDelaySeconds: 2, PeriodSeconds: 5, FailureThreshold: 3,
			},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromString("health")}},
				InitialDelaySeconds: 1, PeriodSeconds: 2, FailureThreshold: 3,
			},
			Resources: gw.Spec.Resources,
			VolumeMounts: []corev1.VolumeMount{
				{Name: "gateway-config", MountPath: "/etc/openshell", ReadOnly: true},
				{Name: "sandbox-jwt", MountPath: "/etc/openshell-jwt", ReadOnly: true},
			},
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: ptr.To(false),
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
		}

		if !isOCP {
			container.SecurityContext.RunAsNonRoot = ptr.To(true)
		}

		if tlsEnabled {
			container.VolumeMounts = append(container.VolumeMounts,
				corev1.VolumeMount{Name: "tls-cert", MountPath: "/etc/openshell-tls/server", ReadOnly: true},
				corev1.VolumeMount{Name: "tls-client-ca", MountPath: "/etc/openshell-tls/client-ca", ReadOnly: true},
			)
		}

		volumes := []corev1.Volume{
			{Name: "gateway-config", VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: gw.Name + "-config"}},
			}},
			{Name: "sandbox-jwt", VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: gw.Name + "-jwt-keys", DefaultMode: ptr.To(int32(0400))},
			}},
			{Name: "auth-bridge-keys", VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: gw.Name + "-auth-bridge-keys", DefaultMode: ptr.To(int32(0400))},
			}},
		}

		if tlsEnabled {
			serverSecretName := gw.Name + "-server-tls"
			if gw.Spec.TLS.ServerCertSecretName != "" {
				serverSecretName = gw.Spec.TLS.ServerCertSecretName
			}
			clientCASecretName := gw.Name + "-client-tls"
			volumes = append(volumes,
				corev1.Volume{Name: "tls-cert", VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{SecretName: serverSecretName},
				}},
				corev1.Volume{Name: "tls-client-ca", VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: clientCASecretName,
						Items:      []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}},
					},
				}},
			)
		}

		containers := []corev1.Container{container}

		if authBridgeEnabled(gw, isOCP) {
			authPortName := "auth"
			authPort := int32(8085)
			probeScheme := corev1.URISchemeHTTP
			authEnv := []corev1.EnvVar{
				{Name: "AUTH_BRIDGE_ISSUER", Value: authBridgeInternalURL(gw)},
				{Name: "AUTH_BRIDGE_EXTERNAL_ISSUER", Value: authBridgeExternalURL(gw)},
				{Name: "AUTH_BRIDGE_OPENSHIFT_ISSUER", Value: "https://oauth-openshift." + clusterDomain(gw)},
				{Name: "AUTH_BRIDGE_CLIENT_ID", Value: "openshell"},
				{Name: "AUTH_BRIDGE_CLIENT_SECRET", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: gw.Name + "-oauth-client"},
						Key:                  "secret",
					},
				}},
				{Name: "AUTH_BRIDGE_USER_GROUP", Value: gw.Spec.Auth.OpenShift.UserGroup},
				{Name: "AUTH_BRIDGE_ADMIN_GROUP", Value: gw.Spec.Auth.OpenShift.AdminGroup},
				{Name: "AUTH_BRIDGE_TOKEN_TTL", Value: tokenTTL(gw)},
				{Name: "AUTH_BRIDGE_SIGNING_KEY", Value: "/etc/auth-bridge-keys/signing.pem"},
				{Name: "AUTH_BRIDGE_PUBLIC_KEY", Value: "/etc/auth-bridge-keys/public.pem"},
				{Name: "AUTH_BRIDGE_KID", Value: "/etc/auth-bridge-keys/kid"},
			}
			authVolumeMounts := []corev1.VolumeMount{
				{Name: "auth-bridge-keys", MountPath: "/etc/auth-bridge-keys", ReadOnly: true},
			}
			if tlsEnabled {
				authPortName = "auth-tls"
				authPort = 8443
				probeScheme = corev1.URISchemeHTTPS
				authEnv = append(authEnv,
					corev1.EnvVar{Name: "AUTH_BRIDGE_LISTEN", Value: "127.0.0.1:8085"},
					corev1.EnvVar{Name: "AUTH_BRIDGE_TLS_LISTEN", Value: ":8443"},
					corev1.EnvVar{Name: "AUTH_BRIDGE_TLS_CERT", Value: "/etc/auth-bridge-tls/tls.crt"},
					corev1.EnvVar{Name: "AUTH_BRIDGE_TLS_KEY", Value: "/etc/auth-bridge-tls/tls.key"},
				)
				authVolumeMounts = append(authVolumeMounts,
					corev1.VolumeMount{Name: "tls-cert", MountPath: "/etc/auth-bridge-tls", ReadOnly: true},
				)
			}
			containers = append(containers, corev1.Container{
				Name:         "auth-bridge",
				Image:        authBridgeImage(gw),
				Env:          authEnv,
				VolumeMounts: authVolumeMounts,
				Ports: []corev1.ContainerPort{
					{Name: authPortName, ContainerPort: authPort, Protocol: corev1.ProtocolTCP},
				},
				LivenessProbe: &corev1.Probe{
					ProbeHandler:  corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromString(authPortName), Scheme: probeScheme}},
					PeriodSeconds: 10,
				},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromString(authPortName), Scheme: probeScheme}},
					InitialDelaySeconds: 1, PeriodSeconds: 5,
				},
				StartupProbe: &corev1.Probe{
					ProbeHandler:     corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromString(authPortName), Scheme: probeScheme}},
					PeriodSeconds:    2,
					FailureThreshold: 15,
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("10m"),
						corev1.ResourceMemory: resource.MustParse("32Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
				},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			})
		}

		podSpec := corev1.PodSpec{
			ServiceAccountName:            gw.Name,
			TerminationGracePeriodSeconds: ptr.To(int64(5)),
			Containers:                    containers,
			Volumes:                       volumes,
		}

		if !isOCP {
			podSpec.SecurityContext = &corev1.PodSecurityContext{
				FSGroup: ptr.To(int64(1000)), RunAsUser: ptr.To(int64(1000)),
			}
		}

		deploy.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels:      labels,
				Annotations: map[string]string{"ogo.aknochow.io/config-hash": configHash},
			},
			Spec: podSpec,
		}
		return nil
	})
	return err
}

// --- Service ---

func (r *OpenShellGatewayReconciler) reconcileService(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: gw.Name, Namespace: gatewayNamespace(gw)}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = gatewayLabels(gw)
		isOCP := openshift.IsOpenShift(r.DiscoveryClient)
		ports := []corev1.ServicePort{
			{Name: "grpc", Port: 8080, TargetPort: intstr.FromString("grpc"), Protocol: corev1.ProtocolTCP, AppProtocol: ptr.To("grpc")},
			{Name: "metrics", Port: 9090, TargetPort: intstr.FromString("metrics"), Protocol: corev1.ProtocolTCP},
		}
		if authBridgeEnabled(gw, isOCP) {
			targetPort := "auth"
			if gw.Spec.TLS.Enabled == nil || *gw.Spec.TLS.Enabled {
				targetPort = "auth-tls"
			}
			ports = append(ports, corev1.ServicePort{
				Name: "auth", Port: 8085, TargetPort: intstr.FromString(targetPort), Protocol: corev1.ProtocolTCP,
			})
		}
		svc.Spec = corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: selectorLabels(gw),
			Ports:    ports,
		}
		return nil
	})
	return err
}

// --- NetworkPolicy ---

func (r *OpenShellGatewayReconciler) reconcileNetworkPolicy(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	if gw.Spec.NetworkPolicy.Enabled != nil && !*gw.Spec.NetworkPolicy.Enabled {
		existing := &networkingv1.NetworkPolicy{}
		if err := r.Get(ctx, types.NamespacedName{Name: gw.Name + "-sandbox-ssh", Namespace: sandboxNamespace(gw)}, existing); err == nil {
			if err := r.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("cleaning up disabled NetworkPolicy: %w", err)
			}
		}
		return nil
	}

	np := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-sandbox-ssh", Namespace: sandboxNamespace(gw)}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, np, func() error {
		np.Labels = gatewayLabels(gw)
		np.Spec = networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"openshell.ai/managed-by": "openshell"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": gatewayNamespace(gw)}},
					PodSelector:       &metav1.LabelSelector{MatchLabels: selectorLabels(gw)},
				}},
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(2222))}},
			}},
		}
		return nil
	})
	return err
}

// --- OpenShift Route ---

func (r *OpenShellGatewayReconciler) reconcileRoute(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	if gw.Spec.Route.Enabled != nil && !*gw.Spec.Route.Enabled {
		return nil
	}

	ns := gatewayNamespace(gw)
	tlsEnabled := gw.Spec.TLS.Enabled == nil || *gw.Spec.TLS.Enabled
	tlsTermination := "passthrough"
	if !tlsEnabled {
		tlsTermination = "edge"
	}
	tlsConfig := map[string]interface{}{"termination": tlsTermination}
	if !tlsEnabled {
		tlsConfig["insecureEdgeTerminationPolicy"] = "Redirect"
	}
	spec := map[string]interface{}{
		"to":   map[string]interface{}{"kind": "Service", "name": gw.Name},
		"port": map[string]interface{}{"targetPort": "grpc"},
		"tls":  tlsConfig,
	}
	if gw.Spec.Route.Hostname != "" {
		spec["host"] = gw.Spec.Route.Hostname
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
	err := r.Get(ctx, types.NamespacedName{Name: gw.Name, Namespace: ns}, existing)
	if apierrors.IsNotFound(err) {
		route := &unstructured.Unstructured{}
		route.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
		route.SetName(gw.Name)
		route.SetNamespace(ns)
		route.SetLabels(gatewayLabels(gw))
		route.Object["spec"] = spec
		return r.Create(ctx, route)
	}
	if err != nil {
		return err
	}

	existingHost, _, _ := unstructured.NestedString(existing.Object, "spec", "host")
	existingTLS, _, _ := unstructured.NestedString(existing.Object, "spec", "tls", "termination")
	needsRecreate := (gw.Spec.Route.Hostname != "" && existingHost != gw.Spec.Route.Hostname) ||
		existingTLS != tlsTermination
	if needsRecreate {
		if err := r.Delete(ctx, existing); err != nil {
			return fmt.Errorf("deleting route for spec change: %w", err)
		}
		route := &unstructured.Unstructured{}
		route.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
		route.SetName(gw.Name)
		route.SetNamespace(ns)
		route.SetLabels(gatewayLabels(gw))
		route.Object["spec"] = spec
		return r.Create(ctx, route)
	}

	return nil
}

// --- Gateway API ---

func gatewayAPIEnabled(gw *ogov1alpha1.OpenShellGateway, hasGWAPI bool) bool {
	if gw.Spec.Route.GatewayAPI.Enabled != nil {
		return *gw.Spec.Route.GatewayAPI.Enabled
	}
	return hasGWAPI
}

func gatewayClassName(gw *ogov1alpha1.OpenShellGateway) string {
	if gw.Spec.Route.GatewayAPI.GatewayClassName != "" {
		return gw.Spec.Route.GatewayAPI.GatewayClassName
	}
	return staticGatewayClassName
}

func (r *OpenShellGatewayReconciler) reconcileGatewayAPI(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	ns := gatewayNamespace(gw)
	hostname := gw.Spec.Route.Hostname
	if hostname == "" {
		// An incomplete config waiting on the user, not a reconcile
		// failure - set the same condition reconcileEnvoyRoute reports for
		// this exact case (it runs later, but only on OpenShift; setting
		// it here too keeps a vanilla Kubernetes cluster from getting zero
		// visibility into why no Gateway API resources exist yet). Return
		// nil so the caller doesn't escalate to Phase: Failed - the
		// terminal reconcile requeue picks this up again once hostname is
		// set, same as every other "waiting for config/dependency" state.
		meta.SetStatusCondition(&gw.Status.Conditions, metav1.Condition{
			Type: ogov1alpha1.ConditionEnvoyRouteReady, Status: metav1.ConditionFalse,
			Reason: reasonHostnameMissing, Message: "route.hostname is required when using Gateway API",
		})
		return nil
	}

	tlsSecretName := gw.Name + "-gateway-tls"
	if err := r.reconcileGatewayTLSCert(ctx, gw, tlsSecretName, hostname); err != nil {
		return fmt.Errorf("reconciling gateway TLS certificate: %w", err)
	}

	gwGVK := schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"}
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(gwGVK)
	err := r.Get(ctx, types.NamespacedName{Name: gw.Name, Namespace: ns}, existing)
	if apierrors.IsNotFound(err) {
		gatewayCR := &unstructured.Unstructured{}
		gatewayCR.SetGroupVersionKind(gwGVK)
		gatewayCR.SetName(gw.Name)
		gatewayCR.SetNamespace(ns)
		gatewayCR.SetLabels(gatewayLabels(gw))
		gatewayCR.Object["spec"] = map[string]interface{}{
			"gatewayClassName": gatewayClassName(gw),
			"listeners": []interface{}{
				map[string]interface{}{
					"name":     "https",
					"port":     int64(443),
					"protocol": "HTTPS",
					"hostname": hostname,
					"tls": map[string]interface{}{
						"mode": "Terminate",
						"certificateRefs": []interface{}{
							map[string]interface{}{
								"name": tlsSecretName,
							},
						},
					},
					"allowedRoutes": map[string]interface{}{
						"namespaces": map[string]interface{}{
							"from": "Same",
						},
					},
				},
			},
		}
		if err := r.Create(ctx, gatewayCR); err != nil {
			return fmt.Errorf("creating Gateway: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("getting Gateway: %w", err)
	} else {
		var existingHostname string
		listeners, _, _ := unstructured.NestedSlice(existing.Object, "spec", "listeners")
		if len(listeners) > 0 {
			if l, ok := listeners[0].(map[string]interface{}); ok {
				if h, ok := l["hostname"].(string); ok {
					existingHostname = h
				}
			}
		}
		existingClass, _, _ := unstructured.NestedString(existing.Object, "spec", "gatewayClassName")
		if existingHostname != hostname || existingClass != gatewayClassName(gw) {
			logf.FromContext(ctx).Info("Gateway spec drifted, recreating",
				"oldHostname", existingHostname, "newHostname", hostname,
				"oldClass", existingClass, "newClass", gatewayClassName(gw))
			if err := r.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting drifted Gateway: %w", err)
			}
			return nil
		}
	}

	grpcGVK := schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "GRPCRoute"}
	existingRoute := &unstructured.Unstructured{}
	existingRoute.SetGroupVersionKind(grpcGVK)
	err = r.Get(ctx, types.NamespacedName{Name: gw.Name, Namespace: ns}, existingRoute)
	if apierrors.IsNotFound(err) {
		grpcRoute := &unstructured.Unstructured{}
		grpcRoute.SetGroupVersionKind(grpcGVK)
		grpcRoute.SetName(gw.Name)
		grpcRoute.SetNamespace(ns)
		grpcRoute.SetLabels(gatewayLabels(gw))
		grpcRoute.Object["spec"] = map[string]interface{}{
			"parentRefs": []interface{}{
				map[string]interface{}{
					"name": gw.Name,
				},
			},
			"hostnames": []interface{}{hostname},
			"rules": []interface{}{
				map[string]interface{}{
					"backendRefs": []interface{}{
						map[string]interface{}{
							"name": gw.Name,
							"port": int64(8080),
						},
					},
				},
			},
		}
		if err := r.Create(ctx, grpcRoute); err != nil {
			return fmt.Errorf("creating GRPCRoute: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("getting GRPCRoute: %w", err)
	}

	// Disable Envoy's default 15-second stream timeout for long-lived gRPC
	// streams (SSH relay, WatchSandbox). Without this, Envoy kills the
	// connection and the CLI reports "missing grpc-status trailer".
	btpGVK := schema.GroupVersionKind{Group: "gateway.envoyproxy.io", Version: "v1alpha1", Kind: "BackendTrafficPolicy"}
	existingBTP := &unstructured.Unstructured{}
	existingBTP.SetGroupVersionKind(btpGVK)
	if err := r.Get(ctx, types.NamespacedName{Name: gw.Name + "-timeout", Namespace: ns}, existingBTP); err != nil && !apierrors.IsNotFound(err) {
		logf.FromContext(ctx).Error(err, "Failed to check BackendTrafficPolicy")
	} else if apierrors.IsNotFound(err) {
		btp := &unstructured.Unstructured{}
		btp.SetGroupVersionKind(btpGVK)
		btp.SetName(gw.Name + "-timeout")
		btp.SetNamespace(ns)
		btp.SetLabels(gatewayLabels(gw))
		btp.Object["spec"] = map[string]interface{}{
			"targetRefs": []interface{}{
				map[string]interface{}{
					"group": "gateway.networking.k8s.io",
					"kind":  "GRPCRoute",
					"name":  gw.Name,
				},
			},
			"timeout": map[string]interface{}{
				"http": map[string]interface{}{
					"requestTimeout": "0s",
				},
			},
		}
		if createErr := r.Create(ctx, btp); createErr != nil {
			logf.FromContext(ctx).Error(createErr, "Failed to create BackendTrafficPolicy (Envoy Gateway may not be installed)")
		}
	}

	oldRoute := &unstructured.Unstructured{}
	oldRoute.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
	if err := r.Get(ctx, types.NamespacedName{Name: gw.Name, Namespace: ns}, oldRoute); err == nil {
		logf.FromContext(ctx).Info("Cleaning up direct Route superseded by Gateway API", "route", gw.Name)
		if err := r.Delete(ctx, oldRoute); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("cleaning up superseded Route: %w", err)
		}
	}

	return nil
}

func (r *OpenShellGatewayReconciler) reconcileGatewayTLSCert(ctx context.Context, gw *ogov1alpha1.OpenShellGateway, secretName, hostname string) error {
	ns := gatewayNamespace(gw)

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ns}, existing)
	if err != nil && !apierrors.IsNotFound(err) {
		if _, discoveryErr := r.DiscoveryClient.ServerResourcesForGroupVersion("cert-manager.io/v1"); discoveryErr != nil {
			return fmt.Errorf("cert-manager CRDs not installed — required for Gateway API TLS")
		}
		return err
	}

	issuerName := gw.Spec.TLS.CertManager.IssuerName
	if issuerName == "" {
		issuerName = "letsencrypt"
	}
	issuerKind := gw.Spec.TLS.CertManager.IssuerKind
	if issuerKind == "" {
		issuerKind = "ClusterIssuer"
	}
	desiredDNSNames := []string{hostname}

	if apierrors.IsNotFound(err) {
		cert := &unstructured.Unstructured{}
		cert.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
		cert.SetName(secretName)
		cert.SetNamespace(ns)
		cert.SetLabels(gatewayLabels(gw))
		cert.Object["spec"] = map[string]interface{}{
			"secretName": secretName,
			"dnsNames":   []interface{}{hostname},
			"issuerRef": map[string]interface{}{
				"name": issuerName,
				"kind": issuerKind,
			},
		}
		return r.Create(ctx, cert)
	}

	// Keep the Certificate in sync with the CR's current desired state —
	// route.Hostname or the issuer config can change between deployments
	// (e.g. promoting a new build to staging), and cert-manager won't
	// reissue for a new hostname/issuer unless the Certificate spec itself
	// is updated to request it.
	existingDNSNames, _, err := unstructured.NestedStringSlice(existing.Object, "spec", "dnsNames")
	if err != nil {
		return fmt.Errorf("reading existing Certificate dnsNames: %w", err)
	}
	existingIssuerName, _, err := unstructured.NestedString(existing.Object, "spec", "issuerRef", "name")
	if err != nil {
		return fmt.Errorf("reading existing Certificate issuerRef.name: %w", err)
	}
	existingIssuerKind, _, err := unstructured.NestedString(existing.Object, "spec", "issuerRef", "kind")
	if err != nil {
		return fmt.Errorf("reading existing Certificate issuerRef.kind: %w", err)
	}
	if slices.Equal(existingDNSNames, desiredDNSNames) &&
		existingIssuerName == issuerName && existingIssuerKind == issuerKind {
		return nil
	}
	if err := unstructured.SetNestedStringSlice(existing.Object, desiredDNSNames, "spec", "dnsNames"); err != nil {
		return fmt.Errorf("updating Certificate dnsNames: %w", err)
	}
	if err := unstructured.SetNestedField(existing.Object, issuerName, "spec", "issuerRef", "name"); err != nil {
		return fmt.Errorf("updating Certificate issuerRef.name: %w", err)
	}
	if err := unstructured.SetNestedField(existing.Object, issuerKind, "spec", "issuerRef", "kind"); err != nil {
		return fmt.Errorf("updating Certificate issuerRef.kind: %w", err)
	}
	return r.Update(ctx, existing)
}

// reconcileEnvoyRoute bridges the OpenShift Route that fronts the
// Envoy-managed proxy Service. It always returns a condition (not just an
// error) so `oc get openshellgateway -o yaml` shows *why* the route isn't
// up when it isn't - previously several no-route cases returned bare nil,
// which was indistinguishable from success.
func (r *OpenShellGatewayReconciler) reconcileEnvoyRoute(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (metav1.Condition, error) {
	if gw.Spec.Route.Enabled != nil && !*gw.Spec.Route.Enabled {
		// Zero-value Condition (empty Type) signals "nothing to report" to
		// the call site, which skips SetStatusCondition entirely - a
		// disabled route isn't "not ready", it's not applicable, and
		// shouldn't leave a stale EnvoyRouteReady=False condition visible
		// on a CR that will never use this code path.
		return metav1.Condition{}, nil
	}

	hostname := gw.Spec.Route.Hostname
	if hostname == "" {
		// nil error, matching reconcileGatewayAPI's handling of the same
		// condition (this function runs right after it, on OpenShift
		// only) - the condition alone is enough for the caller and
		// updateStatus to act on; a non-nil error here would exist only to
		// be inspected and discarded at the call site, which is exactly
		// the "unnecessary complexity" that made this exact area error
		// prone the last time it changed.
		return metav1.Condition{
			Type: ogov1alpha1.ConditionEnvoyRouteReady, Status: metav1.ConditionFalse,
			Reason: reasonHostnameMissing, Message: "route.hostname is required when using Gateway API",
		}, nil
	}

	svcList := &corev1.ServiceList{}
	if err := r.List(ctx, svcList, client.InNamespace(envoyGatewaySystemNS), client.MatchingLabels{
		"gateway.envoyproxy.io/owning-gateway-name":      gw.Name,
		"gateway.envoyproxy.io/owning-gateway-namespace": gatewayNamespace(gw),
	}); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list Envoy proxy services")
		return metav1.Condition{
				Type: ogov1alpha1.ConditionEnvoyRouteReady, Status: metav1.ConditionFalse,
				Reason: "ListFailed", Message: "Failed to list Envoy proxy services - see operator logs for details",
			},
			fmt.Errorf("listing Envoy proxy services: %w", err)
	}
	if len(svcList.Items) == 0 {
		return metav1.Condition{
			Type: ogov1alpha1.ConditionEnvoyRouteReady, Status: metav1.ConditionFalse,
			Reason: "ProxyServiceNotFound",
			Message: "Waiting for Envoy Gateway to provision the proxy Service " +
				"(labeled gateway.envoyproxy.io/owning-gateway-name/-namespace) before the Route can be created",
		}, nil
	}
	if len(svcList.Items) > 1 {
		// The API doesn't guarantee list ordering, so sort by name first
		// for a stable pick across reconcile passes - this label selector
		// should always match exactly one Service in practice, so this is
		// about determinism, not correctness. Surface it if that
		// assumption ever breaks (e.g. leftover state from a prior Gateway
		// not fully cleaned up).
		sort.Slice(svcList.Items, func(i, j int) bool {
			return svcList.Items[i].Name < svcList.Items[j].Name
		})
		logf.FromContext(ctx).Info("Multiple Envoy proxy services matched the owning-gateway labels, using the first by name",
			"count", len(svcList.Items))
	}

	envoySvc := svcList.Items[0]
	routeName := gw.Name + "-gw"

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
	err := r.Get(ctx, types.NamespacedName{Name: routeName, Namespace: envoySvc.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		route := &unstructured.Unstructured{}
		route.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
		route.SetName(routeName)
		route.SetNamespace(envoySvc.Namespace)
		route.SetLabels(gatewayLabels(gw))
		route.Object["spec"] = map[string]interface{}{
			"host": hostname,
			"to":   map[string]interface{}{"kind": "Service", "name": envoySvc.Name},
			"port": map[string]interface{}{"targetPort": int64(10443)},
			"tls":  map[string]interface{}{"termination": "passthrough"},
		}
		if err := r.Create(ctx, route); err != nil {
			logf.FromContext(ctx).Error(err, "Failed to create Route")
			return metav1.Condition{
					Type: ogov1alpha1.ConditionEnvoyRouteReady, Status: metav1.ConditionFalse,
					Reason: "CreateFailed", Message: "Failed to create Route - see operator logs for details",
				},
				fmt.Errorf("creating envoy route: %w", err)
		}
		return metav1.Condition{
			Type: ogov1alpha1.ConditionEnvoyRouteReady, Status: metav1.ConditionTrue,
			Reason: "Created", Message: fmt.Sprintf("Route %s created for host %s", routeName, hostname),
		}, nil
	}
	if err != nil {
		logf.FromContext(ctx).Error(err, "Failed to get existing Route")
		return metav1.Condition{
				Type: ogov1alpha1.ConditionEnvoyRouteReady, Status: metav1.ConditionFalse,
				Reason: "GetFailed", Message: "Failed to get existing Route - see operator logs for details",
			},
			fmt.Errorf("getting envoy route: %w", err)
	}

	// Keep the Route in sync with the CR's current desired state - the
	// hostname can change between deployments, and the proxy Service name
	// changes if Envoy Gateway ever re-provisions it under a new name.
	// route.openshift.io Routes don't support mutating host/to via a plain
	// Update in this codebase's established convention (see reconcileRoute)
	// - delete and recreate instead.
	existingHost, _, _ := unstructured.NestedString(existing.Object, "spec", "host")
	existingToName, _, _ := unstructured.NestedString(existing.Object, "spec", "to", "name")
	if existingHost != hostname || existingToName != envoySvc.Name {
		// Not atomic - Route.spec.host/to can't be updated in-place per this
		// codebase's established convention (see reconcileRoute), so there's
		// an unavoidable gap between Delete and Create where no Route
		// exists. Building the replacement object first only shaves
		// negligible local CPU time off that gap, not the network round
		// trips that dominate it. The real mitigation is on the recreate
		// failure path below: if Create fails after Delete succeeded, the
		// non-nil error this function returns already drives the caller's
		// RequeueAfter: 30s retry, and that retry hits the "not found"
		// branch above (fresh create), not this drift branch again - so
		// recovery is bounded to one reconcile interval, not indefinite.
		route := &unstructured.Unstructured{}
		route.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
		route.SetName(routeName)
		route.SetNamespace(envoySvc.Namespace)
		route.SetLabels(gatewayLabels(gw))
		route.Object["spec"] = map[string]interface{}{
			"host": hostname,
			"to":   map[string]interface{}{"kind": "Service", "name": envoySvc.Name},
			"port": map[string]interface{}{"targetPort": int64(10443)},
			"tls":  map[string]interface{}{"termination": "passthrough"},
		}

		if err := r.Delete(ctx, existing); err != nil {
			logf.FromContext(ctx).Error(err, "Failed to delete drifted Route")
			return metav1.Condition{
					Type: ogov1alpha1.ConditionEnvoyRouteReady, Status: metav1.ConditionFalse,
					Reason: "UpdateFailed", Message: "Failed to delete drifted Route - see operator logs for details",
				},
				fmt.Errorf("deleting drifted envoy route: %w", err)
		}
		if err := r.Create(ctx, route); err != nil {
			logf.FromContext(ctx).Error(err, "Failed to recreate Route after deleting the drifted one - "+
				"no Route exists until the next reconcile retries this")
			return metav1.Condition{
					Type: ogov1alpha1.ConditionEnvoyRouteReady, Status: metav1.ConditionFalse,
					Reason: "RecreateFailed",
					Message: "Deleted the drifted Route but failed to recreate it - no Route exists until " +
						"the next reconcile retries this; see operator logs for details",
				},
				fmt.Errorf("recreating drifted envoy route: %w", err)
		}
		return metav1.Condition{
			Type: ogov1alpha1.ConditionEnvoyRouteReady, Status: metav1.ConditionTrue,
			Reason: "Created", Message: fmt.Sprintf("Route %s recreated for host %s", routeName, hostname),
		}, nil
	}

	return metav1.Condition{
		Type: ogov1alpha1.ConditionEnvoyRouteReady, Status: metav1.ConditionTrue,
		Reason: "Exists", Message: fmt.Sprintf("Route %s exists for host %s", routeName, hostname),
	}, nil
}

// --- SCC Binding ---

func (r *OpenShellGatewayReconciler) reconcileSCCBinding(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	crb := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: gw.Name + "-sandbox-scc-privileged"}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, crb, func() error {
		crb.Labels = gatewayLabels(gw)
		crb.RoleRef = rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "system:openshift:scc:privileged"}
		crb.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: gw.Name + "-sandbox", Namespace: sandboxNamespace(gw)}}
		return nil
	})
	return err
}

// --- Status ---

func (r *OpenShellGatewayReconciler) updateStatus(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	// Re-fetch to avoid conflicts from earlier mutations
	latest := &ogov1alpha1.OpenShellGateway{}
	if err := r.Get(ctx, types.NamespacedName{Name: gw.Name}, latest); err != nil {
		return err
	}

	ns := gatewayNamespace(gw)

	deploy := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: gw.Name, Namespace: ns}, deploy); err != nil {
		latest.Status.Phase = ogov1alpha1.PhaseFailed
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type: ogov1alpha1.ConditionAvailable, Status: metav1.ConditionFalse,
			Reason: "DeploymentNotFound", Message: "Gateway deployment not found",
		})
		return r.Status().Update(ctx, latest)
	}

	desiredReplicas := int32(1)
	if deploy.Spec.Replicas != nil {
		desiredReplicas = *deploy.Spec.Replicas
	}
	podsReady := deploy.Status.ReadyReplicas > 0 && deploy.Status.ReadyReplicas == desiredReplicas

	// A Gateway API deployment isn't actually reachable until the Envoy
	// Route bridges it, even once the gateway pod itself is Ready — without
	// this check, Available/Phase reported "Running" purely from pod
	// readiness while EnvoyRouteReady was still False (e.g.
	// ProxyServiceNotFound), a silent partial-success this PR otherwise
	// exists to eliminate. EnvoyRouteReady is only ever set when the
	// Gateway API path is active, so its absence here means that path
	// isn't in use and shouldn't block Available.
	envoyRouteBlocking := false
	if c := meta.FindStatusCondition(gw.Status.Conditions, ogov1alpha1.ConditionEnvoyRouteReady); c != nil && c.Status != metav1.ConditionTrue {
		envoyRouteBlocking = true
	}

	switch {
	case podsReady && !envoyRouteBlocking:
		latest.Status.Phase = ogov1alpha1.PhaseRunning
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type: ogov1alpha1.ConditionAvailable, Status: metav1.ConditionTrue,
			Reason: "Ready", Message: "Gateway is running",
		})
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type: ogov1alpha1.ConditionProgressing, Status: metav1.ConditionFalse,
			Reason: "Complete", Message: "Rollout complete",
		})
	case podsReady && envoyRouteBlocking:
		latest.Status.Phase = ogov1alpha1.PhaseCreating
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type: ogov1alpha1.ConditionAvailable, Status: metav1.ConditionFalse,
			Reason: "EnvoyRouteNotReady", Message: "Waiting for the Envoy Gateway Route to become ready",
		})
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type: ogov1alpha1.ConditionProgressing, Status: metav1.ConditionTrue,
			Reason: "Deploying", Message: "Waiting for the Envoy Gateway Route",
		})
	default:
		latest.Status.Phase = ogov1alpha1.PhaseCreating
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type: ogov1alpha1.ConditionAvailable, Status: metav1.ConditionFalse,
			Reason: "NotReady", Message: "Waiting for pods",
		})
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type: ogov1alpha1.ConditionProgressing, Status: metav1.ConditionTrue,
			Reason: "Deploying", Message: "Gateway pods starting",
		})
	}

	meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
		Type: ogov1alpha1.ConditionDegraded, Status: metav1.ConditionFalse,
		Reason: "OK", Message: "",
	})

	for _, condType := range []string{
		ogov1alpha1.ConditionEnvoyGatewayReady,
		ogov1alpha1.ConditionDatabaseReady,
		ogov1alpha1.ConditionEnvoyProxySCCReady,
		ogov1alpha1.ConditionOpenShiftGroups,
		ogov1alpha1.ConditionEnvoyRouteReady,
	} {
		if c := meta.FindStatusCondition(gw.Status.Conditions, condType); c != nil {
			meta.SetStatusCondition(&latest.Status.Conditions, *c)
		}
	}

	if gw.Spec.Route.Hostname != "" {
		latest.Status.GatewayURL = "https://" + gw.Spec.Route.Hostname + ":443"
	} else {
		latest.Status.GatewayURL = fmt.Sprintf("https://%s.%s.svc.cluster.local:8080", gw.Name, ns)
	}

	latest.Status.ClientCertSecretName = gw.Name + "-client-tls"
	latest.Status.ObservedGeneration = gw.Generation

	return r.Status().Update(ctx, latest)
}

func (r *OpenShellGatewayReconciler) setDegraded(ctx context.Context, gw *ogov1alpha1.OpenShellGateway, step string, reconcileErr error) error {
	log := logf.FromContext(ctx)
	latest := &ogov1alpha1.OpenShellGateway{}
	if err := r.Get(ctx, types.NamespacedName{Name: gw.Name}, latest); err != nil {
		return reconcileErr
	}
	latest.Status.Phase = ogov1alpha1.PhaseFailed
	latest.Status.ObservedGeneration = gw.Generation
	meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
		Type: ogov1alpha1.ConditionDegraded, Status: metav1.ConditionTrue,
		Reason: "ReconcileError", Message: fmt.Sprintf("%s: %v", step, reconcileErr),
	})
	if err := r.Status().Update(ctx, latest); err != nil {
		log.Error(err, "Failed to update degraded status")
	}
	return reconcileErr
}

// --- Dependencies ---

func (r *OpenShellGatewayReconciler) dependencies() []DependencyReconciler {
	return []DependencyReconciler{
		&EnvoyGatewayReconciler{Client: r.Client, DiscoveryClient: r.DiscoveryClient},
		&EnvoyProxySCCReconciler{Client: r.Client, DiscoveryClient: r.DiscoveryClient},
		&PostgreSQLReconciler{Client: r.Client},
		&GroupsReconciler{Client: r.Client, DiscoveryClient: r.DiscoveryClient},
	}
}

// --- Setup ---

func (r *OpenShellGatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	labelMatcher := handler.EnqueueRequestsFromMapFunc(r.findGatewayForManagedResource)
	return ctrl.NewControllerManagedBy(mgr).
		For(&ogov1alpha1.OpenShellGateway{}).
		Watches(&appsv1.Deployment{}, labelMatcher).
		Watches(&corev1.Service{}, labelMatcher).
		Watches(&corev1.ConfigMap{}, labelMatcher).
		Named("openshellgateway").
		Complete(r)
}

func (r *OpenShellGatewayReconciler) findGatewayForManagedResource(ctx context.Context, obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	if labels[labelManagedBy] != managedByValue {
		return nil
	}
	name := labels[labelInstance]
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: name}}}
}

// --- Helpers ---

func gatewayNamespace(gw *ogov1alpha1.OpenShellGateway) string {
	if gw.Spec.Namespace != "" {
		return gw.Spec.Namespace
	}
	return defaultNamespace
}

func sandboxNamespace(gw *ogov1alpha1.OpenShellGateway) string {
	if gw.Spec.Sandbox.Namespace != "" {
		return gw.Spec.Sandbox.Namespace
	}
	return gatewayNamespace(gw)
}

func uniqueNamespaces(gw *ogov1alpha1.OpenShellGateway) []string {
	ns := gatewayNamespace(gw)
	sns := sandboxNamespace(gw)
	if ns == sns {
		return []string{ns}
	}
	return []string{ns, sns}
}

func gatewayLabels(gw *ogov1alpha1.OpenShellGateway) map[string]string {
	return map[string]string{
		labelName: "openshell", labelInstance: gw.Name,
		labelManagedBy: managedByValue, labelPartOf: "openshell-gateway",
	}
}

func selectorLabels(gw *ogov1alpha1.OpenShellGateway) map[string]string {
	return map[string]string{labelName: "openshell", labelInstance: gw.Name}
}

func computeServerSANs(gw *ogov1alpha1.OpenShellGateway) []string {
	ns := gatewayNamespace(gw)
	sans := []string{
		gw.Name,
		fmt.Sprintf("%s.%s.svc", gw.Name, ns),
		fmt.Sprintf("%s.%s.svc.cluster.local", gw.Name, ns),
		"localhost",
		fmt.Sprintf("%s.localhost", gw.Name),
		fmt.Sprintf("*.%s.localhost", gw.Name),
		"host.docker.internal",
		"127.0.0.1",
	}
	if gw.Spec.Route.Hostname != "" {
		sans = append(sans,
			gw.Spec.Route.Hostname,
			"openshell-auth."+domainSuffix(gw.Spec.Route.Hostname),
		)
	}
	return sans
}

func computeConfigHash(toml string) string {
	h := sha256.Sum256([]byte(toml))
	return fmt.Sprintf("%x", h[:16])
}

// --- Auth Bridge ---

func authBridgeEnabled(gw *ogov1alpha1.OpenShellGateway, isOCP bool) bool {
	if gw.Spec.Auth.OpenShift.Enabled != nil {
		return *gw.Spec.Auth.OpenShift.Enabled
	}
	return isOCP
}

func authBridgeImage(gw *ogov1alpha1.OpenShellGateway) string {
	if gw.Spec.AuthBridgeImage != "" {
		return gw.Spec.AuthBridgeImage
	}
	return "quay.io/aknochow/ogo-auth-bridge:latest"
}

func domainSuffix(hostname string) string {
	if _, after, ok := strings.Cut(hostname, "."); ok {
		return after
	}
	return hostname
}

func authBridgeExternalURL(gw *ogov1alpha1.OpenShellGateway) string {
	routeEnabled := gw.Spec.Route.Enabled == nil || *gw.Spec.Route.Enabled
	if routeEnabled && gw.Spec.Route.Hostname != "" {
		return "https://openshell-auth." + domainSuffix(gw.Spec.Route.Hostname)
	}
	scheme := "https"
	if gw.Spec.TLS.Enabled != nil && !*gw.Spec.TLS.Enabled {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s.%s.svc:8085", scheme, gw.Name, gatewayNamespace(gw))
}

func authBridgeInternalURL(_ *ogov1alpha1.OpenShellGateway) string {
	return "http://localhost:8085"
}

func tokenTTL(gw *ogov1alpha1.OpenShellGateway) string {
	if gw.Spec.Auth.OpenShift.TokenTTL != "" {
		return gw.Spec.Auth.OpenShift.TokenTTL
	}
	return "8h"
}

func clusterDomain(gw *ogov1alpha1.OpenShellGateway) string {
	if gw.Spec.Route.Hostname != "" {
		return domainSuffix(gw.Spec.Route.Hostname)
	}
	return "apps.example.com"
}

func (r *OpenShellGatewayReconciler) reconcileAuthBridgeRoute(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	ns := gatewayNamespace(gw)
	routeName := gw.Name + "-auth"
	hostname := ""
	if gw.Spec.Route.Hostname != "" {
		hostname = "openshell-auth." + domainSuffix(gw.Spec.Route.Hostname)
	}
	tlsConfig, err := r.authBridgeRouteTLS(ctx, gw)
	if err != nil {
		return err
	}
	spec := map[string]interface{}{
		"to":             map[string]interface{}{"kind": "Service", "name": gw.Name, "weight": int64(100)},
		"port":           map[string]interface{}{"targetPort": "auth"},
		"tls":            tlsConfig,
		"wildcardPolicy": "None",
	}
	if hostname != "" {
		spec["host"] = hostname
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
	err = r.Get(ctx, types.NamespacedName{Name: routeName, Namespace: ns}, existing)
	if apierrors.IsNotFound(err) {
		route := &unstructured.Unstructured{}
		route.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
		route.SetName(routeName)
		route.SetNamespace(ns)
		route.SetLabels(gatewayLabels(gw))
		route.Object["spec"] = spec
		return r.Create(ctx, route)
	}
	if err != nil {
		return err
	}
	labels := existing.GetLabels()
	if labels[labelManagedBy] != managedByValue || labels[labelInstance] != gw.Name {
		return fmt.Errorf("route %s/%s exists but is not managed by OGO", ns, routeName)
	}

	existingSpec, _, err := unstructured.NestedMap(existing.Object, "spec")
	if err != nil {
		return fmt.Errorf("reading auth-bridge Route spec: %w", err)
	}
	if hostname == "" {
		if existingHost, _, _ := unstructured.NestedString(existing.Object, "spec", "host"); existingHost != "" {
			spec["host"] = existingHost
		}
	}
	if apiequality.Semantic.DeepEqual(existingSpec, spec) {
		return nil
	}
	existing.Object["spec"] = spec
	return r.Update(ctx, existing)
}

func (r *OpenShellGatewayReconciler) authBridgeRouteTLS(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (map[string]interface{}, error) {
	if gw.Spec.TLS.Enabled != nil && !*gw.Spec.TLS.Enabled {
		return map[string]interface{}{
			"termination":                   "edge",
			"insecureEdgeTerminationPolicy": "Redirect",
		}, nil
	}

	configMapName := gw.Name + "-auth-ca"
	configMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: gatewayNamespace(gw)}, configMap); err != nil {
		return nil, fmt.Errorf("reading auth-bridge CA ConfigMap: %w", err)
	}
	destinationCA := configMap.Data["ca.crt"]
	if destinationCA == "" {
		return nil, fmt.Errorf("auth-bridge CA ConfigMap %q is missing ca.crt", configMapName)
	}
	return map[string]interface{}{
		"termination":                   "reencrypt",
		"insecureEdgeTerminationPolicy": "Redirect",
		"destinationCACertificate":      destinationCA,
	}, nil
}

func (r *OpenShellGatewayReconciler) reconcileOAuthClient(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	ns := gatewayNamespace(gw)
	secretName := gw.Name + "-oauth-client"

	// Ensure OAuth client secret exists
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ns}, secret)
	if apierrors.IsNotFound(err) {
		clientSecret := generateOAuthSecret()
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns, Labels: gatewayLabels(gw)},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{"secret": []byte(clientSecret)},
		}
		if err := r.Create(ctx, secret); err != nil {
			return fmt.Errorf("creating OAuth client secret: %w", err)
		}
	} else if err != nil {
		return err
	}

	clientSecret := string(secret.Data["secret"])
	callbackURL := authBridgeExternalURL(gw) + "/callback"

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(schema.GroupVersionKind{Group: "oauth.openshift.io", Version: "v1", Kind: "OAuthClient"})
	err = r.Get(ctx, types.NamespacedName{Name: "openshell"}, existing)
	if apierrors.IsNotFound(err) {
		oauthClient := &unstructured.Unstructured{}
		oauthClient.SetGroupVersionKind(schema.GroupVersionKind{Group: "oauth.openshift.io", Version: "v1", Kind: "OAuthClient"})
		oauthClient.SetName("openshell")
		oauthClient.SetLabels(gatewayLabels(gw))
		oauthClient.Object["secret"] = clientSecret
		oauthClient.Object["grantMethod"] = "auto"
		oauthClient.Object["redirectURIs"] = []interface{}{callbackURL}
		return r.Create(ctx, oauthClient)
	}
	if err != nil {
		return err
	}

	// Keep an existing OAuthClient in sync. The namespace Secret is the
	// source of truth and can be regenerated independently of this
	// cluster-scoped object (e.g. a redeploy onto a fresh namespace), which
	// must not leave the OAuthClient pointing at a stale secret/redirect —
	// that fails OIDC token exchange with "unauthorized_client".
	existingSecret, _, err := unstructured.NestedString(existing.Object, "secret")
	if err != nil {
		return fmt.Errorf("reading existing OAuthClient secret: %w", err)
	}
	existingRedirectURIs, _, err := unstructured.NestedStringSlice(existing.Object, "redirectURIs")
	if err != nil {
		return fmt.Errorf("reading existing OAuthClient redirectURIs: %w", err)
	}
	existingGrantMethod, _, err := unstructured.NestedString(existing.Object, "grantMethod")
	if err != nil {
		return fmt.Errorf("reading existing OAuthClient grantMethod: %w", err)
	}
	if existingSecret == clientSecret && slices.Equal(existingRedirectURIs, []string{callbackURL}) &&
		existingGrantMethod == "auto" {
		return nil
	}
	existing.Object["secret"] = clientSecret
	existing.Object["redirectURIs"] = []interface{}{callbackURL}
	existing.Object["grantMethod"] = "auto"
	return r.Update(ctx, existing)
}

func generateOAuthSecret() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return fmt.Sprintf("%x", b)
}
