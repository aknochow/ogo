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
	"reflect"
	"testing"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAuthBridgeRouteUsesReencryptTLS(t *testing.T) {
	const (
		gatewayName = "test-gateway"
		namespace   = "test-namespace"
	)
	routeGVK := schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"}

	for _, existing := range []bool{false, true} {
		name := "create"
		if existing {
			name = "upgrade edge route"
		}
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			scheme.AddKnownTypeWithName(routeGVK, &unstructured.Unstructured{})
			scheme.AddKnownTypeWithName(routeGVK.GroupVersion().WithKind("RouteList"), &unstructured.UnstructuredList{})
			metav1.AddToGroupVersion(scheme, routeGVK.GroupVersion())

			objects := []client.Object{&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: gatewayName + "-auth-ca", Namespace: namespace},
				Data:       map[string]string{"ca.crt": "test-ca"},
			}}
			if existing {
				route := &unstructured.Unstructured{}
				route.SetGroupVersionKind(routeGVK)
				route.SetName(gatewayName + "-auth")
				route.SetNamespace(namespace)
				route.SetLabels(map[string]string{labelManagedBy: managedByValue, labelInstance: gatewayName})
				route.Object["spec"] = map[string]interface{}{
					"tls": map[string]interface{}{"termination": "edge"},
				}
				objects = append(objects, route)
			}

			r := &OpenShellGatewayReconciler{Client: clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()}
			gw := &ogov1alpha1.OpenShellGateway{
				ObjectMeta: metav1.ObjectMeta{Name: gatewayName},
				Spec:       ogov1alpha1.OpenShellGatewaySpec{Namespace: namespace},
			}
			if err := r.reconcileAuthBridgeRoute(context.Background(), gw); err != nil {
				t.Fatalf("reconcile auth bridge Route: %v", err)
			}

			route := &unstructured.Unstructured{}
			route.SetGroupVersionKind(routeGVK)
			if err := r.Get(context.Background(), types.NamespacedName{Name: gatewayName + "-auth", Namespace: namespace}, route); err != nil {
				t.Fatal(err)
			}
			termination, _, _ := unstructured.NestedString(route.Object, "spec", "tls", "termination")
			if termination != "reencrypt" {
				t.Fatalf("termination = %q, want reencrypt", termination)
			}
			destinationCA, _, _ := unstructured.NestedString(route.Object, "spec", "tls", "destinationCACertificate")
			if destinationCA != "test-ca" {
				t.Fatalf("destination CA = %q, want test-ca", destinationCA)
			}
		})
	}
}

