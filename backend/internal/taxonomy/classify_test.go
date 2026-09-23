package taxonomy_test

import (
	"context"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/taxonomy"
)

// The classifier (ticket 06): keyword evidence over the asset's own
// metadata, one assignment per category, deterministic ordering.

// seededClassifier builds a classifier over the Irish seed.
func seededClassifier(t *testing.T) (*taxonomy.Classifier, *taxonomy.MemoryStore, []taxonomy.Term) {
	t.Helper()
	store := taxonomy.NewMemoryStore()
	if _, err := taxonomy.Seed(context.Background(), store); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	terms, err := store.Terms(context.Background())
	if err != nil {
		t.Fatalf("Terms: %v", err)
	}
	return taxonomy.NewClassifier(terms), store, terms
}

func TestClassifyAssignsOneTermPerCategory(t *testing.T) {
	classifier, _, terms := seededClassifier(t)

	// A dairy-herd registry from Galway: crop (dairy), region (co-galway),
	// data type (registry). No growth stage or outcome evidence.
	text := "Herd registry — Friesian milking herd records from County Galway, spring 2026"
	got := classifier.Classify(context.Background(), text)

	byCategory := map[taxonomy.Category]taxonomy.Assignment{}
	for _, a := range got {
		if _, dup := byCategory[a.Category]; dup {
			t.Fatalf("two assignments for %q: %+v", a.Category, got)
		}
		byCategory[a.Category] = a
	}
	wantCategory := map[string]string{
		"crop":      "dairy",
		"region":    "co-galway",
		"data_type": "registry",
	}
	for cat, value := range wantCategory {
		a, ok := byCategory[taxonomy.Category(cat)]
		if !ok {
			t.Fatalf("no %q assignment from %q (got %+v)", cat, text, got)
		}
		if a.Value != value {
			t.Errorf("%q assignment = %s, want %s", cat, a.Value, value)
		}
		if a.Source != taxonomy.SourceClassifier {
			t.Errorf("%q source = %q, want classifier", cat, a.Source)
		}
		if a.Confidence <= 0 || a.Confidence > 1 {
			t.Errorf("%q confidence = %v, want in (0, 1]", cat, a.Confidence)
		}
		if a.AssignedAt.IsZero() {
			t.Errorf("%q assignment carries no timestamp", cat)
		}
	}
	// Categories without evidence are absent, and the returned order is
	// the fixed category order.
	for _, cat := range []string{"growth_stage", "intervention", "outcome"} {
		if _, ok := byCategory[taxonomy.Category(cat)]; ok {
			t.Errorf("classifier assigned %q with no evidence for it", cat)
		}
	}
	if len(got) != 3 {
		t.Fatalf("assignments = %d, want 3 (%+v)", len(got), got)
	}
	// The returned order is the fixed Categories order, so consumers see a
	// stable document.
	rank := func(c taxonomy.Category) int {
		for i, known := range taxonomy.Categories {
			if known == c {
				return i
			}
		}
		return -1
	}
	for i := 1; i < len(got); i++ {
		if rank(got[i-1].Category) >= rank(got[i].Category) {
			t.Errorf("assignments not in Categories order: %+v", got)
		}
	}
	_ = terms
}

