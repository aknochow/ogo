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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	"github.com/aknochow/ogo/internal/gateway"
	"github.com/aknochow/ogo/internal/openshift"
	"github.com/aknochow/ogo/internal/pki"
)

const (
	finalizerName   = "ogo.aknochow.io/gateway-cleanup"
	labelManagedBy  = "app.kubernetes.io/managed-by"
	labelInstance   = "app.kubernetes.io/instance"
	labelName       = "app.kubernetes.io/name"
	labelPartOf     = "app.kubernetes.io/part-of"
	requeueInterval = 60 * time.Second
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

func (r *OpenShellGatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	gw := &ogov1alpha1.OpenShellGateway{}
	if err := r.Get(ctx, req.NamespacedName, gw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Singleton enforcement
	gwList := &ogov1alpha1.OpenShellGatewayList{}
	if err := r.List(ctx, gwList); err != nil {
		return ctrl.Result{}, err
	}
	if len(gwList.Items) > 1 {
		meta.SetStatusCondition(&gw.Status.Conditions, metav1.Condition{
			Type: ogov1alpha1.ConditionDegraded, Status: metav1.ConditionTrue,
			Reason: "MultipleGateways", Message: "Only one OpenShellGateway is allowed per cluster",
		})
		gw.Status.Phase = ogov1alpha1.PhaseFailed
		return ctrl.Result{}, r.Status().Update(ctx, gw)
	}

	if !gw.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, gw)
	}

	if !controllerutil.ContainsFinalizer(gw, finalizerName) {
		controllerutil.AddFinalizer(gw, finalizerName)
		if err := r.Update(ctx, gw); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	isOCP := openshift.IsOpenShift(r.DiscoveryClient)
	ns := gatewayNamespace(gw)
	sandboxNS := sandboxNamespace(gw)

	log.Info("Reconciling OpenShellGateway", "namespace", ns, "sandbox_namespace", sandboxNS, "openshift", isOCP)

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
		{"JWTKeys", r.reconcileJWTKeys},
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

	if isOCP {
		if err := r.reconcileRoute(ctx, gw); err != nil {
			log.Error(err, "Failed to reconcile Route")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setDegraded(ctx, gw, "Route", err)
		}
		if err := r.reconcileSCCBinding(ctx, gw); err != nil {
			log.Error(err, "Failed to reconcile SCC binding")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setDegraded(ctx, gw, "SCCBinding", err)
		}
		if authBridgeEnabled(gw, isOCP) {
			if err := r.reconcileAuthBridgeRoute(ctx, gw); err != nil {
				log.Error(err, "Failed to reconcile auth-bridge Route")
			}
			if err := r.reconcileOAuthClient(ctx, gw); err != nil {
				log.Error(err, "Failed to reconcile OAuthClient")
			}
		}
	}

	return ctrl.Result{RequeueAfter: requeueInterval}, r.updateStatus(ctx, gw, isOCP)
}

// --- Deletion ---

func (r *OpenShellGatewayReconciler) reconcileDelete(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(gw, finalizerName) {
		return ctrl.Result{}, nil
	}

	log.Info("Cleaning up gateway resources")

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

	for _, obj := range clusterResources {
		if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete cluster resource", "resource", obj.GetName())
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
			}
		}
	}

	controllerutil.RemoveFinalizer(gw, finalizerName)
	return ctrl.Result{}, r.Update(ctx, gw)
}

// --- Namespace ---

func (r *OpenShellGatewayReconciler) reconcileNamespace(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	for _, nsName := range uniqueNamespaces(gw) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, ns, func() error {
			if ns.Labels == nil {
				ns.Labels = map[string]string{}
			}
			ns.Labels[labelManagedBy] = "ogo"
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

	if gw.Spec.TLS.CertManager.Enabled {
		return r.reconcileCertManagerCertificate(ctx, gw)
	}

	return r.reconcileSelfSignedTLS(ctx, gw)
}

func (r *OpenShellGatewayReconciler) reconcileCertManagerCertificate(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	ns := gatewayNamespace(gw)
	issuerName := gw.Spec.TLS.CertManager.IssuerName
	if issuerName == "" {
		issuerName = "letsencrypt"
	}
	issuerKind := gw.Spec.TLS.CertManager.IssuerKind
	if issuerKind == "" {
		issuerKind = "ClusterIssuer"
	}

	if gw.Spec.Route.Hostname == "" {
		return fmt.Errorf("cert-manager requires route.hostname for public certificate issuance")
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
	err := r.Get(ctx, types.NamespacedName{Name: gw.Name + "-server-tls", Namespace: ns}, existing)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		_, discoveryErr := r.DiscoveryClient.ServerResourcesForGroupVersion("cert-manager.io/v1")
		if discoveryErr != nil {
			return fmt.Errorf("cert-manager CRDs not installed on cluster")
		}
		return err
	}

	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"})
	cert.SetName(gw.Name + "-server-tls")
	cert.SetNamespace(ns)
	cert.SetLabels(gatewayLabels(gw))
	cert.Object["spec"] = map[string]interface{}{
		"secretName": gw.Name + "-server-tls",
		"issuerRef":  map[string]interface{}{"name": issuerName, "kind": issuerKind},
		"dnsNames":   []interface{}{gw.Spec.Route.Hostname},
	}
	return r.Create(ctx, cert)
}

func (r *OpenShellGatewayReconciler) reconcileSelfSignedTLS(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	ns := gatewayNamespace(gw)
	serverSecretName := gw.Name + "-server-tls"
	clientSecretName := gw.Name + "-client-tls"
	sans := computeServerSANs(gw)
	sansHash := pki.HashSANs(sans)

	serverSecret := &corev1.Secret{}
	serverErr := r.Get(ctx, types.NamespacedName{Name: serverSecretName, Namespace: ns}, serverSecret)
	clientSecret := &corev1.Secret{}
	clientErr := r.Get(ctx, types.NamespacedName{Name: clientSecretName, Namespace: ns}, clientSecret)

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

	client := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: clientSecretName, Namespace: ns}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, client, func() error {
		client.Labels = gatewayLabels(gw)
		client.Type = corev1.SecretTypeTLS
		client.Data = map[string][]byte{"tls.crt": bundle.ClientCert, "tls.key": bundle.ClientKey, "ca.crt": bundle.CACert}
		return nil
	}); err != nil {
		return fmt.Errorf("creating client TLS secret: %w", err)
	}

	return nil
}