func TestAuthBridgeRouteRepairsManagedDrift(t *testing.T) {
	const (
		gatewayName = "test-gateway"
		namespace   = "test-namespace"
	)
	routeGVK := schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	scheme.AddKnownTypeWithName(routeGVK, &unstructured.Unstructured{})
	metav1.AddToGroupVersion(scheme, routeGVK.GroupVersion())

	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(routeGVK)
	route.SetName(gatewayName + "-auth")
	route.SetNamespace(namespace)
	route.SetLabels(map[string]string{labelManagedBy: managedByValue, labelInstance: gatewayName})
	route.Object["spec"] = map[string]interface{}{
		"host": "old.example.com",
		"to":   map[string]interface{}{"kind": "Service", "name": "wrong", "weight": int64(0)},
		"alternateBackends": []interface{}{
			map[string]interface{}{"kind": "Service", "name": "wrong", "weight": int64(100)},
		},
		"port": map[string]interface{}{"targetPort": "wrong"},
		"tls": map[string]interface{}{
			"termination":              "reencrypt",
			"destinationCACertificate": "test-ca",
		},
	}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: gatewayName + "-auth-ca", Namespace: namespace},
		Data:       map[string]string{"ca.crt": "test-ca"},
	}
	r := &OpenShellGatewayReconciler{Client: clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(route, configMap).Build()}
	gw := &ogov1alpha1.OpenShellGateway{
		ObjectMeta: metav1.ObjectMeta{Name: gatewayName},
		Spec: ogov1alpha1.OpenShellGatewaySpec{
			Namespace: namespace,
			Route:     ogov1alpha1.RouteSpec{Hostname: "gateway.apps.example.com"},
		},
	}
	if err := r.reconcileAuthBridgeRoute(context.Background(), gw); err != nil {
		t.Fatal(err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(routeGVK)
	if err := r.Get(context.Background(), types.NamespacedName{Name: route.GetName(), Namespace: namespace}, updated); err != nil {
		t.Fatal(err)
	}
	assertNested := func(want string, fields ...string) {
		t.Helper()
		got, _, _ := unstructured.NestedString(updated.Object, fields...)
		if got != want {
			t.Errorf("%v = %q, want %q", fields, got, want)
		}
	}
	assertNested("openshell-auth.apps.example.com", "spec", "host")
	assertNested("Service", "spec", "to", "kind")
	assertNested(gatewayName, "spec", "to", "name")
	assertNested("auth", "spec", "port", "targetPort")
	assertNested("Redirect", "spec", "tls", "insecureEdgeTerminationPolicy")
	weight, _, _ := unstructured.NestedInt64(updated.Object, "spec", "to", "weight")
	if weight != 100 {
		t.Errorf("backend weight = %d, want 100", weight)
	}
	if _, found, _ := unstructured.NestedSlice(updated.Object, "spec", "alternateBackends"); found {
		t.Error("alternateBackends was not removed")
	}
}

func TestAuthBridgeRouteDoesNotAdoptUnmanagedRoute(t *testing.T) {
	const (
		gatewayName = "test-gateway"
		namespace   = "test-namespace"
	)
	routeGVK := schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	scheme.AddKnownTypeWithName(routeGVK, &unstructured.Unstructured{})
	metav1.AddToGroupVersion(scheme, routeGVK.GroupVersion())

	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(routeGVK)
	route.SetName(gatewayName + "-auth")
	route.SetNamespace(namespace)
	route.Object["spec"] = map[string]interface{}{"host": "user.example.com"}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: gatewayName + "-auth-ca", Namespace: namespace},
		Data:       map[string]string{"ca.crt": "test-ca"},
	}
	r := &OpenShellGatewayReconciler{Client: clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(route, configMap).Build()}
	before := &unstructured.Unstructured{}
	before.SetGroupVersionKind(routeGVK)
	if err := r.Get(context.Background(), types.NamespacedName{Name: route.GetName(), Namespace: namespace}, before); err != nil {
		t.Fatal(err)
	}
	gw := &ogov1alpha1.OpenShellGateway{
		ObjectMeta: metav1.ObjectMeta{Name: gatewayName},
		Spec:       ogov1alpha1.OpenShellGatewaySpec{Namespace: namespace},
	}
	if err := r.reconcileAuthBridgeRoute(context.Background(), gw); err == nil {
		t.Fatal("expected unmanaged Route conflict")
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(routeGVK)
	if err := r.Get(context.Background(), types.NamespacedName{Name: route.GetName(), Namespace: namespace}, updated); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Object, updated.Object) {
		t.Fatalf("unmanaged Route changed: %#v", updated.Object)
	}
}

func TestAuthBridgeInternalTLSURLAndSAN(t *testing.T) {
	gw := &ogov1alpha1.OpenShellGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "test-gateway"},
		Spec: ogov1alpha1.OpenShellGatewaySpec{
			Namespace: "test-namespace",
			Route: ogov1alpha1.RouteSpec{
				Enabled: ptr.To(false), Hostname: "gateway.apps.example.com",
			},
		},
	}
	if got := authBridgeExternalURL(gw); got != "https://test-gateway.test-namespace.svc:8085" {
		t.Fatalf("internal auth URL = %q", got)
	}

	wantSAN := "openshell-auth.apps.example.com"
	for _, san := range computeServerSANs(gw) {
		if san == wantSAN {
			return
		}
	}
	t.Fatalf("server SANs do not include %q", wantSAN)
}
