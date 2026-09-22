package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/mailsink"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
)

// bootstrap seeds the two facts every deployment needs before anything
// works: a first Platform Admin (registration refuses the role, so admins
// are provisioned out of band) and the initial Terms of Service version.
// Both are idempotent: existing rows win, restarts are no-ops.
type bootstrapConfig struct {
	AdminEmail    string
	AdminPassword string
	TOSVersion    string
	TOSBody       string
}

func runBootstrap(ctx context.Context, store membership.Store, mail mailsink.Sink, bc bootstrapConfig) error {
	if bc.AdminEmail == "" || bc.AdminPassword == "" {
		return fmt.Errorf("bootstrap: BOOTSTRAP_ADMIN_EMAIL and BOOTSTRAP_ADMIN_PASSWORD are required")
	}
	if bc.TOSVersion == "" || bc.TOSBody == "" {
		return fmt.Errorf("bootstrap: BOOTSTRAP_TOS_VERSION and BOOTSTRAP_TOS_BODY are required")
	}

	// TOS first: the admin's own acceptance references it.
	if _, err := store.CurrentTOS(ctx); err != nil {
		if err := store.PublishTOS(ctx, membership.TOSVersion{Version: bc.TOSVersion, Body: bc.TOSBody}); err != nil {
			return fmt.Errorf("bootstrap: failed to publish initial TOS: %w", err)
		}
		log.Printf("bootstrap: published initial Terms of Service version %s", bc.TOSVersion)
	}

	acct := membership.Account{
		Email:        bc.AdminEmail,
		PasswordHash: "",
		DisplayName:  "Platform Admin",
		Role:         membership.RolePlatformAdmin,
		Status:       membership.StatusActive,
	}
	existing, err := store.AccountByEmail(ctx, acct.Email)
	switch {
	case err == nil:
		acct = existing // already provisioned; leave credentials as they are
	default:
		hash, hashErr := membership.HashPassword(bc.AdminPassword)
		if hashErr != nil {
			return fmt.Errorf("bootstrap: failed to hash admin password: %w", hashErr)
		}
		acct.PasswordHash = hash
		if err := store.CreateAccount(ctx, &acct); err != nil {
			return fmt.Errorf("bootstrap: failed to create admin account: %w", err)
		}
		// Admins are provisioned verified; the log link flow is for
		// self-service registrants.
		if err := store.SetAccountVerified(ctx, acct.ID, time.Now()); err != nil {
			return fmt.Errorf("bootstrap: failed to verify admin account: %w", err)
		}
		log.Printf("bootstrap: provisioned Platform Admin %s", acct.Email)
	}

	// The admin accepts the current TOS so its sessions are never flagged.
	if _, err := store.LatestAcceptance(ctx, acct.ID); err != nil {
		tos, err := store.CurrentTOS(ctx)
		if err != nil {
			return fmt.Errorf("bootstrap: failed to load current TOS: %w", err)
		}
		if err := store.RecordAcceptance(ctx, membership.TOSAcceptance{AccountID: acct.ID, Version: tos.Version}); err != nil {
			return fmt.Errorf("bootstrap: failed to record admin TOS acceptance: %w", err)
		}
	}

	// The admin's own verification link also lands in the log sink when the
	// account is fresh — harmless duplication in dev, useful in staging.
	_ = mail // reserved for the SMTP sink; bootstrap itself logs above.
	return nil
}
