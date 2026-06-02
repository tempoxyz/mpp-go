package server

import (
	"github.com/stretchr/testify/assert"
	"strings"
	"testing"
)

func TestDetectRealmPrecedenceAndDefault(t *testing.T) {
	for _, envVar := range realmEnvVars {
		t.Setenv(envVar, "")
	}

	if got := DetectRealm(); got != "MPP Payment" {
		assert.Failf(t, "", "DetectRealm() = %q, want %q", got, "MPP Payment")
		return
	}

	t.Setenv("HOSTNAME", "host.example.com")
	t.Setenv("VERCEL_URL", "vercel.example.com")
	t.Setenv("MPP_REALM", "api.example.com")

	if got := DetectRealm(); got != "api.example.com" {
		assert.Failf(t, "", "DetectRealm() = %q, want %q", got, "api.example.com")
		return
	}
}

func TestDetectSecretKey(t *testing.T) {
	t.Setenv("MPP_SECRET_KEY", "")
	if _, err := DetectSecretKey(); err == nil || !strings.Contains(err.Error(), "MPP_SECRET_KEY environment variable is not set") {
		assert.Failf(t, "", "DetectSecretKey() error = %v, want missing env error", err)
		return
	}

	t.Setenv("MPP_SECRET_KEY", "secret-key")
	key, err := DetectSecretKey()
	if !assert.NoErrorf(t, err,
		"DetectSecretKey() error = %v", err) {
		return
	}
	if !assert.Equalf(t, "secret-key", key,
		"DetectSecretKey() = %q, want %q", key, "secret-key") {
		return
	}

}
