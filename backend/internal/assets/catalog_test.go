package assets_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/anonymize"
	"github.com/syaikhipin/entrustdn-final/backend/internal/assets"
	"github.com/syaikhipin/entrustdn-final/backend/internal/objectstore"
	"github.com/syaikhipin/entrustdn-final/backend/internal/taxonomy"
)

// The ticket-06 surfaces of the assets service: auto-categorization at
// ingest, org corrections, and the consumer catalog with facets.

// stubCategorizer pins the Categorizer seam: whatever it is handed, it
// returns the same assignments.
type stubCategorizer struct {
	gotText string
	calls   int
	out     []taxonomy.Assignment
}

func (s *stubCategorizer) Classify(_ context.Context, text string) []taxonomy.Assignment {
	s.calls++
	s.gotText = text
	return s.out
}

// catalogTestHelpers: the small doubles these tests need.
func newMemoryBlobs(t *testing.T) *objectstore.Memory {
	t.Helper()
	return objectstore.NewMemory()
}

func stringReader(s string) *strings.Reader { return strings.NewReader(s) }

func mustTime(t *testing.T, iso string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		t.Fatalf("parse %s: %v", iso, err)
	}
	return ts
}

func findTaxTerm(t *testing.T, terms []taxonomy.Term, cat taxonomy.Category, value string) taxonomy.Term {
	t.Helper()
	for _, term := range terms {
		if term.Category == cat && term.Value == value {
			return term
		}
	}
	t.Fatalf("no %s/%s term in the test vocabulary", cat, value)
	return taxonomy.Term{}
}

func facetByCategory(f assets.Facets, cat taxonomy.Category) assets.FacetValues { return f[cat] }

// newCategorizedService returns an ingest service with the stub wired and
// its stores.
func newCategorizedService(t *testing.T) (*assets.Service, *assets.MemoryStore, *stubCategorizer) {
	t.Helper()
	meta := assets.NewMemoryStore()
	blobs := newMemoryBlobs(t)
	stub := &stubCategorizer{}
	svc := assets.NewService(meta, blobs, assets.NewPipeline(anonymize.NewStage(anonymize.NewMemoryMap()))).
		WithCategorizer(stub)
	return svc, meta, stub
}

func TestIngestStampsClassifierAssignments(t *testing.T) {
	svc, _, stub := newCategorizedService(t)
	stub.out = []taxonomy.Assignment{
		{Category: taxonomy.CategoryCrop, TermID: "t1", Value: "dairy", Label: "Dairy", Confidence: 0.8, Source: taxonomy.SourceClassifier},
	}

	a := &assets.Asset{OrgID: "org-1", Name: "Herd registry", Description: "Friesian milking records"}
	res, err := svc.Ingest(context.Background(), a, stringReader("col1,col2\n1,2\n"))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(res.Asset.Categories) != 1 || res.Asset.Categories[0].Value != "dairy" {
		t.Fatalf("Categories = %+v, want the classifier's dairy stamp", res.Asset.Categories)
	}
	if stub.calls != 1 {
		t.Errorf("classifier called %d times, want 1", stub.calls)
	}
	// The classification text covers name, description, and provenance.
	a2 := &assets.Asset{OrgID: "org-1", Name: "Herd registry", Description: "Friesian milking records",
		Provenance: assets.Provenance{Source: "co-op collection"}}
	if _, err := svc.Ingest(context.Background(), a2, stringReader("col1,col2\n1,2\n")); err != nil {
		t.Fatalf("Ingest 2: %v", err)
	}
	for _, want := range []string{"Herd registry", "Friesian milking records", "co-op collection"} {
		if !strings.Contains(stub.gotText, want) {
			t.Errorf("classification text %q lacks %q", stub.gotText, want)
		}
	}
}

func TestIngestWithoutCategorizerLeavesCategoriesEmpty(t *testing.T) {
	meta := assets.NewMemoryStore()
	svc := assets.NewService(meta, newMemoryBlobs(t), assets.NewPipeline(anonymize.NewStage(anonymize.NewMemoryMap())))
	res, err := svc.Ingest(context.Background(), &assets.Asset{OrgID: "org-1", Name: "Notes"},
		stringReader("data\n"))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Asset.Categories != nil {
		t.Errorf("Categories = %+v, want nil without a categorizer", res.Asset.Categories)
	}
}

