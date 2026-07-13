/*
Copyright 2026 Adam Knochowski.

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

package authbridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	s, err := NewServer(Config{
		Issuer:         "http://localhost:8085",
		ExternalIssuer: "https://openshell-auth.apps.example.com",
		Audience:       "openshell-cli",
		ListenAddr:     ":8085",
		OpenShiftOAuth: "https://oauth-openshift.apps.example.com",
		ClientID:       "openshell",
		ClientSecret:   "test-secret",
		UserGroup:      "openshell-users",
		TokenTTL:       8 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestDiscoveryEndpoint(t *testing.T) {
	s := testServer(t)
	handler := s.Handler()

	req := httptest.NewRequest("GET", "/.well-known/openid-configuration", nil)
	req.Host = "openshell-auth.apps.example.com"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var discovery map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&discovery); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if discovery["issuer"] != "https://openshell-auth.apps.example.com" {
		t.Errorf("issuer = %v, want external issuer", discovery["issuer"])
	}
	if _, hasIDToken := discovery["id_token_signing_alg_values_supported"]; hasIDToken {
		t.Error("discovery should not advertise id_token signing (not returned)")
	}
	authMethods, ok := discovery["token_endpoint_auth_methods_supported"].([]interface{})
	if !ok || len(authMethods) == 0 || authMethods[0] != "client_secret_basic" {
		t.Errorf("auth methods = %v, want [client_secret_basic]", discovery["token_endpoint_auth_methods_supported"])
	}
}

func TestDiscoveryLocalhost(t *testing.T) {
	s := testServer(t)
	handler := s.Handler()

	req := httptest.NewRequest("GET", "/.well-known/openid-configuration", nil)
	req.Host = "localhost:8085"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	var discovery map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&discovery); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if discovery["issuer"] != "http://localhost:8085" {
		t.Errorf("issuer = %v, want internal issuer for localhost", discovery["issuer"])
	}
}

func TestJWKSEndpoint(t *testing.T) {
	s := testServer(t)
	handler := s.Handler()

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/jwks", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var jwks JWKSResponse
	if err := json.NewDecoder(w.Body).Decode(&jwks); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(jwks.Keys) != 1 {
		t.Fatalf("keys = %d, want 1", len(jwks.Keys))
	}
	if jwks.Keys[0].Alg != "RS256" {
		t.Errorf("alg = %q, want RS256", jwks.Keys[0].Alg)
	}
}

func TestHealthEndpoint(t *testing.T) {
	s := testServer(t)
	handler := s.Handler()

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("body = %q, want ok", w.Body.String())
	}
}

func TestIsAllowedRedirectURI(t *testing.T) {
	s := testServer(t)
	tests := []struct {
		name    string
		uri     string
		allowed bool
	}{
		{"empty", "", false},
		{"localhost", "http://localhost:12345/callback", true},
		{"localhost no port", "http://localhost/callback", true},
		{"localhost any path", "http://localhost:8080/any/path", true},
		{"127.0.0.1", "http://127.0.0.1:9999/callback", true},
		{"external issuer callback", "https://openshell-auth.apps.example.com/callback", true},
		{"external issuer callback query", "https://openshell-auth.apps.example.com/callback?code=x", true},
		{"external issuer wrong path", "https://openshell-auth.apps.example.com/steal", false},
		{"external issuer root", "https://openshell-auth.apps.example.com/", false},
		{"evil host", "https://evil.com/steal", false},
		{"no scheme", "//evil.com/callback", false},
		{"ftp scheme", "ftp://localhost/file", false},
		{"javascript", "javascript:alert(1)", false},
		{"no host", "http:///path", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.isAllowedRedirectURI(tt.uri)
			if got != tt.allowed {
				t.Errorf("isAllowedRedirectURI(%q) = %v, want %v", tt.uri, got, tt.allowed)
			}
		})
	}
}

func TestTokenEndpointRejectsGet(t *testing.T) {
	s := testServer(t)
	handler := s.Handler()

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/token", nil))

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestTokenEndpointRejectsInvalidCode(t *testing.T) {
	s := testServer(t)
	handler := s.Handler()

	form := url.Values{"grant_type": {"authorization_code"}, "code": {"invalid-code"}}
	req := httptest.NewRequest("POST", "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestTokenEndpointRejectsWrongGrantType(t *testing.T) {
	s := testServer(t)
	handler := s.Handler()

	form := url.Values{"grant_type": {"client_credentials"}, "code": {"test"}}
	req := httptest.NewRequest("POST", "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestPendingCodesCapEnforced(t *testing.T) {
	s := testServer(t)

	s.codesMu.Lock()
	for i := 0; i < maxPendingCodes; i++ {
		s.codes[generateCode()] = &pendingCode{
			expiresAt: time.Now().Add(5 * time.Minute),
		}
	}
	s.codesMu.Unlock()

	handler := s.Handler()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/authorize?redirect_uri=http://localhost:9999/callback&state=test", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when codes map is full", w.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	s := testServer(t)
	handler := s.Handler()

	endpoints := []string{"/.well-known/openid-configuration", "/jwks", "/healthz"}
	for _, ep := range endpoints {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", ep, nil))

		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing X-Content-Type-Options", ep)
		}
		if w.Header().Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: missing X-Frame-Options", ep)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: missing Cache-Control", ep)
		}
	}
}

func TestIsAuthorized(t *testing.T) {
	s := testServer(t)
	s.config.UserGroup = "openshell-users"

	tests := []struct {
		name       string
		username   string
		groups     []string
		authorized bool
	}{
		{"member", "alice", []string{"other", "openshell-users"}, true},
		{"not member", "bob", []string{"other", "admins"}, false},
		{"empty groups", "bob", []string{}, false},
		{"admin but not user group", "bob", []string{"openshell-admins"}, false},
		{"kube:admin bypasses", "kube:admin", []string{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.isAuthorized(tt.username, tt.groups)
			if got != tt.authorized {
				t.Errorf("isAuthorized(%q, %v) = %v, want %v", tt.username, tt.groups, got, tt.authorized)
			}
		})
	}
}

func TestIsAuthorizedEmptyConfig(t *testing.T) {
	s := testServer(t)
	s.config.UserGroup = ""
	if s.isAuthorized("alice", []string{"any-group"}) {
		t.Error("empty userGroup should reject all users")
	}
}

func TestTokenExchangeRejectsGet(t *testing.T) {
	s := testServer(t)
	handler := s.Handler()

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/token/exchange", nil))

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestTokenExchangeRejectsMissingBearer(t *testing.T) {
	s := testServer(t)
	handler := s.Handler()

	req := httptest.NewRequest("POST", "/token/exchange", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestTokenExchangeRejectsEmptyBearer(t *testing.T) {
	s := testServer(t)
	handler := s.Handler()

	req := httptest.NewRequest("POST", "/token/exchange", nil)
	req.Header.Set("Authorization", "Basic dGVzdDp0ZXN0")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for non-Bearer auth", w.Code)
	}
}

func TestTokenExchangeWithMockOpenShift(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/apis/user.openshift.io/v1/users/~" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer valid-ocp-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"metadata":{"name":"alice","uid":"uid-alice"},"groups":["openshell-users"]}`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer mock.Close()

	t.Setenv("KUBERNETES_API_URL", mock.URL)

	s := testServer(t)
	handler := s.Handler()

	req := httptest.NewRequest("POST", "/token/exchange", nil)
	req.Header.Set("Authorization", "Bearer valid-ocp-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["access_token"] == nil || resp["access_token"] == "" {
		t.Error("access_token is empty")
	}
	if resp["token_type"] != "Bearer" {
		t.Errorf("token_type = %v, want Bearer", resp["token_type"])
	}
	if resp["expires_at"] == nil {
		t.Error("expires_at is missing")
	}
}

func TestTokenExchangeRejectsNonMember(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/apis/user.openshift.io/v1/users/~" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"metadata":{"name":"bob","uid":"uid-bob"},"groups":["other-group"]}`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer mock.Close()

	t.Setenv("KUBERNETES_API_URL", mock.URL)

	s := testServer(t)
	handler := s.Handler()

	req := httptest.NewRequest("POST", "/token/exchange", nil)
	req.Header.Set("Authorization", "Bearer some-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for non-member", w.Code)
	}
}

func TestTokenExchangeRejectsInvalidToken(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer mock.Close()

	t.Setenv("KUBERNETES_API_URL", mock.URL)

	s := testServer(t)
	handler := s.Handler()

	req := httptest.NewRequest("POST", "/token/exchange", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for invalid token", w.Code)
	}
}

func TestMapRoles(t *testing.T) {
	s := testServer(t)
	s.config.AdminGroup = "openshell-admins"

	tests := []struct {
		name      string
		groups    []string
		wantAdmin bool
	}{
		{"user only", []string{"openshell-users"}, false},
		{"user and admin", []string{"openshell-users", "openshell-admins"}, true},
		{"admin only", []string{"openshell-admins"}, true},
		{"no relevant groups", []string{"other"}, false},
		{"empty", []string{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roles := s.mapRoles(tt.groups)
			hasUser := false
			hasAdmin := false
			for _, r := range roles {
				if r == "openshell-user" {
					hasUser = true
				}
				if r == "openshell-admin" {
					hasAdmin = true
				}
			}
			if !hasUser {
				t.Error("should always have openshell-user role")
			}
			if hasAdmin != tt.wantAdmin {
				t.Errorf("admin = %v, want %v", hasAdmin, tt.wantAdmin)
			}
		})
	}
}

func TestMapRolesNoAdminGroup(t *testing.T) {
	s := testServer(t)
	s.config.AdminGroup = ""

	roles := s.mapRoles([]string{"openshell-admins"})
	for _, r := range roles {
		if r == "openshell-admin" {
			t.Error("should not assign admin role when adminGroup is empty")
		}
	}
}

func TestSplitState(t *testing.T) {
	tests := []struct {
		input string
		want  [2]string
	}{
		{"bridge:client", [2]string{"bridge", "client"}},
		{"nodelimiter", [2]string{"nodelimiter", ""}},
		{"a:b:c", [2]string{"a", "b:c"}},
		{":", [2]string{"", ""}},
		{"", [2]string{"", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := splitState(tt.input)
			if got != tt.want {
				t.Errorf("splitState(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestTokenEndpointHappyPath(t *testing.T) {
	s := testServer(t)
	handler := s.Handler()

	jwt, err := s.signer.MintToken("http://localhost:8085", "openshell-cli", "uid-1", "alice", "", []string{"openshell-user"}, 8*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	code := generateCode()
	s.codesMu.Lock()
	s.codes[code] = &pendingCode{jwt: jwt, expiresAt: time.Now().Add(60 * time.Second)}
	s.codesMu.Unlock()

	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}}
	req := httptest.NewRequest("POST", "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["access_token"] == nil || resp["access_token"] == "" {
		t.Error("access_token is empty")
	}
	if resp["token_type"] != "Bearer" {
		t.Errorf("token_type = %v, want Bearer", resp["token_type"])
	}
}

func TestSweepExpiredCodes(t *testing.T) {
	s := testServer(t)

	s.codesMu.Lock()
	s.codes["expired"] = &pendingCode{expiresAt: time.Now().Add(-1 * time.Minute)}
	s.codes["valid"] = &pendingCode{expiresAt: time.Now().Add(5 * time.Minute)}
	s.codesMu.Unlock()

	// Trigger a manual sweep
	now := time.Now()
	s.codesMu.Lock()
	for k, v := range s.codes {
		if now.After(v.expiresAt) {
			delete(s.codes, k)
		}
	}
	s.codesMu.Unlock()

	s.codesMu.Lock()
	defer s.codesMu.Unlock()
	if _, ok := s.codes["expired"]; ok {
		t.Error("expired code should have been swept")
	}
	if _, ok := s.codes["valid"]; !ok {
		t.Error("valid code should still exist")
	}
}

func TestAuthorizeRejectsInvalidRedirectURI(t *testing.T) {
	s := testServer(t)
	handler := s.Handler()

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/authorize?redirect_uri=https://evil.com/steal&state=test", nil))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid redirect_uri", w.Code)
	}
}
