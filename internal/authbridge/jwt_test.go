package authbridge

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewJWTSigner(t *testing.T) {
	signer, err := NewJWTSigner()
	if err != nil {
		t.Fatalf("NewJWTSigner() error: %v", err)
	}
	if signer.kid == "" {
		t.Error("kid should not be empty")
	}
	if len(signer.publicKey) != ed25519.PublicKeySize {
		t.Errorf("public key size = %d, want %d", len(signer.publicKey), ed25519.PublicKeySize)
	}
	if len(signer.privateKey) != ed25519.PrivateKeySize {
		t.Errorf("private key size = %d, want %d", len(signer.privateKey), ed25519.PrivateKeySize)
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

	// Verify header
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var header map[string]string
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if header["alg"] != "EdDSA" {
		t.Errorf("alg = %q, want EdDSA", header["alg"])
	}
	if header["typ"] != "JWT" {
		t.Errorf("typ = %q, want JWT", header["typ"])
	}
	if header["kid"] != signer.kid {
		t.Errorf("kid = %q, want %q", header["kid"], signer.kid)
	}

	// Verify claims
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims Claims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	if claims.Issuer != "http://localhost:8085" {
		t.Errorf("iss = %q, want http://localhost:8085", claims.Issuer)
	}
	if claims.Audience != "openshell-cli" {
		t.Errorf("aud = %q, want openshell-cli", claims.Audience)
	}
	if claims.Subject != "user-123" {
		t.Errorf("sub = %q, want user-123", claims.Subject)
	}
	if claims.PreferredUsername != "testuser" {
		t.Errorf("preferred_username = %q, want testuser", claims.PreferredUsername)
	}
	if claims.Email != "test@example.com" {
		t.Errorf("email = %q, want test@example.com", claims.Email)
	}
	if len(claims.RealmAccess.Roles) != 1 || claims.RealmAccess.Roles[0] != "openshell-user" {
		t.Errorf("roles = %v, want [openshell-user]", claims.RealmAccess.Roles)
	}

	// Verify Ed25519 signature
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	signingInput := parts[0] + "." + parts[1]
	if !ed25519.Verify(signer.publicKey, []byte(signingInput), sig) {
		t.Error("signature verification failed")
	}
}

func TestMintTokenExpiry(t *testing.T) {
	signer, _ := NewJWTSigner()
	token, _ := signer.MintToken("iss", "aud", "sub", "user", "", nil, 1*time.Hour)

	parts := strings.Split(token, ".")
	claimsJSON, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims Claims
	json.Unmarshal(claimsJSON, &claims)

	now := time.Now().Unix()
	if claims.ExpiresAt < now || claims.ExpiresAt > now+3601 {
		t.Errorf("exp = %d, want within 1 hour of %d", claims.ExpiresAt, now)
	}
	if claims.IssuedAt < now-1 || claims.IssuedAt > now+1 {
		t.Errorf("iat = %d, want ~%d", claims.IssuedAt, now)
	}
}

func TestJWKS(t *testing.T) {
	signer, _ := NewJWTSigner()
	jwks := signer.JWKS()

	if len(jwks.Keys) != 1 {
		t.Fatalf("JWKS has %d keys, want 1", len(jwks.Keys))
	}
	key := jwks.Keys[0]
	if key.Kty != "OKP" {
		t.Errorf("kty = %q, want OKP", key.Kty)
	}
	if key.Crv != "Ed25519" {
		t.Errorf("crv = %q, want Ed25519", key.Crv)
	}
	if key.Alg != "EdDSA" {
		t.Errorf("alg = %q, want EdDSA", key.Alg)
	}
	if key.Use != "sig" {
		t.Errorf("use = %q, want sig", key.Use)
	}
	if key.Kid != signer.kid {
		t.Errorf("kid = %q, want %q", key.Kid, signer.kid)
	}

	// Verify the X value decodes to the public key
	xBytes, err := base64.RawURLEncoding.DecodeString(key.X)
	if err != nil {
		t.Fatalf("decode X: %v", err)
	}
	if !signer.publicKey.Equal(ed25519.PublicKey(xBytes)) {
		t.Error("JWKS X does not match public key")
	}
}

func TestDifferentSignersProduceDifferentKids(t *testing.T) {
	s1, _ := NewJWTSigner()
	s2, _ := NewJWTSigner()
	if s1.kid == s2.kid {
		t.Error("two signers should have different kids")
	}
}
