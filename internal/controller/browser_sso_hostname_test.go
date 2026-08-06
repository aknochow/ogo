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
	"testing"

	ogov1alpha1 "github.com/aknochow/ogo/api/v1alpha1"
	"k8s.io/utils/ptr"
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