func TestClassifyTableDriven(t *testing.T) {
	classifier, _, _ := seededClassifier(t)
	tests := []struct {
		name      string
		text      string
		wantCats  map[string]string // category → value
		wantNoCpy []string          // categories that must stay empty
	}{
		{
			name:      "tillage field notes",
			text:      "Spring barley yield trial, Teagasc Oak Park, nitrogen fertiliser applied at tillering",
			wantCats:  map[string]string{"crop": "tillage", "growth_stage": "vegetative"},
			wantNoCpy: []string{"region"},
		},
		{
			name:     "beef suckler data",
			text:     "Suckler cow performance, Angus cross, dosing and vaccine records",
			wantCats: map[string]string{"crop": "beef", "intervention": "veterinary"},
		},
		{
			name:     "soil analysis",
			text:     "Soil sample analysis: pH, organic matter and compaction per paddock",
			wantCats: map[string]string{"outcome": "soil-health"},
		},
		{
			name:      "no evidence means no assignment",
			text:      "Quarterly financial accounts 2026",
			wantCats:  map[string]string{"data_type": "financial"},
			wantNoCpy: []string{"crop", "region"},
		},
		{
			name:     "empty text",
			text:     "",
			wantCats: map[string]string{},
		},
		{
			name:     "stopwords do not classify",
			text:     "the and for with from data dataset records file",
			wantCats: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifier.Classify(context.Background(), tt.text)
			byCategory := map[string]taxonomy.Assignment{}
			for _, a := range got {
				byCategory[string(a.Category)] = a
			}
			for cat, value := range tt.wantCats {
				a, ok := byCategory[cat]
				if !ok {
					t.Fatalf("no %q assignment (got %+v)", cat, got)
				}
				if a.Value != value {
					t.Errorf("%q = %s, want %s", cat, a.Value, value)
				}
			}
			for _, cat := range tt.wantNoCpy {
				if _, ok := byCategory[cat]; ok {
					t.Errorf("%q assigned with no evidence: %+v", cat, byCategory[cat])
				}
			}
		})
	}
}

func TestClassifyIsDeterministic(t *testing.T) {
	classifier, _, _ := seededClassifier(t)
	text := "Milk yield and butterfat quality records for the Friesian herd, Moorepark"
	first := classifier.Classify(context.Background(), text)
	for i := 0; i < 20; i++ {
		again := classifier.Classify(context.Background(), text)
		if len(again) != len(first) {
			t.Fatalf("run %d: %d assignments, first run had %d", i, len(again), len(first))
		}
		for j := range again {
			if again[j].Category != first[j].Category || again[j].Value != first[j].Value {
				t.Fatalf("run %d: [%d] = %+v, want %+v", i, j, again[j], first[j])
			}
		}
	}
}

func TestClassifyNilAndEmptyClassifier(t *testing.T) {
	var nilClassifier *taxonomy.Classifier
	if got := nilClassifier.Classify(context.Background(), "dairy herd"); got != nil {
		t.Errorf("nil Classify = %+v, want nil", got)
	}
	if got := taxonomy.NewClassifier(nil).Classify(context.Background(), "dairy herd"); got != nil {
		t.Errorf("empty Classify = %+v, want nil", got)
	}
}

func TestClassifyMultiWordKeywords(t *testing.T) {
	// Multi-word seed keywords ("spring barley", "milk yield", "tb test")
	// must reach classification through their parts — they were dead
	// vocabulary when the index keyed the whole phrase.
	classifier, _, _ := seededClassifier(t)
	tests := []struct {
		name     string
		text     string
		category taxonomy.Category
		want     string
	}{
		{name: "spring barley → tillage", text: "Spring barley trial results", category: taxonomy.CategoryCrop, want: "tillage"},
		{name: "milk yield → yield outcome", text: "Milk yield per cow this lactation", category: taxonomy.CategoryOutcome, want: "yield"},
		{name: "tb test → veterinary", text: "TB test dates for the herd", category: taxonomy.CategoryIntervention, want: "veterinary"},
		{name: "herd book → registry", text: "The co-op herd book extract", category: taxonomy.CategoryDataType, want: "registry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifier.Classify(context.Background(), tt.text)
			for _, a := range got {
				if a.Category == tt.category && a.Value != tt.want {
					t.Fatalf("%q classified as %s/%s, want %s", tt.text, a.Category, a.Value, tt.want)
				}
			}
			found := false
			for _, a := range got {
				if a.Category == tt.category {
					found = true
				}
			}
			if !found {
				t.Fatalf("%q gave no %q assignment (got %+v)", tt.text, tt.category, got)
			}
		})
	}
}

func findTerm(t *testing.T, terms []taxonomy.Term, cat taxonomy.Category, value string) taxonomy.Term {
	t.Helper()
	for _, term := range terms {
		if term.Category == cat && term.Value == value {
			return term
		}
	}
	t.Fatalf("no %s/%s term in the test vocabulary", cat, value)
	return taxonomy.Term{}
}
