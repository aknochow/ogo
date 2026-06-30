package authbridge

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

const algRS256 = "RS256"

func TestNewJWTSigner(t *testing.T) {
	signer, err := NewJWTSigner()
	if err != nil {
		t.Fatalf("NewJWTSigner() error: %v", err)
	}
	if signer.kid == "" {
		t.Error("kid should not be empty")
	}
	if signer.privateKey == nil {
		t.Error("privateKey should not be nil")
	}
}

func TestMintToken(t *testing.T) {
	signer, err := NewJWTSigner()
	if err != nil {
		t.Fatalf("NewJWTSigner() error: %v", err)
	}

	token, err := signer.MintToken(
		"http://localhost:8085",
		"openshell-cli",
		"user-123",
		"testuser",
		"test@example.com",
		[]string{"openshell-user"},
		8*time.Hour,
	)
	if err != nil {
		t.Fatalf("MintToken() error: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3", len(parts))
	}

	headerJSON, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var header map[string]string
	_ = json.Unmarshal(headerJSON, &header)
	if header["alg"] != algRS256 {
		t.Errorf("alg = %q, want %s", header["alg"], algRS256)
	}
	if header["kid"] != signer.kid {
		t.Errorf("kid = %q, want %q", header["kid"], signer.kid)
	}

	claimsJSON, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims Claims
	_ = json.Unmarshal(claimsJSON, &claims)
	if claims.Issuer != "http://localhost:8085" {
		t.Errorf("iss = %q, want http://localhost:8085", claims.Issuer)
	}
	if claims.Subject != "user-123" {
		t.Errorf("sub = %q, want user-123", claims.Subject)
	}
	if claims.PreferredUsername != "testuser" {
		t.Errorf("preferred_username = %q, want testuser", claims.PreferredUsername)
	}

	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&signer.privateKey.PublicKey, crypto.SHA256, hash[:], sig); err != nil {
		t.Errorf("signature verification failed: %v", err)
	}
}

func TestMintTokenExpiry(t *testing.T) {
	signer, _ := NewJWTSigner()
	token, _ := signer.MintToken("iss", "aud", "sub", "user", "", nil, 1*time.Hour)

	parts := strings.Split(token, ".")
	claimsJSON, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims Claims
	_ = json.Unmarshal(claimsJSON, &claims)

	now := time.Now().Unix()
	if claims.ExpiresAt < now || claims.ExpiresAt > now+3601 {
		t.Errorf("exp = %d, want within 1 hour of %d", claims.ExpiresAt, now)
	}
}

func TestJWKS(t *testing.T) {
	signer, _ := NewJWTSigner()
	jwks := signer.JWKS()

	if len(jwks.Keys) != 1 {
		t.Fatalf("JWKS has %d keys, want 1", len(jwks.Keys))
	}
	key := jwks.Keys[0]
	if key.Kty != "RSA" {
		t.Errorf("kty = %q, want RSA", key.Kty)
	}
	if key.Alg != algRS256 {
		t.Errorf("alg = %q, want %s", key.Alg, algRS256)
	}
	if key.Kid != signer.kid {
		t.Errorf("kid = %q, want %q", key.Kid, signer.kid)
	}

	nBytes, _ := base64.RawURLEncoding.DecodeString(key.N)
	eBytes, _ := base64.RawURLEncoding.DecodeString(key.E)
	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)
	if n.Cmp(signer.privateKey.N) != 0 {
		t.Error("JWKS N does not match public key")
	}
	if int(e.Int64()) != signer.privateKey.E {
		t.Error("JWKS E does not match public key")
	}
}

func TestDifferentSignersProduceDifferentKids(t *testing.T) {
	s1, _ := NewJWTSigner()
	s2, _ := NewJWTSigner()
	if s1.kid == s2.kid {
		t.Error("two signers should have different kids")
	}
}
