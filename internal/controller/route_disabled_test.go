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
	"errors"
	"slices"
	"strings"
	"testing"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var routeGVK = schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"}

func TestReconcileObsoleteRoutes(t *testing.T) {
	const (
		gatewayName      = "test-gateway"
		gatewayNamespace = "test-namespace"
		envoyNamespace   = envoyGatewaySystemNS
	)

	for _, tt := range []struct {
		name        string
		route       bool
		useGWAPI    bool
		authEnabled bool
		want        map[string]bool
	}{
		{
			name: "routing disabled",
			want: map[string]bool{},
		},
		{
			name:        "direct route with auth",
			route:       true,
			authEnabled: true,
			want: map[string]bool{
				gatewayNamespace + "/" + gatewayName:           true,
				gatewayNamespace + "/" + gatewayName + "-auth": true,
			},
		},
		{
			name:     "gateway API route without auth",
			route:    true,
			useGWAPI: true,
			want: map[string]bool{
				envoyNamespace + "/" + gatewayName + "-gw": true,
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			routes := []*unstructured.Unstructured{
				newTestRoute(gatewayName, gatewayNamespace, true),
				newTestRoute(gatewayName+"-auth", gatewayNamespace, true),
				newTestRoute(gatewayName+"-gw", envoyNamespace, true),
				newTestRoute(gatewayName, "old-namespace", true),
				newTestRoute(gatewayName, "user-namespace", false),
			}
			objects := make([]client.Object, 0, len(routes))
			for _, route := range routes {
				objects = append(objects, route)
			}

			r := &OpenShellGatewayReconciler{Client: newRouteClient(objects...)}
			gw := &ogov1alpha1.OpenShellGateway{
				ObjectMeta: metav1.ObjectMeta{Name: gatewayName},
				Spec: ogov1alpha1.OpenShellGatewaySpec{
					Namespace: gatewayNamespace,
					Route:     ogov1alpha1.RouteSpec{Enabled: ptr.To(tt.route)},
				},
			}

			if err := r.reconcileObsoleteRoutes(context.Background(), gw, tt.useGWAPI, tt.authEnabled); err != nil {
				t.Fatalf("reconcile obsolete routes: %v", err)
			}

			for _, route := range routes {
				key := route.GetNamespace() + "/" + route.GetName()
				want := tt.want[key] || route.GetNamespace() == "user-namespace"
				got := &unstructured.Unstructured{}
				got.SetGroupVersionKind(routeGVK)
				err := r.Get(context.Background(), types.NamespacedName{Name: route.GetName(), Namespace: route.GetNamespace()}, got)
				if want && err != nil {
					t.Errorf("Route %s should remain: %v", key, err)
				}
				if !want && !apierrors.IsNotFound(err) {
					t.Errorf("Route %s should be deleted: %v", key, err)
				}
			}
		})
	}
}

func TestDeleteManagedRoutes(t *testing.T) {
	gw := &ogov1alpha1.OpenShellGateway{ObjectMeta: metav1.ObjectMeta{Name: "test-gateway"}}
	managed := []*unstructured.Unstructured{
		newTestRoute(gw.Name, "gateway-namespace", true),
		newTestRoute(gw.Name+"-auth", "gateway-namespace", true),
		newTestRoute(gw.Name+"-gw", envoyGatewaySystemNS, true),
	}
	unmanaged := newTestRoute(gw.Name, "user-namespace", false)
	objects := make([]client.Object, 1, len(managed)+1)
	objects[0] = unmanaged
	for _, route := range managed {
		objects = append(objects, route)
	}
	r := &OpenShellGatewayReconciler{Client: newRouteClient(objects...)}

	if err := r.deleteManagedRoutes(context.Background(), gw); err != nil {
		t.Fatalf("delete managed routes: %v", err)
	}
	for _, route := range managed {
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(routeGVK)
		err := r.Get(context.Background(), client.ObjectKeyFromObject(route), got)
		if !apierrors.IsNotFound(err) {
			t.Errorf("managed Route %s/%s was not deleted: %v", route.GetNamespace(), route.GetName(), err)
		}
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(routeGVK)
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(unmanaged), got); err != nil {
		t.Fatalf("unmanaged Route was changed: %v", err)
	}
}

func TestReconcileDeleteRetainsFinalizerWhenRouteCleanupFails(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		ogov1alpha1.AddToScheme,
		appsv1.AddToScheme,
		corev1.AddToScheme,
		networkingv1.AddToScheme,
		rbacv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	scheme.AddKnownTypeWithName(routeGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(routeGVK.GroupVersion().WithKind("RouteList"), &unstructured.UnstructuredList{})
	gw := &ogov1alpha1.OpenShellGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "test-gateway", Finalizers: []string{finalizerName}},
		Spec:       ogov1alpha1.OpenShellGatewaySpec{Namespace: "test-namespace"},
	}
	fakeClient := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(gw).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if list.GetObjectKind().GroupVersionKind() == routeGVK.GroupVersion().WithKind("RouteList") {
					return errors.New("route list failed")
				}
				return c.List(ctx, list, opts...)
			},
		}).Build()
	r := &OpenShellGatewayReconciler{
		Client: fakeClient, Scheme: scheme, DiscoveryClient: k8sfake.NewSimpleClientset().Discovery(),
	}

	result, err := r.reconcileDelete(context.Background(), gw)
	if err == nil || result.RequeueAfter == 0 {
		t.Fatalf("result = %#v, error = %v, want cleanup retry", result, err)
	}
	updated := &ogov1alpha1.OpenShellGateway{}
	if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(gw), updated); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(updated.Finalizers, finalizerName) {
		t.Fatal("finalizer was removed after Route cleanup failed")
	}
}

