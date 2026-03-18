package server

import (
	"fmt"
	"os"
)

// realmEnvVars is the list of environment variables checked for realm
// auto-detection, in priority order.
var realmEnvVars = []string{
	"MPP_REALM",
	"FLY_APP_NAME",
	"HEROKU_APP_NAME",
	"RAILWAY_PUBLIC_DOMAIN",
	"RENDER_EXTERNAL_HOSTNAME",
	"VERCEL_URL",
	"WEBSITE_HOSTNAME",
	"HOST",
	"HOSTNAME",
}

// DetectRealm auto-detects the server realm from environment variables.
// It checks MPP_REALM, FLY_APP_NAME, HEROKU_APP_NAME, RAILWAY_PUBLIC_DOMAIN,
// RENDER_EXTERNAL_HOSTNAME, VERCEL_URL, WEBSITE_HOSTNAME, HOST, and HOSTNAME
// in that order. Returns "MPP Payment" if none are set.
func DetectRealm() string {
	for _, envVar := range realmEnvVars {
		if v := os.Getenv(envVar); v != "" {
			return v
		}
	}
	return "MPP Payment"
}

// DetectSecretKey reads the MPP_SECRET_KEY environment variable.
// Returns an error if it is not set.
func DetectSecretKey() (string, error) {
	key := os.Getenv("MPP_SECRET_KEY")
	if key == "" {
		return "", fmt.Errorf("MPP_SECRET_KEY environment variable is not set")
	}
	return key, nil
}
