package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Ticket 05's defining tests at Seam 1: adversarial fixtures containing
// names, phones, emails, and field coordinates go up through the upload
// API, and the assertions are identity-leak assertions — none of them
// survive a download. The pseudonym-map endpoints (list, erase) are also
// proven here: erase is the GDPR surface, and neither endpoint can ever
// return a raw identifier.

func TestUploadRoundTripLeaksNoIdentity(t *testing.T) {
	srv, _, mail, _ := newAssetsServer(t)
	_, token := newApprovedOrg(t, srv, mail, "leak-check-org@example.org")

	adversarial := "name,phone,email,latitude,longitude,notes\n" +
		"Mary Byrne,087 123 4567,mary.byrne@farm.ie,52.123456,-8.654321,reached via Dr John Smith\n" +
		"Pat Smith,0869876543,pat.smith@farm.ie,51.987654,-9.123456,call +353 1 234 5678\n"
	code, doc := uploadAsset(t, srv, token, map[string]string{
		"name": "Adversarial herd book", "source": "co-op registry 2026",
	}, "adversarial.csv", adversarial)
	if code != http.StatusCreated {
		t.Fatalf("upload = %d (doc: %v)", code, doc)
	}
	a := assetFromDoc(t, doc)
	assetID := a["id"].(string)

	// The upload record itself reports the anonymize stage ran.
	pipeline, ok := a["pipeline"].([]any)
	if !ok || len(pipeline) != 1 || pipeline[0] != "anonymize" {
		t.Fatalf("pipeline = %v, want [anonymize]", a["pipeline"])
	}

	// The org's own download comes back clean — the stored bytes are the
	// delivered bytes (ADR 0005: cleaned at ingest, not at read time).
	// Pseudonym tokens are longer than the names they replace, so the
	// cleaned size differs from the raw upload — the record must state the
	// cleaned byte count, not the upload's.
	downloaded := downloadAsset(t, srv, token, assetID)
	if int(a["size_bytes"].(float64)) != len(downloaded) {
		t.Errorf("size_bytes = %v, want the cleaned download length %d", a["size_bytes"], len(downloaded))
	}
	for _, needle := range []string{
		"Mary Byrne", "Pat Smith", "John Smith",
		"087 123 4567", "0869876543", "+353 1 234 5678",
		"mary.byrne@farm.ie", "pat.smith@farm.ie",
		"52.123456", "-8.654321", "51.987654", "-9.123456",
	} {
		if bytes.Contains(downloaded, []byte(needle)) {
			t.Errorf("IDENTITY LEAK: %q survives the round trip:\n%s", needle, downloaded)
		}
	}
}

func downloadAsset(t *testing.T, srv *httptest.Server, token, id string) []byte {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/assets/"+id+"/content", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read download: %v", err)
	}
	return body
}

func TestPseudonymMapEndpointsListAndErase(t *testing.T) {
	srv, _, mail, _ := newAssetsServer(t)
	_, token := newApprovedOrg(t, srv, mail, "map-org@example.org")

	// An upload seeds the map: one name column, two farmers.
	uploadAsset(t, srv, token, map[string]string{"name": "map seed"},
		"farmers.csv", "name,yield\nMary Byrne,9.2\nPat Smith,8.1\n")

	// List: kinds and pseudonyms only, count reported.
	code, doc := getJSON(t, srv, "/api/v1/pseudonyms", token)
	if code != http.StatusOK {
		t.Fatalf("GET pseudonyms = %d (doc: %v)", code, doc)
	}
	rows, ok := doc["pseudonyms"].([]any)
	if !ok {
		t.Fatalf("pseudonyms = %v, want an array", doc["pseudonyms"])
	}
	if len(rows) != 2 {
		t.Fatalf("pseudonyms = %v, want two rows (one per farmer)", rows)
	}
	for _, row := range rows {
		r := row.(map[string]any)
		if r["kind"] != "name" {
			t.Errorf("row kind = %v, want name", r["kind"])
		}
		if p, ok := r["pseudonym"].(string); !ok || p == "" || p == "Mary Byrne" || p == "Pat Smith" {
			t.Errorf("row pseudonym = %v, want an opaque token", r["pseudonym"])
		}
	}
	if doc["total"].(float64) != 2 {
		t.Errorf("total = %v, want 2", doc["total"])
	}

	// Erase: the GDPR surface. The map empties.
	code, doc = deleteJSON(t, srv, "/api/v1/pseudonyms", token)
	if code != http.StatusOK || doc["erased"] != true {
		t.Fatalf("DELETE pseudonyms = (%d, %v), want (200, erased)", code, doc)
	}
	if _, doc = getJSON(t, srv, "/api/v1/pseudonyms", token); doc["total"].(float64) != 0 {
		t.Errorf("total after erase = %v, want 0", doc["total"])
	}

	// A stranger's token never sees another org's map.
	_, strangerToken := newApprovedOrg(t, srv, mail, "map-stranger@example.org")
	_, doc = getJSON(t, srv, "/api/v1/pseudonyms", strangerToken)
	if doc["total"].(float64) != 0 {
		t.Errorf("stranger sees total %v — map leaked across orgs", doc["total"])
	}
}

func getJSON(t *testing.T, srv *httptest.Server, path, token string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return doJSON(t, srv, req)
}

func deleteJSON(t *testing.T, srv *httptest.Server, path, token string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return doJSON(t, srv, req)
}

func doJSON(t *testing.T, srv *httptest.Server, req *http.Request) (int, map[string]any) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	var doc map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("decode %s %s response: %v", req.Method, req.URL, err)
	}
	return resp.StatusCode, doc
}
