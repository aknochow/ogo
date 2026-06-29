package authbridge

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

type JWTSigner struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	kid        string
}

func NewJWTSigner() (*JWTSigner, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating Ed25519 key: %w", err)
	}
	hash := sha256.Sum256(pub)
	kid := hex.EncodeToString(hash[:8])
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
