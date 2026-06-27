package pki

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

// Bundle holds the generated PKI materials.
type Bundle struct {
	CACert     []byte
	CAKey      []byte
	ServerCert []byte
	ServerKey  []byte
	ClientCert []byte
	ClientKey  []byte
}

// JWTKeys holds the generated JWT signing materials.
type JWTKeys struct {
	SigningKey []byte
	PublicKey  []byte
	KID        string
}

// GeneratePKI creates a self-signed CA, server certificate, and client certificate.
// SANs are applied to the server certificate.
func GeneratePKI(sans []string) (*Bundle, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating CA key: %w", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject: pkix.Name{
			CommonName: "openshell-ca",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("creating CA certificate: %w", err)
	}
	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		return nil, fmt.Errorf("parsing CA certificate: %w", err)
	}

	serverCert, serverKey, err := issueCert(caCert, caKey, "openshell-server", sans, x509.ExtKeyUsageServerAuth)
	if err != nil {
		return nil, fmt.Errorf("issuing server certificate: %w", err)
	}

	clientCert, clientKey, err := issueCert(caCert, caKey, "openshell-client", nil, x509.ExtKeyUsageClientAuth)
	if err != nil {
		return nil, fmt.Errorf("issuing client certificate: %w", err)
	}

	caKeyBytes, err := marshalECPrivateKey(caKey)
	if err != nil {
		return nil, err
	}

	caCertPEM := pemEncodeCert(caCertDER)

	// Server cert includes the full chain (leaf + CA) for strict TLS clients
	serverCertChain := append(serverCert, caCertPEM...)

	return &Bundle{
		CACert:     caCertPEM,
		CAKey:      caKeyBytes,
		ServerCert: serverCertChain,
		ServerKey:  serverKey,
		ClientCert: clientCert,
		ClientKey:  clientKey,
	}, nil
}

// GenerateJWTKeys creates an Ed25519 keypair for JWT signing.
func GenerateJWTKeys() (*JWTKeys, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating Ed25519 key: %w", err)
	}

	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshaling private key: %w", err)
	}

	pubBytes, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("marshaling public key: %w", err)
	}

	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privBytes})
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes})

	hash := sha256.Sum256(pubBytes)
	kid := hex.EncodeToString(hash[:16])

	return &JWTKeys{
		SigningKey: privPEM,
		PublicKey:  pubPEM,
		KID:        kid,
	}, nil
}

// HashSANs computes a deterministic hash of a SAN list for drift detection.
func HashSANs(sans []string) string {
	h := sha256.New()
	for _, s := range sans {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func issueCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, sans []string, usage x509.ExtKeyUsage) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject: pkix.Name{
			CommonName: cn,
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{usage},
		BasicConstraintsValid: true,
	}

	for _, san := range sans {
		if ip := net.ParseIP(san); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, san)
		}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}

	keyBytes, err := marshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}

	return pemEncodeCert(certDER), keyBytes, nil
}

func marshalECPrivateKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshaling EC private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

func pemEncodeCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func newSerial() *big.Int {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return serial
}
