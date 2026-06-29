package authbridge

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"time"
)

type JWTSigner struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	kid        string
}

func NewJWTSigner() (*JWTSigner, error) {
	signingKeyPath := os.Getenv("AUTH_BRIDGE_SIGNING_KEY")
	publicKeyPath := os.Getenv("AUTH_BRIDGE_PUBLIC_KEY")
	kidPath := os.Getenv("AUTH_BRIDGE_KID")

	if signingKeyPath != "" && publicKeyPath != "" {
		return loadJWTSigner(signingKeyPath, publicKeyPath, kidPath)
	}
	return generateJWTSigner()
}

func generateJWTSigner() (*JWTSigner, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating Ed25519 key: %w", err)
	}
	hash := sha256.Sum256(pub)
	kid := hex.EncodeToString(hash[:8])
	return &JWTSigner{privateKey: priv, publicKey: pub, kid: kid}, nil
}

func loadJWTSigner(signingKeyPath, publicKeyPath, kidPath string) (*JWTSigner, error) {
	signingPEM, err := os.ReadFile(signingKeyPath)
	if err != nil {
		return nil, fmt.Errorf("reading signing key from %s: %w", signingKeyPath, err)
	}
	block, _ := pem.Decode(signingPEM)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in %s", signingKeyPath)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing signing key: %w", err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("signing key is not Ed25519")
	}

	publicPEM, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return nil, fmt.Errorf("reading public key from %s: %w", publicKeyPath, err)
	}
	pubBlock, _ := pem.Decode(publicPEM)
	if pubBlock == nil {
		return nil, fmt.Errorf("no PEM block found in %s", publicKeyPath)
	}
	pubKey, err := x509.ParsePKIXPublicKey(pubBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing public key: %w", err)
	}
	pub, ok := pubKey.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is not Ed25519")
	}

	hash := sha256.Sum256(pub)
	kid := hex.EncodeToString(hash[:8])
	if kidPath != "" {
		kidBytes, err := os.ReadFile(kidPath)
		if err == nil {
			kid = strings.TrimSpace(string(kidBytes))
		}
	}

	return &JWTSigner{privateKey: priv, publicKey: pub, kid: kid}, nil
}

type Claims struct {
	Issuer            string      `json:"iss"`
	Subject           string      `json:"sub"`
	Audience          string      `json:"aud"`
	ExpiresAt         int64       `json:"exp"`
	IssuedAt          int64       `json:"iat"`
	PreferredUsername string      `json:"preferred_username"`
	Email             string      `json:"email,omitempty"`
	RealmAccess       RealmAccess `json:"realm_access"`
}

type RealmAccess struct {
	Roles []string `json:"roles"`
}

func (s *JWTSigner) MintToken(issuer, audience, subject, username, email string, roles []string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		Issuer:            issuer,
		Subject:           subject,
		Audience:          audience,
		ExpiresAt:         now.Add(ttl).Unix(),
		IssuedAt:          now.Unix(),
		PreferredUsername: username,
		Email:             email,
		RealmAccess:       RealmAccess{Roles: roles},
	}

	header := map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": s.kid}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerB64 + "." + claimsB64

	signature := ed25519.Sign(s.privateKey, []byte(signingInput))
	sigB64 := base64.RawURLEncoding.EncodeToString(signature)

	return signingInput + "." + sigB64, nil
}

type JWKSResponse struct {
	Keys []JWK `json:"keys"`
}

type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
}

func (s *JWTSigner) JWKS() JWKSResponse {
	return JWKSResponse{
		Keys: []JWK{{
			Kty: "OKP",
			Crv: "Ed25519",
			X:   base64.RawURLEncoding.EncodeToString(s.publicKey),
			Kid: s.kid,
			Use: "sig",
			Alg: "EdDSA",
		}},
	}
}
