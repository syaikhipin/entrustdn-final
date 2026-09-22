package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/assets"
	"github.com/syaikhipin/entrustdn-final/backend/internal/postgres"
)

// The live Postgres implementation of assets.Store, exercised against a
// real database (ADR 0007). These complement the Seam 1 tests (in-memory
// doubles) by proving the SQL layer honors the same contract: org scoping,
// metadata-only updates, and delete-returns-the-record-for-blob-cleanup.

func newAssetsStore(t *testing.T) (*postgres.AssetsStore, *pgxpool.Pool) {
	t.Helper()
	url := skipIfNoDatabase(t)
	pool := openCleanTestDB(t, url)
	return postgres.NewAssetsStore(pool), pool
}

// insertTestOrg adds one active Farmer Organization account directly, so an
// asset row has its foreign key. Returns the account ID.
func insertTestOrg(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO accounts (email, display_name, role, status, password_hash, verified_at)
		VALUES ($1, 'PG Test Org', 'farmer_organization', 'active', 'x', now())
		RETURNING id`, email).Scan(&id)
	if err != nil {
		t.Fatalf("insert test org: %v", err)
	}
	return id
}

// mustAssetID mints an asset ID through the domain package's own generator.
func mustAssetID(t *testing.T) string {
	t.Helper()
	id, err := assets.NewID()
	if err != nil {
		t.Fatalf("mint asset id: %v", err)
	}
	return id
}

func seedAsset(t *testing.T, store *postgres.AssetsStore, orgID, name string) assets.Asset {
	t.Helper()
	collected := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	a := assets.Asset{
		ID:          mustAssetID(t),
		OrgID:       orgID,
		Name:        name,
		Description: "seeded",
		SizeBytes:   42,
		Format:      "csv",
		Pipeline:    []string{"anonymize"},
		Provenance: assets.Provenance{
			Source:      "co-op records",
			CollectedAt: &collected,
			Notes:       "spring batch",
		},
		ObjectKey: "assets/" + orgID + "/" + name,
	}
	if err := store.CreateAsset(t.Context(), &a); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	return a
}

func TestStoreAssetLifecycle(t *testing.T) {
	store, pool := newAssetsStore(t)
	ctx := t.Context()

	orgID := insertTestOrg(t, pool, "pg-asset-lifecycle@example.org")
	a := seedAsset(t, store, orgID, "lifecycle.csv")

	got, err := store.AssetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("AssetByID: %v", err)
	}
	if got.Name != "lifecycle.csv" || got.OrgID != orgID {
		t.Errorf("AssetByID = %+v, want the seeded record", got)
	}
	if len(got.Pipeline) != 1 || got.Pipeline[0] != "anonymize" {
		t.Errorf("Pipeline = %v, want [anonymize]", got.Pipeline)
	}
	if got.Provenance.CollectedAt == nil || !got.Provenance.CollectedAt.Equal(*a.Provenance.CollectedAt) {
		t.Errorf("CollectedAt = %v, want %v", got.Provenance.CollectedAt, a.Provenance.CollectedAt)
	}
	if got.Provenance.Source != "co-op records" {
		t.Errorf("Provenance.Source = %q, want co-op records", got.Provenance.Source)
	}

	// Org scoping: another org's listing never includes it.
	otherID := insertTestOrg(t, pool, "pg-asset-other@example.org")
	mine, err := store.AssetsByOrg(ctx, orgID)
	if err != nil || len(mine) != 1 {
		t.Fatalf("AssetsByOrg(owner) = %d assets, %v; want 1", len(mine), err)
	}
	theirs, _ := store.AssetsByOrg(ctx, otherID)
	if len(theirs) != 0 {
		t.Errorf("AssetsByOrg(stranger) = %d assets, want 0", len(theirs))
	}

	// Metadata update touches only editable fields.
	got.Description = "curated by the co-op"
	got.Name = "curated.csv"
	if err := store.UpdateAssetMeta(ctx, got); err != nil {
		t.Fatalf("UpdateAssetMeta: %v", err)
	}
	after, _ := store.AssetByID(ctx, a.ID)
	if after.Name != "curated.csv" || after.Description != "curated by the co-op" {
		t.Errorf("meta not persisted: %+v", after)
	}
	if after.SizeBytes != a.SizeBytes || after.Format != a.Format || after.ObjectKey != a.ObjectKey {
		t.Errorf("immutable fields changed: %+v", after)
	}

	// Delete returns the record (for blob cleanup) and removes the row.
	deleted, err := store.DeleteAsset(ctx, a.ID)
	if err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	if deleted.ObjectKey != a.ObjectKey {
		t.Errorf("DeleteAsset returned key %q, want %q", deleted.ObjectKey, a.ObjectKey)
	}
	if _, err := store.AssetByID(ctx, a.ID); !errors.Is(err, assets.ErrNotFound) {
		t.Errorf("post-delete AssetByID error = %v, want ErrNotFound", err)
	}
}

func TestStoreAssetNotFound(t *testing.T) {
	store, _ := newAssetsStore(t)
	if _, err := store.AssetByID(t.Context(), "00000000-0000-0000-0000-000000000000"); !errors.Is(err, assets.ErrNotFound) {
		t.Errorf("missing AssetByID error = %v, want ErrNotFound", err)
	}
}
