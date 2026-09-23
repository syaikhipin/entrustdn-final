package taxonomy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/taxonomy"
)

// The taxonomy domain (ticket 06): categories and terms, the Irish seed,
// and the CRUD contract over the in-memory double.

func TestCategoryValid(t *testing.T) {
	tests := []struct {
		cat  taxonomy.Category
		want bool
	}{
		{taxonomy.CategoryCrop, true},
		{taxonomy.CategoryRegion, true},
		{taxonomy.CategoryGrowthStage, true},
		{taxonomy.CategoryIntervention, true},
		{taxonomy.CategoryOutcome, true},
		{taxonomy.CategoryDataType, true},
		{taxonomy.Category("livestock"), false},
		{taxonomy.Category(""), false},
	}
	for _, tt := range tests {
		if got := tt.cat.Valid(); got != tt.want {
			t.Errorf("%q.Valid() = %v, want %v", tt.cat, got, tt.want)
		}
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"County Galway", "county-galway"},
		{"  Dairy  ", "dairy"},
		{"Soil health!", "soil-health"},
		{"Friesian/Holstein herd", "friesian-holstein-herd"},
		{"---", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := taxonomy.Slugify(tt.in); got != tt.want {
			t.Errorf("Slugify(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTermValidate(t *testing.T) {
	tests := []struct {
		name    string
		term    taxonomy.Term
		wantErr bool
	}{
		{name: "valid", term: taxonomy.Term{Category: taxonomy.CategoryCrop, Value: "dairy", Label: "Dairy"}},
		{name: "unknown category", term: taxonomy.Term{Category: "nope", Value: "dairy", Label: "Dairy"}, wantErr: true},
		{name: "value not a slug", term: taxonomy.Term{Category: taxonomy.CategoryCrop, Value: "Dairy!", Label: "Dairy"}, wantErr: true},
		{name: "empty value", term: taxonomy.Term{Category: taxonomy.CategoryCrop, Value: "", Label: "Dairy"}, wantErr: true},
		{name: "no label", term: taxonomy.Term{Category: taxonomy.CategoryCrop, Value: "dairy", Label: "  "}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.term.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSeedIsIdempotentAndCoversIreland(t *testing.T) {
	store := taxonomy.NewMemoryStore()

	first, err := taxonomy.Seed(context.Background(), store)
	if err != nil {
		t.Fatalf("first Seed: %v", err)
	}
	if first.Created == 0 || first.Skipped != 0 {
		t.Errorf("first Seed = %+v, want everything created", first)
	}

	// Second pass: every (category, value) pair is taken.
	second, err := taxonomy.Seed(context.Background(), store)
	if err != nil {
		t.Fatalf("second Seed: %v", err)
	}
	if second.Created != 0 || second.Skipped != first.Created {
		t.Errorf("second Seed = %+v, want all skipped", second)
	}

	terms, err := store.Terms(context.Background())
	if err != nil {
		t.Fatalf("Terms: %v", err)
	}
	byCategory := map[taxonomy.Category]int{}
	for _, term := range terms {
		byCategory[term.Category]++
		if term.Keywords == nil {
			t.Errorf("seed term %s/%s carries no keywords", term.Category, term.Value)
		}
	}
	// The six axes exist, regions cover counties, and the crop profile has
	// the dairy/beef/tillage spine (ticket 06's Irish profile).
	for _, cat := range taxonomy.Categories {
		if byCategory[cat] == 0 {
			t.Errorf("seed covers no %q terms", cat)
		}
	}
	values := map[string]bool{}
	for _, term := range terms {
		if term.Category == taxonomy.CategoryRegion {
			values[term.Value] = true
		}
	}
	for _, county := range []string{"co-galway", "co-cork", "co-mayo"} {
		if !values[county] {
			t.Errorf("Irish seed lacks region %q", county)
		}
	}
	for _, crop := range []string{"dairy", "beef", "tillage"} {
		found := false
		for _, term := range terms {
			if term.Category == taxonomy.CategoryCrop && term.Value == crop {
				found = true
			}
		}
		if !found {
			t.Errorf("Irish seed lacks crop %q", crop)
		}
	}
}

func TestSeedIfEmptyLeavesExistingTermsAlone(t *testing.T) {
	store := taxonomy.NewMemoryStore()
	if _, err := taxonomy.Seed(context.Background(), store); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	// The admin removes a term and adds their own.
	terms, _ := store.Terms(context.Background())
	if err := store.DeleteTerm(context.Background(), terms[0].ID); err != nil {
		t.Fatalf("DeleteTerm: %v", err)
	}
	custom := taxonomy.Term{Category: taxonomy.CategoryCrop, Value: "goats", Label: "Goats"}
	if err := store.CreateTerm(context.Background(), &custom); err != nil {
		t.Fatalf("CreateTerm: %v", err)
	}

	res, err := taxonomy.SeedIfEmpty(context.Background(), store)
	if err != nil {
		t.Fatalf("SeedIfEmpty: %v", err)
	}
	if res.Created != 0 {
		t.Errorf("SeedIfEmpty created %d terms over an existing vocabulary", res.Created)
	}
	after, _ := store.Terms(context.Background())
	if len(after) != len(terms) { // one removed, one added
		t.Errorf("Terms after SeedIfEmpty = %d, want %d (admin edits preserved)", len(after), len(terms))
	}
}

func TestMemoryStoreDuplicatePair(t *testing.T) {
	store := taxonomy.NewMemoryStore()
	a := taxonomy.Term{Category: taxonomy.CategoryCrop, Value: "dairy", Label: "Dairy"}
	if err := store.CreateTerm(context.Background(), &a); err != nil {
		t.Fatalf("CreateTerm: %v", err)
	}
	b := taxonomy.Term{Category: taxonomy.CategoryCrop, Value: "dairy", Label: "Dairy again"}
	err := store.CreateTerm(context.Background(), &b)
	if !errors.Is(err, taxonomy.ErrExists) {
		t.Errorf("duplicate CreateTerm error = %v, want ErrExists", err)
	}
	// Same value in another category is fine.
	c := taxonomy.Term{Category: taxonomy.CategoryDataType, Value: "dairy", Label: "Dairy records"}
	if err := store.CreateTerm(context.Background(), &c); err != nil {
		t.Errorf("same value in another category: %v", err)
	}
}

func TestDefaultKeywordsFallsBackToLabelAndValue(t *testing.T) {
	term := taxonomy.Term{Category: taxonomy.CategoryRegion, Value: "co-galway", Label: "County Galway"}
	got := term.DefaultKeywords()
	// Label words first, then the value's dash-split parts.
	want := []string{"county", "galway", "co"}
	if len(got) != len(want) {
		t.Fatalf("DefaultKeywords() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("DefaultKeywords()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Curated keywords win and are copied, not aliased.
	term.Keywords = []string{"gaeltacht"}
	got = term.DefaultKeywords()
	if len(got) != 1 || got[0] != "gaeltacht" {
		t.Errorf("DefaultKeywords() with curated keywords = %v, want [gaeltacht]", got)
	}
	got[0] = "mutated"
	if term.Keywords[0] != "gaeltacht" {
		t.Error("DefaultKeywords returned a slice aliasing the term's keywords")
	}
}
