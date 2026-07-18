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
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"
)

const algRS256 = "RS256"

type JWTSigner struct {
	privateKey *rsa.PrivateKey
	kid        string
}

func NewJWTSigner() (*JWTSigner, error) {
	signingKeyPath := os.Getenv("AUTH_BRIDGE_SIGNING_KEY")
	publicKeyPath := os.Getenv("AUTH_BRIDGE_PUBLIC_KEY")
	kidPath := os.Getenv("AUTH_BRIDGE_KID")

	if signingKeyPath != "" && publicKeyPath != "" {
		return loadJWTSigner(signingKeyPath, kidPath)
	}
	return generateJWTSigner()
}

func generateJWTSigner() (*JWTSigner, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generating RSA key: %w", err)
	}
	pubBytes := x509.MarshalPKCS1PublicKey(&key.PublicKey)
	hash := sha256.Sum256(pubBytes)
	kid := hex.EncodeToString(hash[:16])
	return &JWTSigner{privateKey: key, kid: kid}, nil
}

func loadJWTSigner(signingKeyPath, kidPath string) (*JWTSigner, error) {
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
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("signing key is not RSA")
	}

	kid := ""
	if kidPath != "" {
		kidBytes, err := os.ReadFile(kidPath)
		if err == nil {
			kid = strings.TrimSpace(string(kidBytes))
		}
	}
	if kid == "" {
		pubBytes := x509.MarshalPKCS1PublicKey(&rsaKey.PublicKey)
		hash := sha256.Sum256(pubBytes)
		kid = hex.EncodeToString(hash[:16])
	}

	return &JWTSigner{privateKey: rsaKey, kid: kid}, nil
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

	header := map[string]string{"alg": algRS256, "typ": "JWT", "kid": s.kid}

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

	hash := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.privateKey, crypto.SHA256, hash[:])
	if err != nil {
		return "", fmt.Errorf("signing JWT: %w", err)
	}
	sigB64 := base64.RawURLEncoding.EncodeToString(signature)

	return signingInput + "." + sigB64, nil
}

type JWKSResponse struct {
	Keys []JWK `json:"keys"`
}

type JWK struct {
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
}

func (s *JWTSigner) JWKS() JWKSResponse {
	pub := &s.privateKey.PublicKey
	return JWKSResponse{
		Keys: []JWK{{
			Kty: "RSA",
			N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			Kid: s.kid,
			Use: "sig",
			Alg: algRS256,
		}},
	}
}
