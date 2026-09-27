package postgres_test

import (
	"errors"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/memoryprov"
	"github.com/syaikhipin/entrustdn-final/backend/internal/postgres"
)

// The live Postgres implementation of memoryprov.Store (ticket 14),
// exercised against a real database (ADR 0007): the name's uniqueness is
// the integrity constraint, list order is stable, and deletes refuse
// unknown IDs.

func newMemoryProvidersStore(t *testing.T) *postgres.MemoryProvidersStore {
	t.Helper()
	url := skipIfNoDatabase(t)
	pool := openCleanTestDB(t, url)
	return postgres.NewMemoryProvidersStore(pool)
}

func TestMemoryProvidersStoreRoundTrip(t *testing.T) {
	store := newMemoryProvidersStore(t)
	svc := memoryprov.NewService(store)

	// Empty registry lists as an empty slice.
	got, err := svc.List(t.Context())
	if err != nil {
		t.Fatalf("empty List: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("empty List = %#v, want an empty non-nil slice", got)
	}

	first, err := svc.Create(t.Context(), "mem0-primary", "http://memory.local:8080/mcp")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if first.ID == "" || first.CreatedAt.IsZero() {
		t.Errorf("created provider = %+v, want minted ID and timestamps", first)
	}
	second, err := svc.Create(t.Context(), "hindsight", "http://hindsight.local/mcp")
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}

	got, err = svc.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].Name != "hindsight" || got[1].Name != "mem0-primary" {
		t.Errorf("List = %+v, want newest first", got)
	}

	// Duplicate name refused through the SQL constraint.
	if _, err := svc.Create(t.Context(), "mem0-primary", "http://other.local/mcp"); !errors.Is(err, memoryprov.ErrExists) {
		t.Errorf("duplicate Create err = %v, want ErrExists", err)
	}

	if err := svc.Delete(t.Context(), second.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Delete(t.Context(), second.ID); !errors.Is(err, memoryprov.ErrNotFound) {
		t.Errorf("re-Delete err = %v, want ErrNotFound", err)
	}
	got, _ = svc.List(t.Context())
	if len(got) != 1 || got[0].ID != first.ID {
		t.Errorf("after Delete List = %+v, want only the first provider", got)
	}
}
