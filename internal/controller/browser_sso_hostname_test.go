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
	"testing"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBrowserSSOMissingHostname(t *testing.T) {
	tests := []struct {
		name        string
		authEnabled bool
		routeSpec   ogov1alpha1.RouteSpec
		want        bool
	}{
		{
			name:        "browser SSO and routing enabled, no hostname -- the bug in #50",
			authEnabled: true,
			routeSpec:   ogov1alpha1.RouteSpec{Hostname: ""},
			want:        true,
		},
		{
			name:        "browser SSO and routing enabled, explicit hostname",
			authEnabled: true,
			routeSpec:   ogov1alpha1.RouteSpec{Hostname: "openshell.apps.example.com"},
			want:        false,
		},
		{
			name:        "browser SSO enabled, routing explicitly disabled -- headless-shaped, unaffected",
			authEnabled: true,
			routeSpec:   ogov1alpha1.RouteSpec{Enabled: ptr.To(false), Hostname: ""},
			want:        false,
		},
		{
			name:        "browser SSO disabled -- headless-only access, unaffected regardless of hostname",
			authEnabled: false,
			routeSpec:   ogov1alpha1.RouteSpec{Hostname: ""},
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw := &ogov1alpha1.OpenShellGateway{
				Spec: ogov1alpha1.OpenShellGatewaySpec{Route: tt.routeSpec},
			}
			if got := browserSSOMissingHostname(gw, tt.authEnabled); got != tt.want {
				t.Errorf("browserSSOMissingHostname() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestReconcileOpenShiftRoutingBrowserSSOHostname exercises the actual
// Reconcile-level branching (not just the browserSSOMissingHostname decision
// function above) with a fake client, proving that a missing hostname really
// does skip creating the auth-bridge Route/OAuthClient, and that an explicit
// hostname really does create them. TLS is disabled on the fixture purely to
// avoid needing a server-tls Secret/CA ConfigMap for this test's own sake -
// unrelated to the hostname behavior under test.
func TestReconcileOpenShiftRoutingBrowserSSOHostname(t *testing.T) {
	routeGVK := schema.GroupVersionKind{Group: "route.openshift.io", Version: "v1", Kind: "Route"}
	oauthClientGVK := schema.GroupVersionKind{Group: "oauth.openshift.io", Version: "v1", Kind: "OAuthClient"}

	newScheme := func(t *testing.T) *runtime.Scheme {
		t.Helper()
		scheme := runtime.NewScheme()
		if err := corev1.AddToScheme(scheme); err != nil {
			t.Fatal(err)
		}
		if err := rbacv1.AddToScheme(scheme); err != nil {
			t.Fatal(err)
		}
		if err := ogov1alpha1.AddToScheme(scheme); err != nil {
			t.Fatal(err)
		}
		scheme.AddKnownTypeWithName(routeGVK, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(routeGVK.GroupVersion().WithKind("RouteList"), &unstructured.UnstructuredList{})
		scheme.AddKnownTypeWithName(oauthClientGVK, &unstructured.Unstructured{})
		metav1.AddToGroupVersion(scheme, routeGVK.GroupVersion())
		metav1.AddToGroupVersion(scheme, oauthClientGVK.GroupVersion())
		return scheme
	}

	const gatewayName = "test-gateway"
	const namespace = "test-namespace"

	for _, tt := range []struct {
		name           string
		hostname       string
		wantConditions metav1.ConditionStatus
		wantRoute      bool
	}{
		{
			name:           "no hostname -- auth-bridge Route/OAuthClient must not be created",
			hostname:       "",
			wantConditions: metav1.ConditionFalse,
			wantRoute:      false,
		},
		{
			name:           "explicit hostname -- auth-bridge Route/OAuthClient must be created",
			hostname:       "openshell.apps.example.com",
			wantConditions: metav1.ConditionTrue,
			wantRoute:      true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gw := &ogov1alpha1.OpenShellGateway{
				ObjectMeta: metav1.ObjectMeta{Name: gatewayName},
				Spec: ogov1alpha1.OpenShellGatewaySpec{
					Namespace: namespace,
					TLS:       ogov1alpha1.TLSSpec{Enabled: ptr.To(false)},
					Auth:      ogov1alpha1.AuthSpec{OpenShift: ogov1alpha1.OpenShiftAuth{Enabled: ptr.To(true)}},
					Route:     ogov1alpha1.RouteSpec{Hostname: tt.hostname},
				},
			}
			r := &OpenShellGatewayReconciler{
				Client: clientfake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(gw).Build(),
			}

			step, err := r.reconcileOpenShiftRouting(context.Background(), gw, false, true)
			if err != nil {
				t.Fatalf("reconcileOpenShiftRouting: step=%q err=%v", step, err)
			}

			condition := apimeta.FindStatusCondition(gw.Status.Conditions, ogov1alpha1.ConditionBrowserSSOReady)
			if condition == nil {
				t.Fatal("BrowserSSOReady condition was not set")
			}
			if condition.Status != tt.wantConditions {
				t.Errorf("BrowserSSOReady.Status = %v, want %v", condition.Status, tt.wantConditions)
			}

			route := &unstructured.Unstructured{}
			route.SetGroupVersionKind(routeGVK)
			err = r.Get(context.Background(), types.NamespacedName{Name: gatewayName + "-auth", Namespace: namespace}, route)
			gotRoute := err == nil
			if !gotRoute && !apierrors.IsNotFound(err) {
				t.Fatalf("unexpected error getting auth-bridge Route: %v", err)
			}
			if gotRoute != tt.wantRoute {
				t.Errorf("auth-bridge Route exists = %v, want %v", gotRoute, tt.wantRoute)
			}

			oauthClient := &unstructured.Unstructured{}
			oauthClient.SetGroupVersionKind(oauthClientGVK)
			err = r.Get(context.Background(), types.NamespacedName{Name: "openshell"}, oauthClient)
			gotOAuthClient := err == nil
			if !gotOAuthClient && !apierrors.IsNotFound(err) {
				t.Fatalf("unexpected error getting OAuthClient: %v", err)
			}
			if gotOAuthClient != tt.wantRoute {
				t.Errorf("OAuthClient exists = %v, want %v", gotOAuthClient, tt.wantRoute)
			}
		})
	}
}
