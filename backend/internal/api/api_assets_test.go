package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/agentclient"
	"github.com/syaikhipin/entrustdn-final/backend/internal/anonymize"
	"github.com/syaikhipin/entrustdn-final/backend/internal/api"
	"github.com/syaikhipin/entrustdn-final/backend/internal/assets"
	"github.com/syaikhipin/entrustdn-final/backend/internal/mailsink"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
	"github.com/syaikhipin/entrustdn-final/backend/internal/objectstore"
)

// Data Assets (ticket 04) at Seam 1: uploads stream through the backend
// into the (in-memory) object store, downloads stream out after an
// entitlement check, and the dashboard lists/updates/deletes. ADR 0005's
// anonymize stage must appear in every asset's pipeline record; ADR 0006
// must hold: no presigned URLs, no object keys across the boundary.

// newAssetsServer boots the API with in-memory membership, asset, and blob
// stores and a provisioned admin. Returns the server, the membership store
// (for direct provisioning in tests), the mail buffer, and the blob store.
func newAssetsServer(t *testing.T) (*httptest.Server, *membership.MemoryStore, *bytes.Buffer, *objectstore.Memory) {
	t.Helper()
	agent := fakeAgent(t, "0.1.0")
	memStore := membership.NewMemoryStore()
	if err := memStore.PublishTOS(t.Context(), membership.TOSVersion{Version: "1.0", Body: "Be good."}); err != nil {
		t.Fatalf("seed TOS: %v", err)
	}
	provisionAdmin(t, memStore, "admin@thresh.dev")
	blobs := objectstore.NewMemory()
	pseudonyms := anonymize.NewMemoryMap()
	svc := assets.NewService(assets.NewMemoryStore(), blobs, assets.NewPipeline(anonymize.NewStage(pseudonyms)))
	mail := &bytes.Buffer{}
	srv := httptest.NewServer(api.NewHandler(api.Deps{
		Agent:      agentclient.New(agent.URL),
		Version:    "test-backend",
		Store:      memStore,
		Mail:       mailsink.NewLogSink(mail),
		Assets:     &api.AssetsDeps{Service: svc, Pseudonyms: &api.PseudonymDeps{Map: pseudonyms}},
	}))
	t.Cleanup(srv.Close)
	return srv, memStore, mail, blobs
}

// newApprovedOrg registers, verifies, gets admin approval, and logs in one
// Farmer Organization — the full door an org passes through before it can
// share data. Returns the account ID and session token.
func newApprovedOrg(t *testing.T, srv *httptest.Server, mail *bytes.Buffer, email string) (string, string) {
	t.Helper()
	code, doc := postJSON(t, srv, "/api/v1/register", map[string]any{
		"email": email, "password": "harvest-2026", "display_name": "Co-op", "role": "farmer_organization", "tos_version": "1.0",
	})
	if code != http.StatusCreated {
		t.Fatalf("register org: %d (doc: %v)", code, doc)
	}
	if code, _ := postJSON(t, srv, "/api/v1/verify", map[string]any{"token": verificationTokenFor(t, mail, email)}); code != http.StatusOK {
		t.Fatal("verify org failed")
	}
	orgID := doc["account"].(map[string]any)["id"].(string)

	adminToken := loginWith(t, srv, "admin@thresh.dev", "admin-pass-2026")
	if code, doc := postWithToken(t, srv, "/api/v1/admin/applications/decide", adminToken, map[string]any{
		"account_id": orgID, "decision": "approve",
	}); code != http.StatusOK {
		t.Fatalf("approve org: %d (doc: %v)", code, doc)
	}
	return orgID, loginWith(t, srv, email, "harvest-2026")
}

// uploadAsset POSTs a multipart upload the way the browser's FormData
// would: text fields plus one file part.
func uploadAsset(t *testing.T, srv *httptest.Server, token string, fields map[string]string, filename, body string) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	if filename != "" {
		fw, err := mw.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := fw.Write([]byte(body)); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/assets", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/v1/assets: %v", err)
	}
	defer resp.Body.Close()
	return decodeResp(t, resp)
}

