package authbridge

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Issuer         string // Internal issuer (http://localhost:8085) — used by gateway OIDC discovery
	ExternalIssuer string // External issuer (https://openshell-auth.apps...) — used in JWTs and CLI redirects
	Audience       string
	ListenAddr     string
	OpenShiftOAuth string
	ClientID       string
	ClientSecret   string
	UserGroup      string // Required group for SSO access (empty = reject all SSO users)
	AdminGroup     string
	TokenTTL       time.Duration
}

type Server struct {
	config  Config
	signer  *JWTSigner
	osc     *OpenShiftClient
	codes   map[string]*pendingCode
	codesMu sync.Mutex
	done    chan struct{}
}

type pendingCode struct {
	jwt         string
	expiresAt   time.Time
	redirectURI string
}

func NewServer(config Config) (*Server, error) {
	signer, err := NewJWTSigner()
	if err != nil {
		return nil, err
	}
	srv := &Server{
		config: config,
		signer: signer,
		osc:    NewOpenShiftClient(config.OpenShiftOAuth, config.ClientID, config.ClientSecret),
		codes:  make(map[string]*pendingCode),
		done:   make(chan struct{}),
	}
	go srv.sweepExpiredCodes()
	return srv, nil
}

func (s *Server) Close() {
	close(s.done)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", s.handleDiscovery)
	mux.HandleFunc("/jwks", s.handleJWKS)
	mux.HandleFunc("/authorize", s.handleAuthorize)
	mux.HandleFunc("/callback", s.handleCallback)
	mux.HandleFunc("/token", s.handleToken)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	return securityHeaders(mux)
}

func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	// Serve different discovery based on caller:
	// - Gateway (localhost) gets internal URLs → issuer and jwks_uri on localhost
	// - CLI (external) gets external URLs → all endpoints on external URL
	base := s.config.ExternalIssuer
	issuer := s.config.ExternalIssuer
	host := r.Host
	if strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1") {
		base = s.config.Issuer
		issuer = s.config.Issuer
	}
	discovery := map[string]interface{}{
		"issuer":                                issuer,
		"authorization_endpoint":                s.config.ExternalIssuer + "/authorize",
		"token_endpoint":                        s.config.ExternalIssuer + "/token",
		"jwks_uri":                              base + "/jwks",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"subject_types_supported":               []string{"public"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic"},
		"scopes_supported":                      []string{"openid", "profile", "email"},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(discovery)
}

func (s *Server) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.signer.JWKS())
}

func (s *Server) isAllowedRedirectURI(uri string) bool {
	if uri == "" {
		return false
	}
	u, err := url.Parse(uri)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Host == "localhost" || u.Host == "127.0.0.1" || strings.HasPrefix(u.Host, "localhost:") || strings.HasPrefix(u.Host, "127.0.0.1:") {
		return true
	}
	if s.config.ExternalIssuer != "" {
		allowed, err := url.Parse(s.config.ExternalIssuer)
		if err == nil && u.Host == allowed.Host && strings.HasPrefix(u.Path, "/callback") {
			return true
		}
	}
	return false
}

