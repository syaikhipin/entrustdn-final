package postgres_test

import (
	"errors"
	"testing"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
	"github.com/syaikhipin/entrustdn-final/backend/internal/postgres"
)

// The live Postgres implementation of membership.Store, exercised against a
// real database (ADR 0007). These complement the Seam 1 tests (which use the
// in-memory double) by proving the SQL layer honors the same contract:
// uniqueness conflicts, one-shot tokens, and latest-wins acceptances.

func newStore(t *testing.T) (*postgres.Store, func()) {
	t.Helper()
	url := skipIfNoDatabase(t)
	pool := openCleanTestDB(t, url)
	return postgres.NewStore(pool), func() {}
}

func TestStoreAccountLifecycle(t *testing.T) {
	store, _ := newStore(t)
	ctx := t.Context()

	// Create → conflict on duplicate email → lookup by email.
	acct := membership.Account{
		Email:        "pg-consumer@example.org",
		PasswordHash: "pbkdf2-sha256$1$aa$bb",
		DisplayName:  "PG Consumer",
		Role:         membership.RoleDataConsumer,
		Status:       membership.StatusActive,
	}
	if err := store.CreateAccount(ctx, &acct); err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	if acct.ID == "" || acct.CreatedAt.IsZero() {
		t.Errorf("CreateAccount must fill ID and CreatedAt, got id=%q created=%v", acct.ID, acct.CreatedAt)
	}

	dup := acct
	if err := store.CreateAccount(ctx, &dup); !errors.Is(err, membership.ErrConflict) {
		t.Errorf("duplicate CreateAccount error = %v, want ErrConflict", err)
	}

	byEmail, err := store.AccountByEmail(ctx, acct.Email)
	if err != nil || byEmail.ID != acct.ID {
		t.Errorf("AccountByEmail() = %v, %v; want id %s", byEmail.ID, err, acct.ID)
	}

	// Verify → visible.
	at := time.Now().UTC()
	if err := store.SetAccountVerified(ctx, acct.ID, at); err != nil {
		t.Fatalf("SetAccountVerified() error = %v", err)
	}
	got, err := store.AccountByID(ctx, acct.ID)
	if err != nil {
		t.Fatalf("AccountByID() error = %v", err)
	}
	if got.VerifiedAt == nil || !got.VerifiedAt.After(at.Add(-time.Minute)) {
		t.Errorf("VerifiedAt = %v, want ~%v", got.VerifiedAt, at)
	}

	// Unknown lookups are ErrNotFound.
	if _, err := store.AccountByEmail(ctx, "missing@example.org"); !errors.Is(err, membership.ErrNotFound) {
		t.Errorf("missing AccountByEmail error = %v, want ErrNotFound", err)
	}
}

func TestStoreVerificationTokenIsOneShot(t *testing.T) {
	store, _ := newStore(t)
	ctx := t.Context()

	acct := membership.Account{
		Email: "pg-token@example.org", PasswordHash: "x", DisplayName: "T",
		Role: membership.RoleDataConsumer, Status: membership.StatusActive,
	}
	if err := store.CreateAccount(ctx, &acct); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	if err := store.CreateVerificationToken(ctx, "tok-123", acct.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateVerificationToken: %v", err)
	}

	// First consume wins; second finds nothing.
	id, err := store.ConsumeVerificationToken(ctx, "tok-123", time.Now())
	if err != nil || id != acct.ID {
		t.Fatalf("first consume = (%q, %v), want %q", id, err, acct.ID)
	}
	if _, err := store.ConsumeVerificationToken(ctx, "tok-123", time.Now()); !errors.Is(err, membership.ErrNotFound) {
		t.Errorf("second consume error = %v, want ErrNotFound", err)
	}

	// Expired tokens are dead on arrival.
	if err := store.CreateVerificationToken(ctx, "tok-old", acct.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("CreateVerificationToken: %v", err)
	}
	if _, err := store.ConsumeVerificationToken(ctx, "tok-old", time.Now()); !errors.Is(err, membership.ErrNotFound) {
		t.Errorf("expired consume error = %v, want ErrNotFound", err)
	}
}