func assetFromDoc(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	a, ok := doc["asset"].(map[string]any)
	if !ok {
		t.Fatalf("document carries no asset: %v", doc)
	}
	return a
}

// seedAsset uploads one asset for an org and returns its document.
func seedAsset(t *testing.T, srv *httptest.Server, token, name string) map[string]any {
	t.Helper()
	code, doc := uploadAsset(t, srv, token, map[string]string{"name": name}, name+".csv", "col1,col2\n1,2\n")
	if code != http.StatusCreated {
		t.Fatalf("seed asset %s = %d (doc: %v)", name, code, doc)
	}
	return assetFromDoc(t, doc)
}

func TestOrgUploadsAssetThroughBackend(t *testing.T) {
	srv, _, mail, blobs := newAssetsServer(t)
	_, token := newApprovedOrg(t, srv, mail, "upload-org@example.org")

	csv := "tag,breed,yield\n1,friesian,9.2\n2,angus,7.8\n"
	code, doc := uploadAsset(t, srv, token, map[string]string{
		"name": "Herd registry", "description": "Spring 2026 herd book",
		"source": "Teagasc Moorepark", "notes": "validated with the co-op",
	}, "herd.csv", csv)
	if code != http.StatusCreated {
		t.Fatalf("upload = %d (doc: %v)", code, doc)
	}
	a := assetFromDoc(t, doc)
	if a["name"] != "Herd registry" || a["format"] != "csv" {
		t.Errorf("asset = %v, want name+format recorded", a)
	}
	if a["size_bytes"].(float64) != float64(len(csv)) {
		t.Errorf("size_bytes = %v, want %d (the streamed byte count)", a["size_bytes"], len(csv))
	}
	pipeline, ok := a["pipeline"].([]any)
	if !ok || len(pipeline) != 1 || pipeline[0] != "anonymize" {
		t.Errorf("pipeline = %v, want [anonymize] (ADR 0005 stage recorded at ingest)", a["pipeline"])
	}
	prov, ok := a["provenance"].(map[string]any)
	if !ok || prov["source"] != "Teagasc Moorepark" {
		t.Errorf("provenance = %v, want the source recorded", a["provenance"])
	}
	if _, has := a["object_key"]; has {
		t.Error("object_key crossed the HTTP boundary — ADR 0006 violation")
	}

	// The bytes really streamed into storage, unchanged (pass-through).
	keys := blobs.List()
	if len(keys) != 1 {
		t.Fatalf("blob store has %d objects, want 1", len(keys))
	}
	blob, err := blobs.Get(t.Context(), keys[0])
	if err != nil {
		t.Fatalf("blobs.Get: %v", err)
	}
	defer blob.Body.Close()
	stored, _ := io.ReadAll(blob.Body)
	if string(stored) != csv {
		t.Errorf("stored bytes = %q, want the upload unchanged", stored)
	}
}

func TestUploadRequiresApprovedOrganization(t *testing.T) {
	tests := []struct {
		name     string
		actor    func(t *testing.T, srv *httptest.Server, mail *bytes.Buffer) string
		wantCode int
	}{
		{
			name: "anonymous caller",
			actor: func(t *testing.T, srv *httptest.Server, mail *bytes.Buffer) string {
				return ""
			},
			wantCode: http.StatusUnauthorized,
		},
		{
			name: "data consumer",
			actor: func(t *testing.T, srv *httptest.Server, mail *bytes.Buffer) string {
				_, token := newConsumer(t, srv, mail, "sneaky-consumer@example.org")
				return token
			},
			wantCode: http.StatusForbidden,
		},
		{
			name: "platform admin",
			actor: func(t *testing.T, srv *httptest.Server, mail *bytes.Buffer) string {
				return loginWith(t, srv, "admin@thresh.dev", "admin-pass-2026")
			},
			wantCode: http.StatusForbidden,
		},
		{
			name: "approved org needs no approval bypass — control",
			actor: func(t *testing.T, srv *httptest.Server, mail *bytes.Buffer) string {
				_, token := newApprovedOrg(t, srv, mail, "control-org@example.org")
				return token
			},
			wantCode: http.StatusCreated,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, mail, _ := newAssetsServer(t)
			token := tt.actor(t, srv, mail)
			code, _ := uploadAsset(t, srv, token, nil, "x.csv", "data\n")
			if code != tt.wantCode {
				t.Errorf("upload as %s = %d, want %d", tt.name, code, tt.wantCode)
			}
		})
	}
}

