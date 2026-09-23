package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/agentclient"
	"github.com/syaikhipin/entrustdn-final/backend/internal/anonymize"
	"github.com/syaikhipin/entrustdn-final/backend/internal/api"
	"github.com/syaikhipin/entrustdn-final/backend/internal/assets"
	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/mailsink"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
	"github.com/syaikhipin/entrustdn-final/backend/internal/objectstore"
	"github.com/syaikhipin/entrustdn-final/backend/internal/taxonomy"
)

// Taxonomy & catalog (ticket 06) at Seam 1: the admin CRUDs the vocabulary,
// uploads auto-categorize, the owning org corrects, and the Data Consumer
// browses facets with cached pricing. The demo thread is the ticket's
// demoable: upload → auto-categorized → findable by facet search as
// another user.

// newTaxonomyServer boots the API with in-memory stores, the seeded
// taxonomy, a classifier-backed ingest service, and a price book (a
// closure over a settable variable, so tests can change prices). The mail
// buffer comes back too — registration's verification tokens land there.
func newTaxonomyServer(t *testing.T) (*httptest.Server, *taxonomy.MemoryStore, *assets.MemoryStore, *bytes.Buffer) {
	t.Helper()
	agent := fakeAgent(t, "0.1.0")
	memStore := membership.NewMemoryStore()
	if err := memStore.PublishTOS(t.Context(), membership.TOSVersion{Version: "1.0", Body: "Be good."}); err != nil {
		t.Fatalf("seed TOS: %v", err)
	}
	provisionAdmin(t, memStore, "admin@thresh.dev")

	taxStore := taxonomy.NewMemoryStore()
	if _, err := taxonomy.Seed(t.Context(), taxStore); err != nil {
		t.Fatalf("seed taxonomy: %v", err)
	}
	terms, err := taxStore.Terms(t.Context())
	if err != nil {
		t.Fatalf("list terms: %v", err)
	}
	taxSvc := taxonomy.NewService(taxStore, taxonomy.Counters{})

	mail := &bytes.Buffer{}
	metaStore := assets.NewMemoryStore()
	svc := assets.NewService(metaStore, objectstore.NewMemory(),
		assets.NewPipeline(anonymize.NewStage(anonymize.NewMemoryMap()))).
		WithCategorizer(taxonomy.NewClassifier(terms))

	rules := credits.PricingRules{Data: credits.DataRules{CachedAssetMicrosPerUnit: 2_500_000}}
	srv := httptest.NewServer(api.NewHandler(api.Deps{
		Agent:   agentclient.New(agent.URL),
		Version: "test-backend",
		Store:   memStore,
		Mail:    mailsink.NewLogSink(mail),
		Assets:  &api.AssetsDeps{Service: svc},
		Taxonomy: &api.TaxonomyDeps{
			Service: taxSvc,
			Rules:   func(ctx context.Context) (credits.PricingRules, error) { return rules, nil },
		},
	}))
	t.Cleanup(srv.Close)
	return srv, taxStore, metaStore, mail
}

func TestPublicTermsListRequiresNoAuth(t *testing.T) {
	srv, _, _, _ := newTaxonomyServer(t)
	code, doc := getJSON(t, srv, "/api/v1/taxonomy/terms", "")
	if code != http.StatusOK {
		t.Fatalf("public terms = %d, want 200", code)
	}
	terms, ok := doc["terms"].([]any)
	if !ok || len(terms) == 0 {
		t.Fatalf("terms = %v, want the seeded vocabulary", doc["terms"])
	}
	first, _ := terms[0].(map[string]any)
	if first["category"] == "" || first["value"] == "" || first["label"] == "" {
		t.Errorf("term doc = %v, want category+value+label", first)
	}
}