// --- JWT Keys ---

func (r *OpenShellGatewayReconciler) reconcileJWTKeys(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	ns := gatewayNamespace(gw)
	secretName := gw.Name + "-jwt-keys"

	existing := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ns}, existing); err == nil {
		return nil
	}

	keys, err := pki.GenerateJWTKeys()
	if err != nil {
		return fmt.Errorf("generating JWT keys: %w", err)
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

		var deployOIDCIssuer string
		if authBridgeEnabled(gw, isOCP) {
			deployOIDCIssuer = authBridgeInternalURL(gw)
		}
		configHash := computeConfigHash(gateway.RenderGatewayTOML(gw, sandboxNamespace(gw), deployOIDCIssuer))

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
						LocalObjectReference: corev1.LocalObjectReference{Name: gw.Spec.Database.SecretName},
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
			authBridgeIssuer := authBridgeInternalURL(gw)
			authBridgeExtIssuer := authBridgeExternalURL(gw)
			oauthServerURL := "https://oauth-openshift." + clusterDomain(gw)
			containers = append(containers, corev1.Container{
				Name:  "auth-bridge",
				Image: authBridgeImage(gw),
				Env: []corev1.EnvVar{
					{Name: "AUTH_BRIDGE_ISSUER", Value: authBridgeIssuer},
					{Name: "AUTH_BRIDGE_EXTERNAL_ISSUER", Value: authBridgeExtIssuer},
					{Name: "AUTH_BRIDGE_OPENSHIFT_ISSUER", Value: oauthServerURL},
					{Name: "AUTH_BRIDGE_CLIENT_ID", Value: "openshell"},
					{Name: "AUTH_BRIDGE_CLIENT_SECRET", ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: gw.Name + "-oauth-client"},
							Key:                  "secret",
						},
					}},
					{Name: "AUTH_BRIDGE_ADMIN_GROUP", Value: gw.Spec.Auth.OpenShift.AdminGroup},
				},
				Ports: []corev1.ContainerPort{
					{Name: "auth", ContainerPort: 8085, Protocol: corev1.ProtocolTCP},
				},
				LivenessProbe: &corev1.Probe{
					ProbeHandler:  corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromString("auth")}},
					PeriodSeconds: 10,
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
			ports = append(ports, corev1.ServicePort{Name: "auth", Port: 8085, TargetPort: intstr.FromString("auth"), Protocol: corev1.ProtocolTCP})
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
	spec := map[string]interface{}{
		"to":   map[string]interface{}{"kind": "Service", "name": gw.Name},
		"port": map[string]interface{}{"targetPort": "grpc"},
		"tls":  map[string]interface{}{"termination": "passthrough"},
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
	if gw.Spec.Route.Hostname != "" && existingHost != gw.Spec.Route.Hostname {
		if err := r.Delete(ctx, existing); err != nil {
			return fmt.Errorf("deleting route for hostname change: %w", err)
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

func (r *OpenShellGatewayReconciler) updateStatus(ctx context.Context, gw *ogov1alpha1.OpenShellGateway, isOCP bool) error {
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

	if deploy.Status.ReadyReplicas > 0 && deploy.Status.ReadyReplicas == *deploy.Spec.Replicas {
		latest.Status.Phase = ogov1alpha1.PhaseRunning
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type: ogov1alpha1.ConditionAvailable, Status: metav1.ConditionTrue,
			Reason: "Ready", Message: "Gateway is running",
		})
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type: ogov1alpha1.ConditionProgressing, Status: metav1.ConditionFalse,
			Reason: "Complete", Message: "Rollout complete",
		})
	} else {
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

	latest.Status.GatewayURL = fmt.Sprintf("https://%s.%s.svc.cluster.local:8080", gw.Name, ns)
	if isOCP {
		route := &unstructured.Unstructured{}
		route.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
		if err := r.Get(ctx, types.NamespacedName{Name: gw.Name, Namespace: ns}, route); err == nil {
			if host, ok, _ := unstructured.NestedString(route.Object, "spec", "host"); ok && host != "" {
				latest.Status.GatewayURL = "https://" + host + ":443"
			}
		}
	}

	latest.Status.ClientCertSecretName = gw.Name + "-client-tls"
	latest.Status.ObservedGeneration = gw.Generation

	return r.Status().Update(ctx, latest)
}

