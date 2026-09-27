package api_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/agentclient"
	"github.com/syaikhipin/entrustdn-final/backend/internal/api"
	"github.com/syaikhipin/entrustdn-final/backend/internal/mailsink"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
	"github.com/syaikhipin/entrustdn-final/backend/internal/memoryprov"
)

// Memory Provider endpoints (ticket 14) at Seam 1: the Platform Admin
// registers and removes the external recall services (connection facts
// only). Consumers and members are refused — the registry is admin
// configuration, like the payment gateway.

func newMemoryProvidersServer(t *testing.T) (*httptest.Server, string, *membership.MemoryStore) {
	t.Helper()
	agent := fakeAgent(t, "0.1.0")
	memStore := membership.NewMemoryStore()
	if err := memStore.PublishTOS(t.Context(), membership.TOSVersion{Version: "1.0", Body: "Be good."}); err != nil {
		t.Fatalf("seed TOS: %v", err)
	}
	svc := memoryprov.NewService(memoryprov.NewMemoryStore())
	srv := httptest.NewServer(api.NewHandler(api.Deps{
		Agent:           agentclient.New(agent.URL),
		Version:         "test-backend",
		Store:           memStore,
		Mail:            mailsink.NewLogSink(&bytes.Buffer{}),
		MemoryProviders: &api.MemoryProvidersDeps{Service: svc},
	}))
	t.Cleanup(srv.Close)

	provisionAdmin(t, memStore, "admin@thresh.dev")
	token := loginWith(t, srv, "admin@thresh.dev", "admin-pass-2026")
	return srv, token, memStore
}

// provisionConsumer seeds a verified, TOS-accepting Data Consumer with a
// known password (provisionAdmin's consumer twin).
func provisionConsumer(t *testing.T, store *membership.MemoryStore, email string) {
	t.Helper()
	hash, err := membership.HashPassword("consumer-pass-2026")
	if err != nil {
		t.Fatalf("hash consumer password: %v", err)
	}
	acct := membership.Account{
		Email:        email,
		PasswordHash: hash,
		DisplayName:  "Data Consumer",
		Role:         membership.RoleDataConsumer,
		Status:       membership.StatusActive,
	}
	if err := store.CreateAccount(t.Context(), &acct); err != nil {
		t.Fatalf("provision consumer: %v", err)
	}
	if err := store.SetAccountVerified(t.Context(), acct.ID, time.Now()); err != nil {
		t.Fatalf("verify consumer: %v", err)
	}
	tos, err := store.CurrentTOS(t.Context())
	if err != nil {
		t.Fatalf("current TOS: %v", err)
	}
	if err := store.RecordAcceptance(t.Context(), membership.TOSAcceptance{
		AccountID: acct.ID,
		Version:   tos.Version,
	}); err != nil {
		t.Fatalf("record consumer TOS acceptance: %v", err)
	}
}

func TestMemoryProviderEndpointsRoundTrip(t *testing.T) {
	srv, adminToken, _ := newMemoryProvidersServer(t)

	// Empty registry: an array, never null.
	code, doc := getWithToken(t, srv, "/api/v1/admin/memory-providers", adminToken)
	if code != http.StatusOK {
		t.Fatalf("GET empty = %d, want 200", code)
	}
	if got, _ := doc["memory_providers"].([]any); len(got) != 0 {
		t.Errorf("memory_providers = %v, want an empty array", doc["memory_providers"])
	}

	// Create.
	code, doc = postWithToken(t, srv, "/api/v1/admin/memory-providers", adminToken, map[string]any{
		"name": "mem0-primary", "endpoint": "http://memory.local:8080/mcp",
	})
	if code != http.StatusCreated {
		t.Fatalf("POST = %d (%v), want 201", code, doc)
	}
	created := doc["memory_provider"].(map[string]any)
	if created["name"] != "mem0-primary" || created["endpoint"] != "http://memory.local:8080/mcp" {
		t.Errorf("created = %v", created)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created provider carries no id: %v", created)
	}

	// Duplicate name is a conflict.
	code, _ = postWithToken(t, srv, "/api/v1/admin/memory-providers", adminToken, map[string]any{
		"name": "mem0-primary", "endpoint": "http://other.local/mcp",
	})
	if code != http.StatusConflict {
		t.Errorf("duplicate POST = %d, want 409", code)
	}

	// Unusable endpoint is 422 with the reason.
	code, doc = postWithToken(t, srv, "/api/v1/admin/memory-providers", adminToken, map[string]any{
		"name": "no-scheme", "endpoint": "memory.local:8080/mcp",
	})
	if code != http.StatusUnprocessableEntity || !strings.Contains(doc["error"].(string), "http(s)") {
		t.Errorf("bad endpoint = %d (%v), want 422 mentioning http(s)", code, doc)
	}

	// Delete, then it's gone; deleting again is 404.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/admin/memory-providers/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("DELETE = %d, want 200", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/admin/memory-providers/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("re-DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("re-DELETE = %d, want 404", resp.StatusCode)
	}
}

func TestMemoryProviderEndpointsAreAdminOnly(t *testing.T) {
	srv, _, memStore := newMemoryProvidersServer(t)

	// A data consumer gets 403.
	provisionConsumer(t, memStore, "consumer@thresh.dev")
	token := loginWith(t, srv, "consumer@thresh.dev", "consumer-pass-2026")
	code, _ := getWithToken(t, srv, "/api/v1/admin/memory-providers", token)
	if code != http.StatusForbidden {
		t.Errorf("consumer GET = %d, want 403", code)
	}
	code, _ = postWithToken(t, srv, "/api/v1/admin/memory-providers", token, map[string]any{
		"name": "sneaky", "endpoint": "http://sneaky.local/mcp",
	})
	if code != http.StatusForbidden {
		t.Errorf("consumer POST = %d, want 403", code)
	}

	// Anonymous gets 401.
	resp, err := srv.Client().Get(srv.URL + "/api/v1/admin/memory-providers")
	if err != nil {
		t.Fatalf("anonymous GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous GET = %d, want 401", resp.StatusCode)
	}
}

func TestCreateMemoryProviderRefusesABlankName(t *testing.T) {
	srv, adminToken, _ := newMemoryProvidersServer(t)
	code, doc := postWithToken(t, srv, "/api/v1/admin/memory-providers", adminToken, map[string]any{
		"name": "", "endpoint": "http://x.local/mcp",
	})
	if code != http.StatusUnprocessableEntity {
		t.Errorf("blank name = %d (%v), want 422", code, doc)
	}
}