func TestAdminTaxonomyCRUD(t *testing.T) {
	srv, _, _, mail := newTaxonomyServer(t)
	adminToken := loginWith(t, srv, "admin@thresh.dev", "admin-pass-2026")

	// Create: category+value+label.
	code, doc := postWithToken(t, srv, "/api/v1/admin/taxonomy/terms", adminToken, map[string]any{
		"category": "region", "label": "County Leitrim", "keywords": []string{"leitrim"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create term = %d (doc: %v)", code, doc)
	}
	term := doc["term"].(map[string]any)
	if term["value"] != "county-leitrim" {
		t.Errorf("value = %v, want derived county-leitrim", term["value"])
	}
	termID := term["id"].(string)

	// Duplicate pair is a 409.
	code, _ = postWithToken(t, srv, "/api/v1/admin/taxonomy/terms", adminToken, map[string]any{
		"category": "region", "value": "county-leitrim", "label": "Leitrim again",
	})
	if code != http.StatusConflict {
		t.Errorf("duplicate create = %d, want 409", code)
	}

	// Malformed term is a 400.
	code, _ = postWithToken(t, srv, "/api/v1/admin/taxonomy/terms", adminToken, map[string]any{
		"category": "mood", "label": "Vibes",
	})
	if code != http.StatusBadRequest {
		t.Errorf("bad category create = %d, want 400", code)
	}

	// Update label/keywords.
	code, doc = patchWithToken(t, srv, "/api/v1/admin/taxonomy/terms/"+termID, adminToken, map[string]any{
		"label": "Leitrim (border)", "keywords": []string{"leitrim", "border"},
	})
	if code != http.StatusOK {
		t.Fatalf("update term = %d (doc: %v)", code, doc)
	}
	term = doc["term"].(map[string]any)
	if term["label"] != "Leitrim (border)" {
		t.Errorf("label = %v, want the update", term["label"])
	}

	// Delete: unused → 200; unknown → 404.
	code, _ = deleteWithToken(t, srv, "/api/v1/admin/taxonomy/terms/"+termID, adminToken)
	if code != http.StatusOK {
		t.Errorf("delete unused term = %d, want 200", code)
	}
	code, _ = deleteWithToken(t, srv, "/api/v1/admin/taxonomy/terms/"+termID, adminToken)
	if code != http.StatusNotFound {
		t.Errorf("delete missing term = %d, want 404", code)
	}

	// Non-admins are refused.
	_, orgToken := newApprovedOrg(t, srv, mail, "crud-org@example.org")
	code, _ = postWithToken(t, srv, "/api/v1/admin/taxonomy/terms", orgToken, map[string]any{
		"category": "region", "label": "County Roscommon",
	})
	if code != http.StatusForbidden {
		t.Errorf("create as org = %d, want 403", code)
	}
}

func TestUploadAutoCategorizesAndOrgCorrects(t *testing.T) {
	srv, taxStore, _, mail := newTaxonomyServer(t)
	_, orgToken := newApprovedOrg(t, srv, mail, "demo-org@example.org")

	// Upload a dairy-herd registry from Galway: the classifier stamps it.
	code, doc := uploadAsset(t, srv, orgToken, map[string]string{
		"name": "Herd registry", "description": "Friesian milking herd records, County Galway",
	}, "herd.csv", "tag,breed\n1,friesian\n")
	if code != http.StatusCreated {
		t.Fatalf("upload = %d (doc: %v)", code, doc)
	}
	a := assetFromDoc(t, doc)
	cats, _ := a["categories"].([]any)
	byCat := map[string]map[string]any{}
	for _, c := range cats {
		cm, _ := c.(map[string]any)
		byCat[cm["category"].(string)] = cm
	}
	if byCat["crop"] == nil || byCat["crop"]["value"] != "dairy" {
		t.Errorf("crop category = %v, want auto-assigned dairy", byCat["crop"])
	}
	if byCat["region"] == nil || byCat["region"]["value"] != "co-galway" {
		t.Errorf("region category = %v, want auto-assigned co-galway", byCat["region"])
	}
	if byCat["crop"]["source"] != "classifier" || byCat["crop"]["confidence"].(float64) <= 0 {
		t.Errorf("crop stamp = %v, want classifier source with confidence", byCat["crop"])
	}

	// The org corrects: crop → beef, keep nothing else.
	terms, _ := taxStore.Terms(t.Context())
	var beefID string
	for _, term := range terms {
		if term.Category == taxonomy.CategoryCrop && term.Value == "beef" {
			beefID = term.ID
		}
	}
	code, doc = putWithToken(t, srv, "/api/v1/assets/"+a["id"].(string)+"/categories", orgToken, map[string]any{
		"categories": []map[string]any{{"category": "crop", "term_id": beefID}},
	})
	if code != http.StatusOK {
		t.Fatalf("correct categories = %d (doc: %v)", code, doc)
	}
	corrected := assetFromDoc(t, doc)
	cats, _ = corrected["categories"].([]any)
	if len(cats) != 1 || cats[0].(map[string]any)["value"] != "beef" {
		t.Fatalf("corrected categories = %v, want only beef", corrected["categories"])
	}
	if cats[0].(map[string]any)["source"] != "org" || cats[0].(map[string]any)["confidence"].(float64) != 1 {
		t.Errorf("corrected stamp = %v, want org source with confidence 1", cats[0])
	}

	// A stranger's correction is a 404; a ghost term is a 422.
	_, strangerToken := newApprovedOrg(t, srv, mail, "stranger-org@example.org")
	code, _ = putWithToken(t, srv, "/api/v1/assets/"+a["id"].(string)+"/categories", strangerToken, map[string]any{
		"categories": []map[string]any{{"category": "crop", "term_id": beefID}},
	})
	if code != http.StatusNotFound {
		t.Errorf("stranger correction = %d, want 404", code)
	}
	code, _ = putWithToken(t, srv, "/api/v1/assets/"+a["id"].(string)+"/categories", orgToken, map[string]any{
		"categories": []map[string]any{{"category": "crop", "term_id": "ghost"}},
	})
	if code != http.StatusUnprocessableEntity {
		t.Errorf("ghost-term correction = %d, want 422", code)
	}
}

func TestConsumerCatalogFacetsAndPricing(t *testing.T) {
	srv, _, _, mail := newTaxonomyServer(t)
	_, orgToken := newApprovedOrg(t, srv, mail, "catalog-org@example.org")
	seedAsset(t, srv, orgToken, "Friesian milking herd registry") // dairy-ish text
	seedAsset(t, srv, orgToken, "Angus suckler beef records")     // beef-ish text

	_, consumerToken := newConsumer(t, srv, mail, "browser@example.org")
	code, doc := getJSON(t, srv, "/api/v1/catalog", consumerToken)
	if code != http.StatusOK {
		t.Fatalf("catalog = %d (doc: %v)", code, doc)
	}
	entries, _ := doc["assets"].([]any)
	if len(entries) != 2 {
		t.Fatalf("catalog = %d entries, want 2", len(entries))
	}
	first, _ := entries[0].(map[string]any)
	if first["cached_price_micros"].(float64) != 2_500_000 {
		t.Errorf("cached_price_micros = %v, want the price book's 2.5M", first["cached_price_micros"])
	}
	if _, has := first["org_id"]; has {
		t.Error("catalog entry leaks org_id")
	}
	if _, has := first["object_key"]; has {
		t.Error("catalog entry leaks object_key — ADR 0006 violation")
	}
	facets, _ := doc["facets"].(map[string]any)
	cropFacets, _ := facets["crop"].([]any)
	if len(cropFacets) == 0 {
		t.Fatalf("crop facet = %v, want computed facets", facets)
	}

	// The catalog is consumer-only.
	code, _ = getJSON(t, srv, "/api/v1/catalog", orgToken)
	if code != http.StatusForbidden {
		t.Errorf("catalog as org = %d, want 403", code)
	}
}

func TestFacetFindabilityDemoThread(t *testing.T) {
	// The ticket's demoable, end to end: one org uploads; another user
	// finds it by facet search.
	srv, _, _, mail := newTaxonomyServer(t)
	_, orgToken := newApprovedOrg(t, srv, mail, "uploader-org@example.org")
	_, consumerToken := newConsumer(t, srv, mail, "finder@example.org")

	uploaded := seedAsset(t, srv, orgToken, "Spring barley yield trial Galway")

	_, doc := getJSON(t, srv, "/api/v1/catalog", consumerToken)
	entries, _ := doc["assets"].([]any)
	found := false
	for _, e := range entries {
		em, _ := e.(map[string]any)
		if em["id"] != uploaded["id"] {
			continue
		}
		cats, _ := em["categories"].([]any)
		for _, c := range cats {
			cm, _ := c.(map[string]any)
			if cm["category"] == "crop" && cm["value"] == "tillage" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("uploaded asset not findable under crop=tillage: %+v", entries)
	}
}

// putWithToken covers the one method the other test files' helpers lack.
func putWithToken(t *testing.T, srv *httptest.Server, path, token string, body any) (int, map[string]any) {
	t.Helper()
	return doWithToken(t, http.MethodPut, srv, path, token, body)
}

func doWithToken(t *testing.T, method string, srv *httptest.Server, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, srv.URL+path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	return decodeResp(t, resp)
}