func (r *OpenShellGatewayReconciler) setDegraded(ctx context.Context, gw *ogov1alpha1.OpenShellGateway, step string, reconcileErr error) error {
	latest := &ogov1alpha1.OpenShellGateway{}
	if err := r.Get(ctx, types.NamespacedName{Name: gw.Name}, latest); err != nil {
		return err
	}
	latest.Status.Phase = ogov1alpha1.PhaseFailed
	meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
		Type: ogov1alpha1.ConditionDegraded, Status: metav1.ConditionTrue,
		Reason: "ReconcileError", Message: fmt.Sprintf("%s: %v", step, reconcileErr),
	})
	return r.Status().Update(ctx, latest)
}

// --- Setup ---

func (r *OpenShellGatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ogov1alpha1.OpenShellGateway{}).
		Named("openshellgateway").
		Complete(r)
}

// --- Helpers ---

func gatewayNamespace(gw *ogov1alpha1.OpenShellGateway) string {
	if gw.Spec.Namespace != "" {
		return gw.Spec.Namespace
	}
	return "ogo"
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
		labelManagedBy: "ogo", labelPartOf: "openshell-gateway",
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
		sans = append(sans, gw.Spec.Route.Hostname)
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
	tag := gw.Spec.ImageTag
	if tag == "" {
		tag = "latest"
	}
	return "quay.io/aknochow/ogo-auth-bridge:" + tag
}

func authBridgeExternalURL(gw *ogov1alpha1.OpenShellGateway) string {
	if gw.Spec.Route.Hostname != "" {
		domain := gw.Spec.Route.Hostname
		return "https://openshell-auth." + domain[len("openshell."):]
	}
	return "http://openshell-auth." + gatewayNamespace(gw) + ".svc:8085"
}

func authBridgeInternalURL(_ *ogov1alpha1.OpenShellGateway) string {
	return "http://localhost:8085"
}

func clusterDomain(gw *ogov1alpha1.OpenShellGateway) string {
	if gw.Spec.Route.Hostname != "" {
		host := gw.Spec.Route.Hostname
		idx := len("openshell.")
		if idx < len(host) {
			return host[idx:]
		}
	}
	return "apps.example.com"
}

func (r *OpenShellGatewayReconciler) reconcileAuthBridgeRoute(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	ns := gatewayNamespace(gw)
	routeName := gw.Name + "-auth"

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
	err := r.Get(ctx, types.NamespacedName{Name: routeName, Namespace: ns}, existing)
	if apierrors.IsNotFound(err) {
		hostname := ""
		if gw.Spec.Route.Hostname != "" {
			hostname = "openshell-auth." + gw.Spec.Route.Hostname[len("openshell."):]
		}

		route := &unstructured.Unstructured{}
		route.SetGroupVersionKind(schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"})
		route.SetName(routeName)
		route.SetNamespace(ns)
		route.SetLabels(gatewayLabels(gw))
		spec := map[string]interface{}{
			"to":   map[string]interface{}{"kind": "Service", "name": gw.Name},
			"port": map[string]interface{}{"targetPort": "auth"},
			"tls":  map[string]interface{}{"termination": "edge", "insecureEdgeTerminationPolicy": "Redirect"},
		}
		if hostname != "" {
			spec["host"] = hostname
		}
		route.Object["spec"] = spec
		return r.Create(ctx, route)
	}
	return err
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

	oauthClient := &unstructured.Unstructured{}
	oauthClient.SetGroupVersionKind(schema.GroupVersionKind{Group: "oauth.openshift.io", Version: "v1", Kind: "OAuthClient"})

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(schema.GroupVersionKind{Group: "oauth.openshift.io", Version: "v1", Kind: "OAuthClient"})
	err = r.Get(ctx, types.NamespacedName{Name: "openshell"}, existing)
	if apierrors.IsNotFound(err) {
		oauthClient.SetName("openshell")
		oauthClient.SetLabels(gatewayLabels(gw))
		oauthClient.Object["secret"] = clientSecret
		oauthClient.Object["grantMethod"] = "auto"
		oauthClient.Object["redirectURIs"] = []interface{}{callbackURL}
		return r.Create(ctx, oauthClient)
	}
	return err
}

func generateOAuthSecret() string {
	b := make([]byte, 24)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