func TestUploadRefusesBadPayloads(t *testing.T) {
	tests := []struct {
		name     string
		fields   map[string]string
		filename string
		body     string
		wantCode int
	}{
		{name: "no file part", filename: "", body: "", wantCode: http.StatusBadRequest},
		{name: "empty file", filename: "empty.csv", body: "", wantCode: http.StatusBadRequest},
		{name: "no name and no filename", filename: "", body: "x", wantCode: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, mail, _ := newAssetsServer(t)
			_, token := newApprovedOrg(t, srv, mail, "picky-org@example.org")
			code, _ := uploadAsset(t, srv, token, map[string]string{}, tt.filename, tt.body)
			if code != tt.wantCode {
				t.Errorf("upload (%s) = %d, want %d", tt.name, code, tt.wantCode)
			}
		})
	}
}

func TestDownloadStreamsAfterEntitlementCheck(t *testing.T) {
	srv, _, mail, _ := newAssetsServer(t)
	_, ownerToken := newApprovedOrg(t, srv, mail, "owner-org@example.org")
	_, strangerToken := newApprovedOrg(t, srv, mail, "stranger-org@example.org")

	a := seedAsset(t, srv, ownerToken, "yields")
	assetID := a["id"].(string)

	t.Run("owner streams the exact bytes", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/assets/"+assetID+"/content", nil)
		req.Header.Set("Authorization", "Bearer "+ownerToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("download: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("download = %d, want 200", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "col1,col2\n1,2\n" {
			t.Errorf("streamed = %q, want the stored bytes", body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "text/csv" {
			t.Errorf("Content-Type = %q, want text/csv", ct)
		}
	})

	t.Run("another org gets a 404 that discloses nothing", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/assets/"+assetID+"/content", nil)
		req.Header.Set("Authorization", "Bearer "+strangerToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("download: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("stranger download = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("no session, no bytes", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/api/v1/assets/" + assetID + "/content")
		if err != nil {
			t.Fatalf("download: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("anonymous download = %d, want 401", resp.StatusCode)
		}
	})
}

func TestDashboardListUpdateDelete(t *testing.T) {
	srv, _, mail, blobs := newAssetsServer(t)
	_, token := newApprovedOrg(t, srv, mail, "dashboard-org@example.org")

	first := seedAsset(t, srv, token, "first")
	second := seedAsset(t, srv, token, "second")

	t.Run("dashboard lists the org's assets", func(t *testing.T) {
		code, doc := getWithToken(t, srv, "/api/v1/assets", token)
		if code != http.StatusOK {
			t.Fatalf("list = %d (doc: %v)", code, doc)
		}
		list := doc["assets"].([]any)
		if len(list) != 2 {
			t.Fatalf("dashboard shows %d assets, want 2", len(list))
		}
		names := map[string]bool{}
		for _, raw := range list {
			names[raw.(map[string]any)["name"].(string)] = true
		}
		if !names["first"] || !names["second"] {
			t.Errorf("dashboard = %v, want both assets", names)
		}
	})

	t.Run("update rewrites metadata only", func(t *testing.T) {
		id := first["id"].(string)
		code, doc := patchWithToken(t, srv, "/api/v1/assets/"+id, token, map[string]any{
			"name": "Curated herd data", "description": "cleaned spring batch",
			"source": "re-verified", "collected_at": "2026-05-01T00:00:00Z",
		})
		if code != http.StatusOK {
			t.Fatalf("update = %d (doc: %v)", code, doc)
		}
		a := assetFromDoc(t, doc)
		if a["name"] != "Curated herd data" || a["description"] != "cleaned spring batch" {
			t.Errorf("metadata not updated: %v", a)
		}
		if a["size_bytes"] != first["size_bytes"] || a["format"] != first["format"] {
			t.Errorf("immutable fields changed: %v → %v", first, a)
		}
		pipeline := a["pipeline"].([]any)
		if len(pipeline) != 1 || pipeline[0] != "anonymize" {
			t.Errorf("pipeline changed: %v", pipeline)
		}
		// updated_at renders at second granularity (RFC 3339), so a
		// same-second bump is legal and not asserted here.
	})

	t.Run("stranger sees and changes nothing", func(t *testing.T) {
		_, strangerToken := newApprovedOrg(t, srv, mail, "stranger-dash@example.org")
		id := second["id"].(string)

		getCode, _ := getWithToken(t, srv, "/api/v1/assets/"+id, strangerToken)
		patchCode, _ := patchWithToken(t, srv, "/api/v1/assets/"+id, strangerToken, map[string]any{"name": "mine now"})
		deleteCode, _ := deleteWithToken(t, srv, "/api/v1/assets/"+id, strangerToken)
		for _, tc := range []struct {
			name string
			code int
		}{
			{"get", getCode},
			{"patch", patchCode},
			{"delete", deleteCode},
			{"download", downloadCode(t, srv, id, strangerToken)},
		} {
			if tc.code != http.StatusNotFound {
				t.Errorf("stranger %s = %d, want 404 (undisclosed)", tc.name, tc.code)
			}
		}
		// The asset survived the stranger's attempts.
		code, doc := getWithToken(t, srv, "/api/v1/assets/"+id, token)
		if code != http.StatusOK {
			t.Fatalf("asset gone after stranger attempts: %d", code)
		}
		if got := assetFromDoc(t, doc)["name"]; got != "second" {
			t.Errorf("stranger's rename landed: %v", got)
		}
	})

	t.Run("delete removes object and record", func(t *testing.T) {
		id := second["id"].(string)
		code, doc := deleteWithToken(t, srv, "/api/v1/assets/"+id, token)
		if code != http.StatusOK || doc["deleted"] != true {
			t.Fatalf("delete = %d (doc: %v)", code, doc)
		}
		if code, _ := getWithToken(t, srv, "/api/v1/assets/"+id, token); code != http.StatusNotFound {
			t.Errorf("record survived delete: %d", code)
		}
		// Blob cleanup is part of delete (ticket checklist).
		if got := len(blobs.List()); got != 1 {
			t.Errorf("blob store holds %d objects after delete, want 1", got)
		}
		// And the dashboard drops it.
		_, doc = getWithToken(t, srv, "/api/v1/assets", token)
		if remaining := len(doc["assets"].([]any)); remaining != 1 {
			t.Errorf("dashboard shows %d assets after delete, want 1", remaining)
		}
	})
}

// --- small helpers over the existing Seam 1 plumbing ---

// patchWithToken sends a PATCH with a JSON body.
func patchWithToken(t *testing.T, srv *httptest.Server, path, token string, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v", path, err)
	}
	defer resp.Body.Close()
	return decodeResp(t, resp)
}

// deleteWithToken sends a DELETE.
func deleteWithToken(t *testing.T, srv *httptest.Server, path, token string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	defer resp.Body.Close()
	return decodeResp(t, resp)
}

// downloadCode returns just the status of an authenticated download.
func downloadCode(t *testing.T, srv *httptest.Server, assetID, token string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/assets/"+assetID+"/content", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
