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
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/controller-runtime/pkg/client"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	"github.com/aknochow/ogo/internal/manifests/envoygateway"
	"github.com/aknochow/ogo/internal/openshift"
)

const componentEnvoyGateway = "envoy-gateway"

type EnvoyGatewayReconciler struct {
	client.Client
	DiscoveryClient discovery.DiscoveryInterface
}

func (e *EnvoyGatewayReconciler) Name() string { return "EnvoyGatewayReady" }

func (e *EnvoyGatewayReconciler) Enabled(_ context.Context, gw *ogov1alpha1.OpenShellGateway) bool {
	if gw.Spec.Route.GatewayAPI.Enabled != nil && !*gw.Spec.Route.GatewayAPI.Enabled {
		return false
	}
	if gw.Spec.Route.GatewayAPI.InstallEnvoyGateway != nil && !*gw.Spec.Route.GatewayAPI.InstallEnvoyGateway {
		return false
	}
	if gw.Spec.Route.GatewayAPI.Enabled != nil && *gw.Spec.Route.GatewayAPI.Enabled {
		return true
	}
	return openshift.HasGatewayAPI(e.DiscoveryClient)
}

func (e *EnvoyGatewayReconciler) Reconcile(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) (metav1.Condition, error) {
	log := logf.FromContext(ctx)

	gcName := gatewayClassName(gw)
	gc := &unstructured.Unstructured{}
	gc.SetGroupVersionKind(gatewayClassGVK)
	err := e.Get(ctx, types.NamespacedName{Name: gcName}, gc)
	if err == nil {
		return metav1.Condition{
			Type: ogov1alpha1.ConditionEnvoyGatewayReady, Status: metav1.ConditionTrue,
			Reason: "External", Message: fmt.Sprintf("GatewayClass %q already exists", gcName),
		}, nil
	}
	// On a cluster where gateway.networking.k8s.io isn't registered at all
	// yet (older OpenShift without the Ingress Operator's Gateway API CRDs,
	// or vanilla Kubernetes), the client has no REST mapping for the Kind
	// and this Get returns a client-side NoMatchError, not NotFound -
	// treat it the same as "doesn't exist yet" so the install path below
	// actually runs instead of erroring out before ever attempting it.
	if !errors.IsNotFound(err) && !meta.IsNoMatchError(err) {
		return metav1.Condition{
			Type: ogov1alpha1.ConditionEnvoyGatewayReady, Status: metav1.ConditionFalse,
			Reason: "CheckFailed", Message: fmt.Sprintf("Failed to check GatewayClass: %v", err),
		}, err
	}

	log.Info("Installing Envoy Gateway", "version", envoygateway.Version)

	isOCP := openshift.IsOpenShift(e.DiscoveryClient)
	hasGWAPICRDs := openshift.HasGatewayAPI(e.DiscoveryClient)

	if !hasGWAPICRDs {
		if err := e.applyManifestFile(ctx, "gatewayapi-crds.yaml", gw); err != nil {
			return metav1.Condition{
				Type: ogov1alpha1.ConditionEnvoyGatewayReady, Status: metav1.ConditionFalse,
				Reason: "InstallFailed", Message: fmt.Sprintf("Failed to install Gateway API CRDs: %v", err),
			}, err
		}
		log.Info("Installed Gateway API CRDs")
	}

	if err := e.applyManifestFile(ctx, "envoygateway-crds.yaml", gw); err != nil {
		return metav1.Condition{
			Type: ogov1alpha1.ConditionEnvoyGatewayReady, Status: metav1.ConditionFalse,
			Reason: "InstallFailed", Message: fmt.Sprintf("Failed to install Envoy Gateway CRDs: %v", err),
		}, err
	}

	if err := e.applyManifestFile(ctx, "components.yaml", gw); err != nil {
		return metav1.Condition{
			Type: ogov1alpha1.ConditionEnvoyGatewayReady, Status: metav1.ConditionFalse,
			Reason: "InstallFailed", Message: fmt.Sprintf("Failed to install Envoy Gateway: %v", err),
		}, err
	}

	if isOCP {
		if err := e.grantOpenShiftSCCs(ctx, gw); err != nil {
			return metav1.Condition{
				Type: ogov1alpha1.ConditionEnvoyGatewayReady, Status: metav1.ConditionFalse,
				Reason: "SCCFailed", Message: fmt.Sprintf("Failed to grant SCCs: %v", err),
			}, err
		}
	}

	log.Info("Envoy Gateway installed", "version", envoygateway.Version)
	return metav1.Condition{
		Type: ogov1alpha1.ConditionEnvoyGatewayReady, Status: metav1.ConditionTrue,
		Reason: "Installed", Message: fmt.Sprintf("Envoy Gateway %s installed by OGO", envoygateway.Version),
	}, nil
}

