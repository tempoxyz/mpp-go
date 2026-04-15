package server

import (
	"strings"
	"testing"
)

func TestDetectRealmPrecedenceAndDefault(t *testing.T) {
	for _, envVar := range realmEnvVars {
		t.Setenv(envVar, "")
	}

	if got := DetectRealm(); got != "MPP Payment" {
		t.Fatalf("DetectRealm() = %q, want %q", got, "MPP Payment")
	}

	t.Setenv("HOSTNAME", "host.example.com")
	t.Setenv("VERCEL_URL", "vercel.example.com")
	t.Setenv("MPP_REALM", "api.example.com")

	if got := DetectRealm(); got != "api.example.com" {
		t.Fatalf("DetectRealm() = %q, want %q", got, "api.example.com")
	}
}

func TestDetectSecretKey(t *testing.T) {
	t.Setenv("MPP_SECRET_KEY", "")
	if _, err := DetectSecretKey(); err == nil || !strings.Contains(err.Error(), "MPP_SECRET_KEY environment variable is not set") {
		t.Fatalf("DetectSecretKey() error = %v, want missing env error", err)
	}

	t.Setenv("MPP_SECRET_KEY", "secret-key")
	key, err := DetectSecretKey()
	if err != nil {
		t.Fatalf("DetectSecretKey() error = %v", err)
	}
	if key != "secret-key" {
		t.Fatalf("DetectSecretKey() = %q, want %q", key, "secret-key")
	}
}
