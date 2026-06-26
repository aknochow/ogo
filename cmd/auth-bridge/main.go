package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/aknochow/ogo/internal/authbridge"
)

func main() {
	config := authbridge.Config{
		Issuer:         envOrDefault("AUTH_BRIDGE_ISSUER", "http://localhost:8085"),
		Audience:       envOrDefault("AUTH_BRIDGE_AUDIENCE", "openshell-cli"),
		ListenAddr:     envOrDefault("AUTH_BRIDGE_LISTEN", ":8085"),
		OpenShiftOAuth: envOrDefault("AUTH_BRIDGE_OPENSHIFT_ISSUER", "https://oauth-openshift.apps.example.com"),
		ClientID:       envOrDefault("AUTH_BRIDGE_CLIENT_ID", "openshell"),
		ClientSecret:   os.Getenv("AUTH_BRIDGE_CLIENT_SECRET"),
		AdminGroup:     os.Getenv("AUTH_BRIDGE_ADMIN_GROUP"),
		TokenTTL:       1 * time.Hour,
	}

	server, err := authbridge.NewServer(config)
	if err != nil {
		log.Fatalf("Failed to create auth-bridge server: %v", err)
	}

	fmt.Printf("auth-bridge starting\n  issuer: %s\n  openshift: %s\n  listen: %s\n",
		config.Issuer, config.OpenShiftOAuth, config.ListenAddr)

	if err := http.ListenAndServe(config.ListenAddr, server.Handler()); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
