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

func TestDynamicTLSConfigReloadsCertificate(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	writeBundle := func() {
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

	writeBundle()
	config := dynamicTLSConfig(certFile, keyFile)
	first, err := config.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeBundle()
	second, err := config.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatal("certificate was not reloaded after file rotation")
	}
}

func TestValidateTLSFiles(t *testing.T) {
	for _, tt := range []struct {
		name    string
		cert    string
		key     string
		wantErr bool
	}{
		{name: "disabled"},
		{name: "configured", cert: "/tls/tls.crt", key: "/tls/tls.key"},
		{name: "missing key", cert: "/tls/tls.crt", wantErr: true},
		{name: "missing certificate", key: "/tls/tls.key", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTLSFiles(tt.cert, tt.key)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateTLSFiles() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
