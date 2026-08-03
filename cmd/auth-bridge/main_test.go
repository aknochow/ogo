/*
Copyright 2026.

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

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/aknochow/ogo/internal/pki"
)

func TestDynamicTLSConfigCachesCertificate(t *testing.T) {
	certFile, keyFile := tlsFiles(t)
	config := dynamicTLSConfig(certFile, keyFile)
	first, err := config.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeTLSBundle(t, certFile, keyFile)
	second, err := config.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatal("certificate was reloaded before the cache expired")
	}
}

func TestDynamicTLSConfigReloadsExpiredCertificate(t *testing.T) {
	certFile, keyFile := tlsFiles(t)
	config := dynamicTLSConfigWithTTL(certFile, keyFile, 0)
	first, err := config.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeTLSBundle(t, certFile, keyFile)
	second, err := config.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatal("certificate was not reloaded after the cache expired")
	}
}

func TestDynamicTLSConfigKeepsLastValidCertificate(t *testing.T) {
	certFile, keyFile := tlsFiles(t)
	config := dynamicTLSConfigWithTTL(certFile, keyFile, 0)
	first, err := config.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, []byte("invalid certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	fallback, err := config.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Certificate[0], fallback.Certificate[0]) {
		t.Fatal("last valid certificate was not retained during a broken rotation")
	}
	writeTLSBundle(t, certFile, keyFile)
	reloaded, err := config.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.Certificate[0], reloaded.Certificate[0]) {
		t.Fatal("certificate was not reloaded after the files recovered")
	}
}

func TestValidateTLSFiles(t *testing.T) {
	validCert, validKey := tlsFiles(t)
	invalidCert := filepath.Join(t.TempDir(), "tls.crt")
	invalidKey := filepath.Join(filepath.Dir(invalidCert), "tls.key")
	if err := os.WriteFile(invalidCert, []byte("invalid certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invalidKey, []byte("invalid key"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name    string
		cert    string
		key     string
		wantErr bool
	}{
		{name: "disabled"},
		{name: "configured", cert: validCert, key: validKey},
		{name: "malformed pair", cert: invalidCert, key: invalidKey, wantErr: true},
		{name: "missing files", cert: "/missing/tls.crt", key: "/missing/tls.key", wantErr: true},
		{name: "missing key", cert: validCert, wantErr: true},
		{name: "missing certificate", key: validKey, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTLSFiles(tt.cert, tt.key)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateTLSFiles() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func tlsFiles(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	writeTLSBundle(t, certFile, keyFile)
	return certFile, keyFile
}

func writeTLSBundle(t *testing.T, certFile, keyFile string) {
	t.Helper()
	bundle, err := pki.GeneratePKI([]string{"localhost"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, bundle.ServerCert, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, bundle.ServerKey, 0o600); err != nil {
		t.Fatal(err)
	}
}
