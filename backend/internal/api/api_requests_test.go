package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/agentclient"
	"github.com/syaikhipin/entrustdn-final/backend/internal/api"
	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/mailsink"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
	"github.com/syaikhipin/entrustdn-final/backend/internal/requests"
)

// Request endpoints (ticket 07) at Seam 1: a Data Consumer creates a
// Request, chats with the Agent over the Go↔Agent contract, and watches the
// status and spend. The agent is a contract fake at the HTTP boundary; no
// test touches a real sidecar or a real model gateway.

// fakeClarifierAgent is an httptest agent answering request.clarify.request
// envelopes; it records the payloads it received and echoes the request ID.
type fakeClarifierAgent struct {
	server *httptest.Server
	got    []contract.ClarifyRequest
	reply  contract.ClarifyResponse
}

func newClarifierAgent(t *testing.T, reply contract.ClarifyResponse) *fakeClarifierAgent {
	t.Helper()
	fa := &fakeClarifierAgent{reply: reply}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env contract.Envelope
		_ = json.NewDecoder(r.Body).Decode(&env)
		switch env.Type {
		case contract.TypePingRequest:
			var req contract.PingRequest
			_ = env.PayloadInto(&req)
			_ = json.NewEncoder(w).Encode(contract.Envelope{
				Version: contract.Version, Type: contract.TypePingResponse,
				SentAt:  time.Now().UTC(),
				Payload: mustJSON(contract.PingResponse{Nonce: req.Nonce, Pong: true, AgentVersion: "fake-1.0.0"}),
			})
		case contract.TypeClarifyRequest:
			var req contract.ClarifyRequest
			if err := env.PayloadInto(&req); err != nil {
				http.Error(w, "bad payload", http.StatusBadRequest)
				return
			}
			fa.got = append(fa.got, req)
			resp := fa.reply
			resp.RequestID = req.RequestID
			_ = json.NewEncoder(w).Encode(contract.Envelope{
				Version: contract.Version, Type: contract.TypeClarifyResponse,
				SentAt:  time.Now().UTC(),
				Payload: mustJSON(resp),
			})
		default:
			http.Error(w, "unsupported type", http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	fa.server = srv
	return fa
}

// newRequestsServer boots the API with in-memory stores, a live price book,
// and the fake clarifier agent. Returns the server, admin token, mail
// buffer, ledger, and the fake agent.
func newRequestsServer(t *testing.T) (*httptest.Server, string, *bytes.Buffer, *credits.MemoryStore, *fakeClarifierAgent) {
	t.Helper()
	reply := contract.ClarifyResponse{
		Reply:     "Which counties do you need?",
		Clarified: false,
		Matches: []contract.CatalogMatch{{
			AssetID: "asset-01", Name: "Leinster barley yields 2025", Reason: "already in the catalog",
		}},
		Usage: contract.MeteredUsage{Model: "test-model", InputTokens: 120, CachedInputTokens: 40, OutputTokens: 30},
	}
	agent := newClarifierAgent(t, reply)
	memStore := membership.NewMemoryStore()
	if err := memStore.PublishTOS(t.Context(), membership.TOSVersion{Version: "1.0", Body: "Be good."}); err != nil {
		t.Fatalf("seed TOS: %v", err)
	}
	ledger := credits.NewMemoryStore()
	rules := &[]credits.PricingRules{testPricingRules()}
	mail := &bytes.Buffer{}
	srv := httptest.NewServer(api.NewHandler(api.Deps{
		Agent:   agentclient.New(agent.server.URL),
		Version: "test-backend",
		Store:   memStore,
		Mail:    mailsink.NewLogSink(mail),
		Credits: &api.CreditsDeps{
			Store: ledger,
			Rules: func(context.Context) (credits.PricingRules, error) { return (*rules)[0], nil },
		},
		Requests: &api.RequestsDeps{
			Store: requests.NewMemoryStore(),
			Catalog: func(_ context.Context) ([]contract.CatalogAsset, error) {
				return []contract.CatalogAsset{{
					ID: "asset-01", Name: "Leinster barley yields 2025", Description: "yields",
					CachedPriceMicros: 5_000_000,
				}}, nil
			},
		},
	}))
	t.Cleanup(srv.Close)

	provisionAdmin(t, memStore, "admin@thresh.dev")
	token := loginWith(t, srv, "admin@thresh.dev", "admin-pass-2026")
	return srv, token, mail, ledger, agent
}

// fundConsumer grants the consumer 100 credits through the admin ledger
// endpoint, as the demo does.
func fundConsumer(t *testing.T, srv *httptest.Server, adminToken, consumerID string) {
	t.Helper()
	if code, doc := postWithToken(t, srv, "/api/v1/admin/credits/grant", adminToken, map[string]any{
		"account_id": consumerID, "amount_micros": 100 * credits.MicrosPerCredit, "memo": "pilot seed",
	}); code != http.StatusCreated {
		t.Fatalf("grant = %d (doc: %v)", code, doc)
	}
}

func TestConsumerCreatesChatsAndTracksRequest(t *testing.T) {
	srv, adminToken, mail, ledger, agent := newRequestsServer(t)
	consumerID, token := newConsumer(t, srv, mail, "requester@example.org")
	fundConsumer(t, srv, adminToken, consumerID)

	// Create: description, format, quality bar, budget.
	code, doc := postWithToken(t, srv, "/api/v1/requests", token, map[string]any{
		"description":   "Spring barley yields across Leinster for the 2026 season",
		"format":        "csv",
		"quality_bar":   "Farm-level records with provenance",
		"budget_micros": 10_000_000,
	})
	if code != http.StatusCreated {
		t.Fatalf("create request = %d (doc: %v)", code, doc)
	}
	req := doc["request"].(map[string]any)
	id := req["id"].(string)
	if req["status"] != "clarifying" || req["spent_micros"].(float64) != 0 {
		t.Fatalf("created request = %v, want clarifying with nothing spent", req)
	}

	// Chat: one clarification turn — the agent saw the catalog and the
	// consumer's message, the reply is recorded, and the turn is metered.
	// Worked example at the test price book: 80 fresh @2500/1k + 40 cached
	// @1250/1k + 30 out @10000/1k = 550 micros.
	code, doc = postWithToken(t, srv, "/api/v1/requests/"+id+"/chat", token, map[string]any{
		"message": "I need spring barley yield data for Leinster",
	})
	if code != http.StatusOK {
		t.Fatalf("chat = %d (doc: %v)", code, doc)
	}
	if got := doc["charged_micros"].(float64); got != 550 {
		t.Errorf("charged_micros = %v, want 550", doc["charged_micros"])
	}
	if doc["reply"] != "Which counties do you need?" || doc["clarified"] != false {
		t.Errorf("reply/clarified = (%v, %v), want the agent's reply, not clarified", doc["reply"], doc["clarified"])
	}

	// The agent saw the catalog snapshot and the request context.
	if len(agent.got) != 1 {
		t.Fatalf("agent saw %d clarify envelopes, want 1", len(agent.got))
	}
	sent := agent.got[0]
	if sent.RequestID != id || sent.BudgetMicros != 10_000_000 || len(sent.Catalog) != 1 {
		t.Errorf("agent saw request %q budget %d catalog %d, want this request with the snapshot",
			sent.RequestID, sent.BudgetMicros, len(sent.Catalog))
	}

	// The status view carries the conversation, the match, and the spend.
	code, doc = getWithToken(t, srv, "/api/v1/requests/"+id, token)
	if code != http.StatusOK {
		t.Fatalf("get request = %d", code)
	}
	req = doc["request"].(map[string]any)
	if len(req["messages"].([]any)) != 2 {
		t.Errorf("messages = %v, want consumer + agent turns", req["messages"])
	}
	matches := req["matches"].([]any)
	if len(matches) != 1 || matches[0].(map[string]any)["asset_id"] != "asset-01" {
		t.Errorf("matches = %v, want asset-01 reported by the agent", matches)
	}
	if req["spent_micros"].(float64) != 550 {
		t.Errorf("spent_micros = %v, want 550", req["spent_micros"])
	}

	// The ledger holds the charge; the list view shows the request.
	bal, err := ledger.Balance(t.Context(), credits.AccountScope(consumerID))
	if err != nil || bal != 100*credits.MicrosPerCredit-550 {
		t.Errorf("balance = (%d, %v), want 100 credits − 550 micros", bal, err)
	}
	code, doc = getWithToken(t, srv, "/api/v1/requests", token)
	if code != http.StatusOK || len(doc["requests"].([]any)) != 1 {
		t.Fatalf("list = %d (%v), want the one request", code, doc)
	}
}

func TestChatRefusesWhenTheBudgetCannotCoverTheTurn(t *testing.T) {
	srv, adminToken, mail, _, _ := newRequestsServer(t)
	consumerID, token := newConsumer(t, srv, mail, "tight@example.org")
	fundConsumer(t, srv, adminToken, consumerID)

	// Budget exactly one turn (550 µcr at the test price book): the second
	// turn would double the spend, and must be refused with the request's
	// new status — surfaced, never silent.
	code, doc := postWithToken(t, srv, "/api/v1/requests", token, map[string]any{
		"description": "yields", "format": "csv", "budget_micros": 550,
	})
	if code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	id := doc["request"].(map[string]any)["id"].(string)

	if code, doc := postWithToken(t, srv, "/api/v1/requests/"+id+"/chat", token, map[string]any{"message": "first"}); code != http.StatusOK {
		t.Fatalf("first turn = %d (doc: %v)", code, doc)
	}
	code, doc = postWithToken(t, srv, "/api/v1/requests/"+id+"/chat", token, map[string]any{"message": "second"})
	if code != http.StatusPaymentRequired {
		t.Fatalf("refused turn = %d (doc: %v), want 402", code, doc)
	}
	req := doc["request"].(map[string]any)
	if req["status"] != "budget_exhausted" {
		t.Errorf("refused request status = %v, want budget_exhausted", req["status"])
	}
	if req["spent_micros"].(float64) != 550 {
		t.Errorf("refused request spend = %v, want still 550", req["spent_micros"])
	}
}

func TestClarifiedRequestTakesNoFurtherTurns(t *testing.T) {
	srv, adminToken, mail, _, agent := newRequestsServer(t)
	consumerID, token := newConsumer(t, srv, mail, "done@example.org")
	fundConsumer(t, srv, adminToken, consumerID)
	code, doc := postWithToken(t, srv, "/api/v1/requests", token, map[string]any{
		"description": "yields", "format": "csv", "budget_micros": 10_000_000,
	})
	if code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	id := doc["request"].(map[string]any)["id"].(string)

	// First turn: the agent declares the need fully specified.
	agent.reply.Clarified = true
	defer func() { agent.reply.Clarified = false }()
	if code, doc := postWithToken(t, srv, "/api/v1/requests/"+id+"/chat", token, map[string]any{"message": "all of it"}); code != http.StatusOK || doc["clarified"] != true {
		t.Fatalf("clarifying turn = %d (doc: %v), want 200 clarified", code, doc)
	}
	code, _ = postWithToken(t, srv, "/api/v1/requests/"+id+"/chat", token, map[string]any{"message": "one more thing"})
	if code != http.StatusConflict {
		t.Fatalf("chat on clarified request = %d, want 409", code)
	}
}

func TestStrangersReadRequestsAsNotFound(t *testing.T) {
	srv, adminToken, mail, _, _ := newRequestsServer(t)
	ownerID, ownerToken := newConsumer(t, srv, mail, "owner@example.org")
	_, strangerToken := newConsumer(t, srv, mail, "stranger@example.org")
	fundConsumer(t, srv, adminToken, ownerID)

	code, doc := postWithToken(t, srv, "/api/v1/requests", ownerToken, map[string]any{
		"description": "yields", "format": "csv", "budget_micros": 10_000_000,
	})
	if code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	id := doc["request"].(map[string]any)["id"].(string)

	t.Run("status view", func(t *testing.T) {
		if code, _ := getWithToken(t, srv, "/api/v1/requests/"+id, strangerToken); code != http.StatusNotFound {
			t.Errorf("stranger get = %d, want 404", code)
		}
	})
	t.Run("chat", func(t *testing.T) {
		if code, _ := postWithToken(t, srv, "/api/v1/requests/"+id+"/chat", strangerToken, map[string]any{"message": "hi"}); code != http.StatusNotFound {
			t.Errorf("stranger chat = %d, want 404", code)
		}
	})
}

// newActiveOrg registers, verifies, and gets approved a Farmer
// Organization, returning its login token.
func newActiveOrg(t *testing.T, srv *httptest.Server, mail *bytes.Buffer, adminToken, email string) string {
	t.Helper()
	acct := registerOrg(t, srv, mail, email)
	if code, _ := postWithToken(t, srv, "/api/v1/admin/applications/decide", adminToken, map[string]any{
		"account_id": acct["id"], "decision": "approve",
	}); code != http.StatusOK {
		t.Fatalf("approve org = %d", code)
	}
	return loginWith(t, srv, email, "harvest-2026")
}

// TestOrgSeesFieldableRequests pins the org-facing view (the collections
// form's missing data source): an approved Farmer Organization lists
// clarified Requests — the ones it may field — without the consumer's
// private chat, and consumer-gated endpoints stay consumer-gated.
func TestOrgSeesFieldableRequests(t *testing.T) {
	srv, adminToken, mail, _, agent := newRequestsServer(t)
	consumerID, token := newConsumer(t, srv, mail, "buyer@example.org")
	fundConsumer(t, srv, adminToken, consumerID)
	orgToken := newActiveOrg(t, srv, mail, adminToken, "coop@example.org")

	// One request, still clarifying; one clarified via chat.
	code, doc := postWithToken(t, srv, "/api/v1/requests", token, map[string]any{
		"description": "clarifying survey", "format": "csv", "budget_micros": 10_000_000,
	})
	if code != http.StatusCreated {
		t.Fatalf("create = %d (doc: %v)", code, doc)
	}
	clarifyingID := doc["request"].(map[string]any)["id"].(string)
	code, doc = postWithToken(t, srv, "/api/v1/requests", token, map[string]any{
		"description": "Leinster barley 2026", "format": "csv", "budget_micros": 10_000_000,
	})
	if code != http.StatusCreated {
		t.Fatalf("create 2 = %d", code)
	}
	clarifiedID := doc["request"].(map[string]any)["id"].(string)
	// The fake agent clarifies on the next turn.
	agent.reply.Clarified = true
	code, doc = postWithToken(t, srv, "/api/v1/requests/"+clarifiedID+"/chat", token, map[string]any{"message": "Leinster, CSV, farm-level"})
	if code != http.StatusOK || doc["clarified"] != true {
		t.Fatalf("clarify chat = %d clarified=%v (doc: %v)", code, doc["clarified"], doc)
	}

	// The org's view: the clarified request only, no private chat, and the
	// consumer endpoint stays consumer-gated for the org token.
	code, doc = getWithToken(t, srv, "/api/v1/org/requests", orgToken)
	if code != http.StatusOK {
		t.Fatalf("org list = %d (doc: %v)", code, doc)
	}
	list := doc["requests"].([]any)
	if len(list) != 1 {
		t.Fatalf("org sees %d requests, want only the clarified one", len(list))
	}
	got := list[0].(map[string]any)
	if got["id"].(string) != clarifiedID {
		t.Errorf("org sees %v, want %s", got["id"], clarifiedID)
	}
	if _, has := got["messages"]; has {
		t.Error("org view leaks the consumer's private chat (messages)")
	}
	if code, _ := getWithToken(t, srv, "/api/v1/requests", orgToken); code != http.StatusForbidden {
		t.Errorf("consumer list with org token = %d, want 403", code)
	}
	_ = clarifyingID
}
