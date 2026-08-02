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

package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/aknochow/ogo/internal/authbridge"
)

func main() {
	internalIssuer := envOrDefault("AUTH_BRIDGE_ISSUER", "http://localhost:8085")
	externalIssuer := envOrDefault("AUTH_BRIDGE_EXTERNAL_ISSUER", internalIssuer)
	config := authbridge.Config{
		Issuer:         internalIssuer,
		ExternalIssuer: externalIssuer,
		Audience:       envOrDefault("AUTH_BRIDGE_AUDIENCE", "openshell-cli"),
		ListenAddr:     envOrDefault("AUTH_BRIDGE_LISTEN", ":8085"),
		OpenShiftOAuth: envOrDefault("AUTH_BRIDGE_OPENSHIFT_ISSUER", "https://oauth-openshift.apps.example.com"),
		ClientID:       envOrDefault("AUTH_BRIDGE_CLIENT_ID", "openshell"),
		ClientSecret:   os.Getenv("AUTH_BRIDGE_CLIENT_SECRET"),
		UserGroup:      os.Getenv("AUTH_BRIDGE_USER_GROUP"),
		AdminGroup:     os.Getenv("AUTH_BRIDGE_ADMIN_GROUP"),
		TokenTTL:       parseDuration(os.Getenv("AUTH_BRIDGE_TOKEN_TTL"), 8*time.Hour),
	}

	if config.ClientSecret == "" {
		log.Fatal("AUTH_BRIDGE_CLIENT_SECRET is required")
	}

	server, err := authbridge.NewServer(config)
	if err != nil {
		log.Fatalf("Failed to create auth-bridge server: %v", err)
	}
	defer server.Close()

	fmt.Printf("auth-bridge starting\n  issuer: %s\n  openshift: %s\n  listen: %s\n",
		config.Issuer, config.OpenShiftOAuth, config.ListenAddr)

	handler := server.Handler()
	servers := []*http.Server{newHTTPServer(config.ListenAddr, handler)}
	tlsCert := os.Getenv("AUTH_BRIDGE_TLS_CERT")
	tlsKey := os.Getenv("AUTH_BRIDGE_TLS_KEY")
	if err := validateTLSFiles(tlsCert, tlsKey); err != nil {
		log.Fatal(err)
	}
	if tlsCert != "" {
		servers = append(servers, newHTTPServer(envOrDefault("AUTH_BRIDGE_TLS_LISTEN", ":8443"), handler))
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	for i, srv := range servers {
		cert, key := "", ""
		if i > 0 {
			cert, key = tlsCert, tlsKey
		}
		go func() {
			if err := listenAndServe(srv, cert, key); err != nil && err != http.ErrServerClosed {
				log.Fatalf("Server failed: %v", err)
			}
		}()
	}

	<-stop
	log.Println("Shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var shutdowns sync.WaitGroup
	for _, srv := range servers {
		shutdowns.Add(1)
		go func() {
			defer shutdowns.Done()
			if err := srv.Shutdown(ctx); err != nil {
				log.Printf("Shutdown error: %v", err)
			}
		}()
	}
	shutdowns.Wait()
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
}

func listenAndServe(server *http.Server, cert, key string) error {
	if cert == "" {
		return server.ListenAndServe()
	}
	server.TLSConfig = dynamicTLSConfig(cert, key)
	return server.ListenAndServeTLS("", "")
}

func dynamicTLSConfig(cert, key string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			pair, err := tls.LoadX509KeyPair(cert, key)
			return &pair, err
		},
	}
}

func validateTLSFiles(cert, key string) error {
	if (cert == "") != (key == "") {
		return fmt.Errorf("AUTH_BRIDGE_TLS_CERT and AUTH_BRIDGE_TLS_KEY must be configured together")
	}
	return nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDuration(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}
