package postgres_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/assets"
	"github.com/syaikhipin/entrustdn-final/backend/internal/postgres"
	"github.com/syaikhipin/entrustdn-final/backend/internal/taxonomy"
)

// The live Postgres implementation of taxonomy.Store (ticket 06) against a
// real database (ADR 0007): CRUD with the UNIQUE(category,value) contract,
// the Irish seed, and the assignment counter over the assets' categories.

func newTaxonomyStore(t *testing.T) (*postgres.TaxonomyStore, *pgxpool.Pool) {
	t.Helper()
	url := skipIfNoDatabase(t)
	pool := openCleanTestDB(t, url)
	return postgres.NewTaxonomyStore(pool), pool
}

func mustTermID(t *testing.T) string {
	t.Helper()
	id, err := taxonomy.NewID()
	if err != nil {
		t.Fatalf("mint term id: %v", err)
	}
	return id
}

func TestStoreTaxonomyCRUD(t *testing.T) {
	store, _ := newTaxonomyStore(t)
	ctx := t.Context()

	// Create with a minted ID.
	leitrim := taxonomy.Term{Category: taxonomy.CategoryRegion, Value: "county-leitrim", Label: "County Leitrim", Keywords: []string{"leitrim", "border"}}
	if err := store.CreateTerm(ctx, &leitrim); err != nil {
		t.Fatalf("CreateTerm: %v", err)
	}
	if leitrim.ID == "" || leitrim.CreatedAt.IsZero() {
		t.Fatalf("CreateTerm left ID/CreatedAt empty: %+v", leitrim)
	}

	// Duplicate (category, value) is ErrExists.
	dup := taxonomy.Term{Category: taxonomy.CategoryRegion, Value: "county-leitrim", Label: "Again"}
	if err := store.CreateTerm(ctx, &dup); !errors.Is(err, taxonomy.ErrExists) {
		t.Errorf("duplicate CreateTerm error = %v, want ErrExists", err)
	}

	// Round-trip preserves the term, keywords included.
	got, err := store.TermByID(ctx, leitrim.ID)
	if err != nil {
		t.Fatalf("TermByID: %v", err)
	}
	if got.Category != taxonomy.CategoryRegion || got.Value != "county-leitrim" || got.Label != "County Leitrim" {
		t.Errorf("TermByID = %+v, want the seeded term", got)
	}
	if len(got.Keywords) != 2 || got.Keywords[0] != "leitrim" || got.Keywords[1] != "border" {
		t.Errorf("Keywords = %v, want [leitrim border]", got.Keywords)
	}

	// Update touches label/keywords only.
	got.Label = "Leitrim (border)"
	got.Keywords = []string{"leitrim"}
	if err := store.UpdateTerm(ctx, got); err != nil {
		t.Fatalf("UpdateTerm: %v", err)
	}
	after, _ := store.TermByID(ctx, leitrim.ID)
	if after.Label != "Leitrim (border)" || len(after.Keywords) != 1 {
		t.Errorf("after UpdateTerm = %+v, want label/keywords rewritten", after)
	}
	if after.Category != got.Category || after.Value != got.Value {
		t.Errorf("UpdateTerm changed identity: %+v", after)
	}

	// Terms lists in (category, value) order, including the seed's leftovers
	// from other tests (this table was truncated).
	terms, err := store.Terms(ctx)
	if err != nil {
		t.Fatalf("Terms: %v", err)
	}
	if len(terms) < 1 {
		t.Fatalf("Terms = %d, want at least the created term", len(terms))
	}
	for i := 1; i < len(terms); i++ {
		if terms[i-1].Category > terms[i].Category ||
			(terms[i-1].Category == terms[i].Category && terms[i-1].Value > terms[i].Value) {
			t.Fatalf("Terms out of order: %+v", terms)
		}
	}

	// Delete removes the row; missing → ErrNotFound.
	if err := store.DeleteTerm(ctx, leitrim.ID); err != nil {
		t.Fatalf("DeleteTerm: %v", err)
	}
	if _, err := store.TermByID(ctx, leitrim.ID); !errors.Is(err, taxonomy.ErrNotFound) {
		t.Errorf("post-delete TermByID error = %v, want ErrNotFound", err)
	}
	if err := store.DeleteTerm(ctx, leitrim.ID); !errors.Is(err, taxonomy.ErrNotFound) {
		t.Errorf("second DeleteTerm error = %v, want ErrNotFound", err)
	}
}

