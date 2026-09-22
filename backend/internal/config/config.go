// Package config loads the backend's environment configuration (ADR 0007:
// DATABASE_URL-style env config only; no embedded or file databases).
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config carries everything the backend process needs from its environment.
type Config struct {
	// DatabaseURL is the Postgres connection string (required).
	DatabaseURL string
	// AgentBaseURL is where the agent sidecar listens (required).
	AgentBaseURL string
	// Addr is the backend's listen address (default :8080).
	Addr string
	// DevMode toggles dev behaviors: verification links land in the server
	// log instead of being mailed (default true at pilot stage).
	DevMode bool
	// DevMailSink selects the email sink: "log" (dev) or "smtp" (later).
	DevMailSink string
	// BootstrapAdminEmail / BootstrapAdminPassword provision the first
	// Platform Admin at startup (registration refuses the role). Required.
	BootstrapAdminEmail    string
	BootstrapAdminPassword string
	// BootstrapTOSVersion / BootstrapTOSBody publish the initial Terms of
	// Service version at startup. Required.
	BootstrapTOSVersion string
	BootstrapTOSBody    string
}

// Load reads config from the environment, applying defaults and failing
// loudly on missing required values.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:  os.Getenv("DATABASE_URL"),
		AgentBaseURL: os.Getenv("AGENT_BASE_URL"),
		Addr:         getenvDefault("BACKEND_ADDR", ":8080"),
		DevMailSink:  getenvDefault("MAIL_SINK", "log"),
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required (Postgres, ADR 0007)")
	}
	if cfg.AgentBaseURL == "" {
		return Config{}, fmt.Errorf("AGENT_BASE_URL is required (agent sidecar)")
	}

	devMode, err := parseBoolEnv("DEV_MODE", true)
	if err != nil {
		return Config{}, err
	}
	cfg.DevMode = devMode

	// Bootstrap facts are required with pilot defaults so `make dev` works
	// without extra setup; production deployments override them.
	cfg.BootstrapAdminEmail = getenvDefault("BOOTSTRAP_ADMIN_EMAIL", "admin@thresh.dev")
	cfg.BootstrapAdminPassword = getenvDefault("BOOTSTRAP_ADMIN_PASSWORD", "thresh-admin")
	cfg.BootstrapTOSVersion = getenvDefault("BOOTSTRAP_TOS_VERSION", "1.0")
	cfg.BootstrapTOSBody = getenvDefault("BOOTSTRAP_TOS_BODY",
		`Thresh Terms of Service (pilot v1.0).

By accepting this contract you agree to the pilot's ground rules: Farmer
Organizations keep ownership and control of everything they share; shared
data is anonymized before it leaves the platform; Data Consumers receive
anonymized data only; and Farmer Members are never identifiable. The
platform records which version you accepted and when.`)

	return cfg, nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseBoolEnv(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("failed to parse %s as bool: %w", key, err)
	}
	return b, nil
}
