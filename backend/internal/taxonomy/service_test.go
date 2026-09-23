package taxonomy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/taxonomy"
)

// The taxonomy Service (ticket 06): admin CRUD with validation, the
// in-use delete guard, and the org-correction path.

func newSeededService(t *testing.T) (*taxonomy.Service, *taxonomy.MemoryStore, []taxonomy.Term) {
	t.Helper()
	store := taxonomy.NewMemoryStore()
	if _, err := taxonomy.Seed(context.Background(), store); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	terms, err := store.Terms(context.Background())
	if err != nil {
		t.Fatalf("Terms: %v", err)
	}
	return taxonomy.NewService(store, taxonomy.Counters{}), store, terms
}

func TestServiceCreateDerivesValueFromLabel(t *testing.T) {
	svc, _, _ := newSeededService(t)

	term := &taxonomy.Term{Category: taxonomy.CategoryRegion, Label: "County Leitrim", Keywords: []string{"leitrim"}}
	if err := svc.Create(context.Background(), term); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if term.Value != "county-leitrim" {
		t.Errorf("Value = %q, want derived county-leitrim", term.Value)
	}
	if term.ID == "" {
		t.Error("Create left the term without an ID")
	}

	// The same label in the same category is now a duplicate.
	dup := &taxonomy.Term{Category: taxonomy.CategoryRegion, Label: "County Leitrim"}
	err := svc.Create(context.Background(), dup)
	if !errors.Is(err, taxonomy.ErrExists) {
		t.Errorf("duplicate Create error = %v, want ErrExists", err)
	}
}

func TestServiceCreateRefusesMalformedTerms(t *testing.T) {
	svc, _, _ := newSeededService(t)
	tests := []struct {
		name string
		term taxonomy.Term
	}{
		{name: "unknown category", term: taxonomy.Term{Label: "X", Value: "x"}},
		{name: "no label", term: taxonomy.Term{Category: taxonomy.CategoryCrop, Value: "x"}},
		{name: "value not a slug", term: taxonomy.Term{Category: taxonomy.CategoryCrop, Label: "X", Value: "Not A Slug"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term := tt.term
			if err := svc.Create(context.Background(), &term); err == nil {
				t.Error("Create accepted a malformed term")
			}
		})
	}
}

func TestServiceUpdateRewritesLabelAndKeywordsOnly(t *testing.T) {
	svc, _, terms := newSeededService(t)
	dairy := findTerm(t, terms, taxonomy.CategoryCrop, "dairy")

	updated, err := svc.Update(context.Background(), dairy.ID, "Dairy farming", []string{"dairy", "moocall"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Label != "Dairy farming" {
		t.Errorf("Label = %q, want Dairy farming", updated.Label)
	}
	if updated.Category != taxonomy.CategoryCrop || updated.Value != "dairy" {
		t.Errorf("identity changed: %+v", updated)
	}

	if _, err := svc.Update(context.Background(), "no-such-id", "X", nil); !errors.Is(err, taxonomy.ErrNotFound) {
		t.Errorf("Update on missing term = %v, want ErrNotFound", err)
	}
}

func TestServiceDeleteGuardedByAssignments(t *testing.T) {
	tests := []struct {
		name    string
		usage   taxonomy.Counters
		wantErr error
	}{
		{name: "unused term deletes", usage: taxonomy.Counters{}, wantErr: nil},
		{name: "in-use term refused", usage: taxonomy.Counters{"term-in-use": 3}, wantErr: taxonomy.ErrInUse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := taxonomy.NewMemoryStore()
			if _, err := taxonomy.Seed(context.Background(), store); err != nil {
				t.Fatalf("Seed: %v", err)
			}
			terms, _ := store.Terms(context.Background())
			// Point the guard at the first term; the usage map keys on IDs.
			target := terms[0]
			usage := tt.usage
			if tt.wantErr != nil {
				usage = taxonomy.Counters{target.ID: usage["term-in-use"]}
				if usage[target.ID] == 0 {
					usage[target.ID] = 1
				}
			}
			svc := taxonomy.NewService(store, usage)

			err := svc.Delete(context.Background(), target.ID)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Delete: %v", err)
				}
				if _, err := store.TermByID(context.Background(), target.ID); !errors.Is(err, taxonomy.ErrNotFound) {
					t.Errorf("term still loadable after delete: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Delete error = %v, want %v", err, tt.wantErr)
			}
			if _, err := store.TermByID(context.Background(), target.ID); err != nil {
				t.Errorf("in-use term was deleted anyway: %v", err)
			}
		})
	}
}