// Cleanup deliberately never deletes anything - Envoy Gateway is a
// cluster-scoped shared component, and OGO is meant to run as a single
// singleton per cluster (per AGENTS.md), but auto-deleting a shared
// install on CR deletion is still a bigger blast radius than a one-line
// no-op justifies. Instead this logs exactly which resources OGO created
// (when it did) and the commands to remove them, so a deliberate teardown
// isn't a mystery - previously this was a silent no-op with no way to
// tell whether OGO had installed anything at all.
func (e *EnvoyGatewayReconciler) Cleanup(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	log := logf.FromContext(ctx)
	cond := meta.FindStatusCondition(gw.Status.Conditions, ogov1alpha1.ConditionEnvoyGatewayReady)
	if cond == nil {
		log.Info("Envoy Gateway cleanup skipped — no EnvoyGatewayReady condition recorded, unknown whether OGO installed anything")
		return nil
	}
	if cond.Reason != "Installed" {
		log.Info("Envoy Gateway cleanup skipped — not installed by OGO (external GatewayClass), nothing to remove", "reason", cond.Reason)
		return nil
	}
	log.Info("Envoy Gateway cleanup skipped — shared cluster component, remove manually if intended",
		"removeNamespacedResources", "oc delete deployment,service,configmap,serviceaccount,job,gatewayclass,envoyproxy "+
			"-l app.kubernetes.io/managed-by=ogo,ogo.aknochow.io/component=envoy-gateway -n envoy-gateway-system",
		"removeClusterScopedResources", "oc delete clusterrole,clusterrolebinding "+
			"-l app.kubernetes.io/managed-by=ogo,ogo.aknochow.io/component=envoy-gateway")
	return nil
}

func (e *EnvoyGatewayReconciler) applyManifestFile(ctx context.Context, filename string, gw *ogov1alpha1.OpenShellGateway) error {
	data, err := envoygateway.Manifests.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("reading embedded manifest %s: %w", filename, err)
	}
	return e.applyManifests(ctx, data, gw)
}

func (e *EnvoyGatewayReconciler) applyManifests(ctx context.Context, data []byte, gw *ogov1alpha1.OpenShellGateway) error {
	log := logf.FromContext(ctx)
	labels := ownershipLabels(componentEnvoyGateway, gw)
	reader := yaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))

	for {
		doc, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading YAML document: %w", err)
		}

		doc = bytes.TrimSpace(doc)
		if len(doc) == 0 || string(doc) == "---" {
			continue
		}

		obj := &unstructured.Unstructured{}
		if err := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(doc), len(doc)).Decode(obj); err != nil {
			if strings.Contains(err.Error(), "Object 'Kind' is missing") {
				continue
			}
			return fmt.Errorf("decoding manifest: %w", err)
		}

		if obj.GetKind() == "" {
			continue
		}

		existingLabels := obj.GetLabels()
		if existingLabels == nil {
			existingLabels = make(map[string]string)
		}
		for k, v := range labels {
			existingLabels[k] = v
		}
		obj.SetLabels(existingLabels)

		existing := obj.DeepCopy()
		key := types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}
		if err := e.Get(ctx, key, existing); err == nil {
			// CRDs and Jobs are both create-once, never-update: a CRD's
			// schema is already established, and a Job's spec.selector is
			// server-assigned on creation and immutable afterward, so
			// blindly re-submitting the manifest's spec as an Update
			// always fails once the server has defaulted that field.
			// eg-gateway-helm-certgen (this file's only Job) self-cleans
			// via ttlSecondsAfterFinished anyway, so there's nothing to
			// reconcile even if it were mutable.
			if existing.GetKind() == "CustomResourceDefinition" || existing.GetKind() == "Job" {
				log.V(1).Info("resource already exists, skipping (create-once kind)", "kind", existing.GetKind(), "name", obj.GetName())
				continue
			}
			obj.SetResourceVersion(existing.GetResourceVersion())
			if err := e.Update(ctx, obj); err != nil {
				return fmt.Errorf("updating %s/%s: %w", obj.GetKind(), obj.GetName(), err)
			}
			continue
		}
		if err := e.Create(ctx, obj); err != nil {
			if errors.IsAlreadyExists(err) {
				continue
			}
			return fmt.Errorf("creating %s/%s: %w", obj.GetKind(), obj.GetName(), err)
		}
		log.Info("Created resource", "kind", obj.GetKind(), "name", obj.GetName())
	}
	return nil
}

func (e *EnvoyGatewayReconciler) grantOpenShiftSCCs(ctx context.Context, gw *ogov1alpha1.OpenShellGateway) error {
	log := logf.FromContext(ctx)
	labels := ownershipLabels(componentEnvoyGateway, gw)

	sccBindings := []struct {
		name      string
		sa        string
		namespace string
		scc       string
	}{
		{
			name:      "envoy-gateway-certgen-anyuid",
			sa:        "envoy-gateway-certgen",
			namespace: "envoy-gateway-system",
			scc:       "anyuid",
		},
	}

	for _, b := range sccBindings {
		crb := &unstructured.Unstructured{}
		crb.SetGroupVersionKind(clusterRoleBindingGVK)
		crb.SetName(b.name)
		crb.SetLabels(labels)
		crb.Object["roleRef"] = map[string]interface{}{
			"apiGroup": "rbac.authorization.k8s.io",
			"kind":     "ClusterRole",
			"name":     "system:openshift:scc:" + b.scc,
		}
		crb.Object["subjects"] = []interface{}{
			map[string]interface{}{
				"kind":      "ServiceAccount",
				"name":      b.sa,
				"namespace": b.namespace,
			},
		}

		existing := &unstructured.Unstructured{}
		existing.SetGroupVersionKind(clusterRoleBindingGVK)
		if err := e.Get(ctx, types.NamespacedName{Name: b.name}, existing); err == nil {
			continue
		}
		if err := e.Create(ctx, crb); err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("creating SCC binding %s: %w", b.name, err)
		}
		log.Info("Granted SCC", "binding", b.name, "sa", b.sa, "scc", b.scc)
	}
	return nil
}