func TestSeedTaxonomyIfEmpty(t *testing.T) {
	store, pool := newTaxonomyStore(t)
	ctx := t.Context()

	// Empty table: the seed creates the full Irish vocabulary.
	if err := postgres.SeedTaxonomyIfEmpty(ctx, pool); err != nil {
		t.Fatalf("SeedTaxonomyIfEmpty: %v", err)
	}
	terms, err := store.Terms(ctx)
	if err != nil {
		t.Fatalf("Terms: %v", err)
	}
	if len(terms) == 0 {
		t.Fatal("empty-table seed created no terms, want the full vocabulary")
	}

	// Non-empty table: a no-op — the seed must never resurrect or clobber.
	if err := postgres.SeedTaxonomyIfEmpty(ctx, pool); err != nil {
		t.Fatalf("second SeedTaxonomyIfEmpty: %v", err)
	}
	after, _ := store.Terms(ctx)
	if len(after) != len(terms) {
		t.Errorf("Terms after re-seed = %d, want %d (no resurrection)", len(after), len(terms))
	}
}

func TestAssignmentsByTermAndCategoriesRoundTrip(t *testing.T) {
	taxStore, pool := newTaxonomyStore(t)
	assetStore := postgres.NewAssetsStore(pool)
	ctx := t.Context()

	// Seed one term and give two assets a category pointing at it.
	term := taxonomy.Term{Category: taxonomy.CategoryCrop, Value: "dairy", Label: "Dairy"}
	if err := taxStore.CreateTerm(ctx, &term); err != nil {
		t.Fatalf("CreateTerm: %v", err)
	}
	orgID := insertTestOrg(t, pool, "pg-tax-assign@example.org")
	stamp := func() []taxonomy.Assignment {
		return []taxonomy.Assignment{{
			Category: taxonomy.CategoryCrop, TermID: term.ID,
			Value: term.Value, Label: term.Label,
			Confidence: 0.75, Source: taxonomy.SourceClassifier,
			AssignedAt: time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC),
		}}
	}
	for _, name := range []string{"herd-a.csv", "herd-b.csv"} {
		a := assets.Asset{
			ID: mustAssetID(t), OrgID: orgID, Name: name,
			SizeBytes: 10, Format: "csv", Pipeline: []string{"anonymize"},
			Categories: stamp(),
		}
		if err := assetStore.CreateAsset(ctx, &a); err != nil {
			t.Fatalf("CreateAsset %s: %v", name, err)
		}
	}

	// The counter (on the assets store — it owns the categories column)
	// sees both assets under the term's ID.
	usage, err := assetStore.AssignmentsByTerm(ctx)
	if err != nil {
		t.Fatalf("AssignmentsByTerm: %v", err)
	}
	if usage[term.ID] != 2 {
		t.Errorf("usage[%s] = %d, want 2", term.ID, usage[term.ID])
	}

	// Categories round-trip through the JSONB column, timestamps included.
	got, err := assetStore.AssetByID(ctx, func() string {
		as, _ := assetStore.AssetsByOrg(ctx, orgID)
		return as[0].ID
	}())
	if err != nil {
		t.Fatalf("AssetByID: %v", err)
	}
	if len(got.Categories) != 1 {
		t.Fatalf("Categories = %+v, want 1 assignment", got.Categories)
	}
	c := got.Categories[0]
	if c.TermID != term.ID || c.Value != "dairy" || c.Label != "Dairy" ||
		c.Confidence != 0.75 || c.Source != taxonomy.SourceClassifier {
		t.Errorf("round-tripped assignment = %+v, want the stored stamp", c)
	}
	if !c.AssignedAt.Equal(time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("AssignedAt = %v, want the stored timestamp", c.AssignedAt)
	}

	// Clearing categories via UpdateAssetMeta empties the counter.
	got.Categories = nil
	if err := assetStore.UpdateAssetMeta(ctx, got); err != nil {
		t.Fatalf("UpdateAssetMeta: %v", err)
	}
	usage, err = assetStore.AssignmentsByTerm(ctx)
	if err != nil {
		t.Fatalf("AssignmentsByTerm after clear: %v", err)
	}
	if usage[term.ID] != 0 {
		t.Errorf("usage[%s] after clear = %d, want 0", term.ID, usage[term.ID])
	}
}