func TestServiceDeleteMissingTerm(t *testing.T) {
	svc, _, _ := newSeededService(t)
	if err := svc.Delete(context.Background(), "no-such-id"); !errors.Is(err, taxonomy.ErrNotFound) {
		t.Errorf("Delete missing = %v, want ErrNotFound", err)
	}
}

func TestServiceClassifyReadsLiveVocabulary(t *testing.T) {
	// The ingest seam closes the admin loop: a term created after the
	// service was built must reach the next classification.
	svc, _, _ := newSeededService(t)
	ctx := context.Background()

	if got := svc.Classify(ctx, "Goat census"); got != nil {
		t.Errorf("classification with no goats term = %+v, want none", got)
	}

	term := &taxonomy.Term{Category: taxonomy.CategoryCrop, Label: "Goats", Keywords: []string{"goat"}}
	if err := svc.Create(ctx, term); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := svc.Classify(ctx, "Goat census")
	found := false
	for _, a := range got {
		if a.Category == taxonomy.CategoryCrop && a.Value == "goats" {
			found = true
		}
	}
	if !found {
		t.Fatalf("live-vocabulary Classify = %+v, want crop/goats", got)
	}
}

func TestApplyCorrections(t *testing.T) {
	svc, _, terms := newSeededService(t)
	dairy := findTerm(t, terms, taxonomy.CategoryCrop, "dairy")
	galway := findTerm(t, terms, taxonomy.CategoryRegion, "co-galway")
	beef := findTerm(t, terms, taxonomy.CategoryCrop, "beef")

	t.Run("replaces the whole set with org-sourced stamps", func(t *testing.T) {
		got, err := svc.ApplyCorrections(context.Background(), []taxonomy.Correction{
			{Category: taxonomy.CategoryCrop, TermID: beef.ID},
			{Category: taxonomy.CategoryRegion, TermID: galway.ID},
		})
		if err != nil {
			t.Fatalf("ApplyCorrections: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("assignments = %d, want 2", len(got))
		}
		for _, a := range got {
			if a.Source != taxonomy.SourceOrg || a.Confidence != 1 {
				t.Errorf("correction %+v not stamped org/1", a)
			}
			if a.AssignedAt.IsZero() {
				t.Errorf("correction %+v carries no timestamp", a)
			}
		}
		// Values are re-derived from the store, never trusted from the wire.
		// Output follows the fixed Categories order (crop before region).
		if got[0].Category != taxonomy.CategoryCrop || got[0].TermID != beef.ID {
			t.Errorf("first correction = %+v, want the crop/beef term", got[0])
		}
		if got[1].Value != galway.Value || got[1].Label != galway.Label {
			t.Errorf("region correction = %+v, want the stored term's identity", got[1])
		}
	})

	t.Run("empty corrections clear the categories", func(t *testing.T) {
		got, err := svc.ApplyCorrections(context.Background(), nil)
		if err != nil {
			t.Fatalf("ApplyCorrections(nil): %v", err)
		}
		if len(got) != 0 {
			t.Errorf("nil corrections = %+v, want empty", got)
		}
	})

	t.Run("term from another category is refused", func(t *testing.T) {
		_, err := svc.ApplyCorrections(context.Background(), []taxonomy.Correction{
			{Category: taxonomy.CategoryRegion, TermID: dairy.ID}, // dairy is a crop
		})
		if err == nil {
			t.Error("cross-category correction accepted")
		}
	})

	t.Run("duplicate category is refused", func(t *testing.T) {
		_, err := svc.ApplyCorrections(context.Background(), []taxonomy.Correction{
			{Category: taxonomy.CategoryCrop, TermID: dairy.ID},
			{Category: taxonomy.CategoryCrop, TermID: beef.ID},
		})
		if err == nil {
			t.Error("duplicate-category correction accepted")
		}
	})

	t.Run("unknown term is refused", func(t *testing.T) {
		_, err := svc.ApplyCorrections(context.Background(), []taxonomy.Correction{
			{Category: taxonomy.CategoryCrop, TermID: "ghost"},
		})
		if err == nil {
			t.Error("unknown-term correction accepted")
		}
	})
}
