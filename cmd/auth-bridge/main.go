package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
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

	srv := &http.Server{
		Addr:         config.ListenAddr,
		Handler:      server.Handler(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
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