func TestRouteReconcilersRejectUnmanagedCollisions(t *testing.T) {
	const namespace = "test-namespace"
	gw := &ogov1alpha1.OpenShellGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "test-gateway"},
		Spec: ogov1alpha1.OpenShellGatewaySpec{
			Namespace: namespace,
			Route:     ogov1alpha1.RouteSpec{Hostname: "gateway.example.com"},
		},
	}
	for _, tt := range []struct {
		name      string
		routeName string
		namespace string
		objects   []client.Object
		run       func(*OpenShellGatewayReconciler) error
	}{
		{
			name: "direct", routeName: gw.Name, namespace: namespace,
			run: func(r *OpenShellGatewayReconciler) error { return r.reconcileRoute(context.Background(), gw) },
		},
		{
			name: "auth", routeName: gw.Name + "-auth", namespace: namespace,
			run: func(r *OpenShellGatewayReconciler) error { return r.reconcileAuthBridgeRoute(context.Background(), gw) },
		},
		{
			name: "envoy", routeName: gw.Name + "-gw", namespace: envoyGatewaySystemNS,
			objects: []client.Object{&corev1.Service{ObjectMeta: metav1.ObjectMeta{
				Name: "envoy", Namespace: envoyGatewaySystemNS, Labels: map[string]string{
					"gateway.envoyproxy.io/owning-gateway-name":      gw.Name,
					"gateway.envoyproxy.io/owning-gateway-namespace": namespace,
				},
			}}},
			run: func(r *OpenShellGatewayReconciler) error {
				_, err := r.reconcileEnvoyRoute(context.Background(), gw)
				return err
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			route := newTestRoute(tt.routeName, tt.namespace, false)
			objects := append(tt.objects, route)
			r := &OpenShellGatewayReconciler{Client: newRouteClient(objects...)}
			err := tt.run(r)
			if err == nil || !strings.Contains(err.Error(), "not managed by OGO") {
				t.Fatalf("error = %v, want unmanaged Route conflict", err)
			}
			got := &unstructured.Unstructured{}
			got.SetGroupVersionKind(routeGVK)
			if err := r.Get(context.Background(), client.ObjectKeyFromObject(route), got); err != nil {
				t.Fatalf("unmanaged Route was deleted: %v", err)
			}
			if got.GetLabels()[labelManagedBy] == managedByValue {
				t.Fatal("unmanaged Route was adopted")
			}
		})
	}
}

func TestDisabledRouteClearsEnvoyCondition(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ogov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	gw := &ogov1alpha1.OpenShellGateway{
		ObjectMeta: metav1.ObjectMeta{Name: "test-gateway"},
		Spec: ogov1alpha1.OpenShellGatewaySpec{
			Namespace: "test-namespace",
			Route:     ogov1alpha1.RouteSpec{Enabled: ptr.To(false), Hostname: "gateway.example.com"},
		},
		Status: ogov1alpha1.OpenShellGatewayStatus{Conditions: []metav1.Condition{{
			Type: ogov1alpha1.ConditionEnvoyRouteReady, Status: metav1.ConditionFalse,
			Reason: "ProxyServiceNotFound", Message: "waiting",
		}}},
	}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: gw.Name, Namespace: gw.Spec.Namespace},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr.To(int32(1))},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
	fakeClient := clientfake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&ogov1alpha1.OpenShellGateway{}).
		WithObjects(gw, deployment).
		Build()
	r := &OpenShellGatewayReconciler{Client: fakeClient}

	if err := r.updateStatus(context.Background(), gw); err != nil {
		t.Fatalf("update status: %v", err)
	}
	updated := &ogov1alpha1.OpenShellGateway{}
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: gw.Name}, updated); err != nil {
		t.Fatalf("get updated gateway: %v", err)
	}
	if condition := apimeta.FindStatusCondition(updated.Status.Conditions, ogov1alpha1.ConditionEnvoyRouteReady); condition != nil {
		t.Fatalf("stale EnvoyRouteReady condition was not removed: %#v", condition)
	}
	available := apimeta.FindStatusCondition(updated.Status.Conditions, ogov1alpha1.ConditionAvailable)
	if available == nil || available.Status != metav1.ConditionTrue {
		t.Fatalf("Available condition = %#v, want True", available)
	}
	wantURL := "https://test-gateway.test-namespace.svc.cluster.local:8080"
	if updated.Status.GatewayURL != wantURL {
		t.Fatalf("GatewayURL = %q, want %q", updated.Status.GatewayURL, wantURL)
	}
}

func newRouteClient(objects ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	scheme.AddKnownTypeWithName(routeGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(routeGVK.GroupVersion().WithKind("RouteList"), &unstructured.UnstructuredList{})
	metav1.AddToGroupVersion(scheme, routeGVK.GroupVersion())
	return clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func newTestRoute(name, namespace string, managed bool) *unstructured.Unstructured {
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(routeGVK)
	route.SetName(name)
	route.SetNamespace(namespace)
	if managed {
		route.SetLabels(map[string]string{labelManagedBy: managedByValue, labelInstance: "test-gateway"})
	}
	return route
}
