package pki

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func TestGeneratePKI(t *testing.T) {
	sans := []string{
		"openshell",
		"openshell.openshell.svc",
		"openshell.openshell.svc.cluster.local",
		"localhost",
		"127.0.0.1",
	}

	bundle, err := GeneratePKI(sans)
	if err != nil {
		t.Fatalf("GeneratePKI failed: %v", err)
	}

	// Verify CA cert
	caBlock, _ := pem.Decode(bundle.CACert)
	if caBlock == nil {
		t.Fatal("Failed to decode CA certificate PEM")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("Failed to parse CA certificate: %v", err)
	}
	if !caCert.IsCA {
		t.Error("CA certificate is not marked as CA")
	}
	if caCert.Subject.CommonName != "openshell-ca" {
		t.Errorf("CA CN = %q, want %q", caCert.Subject.CommonName, "openshell-ca")
	}

	// Verify server cert
	serverBlock, _ := pem.Decode(bundle.ServerCert)
	if serverBlock == nil {
		t.Fatal("Failed to decode server certificate PEM")
	}
	serverCert, err := x509.ParseCertificate(serverBlock.Bytes)
	if err != nil {
		t.Fatalf("Failed to parse server certificate: %v", err)
	}
	if serverCert.Subject.CommonName != "openshell-server" {
		t.Errorf("Server CN = %q, want %q", serverCert.Subject.CommonName, "openshell-server")
	}

	expectedDNS := []string{"openshell", "openshell.openshell.svc", "openshell.openshell.svc.cluster.local", "localhost"}
	for _, want := range expectedDNS {
		found := false
		for _, got := range serverCert.DNSNames {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Server cert missing DNS SAN %q, got %v", want, serverCert.DNSNames)
		}
	}

	if len(serverCert.IPAddresses) == 0 {
		t.Error("Server cert has no IP SANs")
	}

	// Verify client cert
	clientBlock, _ := pem.Decode(bundle.ClientCert)
	if clientBlock == nil {
		t.Fatal("Failed to decode client certificate PEM")
	}
	clientCert, err := x509.ParseCertificate(clientBlock.Bytes)
	if err != nil {
		t.Fatalf("Failed to parse client certificate: %v", err)
	}
	if clientCert.Subject.CommonName != "openshell-client" {
		t.Errorf("Client CN = %q, want %q", clientCert.Subject.CommonName, "openshell-client")
	}

	// Verify chain: server and client certs signed by CA
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := serverCert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Errorf("Server cert verification failed: %v", err)
	}
	if _, err := clientCert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("Client cert verification failed: %v", err)
	}

	// Verify private keys are parseable
	serverKeyBlock, _ := pem.Decode(bundle.ServerKey)
	if serverKeyBlock == nil {
		t.Fatal("Failed to decode server key PEM")
	}
	clientKeyBlock, _ := pem.Decode(bundle.ClientKey)
	if clientKeyBlock == nil {
		t.Fatal("Failed to decode client key PEM")
	}
}

func TestGenerateJWTKeys(t *testing.T) {
	keys, err := GenerateJWTKeys()
	if err != nil {
		t.Fatalf("GenerateJWTKeys failed: %v", err)
	}

	if len(keys.SigningKey) == 0 {
		t.Error("Signing key is empty")
	}
	if len(keys.PublicKey) == 0 {
		t.Error("Public key is empty")
	}
	if len(keys.KID) == 0 {
		t.Error("KID is empty")
	}

	sigBlock, _ := pem.Decode(keys.SigningKey)
	if sigBlock == nil || sigBlock.Type != "PRIVATE KEY" {
		t.Errorf("Signing key PEM type = %q, want PRIVATE KEY", sigBlock.Type)
	}

	pubBlock, _ := pem.Decode(keys.PublicKey)
	if pubBlock == nil || pubBlock.Type != "PUBLIC KEY" {
		t.Errorf("Public key PEM type = %q, want PUBLIC KEY", pubBlock.Type)
	}
}

func TestHashSANs(t *testing.T) {
	sans1 := []string{"a", "b", "c"}
	sans2 := []string{"a", "b", "c"}
	sans3 := []string{"a", "b", "d"}

	h1 := HashSANs(sans1)
	h2 := HashSANs(sans2)
	h3 := HashSANs(sans3)

	if h1 != h2 {
		t.Errorf("Same SANs produced different hashes: %q vs %q", h1, h2)
	}
	if h1 == h3 {
		t.Error("Different SANs produced the same hash")
	}
}