func TestSetCategoriesAppliesAndScopesCorrections(t *testing.T) {
	taxStore := taxonomy.NewMemoryStore()
	if _, err := taxonomy.Seed(context.Background(), taxStore); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	terms, _ := taxStore.Terms(context.Background())
	dairy := findTaxTerm(t, terms, taxonomy.CategoryCrop, "dairy")
	galway := findTaxTerm(t, terms, taxonomy.CategoryRegion, "co-galway")
	txo := taxonomy.NewService(taxStore, taxonomy.Counters{})

	svc, _, stub := newCategorizedService(t)
	stub.out = []taxonomy.Assignment{
		{Category: taxonomy.CategoryCrop, TermID: dairy.ID, Value: "dairy", Label: "Dairy", Confidence: 0.8, Source: taxonomy.SourceClassifier},
	}
	ingested, err := svc.Ingest(context.Background(), &assets.Asset{OrgID: "org-1", Name: "Herd registry"},
		stringReader("col\n1\n"))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	id := ingested.Asset.ID

	// The org corrects: same crop, adds region.
	corrected, err := svc.SetCategories(context.Background(), id, "org-1", txo, []taxonomy.Correction{
		{Category: taxonomy.CategoryCrop, TermID: dairy.ID},
		{Category: taxonomy.CategoryRegion, TermID: galway.ID},
	})
	if err != nil {
		t.Fatalf("SetCategories: %v", err)
	}
	if len(corrected.Categories) != 2 {
		t.Fatalf("Categories = %+v, want crop+region", corrected.Categories)
	}
	for _, c := range corrected.Categories {
		if c.Source != taxonomy.SourceOrg || c.Confidence != 1 {
			t.Errorf("corrected assignment %+v not org-sourced with confidence 1", c)
		}
	}
	// The correction persisted.
	reloaded, err := svc.Get(context.Background(), id)
	if err != nil || len(reloaded.Categories) != 2 {
		t.Errorf("reloaded Categories = %+v (%v), want the correction persisted", reloaded.Categories, err)
	}

	// A stranger's correction is refused.
	if _, err := svc.SetCategories(context.Background(), id, "org-2", txo, nil); err != assets.ErrForbidden {
		t.Errorf("stranger SetCategories error = %v, want ErrForbidden", err)
	}

	// An unknown term is a bad-category refusal, not a server fault.
	_, err = svc.SetCategories(context.Background(), id, "org-1", txo, []taxonomy.Correction{
		{Category: taxonomy.CategoryCrop, TermID: "ghost"},
	})
	if !assets.IsBadCategory(err) {
		t.Errorf("ghost-term SetCategories error = %v, want ErrBadCategory", err)
	}
}

func TestCatalogAndFacets(t *testing.T) {
	meta := assets.NewMemoryStore()
	seed := []assets.Asset{
		{ID: "a-dairy-galway", OrgID: "org-1", Name: "Herd A", CreatedAt: mustTime(t, "2026-09-01T10:00:00Z"), Categories: []taxonomy.Assignment{
			{Category: taxonomy.CategoryCrop, TermID: "t-dairy", Value: "dairy", Label: "Dairy"},
			{Category: taxonomy.CategoryRegion, TermID: "t-galway", Value: "co-galway", Label: "County Galway"},
		}},
		{ID: "a-dairy-cork", OrgID: "org-2", Name: "Herd B", CreatedAt: mustTime(t, "2026-09-02T10:00:00Z"), Categories: []taxonomy.Assignment{
			{Category: taxonomy.CategoryCrop, TermID: "t-dairy", Value: "dairy", Label: "Dairy"},
			{Category: taxonomy.CategoryRegion, TermID: "t-cork", Value: "co-cork", Label: "County Cork"},
		}},
		{ID: "a-beef", OrgID: "org-2", Name: "Suckler C", CreatedAt: mustTime(t, "2026-09-03T10:00:00Z"), Categories: []taxonomy.Assignment{
			{Category: taxonomy.CategoryCrop, TermID: "t-beef", Value: "beef", Label: "Beef"},
		}},
		{ID: "a-uncategorized", OrgID: "org-3", Name: "Notes", CreatedAt: mustTime(t, "2026-09-04T10:00:00Z")},
	}
	for i := range seed {
		if err := meta.CreateAsset(context.Background(), &seed[i]); err != nil {
			t.Fatalf("seed %s: %v", seed[i].ID, err)
		}
	}
	svc := assets.NewService(meta, newMemoryBlobs(t), assets.NewPipeline(anonymize.NewStage(anonymize.NewMemoryMap())))

	entries, facets, err := svc.Catalog(context.Background(), 5_000_000)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("catalog = %d entries, want 4 (every org's assets)", len(entries))
	}
	// Newest first.
	wantOrder := []string{"a-uncategorized", "a-beef", "a-dairy-cork", "a-dairy-galway"}
	for i, id := range wantOrder {
		if entries[i].ID != id {
			t.Errorf("entries[%d] = %s, want %s", i, entries[i].ID, id)
		}
	}
	if entries[0].CachedPriceMicros != 5_000_000 {
		t.Errorf("cached price = %d, want the caller's 5_000_000", entries[0].CachedPriceMicros)
	}

	// Facets count per term across the whole catalog: dairy 2, beef 1,
	// galway 1, cork 1 — and dairy tops the crop facet by count.
	crop := facetByCategory(facets, taxonomy.CategoryCrop)
	if len(crop) != 2 || crop[0].Value != "dairy" || crop[0].Count != 2 {
		t.Errorf("crop facet = %+v, want dairy(2) then beef(1)", crop)
	}
	region := facetByCategory(facets, taxonomy.CategoryRegion)
	if len(region) != 2 {
		t.Fatalf("region facet = %+v, want galway+cork", region)
	}
	// Terms no asset uses are absent.
	if _, ok := facets[taxonomy.CategoryOutcome]; ok {
		t.Errorf("outcome facet = %+v, want absent (nothing carries it)", facets[taxonomy.CategoryOutcome])
	}
}
