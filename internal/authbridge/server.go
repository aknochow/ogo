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
	AdminGroup     string
	TokenTTL       time.Duration
}

type Server struct {
	config  Config
	signer  *JWTSigner
	osc     *OpenShiftClient
	codes   map[string]*pendingCode
	codesMu sync.Mutex
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
	return &Server{
		config: config,
		signer: signer,
		osc:    NewOpenShiftClient(config.OpenShiftOAuth, config.ClientID, config.ClientSecret),
		codes:  make(map[string]*pendingCode),
	}, nil
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
	return mux
}

func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	// Serve different discovery based on caller:
	// - Gateway (localhost) gets internal URLs → issuer and jwks_uri on localhost
	// - CLI (external) gets external URLs → all endpoints on external URL
	base := s.config.ExternalIssuer
	issuer := s.config.ExternalIssuer
	host := r.Host
	if host == "localhost:8085" || host == "127.0.0.1:8085" {
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
		"id_token_signing_alg_values_supported": []string{"EdDSA"},
		"scopes_supported":                      []string{"openid", "profile", "email"},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(discovery)
}

func (s *Server) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.signer.JWKS())
}

func (s *Server) isAllowedRedirectURI(uri string) bool {
	if uri == "" {
		return true
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
		if err == nil && u.Host == allowed.Host {
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
	tokenResp, err := s.osc.ExchangeCode(code, callbackURL)
	if err != nil {
		log.Printf("token exchange failed: %v", err)
		http.Error(w, "authentication failed", http.StatusInternalServerError)
		return
	}

	userInfo, err := s.osc.GetUserInfo(tokenResp.AccessToken)
	if err != nil {
		log.Printf("user info failed: %v", err)
		http.Error(w, "failed to get user info", http.StatusInternalServerError)
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
	json.NewEncoder(w).Encode(map[string]interface{}{
		"access_token": pending.jwt,
		"token_type":   "Bearer",
		"expires_in":   int(s.config.TokenTTL.Seconds()),
	})
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
