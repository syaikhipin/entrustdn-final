package config_test

import (
	"strings"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/config"
)

// Config is the boundary between deployment (env vars) and the process.
// These tests assert which vars are required, which are optional with
// defaults, and that malformed input fails loudly rather than silently.

func TestLoadRequiresDatabaseURL(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "nothing set",
			env:     map[string]string{},
			wantErr: "DATABASE_URL",
		},
		{
			name: "agent URL set but no database",
			env: map[string]string{
				"AGENT_BASE_URL": "http://localhost:8001",
			},
			wantErr: "DATABASE_URL",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := config.Load()
			if err == nil {
				t.Fatal("Load() = nil, want error naming DATABASE_URL")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/thresh?sslmode=disable")
	t.Setenv("AGENT_BASE_URL", "http://localhost:8001")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want default :8080", cfg.Addr)
	}
	if cfg.DevMailSink != "log" {
		t.Errorf("DevMailSink = %q, want default log", cfg.DevMailSink)
	}
	if !cfg.DevMode {
		t.Errorf("DevMode = false, want default true (dev links in log)")
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@db.example.com:5432/thresh")
	t.Setenv("AGENT_BASE_URL", "http://agent:8001")
	t.Setenv("BACKEND_ADDR", ":9090")
	t.Setenv("DEV_MODE", "false")
	t.Setenv("MAIL_SINK", "smtp")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Addr != ":9090" {
		t.Errorf("Addr = %q, want :9090", cfg.Addr)
	}
	if cfg.DevMode {
		t.Error("DevMode = true, want false")
	}
	if cfg.DevMailSink != "smtp" {
		t.Errorf("DevMailSink = %q, want smtp", cfg.DevMailSink)
	}
}

func TestLoadRejectsBadDevMode(t *testing.T) {
	tests := []struct {
		name string
		val  string
	}{
		{name: "not a bool", val: "yes-please"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/thresh")
			t.Setenv("AGENT_BASE_URL", "http://localhost:8001")
			t.Setenv("DEV_MODE", tt.val)

			if _, err := config.Load(); err == nil {
				t.Errorf("Load() with DEV_MODE=%q = nil, want error", tt.val)
			}
		})
	}
}
