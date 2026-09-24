package agentclient_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/agentclient"
	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
)

// These tests drive the backend's agent client against a contract-fake agent
// (an httptest server speaking the /contract fixtures' shape). This is the
// backend-side half of Seam 2: the client must send a valid ping.request
// envelope and accept a well-formed ping.response, and it must never
// misreport a broken agent as healthy. Ticket 07 adds the clarify
// round trip over the same endpoint.

// newFakeAgent returns a contract-fake agent answering ping and clarify
// requests.
func newFakeAgent(t *testing.T, nonceSeen *string, respondWith func(w http.ResponseWriter, nonce string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/message" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		var env contract.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			http.Error(w, "bad envelope", http.StatusBadRequest)
			return
		}
		if err := env.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var req contract.PingRequest
		if err := env.PayloadInto(&req); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		*nonceSeen = req.Nonce
		respondWith(w, req.Nonce)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writePingResponse(w http.ResponseWriter, nonce string) {
	resp := contract.PingResponse{Nonce: nonce, Pong: true, AgentVersion: "fake-1.0.0"}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(contract.Envelope{
		Version: contract.Version,
		Type:    contract.TypePingResponse,
		SentAt:  time.Now().UTC(),
		Payload: mustJSON(resp),
	})
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestPingRoundTripsThroughFakeAgent(t *testing.T) {
	tests := []struct {
		name  string
		nonce string
	}{
		{name: "tracer bullet nonce", nonce: "tracer-bullet-01"},
		{name: "fresh nonce", nonce: "fresh-nonce-42"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen string
			srv := newFakeAgent(t, &seen, writePingResponse)

			client := agentclient.New(srv.URL)
			resp, err := client.Ping(context.Background(), tt.nonce)
			if err != nil {
				t.Fatalf("Ping() error = %v", err)
			}
			if resp.Nonce != tt.nonce {
				t.Errorf("response Nonce = %q, want %q", resp.Nonce, tt.nonce)
			}
			if !resp.Pong {
				t.Error("response Pong = false, want true")
			}
			if seen != tt.nonce {
				t.Errorf("agent saw nonce %q, want %q", seen, tt.nonce)
			}
		})
	}
}

func TestPingReportsUnreachableAgent(t *testing.T) {
	client := agentclient.New("http://127.0.0.1:0") // nothing listens here
	if _, err := client.Ping(context.Background(), "n"); err == nil {
		t.Fatal("Ping() against unreachable agent = nil error, want error")
	}
}

func TestPingRejectsResponseWithWrongContractVersion(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "version 2 envelope",
			body: `{"version": 2, "type": "ping.response", "sent_at": "2026-09-21T10:00:01Z", "payload": {"nonce": "n", "pong": true, "agent_version": "fake-1.0.0"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen string
			srv := newFakeAgent(t, &seen, func(w http.ResponseWriter, _ string) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			})

			client := agentclient.New(srv.URL)
			if _, err := client.Ping(context.Background(), "n"); err == nil {
				t.Fatal("Ping() accepting a version-2 envelope = nil error, want error")
			}
		})
	}
}

func TestPingRejectsMismatchedNonce(t *testing.T) {
	var seen string
	srv := newFakeAgent(t, &seen, func(w http.ResponseWriter, _ string) {
		writePingResponse(w, "some-other-nonce")
	})

	client := agentclient.New(srv.URL)
	resp, err := client.Ping(context.Background(), "expected-nonce")
	if err == nil && resp.Nonce != "expected-nonce" {
		t.Errorf("Ping() accepted mismatched nonce (got %q, sent %q) without error", resp.Nonce, "expected-nonce")
	}
	if err != nil && !strings.Contains(err.Error(), "nonce") {
		t.Errorf("Ping() error should mention nonce mismatch, got: %v", err)
	}
}

// clarifyAgent answers clarify envelopes, recording what it received.
func clarifyAgent(t *testing.T, seen *contract.ClarifyRequest, respond func(w http.ResponseWriter, req contract.ClarifyRequest)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env contract.Envelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			http.Error(w, "bad envelope", http.StatusBadRequest)
			return
		}
		if env.Type != contract.TypeClarifyRequest {
			http.Error(w, "want clarify request", http.StatusBadRequest)
			return
		}
		var req contract.ClarifyRequest
		if err := env.PayloadInto(&req); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		*seen = req
		respond(w, req)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeClarifyResponse(w http.ResponseWriter, resp contract.ClarifyResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(contract.Envelope{
		Version: contract.Version,
		Type:    contract.TypeClarifyResponse,
		SentAt:  time.Now().UTC(),
		Payload: mustJSON(resp),
	})
}

func TestClarifyRoundTripsThroughFakeAgent(t *testing.T) {
	var seen contract.ClarifyRequest
	srv := clarifyAgent(t, &seen, func(w http.ResponseWriter, req contract.ClarifyRequest) {
		writeClarifyResponse(w, contract.ClarifyResponse{
			RequestID: req.RequestID,
			Reply:     "Which counties?",
			Clarified: false,
			Matches:   []contract.CatalogMatch{{AssetID: "a-1", Name: "Yields 2025", Reason: "catalog"}},
			Usage:     contract.MeteredUsage{Model: "m", InputTokens: 120, CachedInputTokens: 40, OutputTokens: 30},
		})
	})

	req := contract.ClarifyRequest{
		RequestID:    "req-7",
		Description:  "spring barley yields",
		Format:       "csv",
		QualityBar:   "farm-level",
		BudgetMicros: 1_000,
		SpentMicros:  0,
		Message:      "I need yield data",
		History:      []contract.HistoryTurn{{Role: "consumer", Body: "hello"}},
		Catalog:      []contract.CatalogAsset{{ID: "a-1", Name: "Yields 2025", CachedPriceMicros: 5_000_000}},
	}
	resp, err := agentclient.New(srv.URL).Clarify(context.Background(), req)
	if err != nil {
		t.Fatalf("Clarify() error = %v", err)
	}
	if resp.RequestID != "req-7" || resp.Reply != "Which counties?" {
		t.Errorf("response = %+v, want the fake's reply", resp)
	}
	if seen.RequestID != "req-7" || seen.Message != "I need yield data" {
		t.Errorf("agent saw %+v, want the request we sent", seen)
	}
}

func TestClarifyRejectsWrongRequestIDInReply(t *testing.T) {
	var seen contract.ClarifyRequest
	srv := clarifyAgent(t, &seen, func(w http.ResponseWriter, req contract.ClarifyRequest) {
		writeClarifyResponse(w, contract.ClarifyResponse{
			RequestID: "some-other-request",
			Reply:     "hi",
			Usage:     contract.MeteredUsage{Model: "m", InputTokens: 1, OutputTokens: 1},
		})
	})

	if _, err := agentclient.New(srv.URL).Clarify(context.Background(), contract.ClarifyRequest{
		RequestID: "req-7", Description: "d", Format: "csv", Message: "hi",
	}); err == nil {
		t.Fatal("Clarify() accepting a reply for another request = nil error, want error")
	}
}

func TestClarifyRejectsUnusableReply(t *testing.T) {
	tests := []struct {
		name string
		resp contract.ClarifyResponse
	}{
		{
			name: "empty reply text",
			resp: contract.ClarifyResponse{RequestID: "req-7", Usage: contract.MeteredUsage{Model: "m", InputTokens: 1, OutputTokens: 1}},
		},
		{
			name: "cached tokens exceed input",
			resp: contract.ClarifyResponse{RequestID: "req-7", Reply: "hi",
				Usage: contract.MeteredUsage{Model: "m", InputTokens: 1, CachedInputTokens: 5, OutputTokens: 1}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen contract.ClarifyRequest
			srv := clarifyAgent(t, &seen, func(w http.ResponseWriter, req contract.ClarifyRequest) {
				writeClarifyResponse(w, tt.resp)
			})
			if _, err := agentclient.New(srv.URL).Clarify(context.Background(), contract.ClarifyRequest{
				RequestID: "req-7", Description: "d", Format: "csv", Message: "hi",
			}); err == nil {
				t.Fatal("Clarify() accepting an unusable reply = nil error, want error")
			}
		})
	}
}
