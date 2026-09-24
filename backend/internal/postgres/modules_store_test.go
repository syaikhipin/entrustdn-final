package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/modules"
	"github.com/syaikhipin/entrustdn-final/backend/internal/postgres"
)

// The Postgres-backed modules.Store (ticket 08; ADR 0007: the system of
// record). Version history, promotion, and grants survive restarts; the
// in-memory double from the service tests stands in for these same paths
// when no database is reachable.

func modulesFixture(t *testing.T) (*postgres.ModulesStore, *pgxpool.Pool, string) {
	t.Helper()
	pool := openCleanTestDB(t, skipIfNoDatabase(t))
	// One author account, so module rows have their foreign key.
	var authorID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO accounts (email, display_name, role, status, password_hash, verified_at)
		VALUES ($1, 'PG Test Author', 'data_consumer', 'active', 'x', now())
		RETURNING id`, "modules-pg@example.org").Scan(&authorID)
	if err != nil {
		t.Fatalf("insert test author: %v", err)
	}
	return postgres.NewModulesStore(pool), pool, authorID
}

func storedModule(authorID string) modules.Module {
	return modules.Module{
		ID:         "mod-ver-1",
		ModuleID:   "mod-1",
		Name:       "barley-survey",
		Kind:       modules.KindProcessTemplate,
		AuthorID:   authorID,
		Version:    "1.0.0",
		Capability: "Runs a spring-barley yield survey workflow for Requests.",
		Content:    "# Barley survey",
	}
}

func TestModulesRoundTrip(t *testing.T) {
	store, _, authorID := modulesFixture(t)
	ctx := context.Background()
	m := storedModule(authorID)

	if err := store.CreateModule(ctx, &m); err != nil {
		t.Fatalf("create: %v", err)
	}
	if m.CreatedAt.IsZero() || m.UpdatedAt.IsZero() {
		t.Fatal("create left timestamps zero, want filled")
	}

	got, err := store.ModuleByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Name != m.Name || got.Kind != m.Kind || got.Capability != m.Capability || got.Content != m.Content {
		t.Errorf("round trip drifted: got %+v", got)
	}
}

func TestModulesVersionOrderingAndLatest(t *testing.T) {
	store, _, authorID := modulesFixture(t)
	ctx := context.Background()
	first := storedModule(authorID)
	if err := store.CreateModule(ctx, &first); err != nil {
		t.Fatalf("create: %v", err)
	}
	second := storedModule(authorID)
	second.ID = "mod-ver-2"
	second.Version = "1.1.0"
	if err := store.CreateModuleVersion(ctx, &second); err != nil {
		t.Fatalf("create version: %v", err)
	}

	versions, err := store.ModuleVersions(ctx, first.ModuleID)
	if err != nil || len(versions) != 2 {
		t.Fatalf("versions = (%d, %v), want 2", len(versions), err)
	}
	if versions[0].Version != "1.0.0" || versions[1].Version != "1.1.0" {
		t.Errorf("order = %s then %s, want oldest first", versions[0].Version, versions[1].Version)
	}
	latest, err := store.LatestModuleVersion(ctx, first.ModuleID)
	if err != nil || latest.Version != "1.1.0" {
		t.Errorf("latest = (%s, %v), want 1.1.0", latest.Version, err)
	}
}

func TestModulesVisibilityAndFlags(t *testing.T) {
	store, pool, authorID := modulesFixture(t)
	ctx := context.Background()
	m := storedModule(authorID)
	if err := store.CreateModule(ctx, &m); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Promotion rewrites every version of the module.
	if err := store.SetSystemWide(ctx, m.ModuleID, true); err != nil {
		t.Fatalf("promote: %v", err)
	}
	got, _ := store.ModuleByID(ctx, m.ID)
	if !got.SystemWide {
		t.Error("SystemWide not persisted")
	}

	// Deprecation flags exactly one version.
	if err := store.SetVersionDeprecated(ctx, m.ID, true); err != nil {
		t.Fatalf("deprecate: %v", err)
	}
	got, _ = store.ModuleByID(ctx, m.ID)
	if !got.Deprecated {
		t.Error("Deprecated not persisted")
	}
	if !got.SystemWide {
		t.Error("deprecation clobbered SystemWide")
	}

	// Grants — the grantee is a real account (the column is a UUID FK).
	var granteeID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO accounts (email, display_name, role, status, password_hash, verified_at)
		VALUES ($1, 'PG Test Grantee', 'data_consumer', 'active', 'x', now())
		RETURNING id`, "modules-grantee-pg@example.org").Scan(&granteeID); err != nil {
		t.Fatalf("insert test grantee: %v", err)
	}
	if err := store.AddGrant(ctx, modules.Grant{ModuleID: m.ModuleID, AccountID: granteeID}); err != nil {
		t.Fatalf("add grant: %v", err)
	}
	if ok, _ := store.GrantExists(ctx, m.ModuleID, granteeID); !ok {
		t.Error("GrantExists = false after AddGrant")
	}
	grants, err := store.GrantsForModule(ctx, m.ModuleID)
	if err != nil || len(grants) != 1 {
		t.Fatalf("grants = (%v, %v), want one", grants, err)
	}
	if err := store.RemoveGrant(ctx, m.ModuleID, granteeID); err != nil {
		t.Fatalf("remove grant: %v", err)
	}
	if ok, _ := store.GrantExists(ctx, m.ModuleID, granteeID); ok {
		t.Error("grant survived RemoveGrant")
	}
	if err := store.RemoveGrant(ctx, m.ModuleID, "nobody"); err == nil {
		t.Error("RemoveGrant of a missing grant = no error, want not found")
	}

	// Listings.
	mine, err := store.ModulesByAuthor(ctx, authorID)
	if err != nil || len(mine) != 1 || mine[0].ID != m.ID {
		t.Errorf("ModulesByAuthor = (%v, %v), want this module's version", mine, err)
	}
	world, err := store.SystemWideModules(ctx)
	if err != nil || len(world) != 1 || world[0].ModuleID != m.ModuleID {
		t.Errorf("SystemWideModules = (%v, %v), want the promoted module", world, err)
	}
}

func TestModulesNotFoundPaths(t *testing.T) {
	store, _, _ := modulesFixture(t)
	ctx := context.Background()
	if _, err := store.ModuleByID(ctx, "nope"); err == nil {
		t.Error("ModuleByID(missing) = no error, want not found")
	}
	if _, err := store.LatestModuleVersion(ctx, "nope"); err == nil {
		t.Error("LatestModuleVersion(missing) = no error, want not found")
	}
	if err := store.SetSystemWide(ctx, "nope", true); err == nil {
		t.Error("SetSystemWide(missing) = no error, want not found")
	}
}