const maxPendingCodes = 10000

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	clientRedirectURI := r.URL.Query().Get("redirect_uri")
	state := r.URL.Query().Get("state")

	if !s.isAllowedRedirectURI(clientRedirectURI) {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}

	bridgeState := generateCode()

	s.codesMu.Lock()
	if len(s.codes) >= maxPendingCodes {
		s.codesMu.Unlock()
		http.Error(w, "too many pending requests", http.StatusServiceUnavailable)
		return
	}
	s.codes[bridgeState] = &pendingCode{
		redirectURI: clientRedirectURI,
		expiresAt:   time.Now().Add(5 * time.Minute),
	}
	s.codesMu.Unlock()

	callbackURL := s.config.ExternalIssuer + "/callback"

	params := url.Values{
		"response_type": {"code"},
		"client_id":     {s.config.ClientID},
		"redirect_uri":  {callbackURL},
		"state":         {bridgeState + ":" + state},
		"scope":         {"user:info"},
	}

	http.Redirect(w, r, s.osc.AuthorizationURL()+"?"+params.Encode(), http.StatusFound)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	combinedState := r.URL.Query().Get("state")

	parts := splitState(combinedState)
	bridgeState := parts[0]
	clientState := parts[1]

	s.codesMu.Lock()
	pending, ok := s.codes[bridgeState]
	if ok {
		delete(s.codes, bridgeState)
	}
	s.codesMu.Unlock()

	if !ok || time.Now().After(pending.expiresAt) {
		http.Error(w, "invalid or expired state", http.StatusBadRequest)
		return
	}

	callbackURL := s.config.ExternalIssuer + "/callback"
	tokenResp, err := s.osc.ExchangeCode(r.Context(), code, callbackURL)
	if err != nil {
		log.Printf("token exchange failed: %v", err)
		http.Error(w, "authentication failed", http.StatusInternalServerError)
		return
	}

	userInfo, err := s.osc.GetUserInfo(r.Context(), tokenResp.AccessToken)
	if err != nil {
		log.Printf("user info failed: %v", err)
		http.Error(w, "failed to get user info", http.StatusInternalServerError)
		return
	}

	if !s.isAuthorized(userInfo.Name, userInfo.Groups) {
		log.Printf("user %s not in required group %q", userInfo.Name, s.config.UserGroup)
		http.Error(w, fmt.Sprintf("access denied: user %q is not a member of the required OpenShift group %q", userInfo.Name, s.config.UserGroup), http.StatusForbidden)
		return
	}

	roles := s.mapRoles(userInfo.Groups)

	jwt, err := s.signer.MintToken(
		s.config.Issuer, // Use internal issuer — matches gateway's OIDC config
		s.config.Audience,
		userInfo.UID,
		userInfo.Name,
		"",
		roles,
		s.config.TokenTTL,
	)
	if err != nil {
		log.Printf("JWT minting failed: %v", err)
		http.Error(w, "token generation failed", http.StatusInternalServerError)
		return
	}

	bridgeCode := generateCode()
	s.codesMu.Lock()
	if len(s.codes) >= maxPendingCodes {
		s.codesMu.Unlock()
		http.Error(w, "too many pending requests", http.StatusServiceUnavailable)
		return
	}
	s.codes[bridgeCode] = &pendingCode{
		jwt:       jwt,
		expiresAt: time.Now().Add(60 * time.Second),
	}
	s.codesMu.Unlock()

	redirectURL := pending.redirectURI
	if redirectURL == "" {
		http.Error(w, "missing redirect_uri", http.StatusBadRequest)
		return
	}

	u, err := url.Parse(redirectURL)
	if err != nil {
		http.Error(w, "invalid redirect URI", http.StatusBadRequest)
		return
	}
	q := u.Query()
	q.Set("code", bridgeCode)
	q.Set("state", clientState)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	grantType := r.FormValue("grant_type")
	if grantType != "authorization_code" {
		http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
		return
	}

	code := r.FormValue("code")

	s.codesMu.Lock()
	pending, ok := s.codes[code]
	if ok {
		delete(s.codes, code)
	}
	s.codesMu.Unlock()

	if !ok || time.Now().After(pending.expiresAt) || pending.jwt == "" {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"access_token": pending.jwt,
		"token_type":   "Bearer",
		"expires_in":   int(s.config.TokenTTL.Seconds()),
	})
}

func (s *Server) isAuthorized(username string, groups []string) bool {
	if username == "kube:admin" {
		return true
	}
	if s.config.UserGroup == "" {
		return false
	}
	for _, g := range groups {
		if g == s.config.UserGroup {
			return true
		}
	}
	return false
}

func (s *Server) mapRoles(groups []string) []string {
	roles := []string{"openshell-user"}
	if s.config.AdminGroup != "" {
		for _, g := range groups {
			if g == s.config.AdminGroup {
				roles = append(roles, "openshell-admin")
				break
			}
		}
	}
	return roles
}

func (s *Server) sweepExpiredCodes() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			now := time.Now()
			s.codesMu.Lock()
			for k, v := range s.codes {
				if now.After(v.expiresAt) {
					delete(s.codes, k)
				}
			}
			s.codesMu.Unlock()
		}
	}
}

func generateCode() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

func splitState(combined string) [2]string {
	for i, c := range combined {
		if c == ':' {
			return [2]string{combined[:i], combined[i+1:]}
		}
	}
	return [2]string{combined, ""}
}
