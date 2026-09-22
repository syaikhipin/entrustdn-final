package main

import (
	"bytes"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/mailsink"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
)

// Bootstrap seeding is idempotent: the first run creates the admin + TOS;
// every later run (every restart) is a no-op. Driven through the in-memory
// store double, like the API tests.
func TestBootstrapSeedsAdminAndTOS(t *testing.T) {
	store := membership.NewMemoryStore()
	var sink bytes.Buffer

	bc := bootstrapConfig{
		AdminEmail:    "admin@thresh.dev",
		AdminPassword: "secret-admin-pass",
		TOSVersion:    "1.0",
		TOSBody:       "Be good to farmers.",
	}
	if err := runBootstrap(t.Context(), store, mailsink.NewLogSink(&sink), bc); err != nil {
		t.Fatalf("runBootstrap() error = %v", err)
	}

	acct, err := store.AccountByEmail(t.Context(), bc.AdminEmail)
	if err != nil {
		t.Fatalf("admin account missing after bootstrap: %v", err)
	}
	if acct.Role != membership.RolePlatformAdmin {
		t.Errorf("role = %s, want platform_admin", acct.Role)
	}
	if acct.VerifiedAt == nil {
		t.Error("admin must be provisioned verified")
	}
	if !membership.CheckPassword(acct.PasswordHash, bc.AdminPassword) {
		t.Error("admin password does not verify")
	}
	tos, err := store.CurrentTOS(t.Context())
	if err != nil || tos.Version != bc.TOSVersion {
		t.Errorf("TOS = (%+v, %v), want version %s", tos, err, bc.TOSVersion)
	}
	if acc, err := store.LatestAcceptance(t.Context(), acct.ID); err != nil || acc.Version != bc.TOSVersion {
		t.Errorf("admin acceptance = (%+v, %v), want version %s", acc, err, bc.TOSVersion)
	}
}

func TestBootstrapIsIdempotentAndKeepsCredentials(t *testing.T) {
	store := membership.NewMemoryStore()
	var sink bytes.Buffer
	bc := bootstrapConfig{
		AdminEmail: "admin@thresh.dev", AdminPassword: "first-pass-1",
		TOSVersion: "1.0", TOSBody: "v1",
	}
	if err := runBootstrap(t.Context(), store, mailsink.NewLogSink(&sink), bc); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Second run with a different password: existing credentials win.
	bc.AdminPassword = "second-pass-2"
	bc.TOSBody = "someone edited the text"
	if err := runBootstrap(t.Context(), store, mailsink.NewLogSink(&sink), bc); err != nil {
		t.Fatalf("second run: %v", err)
	}

	acct, _ := store.AccountByEmail(t.Context(), bc.AdminEmail)
	if !membership.CheckPassword(acct.PasswordHash, "first-pass-1") {
		t.Error("bootstrap overwrote the existing admin password")
	}
	if membership.CheckPassword(acct.PasswordHash, "second-pass-2") {
		t.Error("bootstrap replaced credentials on restart")
	}
	tos, _ := store.CurrentTOS(t.Context())
	if tos.Body != "v1" {
		t.Errorf("bootstrap rewrote the published TOS body: %q", tos.Body)
	}
}

func TestBootstrapRejectsMissingConfig(t *testing.T) {
	tests := []struct {
		name string
		bc   bootstrapConfig
	}{
		{name: "no admin email", bc: bootstrapConfig{AdminPassword: "x", TOSVersion: "1", TOSBody: "b"}},
		{name: "no admin password", bc: bootstrapConfig{AdminEmail: "a@b.c", TOSVersion: "1", TOSBody: "b"}},
		{name: "no tos version", bc: bootstrapConfig{AdminEmail: "a@b.c", AdminPassword: "x", TOSBody: "b"}},
		{name: "no tos body", bc: bootstrapConfig{AdminEmail: "a@b.c", AdminPassword: "x", TOSVersion: "1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := membership.NewMemoryStore()
			if err := runBootstrap(t.Context(), store, mailsink.NewLogSink(nilWriter{}), tt.bc); err == nil {
				t.Errorf("runBootstrap(%s) error = nil, want an error", tt.name)
			}
		})
	}
}

type nilWriter struct{}

func (nilWriter) Write(p []byte) (int, error) { return len(p), nil }