func TestStoreTOSAndAcceptances(t *testing.T) {
	store, _ := newStore(t)
	ctx := t.Context()

	if err := store.PublishTOS(ctx, membership.TOSVersion{Version: "1.0", Body: "one"}); err != nil {
		t.Fatalf("PublishTOS 1.0: %v", err)
	}
	time.Sleep(2 * time.Millisecond) // published_at ordering needs a tick
	if err := store.PublishTOS(ctx, membership.TOSVersion{Version: "2.0", Body: "two"}); err != nil {
		t.Fatalf("PublishTOS 2.0: %v", err)
	}

	current, err := store.CurrentTOS(ctx)
	if err != nil || current.Version != "2.0" {
		t.Errorf("CurrentTOS = (%+v, %v), want version 2.0", current, err)
	}
	versions, err := store.TOSVersions(ctx)
	if err != nil || len(versions) < 2 {
		t.Fatalf("TOSVersions = %d versions, %v; want at least 2", len(versions), err)
	}
	if versions[0].Version != "2.0" {
		t.Errorf("versions[0] = %s, want 2.0 first (newest first)", versions[0].Version)
	}

	acct := membership.Account{
		Email: "pg-tos@example.org", PasswordHash: "x", DisplayName: "P",
		Role: membership.RoleDataConsumer, Status: membership.StatusActive,
	}
	if err := store.CreateAccount(ctx, &acct); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// Accept 1.0 then 2.0: latest wins; history keeps both.
	for _, v := range []string{"1.0", "2.0"} {
		if err := store.RecordAcceptance(ctx, membership.TOSAcceptance{AccountID: acct.ID, Version: v}); err != nil {
			t.Fatalf("RecordAcceptance(%s): %v", v, err)
		}
	}
	latest, err := store.LatestAcceptance(ctx, acct.ID)
	if err != nil || latest.Version != "2.0" {
		t.Errorf("LatestAcceptance = (%+v, %v), want version 2.0", latest, err)
	}
	if _, err := store.LatestAcceptance(ctx, "no-such-account"); !errors.Is(err, membership.ErrNotFound) {
		t.Errorf("missing acceptance error = %v, want ErrNotFound", err)
	}
}

func TestStoreApplicationWorkflow(t *testing.T) {
	store, _ := newStore(t)
	ctx := t.Context()

	org := membership.Account{
		Email: "pg-org@example.org", PasswordHash: "x", DisplayName: "PG Org",
		Role: membership.RoleFarmerOrganization, Status: membership.StatusPendingApproval,
	}
	other := membership.Account{
		Email: "pg-active-org@example.org", PasswordHash: "x", DisplayName: "Active Org",
		Role: membership.RoleFarmerOrganization, Status: membership.StatusActive,
	}
	for _, a := range []*membership.Account{&org, &other} {
		if err := store.CreateAccount(ctx, a); err != nil {
			t.Fatalf("CreateAccount(%s): %v", a.Email, err)
		}
	}

	pending, err := store.PendingOrganizations(ctx)
	if err != nil {
		t.Fatalf("PendingOrganizations: %v", err)
	}
	found := false
	for _, a := range pending {
		if a.ID == org.ID {
			found = true
		}
		if a.ID == other.ID {
			t.Errorf("active org %s must not appear as pending", other.ID)
		}
	}
	if !found {
		t.Errorf("pending org %s missing from PendingOrganizations()", org.ID)
	}

	if err := store.SetApplicationStatus(ctx, org.ID, membership.StatusRejected); err != nil {
		t.Fatalf("SetApplicationStatus: %v", err)
	}
	got, _ := store.AccountByID(ctx, org.ID)
	if got.Status != membership.StatusRejected {
		t.Errorf("status = %s, want rejected", got.Status)
	}
}

func TestStoreSessions(t *testing.T) {
	store, _ := newStore(t)
	ctx := t.Context()

	acct := membership.Account{
		Email: "pg-sess@example.org", PasswordHash: "x", DisplayName: "S",
		Role: membership.RoleDataConsumer, Status: membership.StatusActive,
	}
	if err := store.CreateAccount(ctx, &acct); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	sess := membership.Session{Token: "sess-1", AccountID: acct.ID, RequiresReacceptance: true}
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, err := store.SessionByToken(ctx, "sess-1")
	if err != nil || !got.RequiresReacceptance {
		t.Errorf("SessionByToken = (%+v, %v), want flagged session", got, err)
	}

	got.RequiresReacceptance = false
	if err := store.UpdateSession(ctx, got); err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	got, err = store.SessionByToken(ctx, "sess-1")
	if err != nil || got.RequiresReacceptance {
		t.Errorf("after update = (%+v, %v), want flag cleared", got, err)
	}

	if err := store.DeleteSession(ctx, "sess-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := store.SessionByToken(ctx, "sess-1"); !errors.Is(err, membership.ErrNotFound) {
		t.Errorf("deleted session error = %v, want ErrNotFound", err)
	}
}
