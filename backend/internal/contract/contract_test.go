package contract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
)

// These tests pin the Go-side types to the golden fixtures in /contract —
// the single source of truth for the Go↔Agent message contract (Seam 2).
// If a fixture and the typed definitions drift, this suite fails.

// fixturePath resolves a file under the repo-root contract/ directory,
// which sits two levels above this package's directory.
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "contract", "fixtures", name)
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(fixturePath(t, name))
	if err != nil {
		t.Fatalf("failed to read contract fixture %s: %v", name, err)
	}
	return raw
}

func TestPingRequestDecodesFromFixture(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
	}{
		{name: "ping request", fixture: "ping-request.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var env contract.Envelope
			if err := json.Unmarshal(loadFixture(t, tt.fixture), &env); err != nil {
				t.Fatalf("fixture is not valid envelope JSON: %v", err)
			}
			if env.Type != "ping.request" {
				t.Errorf("Type = %q, want %q", env.Type, "ping.request")
			}
			var req contract.PingRequest
			if err := env.PayloadInto(&req); err != nil {
				t.Fatalf("payload does not decode as PingRequest: %v", err)
			}
			if req.Nonce != "tracer-bullet-01" {
				t.Errorf("Nonce = %q, want %q", req.Nonce, "tracer-bullet-01")
			}
		})
	}
}

func TestPingResponseDecodesFromFixture(t *testing.T) {
	var env contract.Envelope
	if err := json.Unmarshal(loadFixture(t, "ping-response.json"), &env); err != nil {
		t.Fatalf("fixture is not valid envelope JSON: %v", err)
	}
	if env.Type != "ping.response" {
		t.Errorf("Type = %q, want %q", env.Type, "ping.response")
	}
	var resp contract.PingResponse
	if err := env.PayloadInto(&resp); err != nil {
		t.Fatalf("payload does not decode as PingResponse: %v", err)
	}
	if resp.Nonce != "tracer-bullet-01" {
		t.Errorf("Nonce = %q, want %q", resp.Nonce, "tracer-bullet-01")
	}
	if !resp.Pong {
		t.Error("Pong = false, want true")
	}
	if resp.AgentVersion == "" {
		t.Error("AgentVersion is empty, want a non-empty version banner")
	}
}

func TestEnvelopeRejectsWrongVersion(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{name: "version 0", json: `{"version": 0, "type": "ping.request", "sent_at": "2026-09-21T10:00:00Z", "payload": {"nonce": "n"}}`},
		{name: "version 2", json: `{"version": 2, "type": "ping.request", "sent_at": "2026-09-21T10:00:00Z", "payload": {"nonce": "n"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var env contract.Envelope
			if err := json.Unmarshal([]byte(tt.json), &env); err != nil {
				return // rejected at decode: acceptable
			}
			if err := env.Validate(); err == nil {
				t.Error("Validate() = nil, want error for wrong contract version")
			}
		})
	}
}

func TestEnvelopeRejectsUnknownFields(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{name: "extra envelope field", json: `{"version": 1, "type": "ping.request", "sent_at": "2026-09-21T10:00:00Z", "payload": {"nonce": "n"}, "extra": true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var env contract.Envelope
			if err := json.Unmarshal([]byte(tt.json), &env); err == nil {
				t.Error("Unmarshal accepted an unknown envelope field, want rejection (additionalProperties: false)")
			}
		})
	}
}

func TestEnvelopeRejectsMissingSentAt(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{name: "no sent_at", json: `{"version": 1, "type": "ping.request", "payload": {"nonce": "n"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var env contract.Envelope
			if err := json.Unmarshal([]byte(tt.json), &env); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if err := env.Validate(); err == nil {
				t.Error("Validate() = nil, want error for missing sent_at")
			}
		})
	}
}

func TestPingResponseRejectsEmptyAgentVersion(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{name: "empty agent_version", json: `{"version": 1, "type": "ping.response", "sent_at": "2026-09-21T10:00:00Z", "payload": {"nonce": "n", "pong": true, "agent_version": ""}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var env contract.Envelope
			if err := json.Unmarshal([]byte(tt.json), &env); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			var resp contract.PingResponse
			if err := env.PayloadInto(&resp); err != nil {
				t.Fatalf("payload decode failed: %v", err)
			}
			if err := resp.Validate(); err == nil {
				t.Error("PingResponse.Validate() = nil, want error for empty agent_version")
			}
		})
	}
}

func TestClarifyRequestDecodesFromFixture(t *testing.T) {
	var env contract.Envelope
	if err := json.Unmarshal(loadFixture(t, "request-clarify-request.json"), &env); err != nil {
		t.Fatalf("fixture is not valid envelope JSON: %v", err)
	}
	if env.Type != contract.TypeClarifyRequest {
		t.Errorf("Type = %q, want %q", env.Type, contract.TypeClarifyRequest)
	}
	var req contract.ClarifyRequest
	if err := env.PayloadInto(&req); err != nil {
		t.Fatalf("payload does not decode as ClarifyRequest: %v", err)
	}
	if req.RequestID != "req-clarify-01" {
		t.Errorf("RequestID = %q, want %q", req.RequestID, "req-clarify-01")
	}
	if req.Description == "" || req.Format != "csv" {
		t.Errorf("Request fields lost in decode: %+v", req)
	}
	if req.BudgetMicros != 10_000_000 || req.SpentMicros != 0 {
		t.Errorf("budget/spent = (%d, %d), want (10000000, 0)", req.BudgetMicros, req.SpentMicros)
	}
	if len(req.History) != 2 || req.History[0].Role != "consumer" {
		t.Errorf("History = %+v, want two turns starting with the consumer", req.History)
	}
	if len(req.Catalog) != 1 || req.Catalog[0].ID != "asset-01" {
		t.Errorf("Catalog = %+v, want one asset asset-01", req.Catalog)
	}
}

func TestClarifyResponseDecodesFromFixture(t *testing.T) {
	var env contract.Envelope
	if err := json.Unmarshal(loadFixture(t, "request-clarify-response.json"), &env); err != nil {
		t.Fatalf("fixture is not valid envelope JSON: %v", err)
	}
	if env.Type != contract.TypeClarifyResponse {
		t.Errorf("Type = %q, want %q", env.Type, contract.TypeClarifyResponse)
	}
	var resp contract.ClarifyResponse
	if err := env.PayloadInto(&resp); err != nil {
		t.Fatalf("payload does not decode as ClarifyResponse: %v", err)
	}
	if resp.RequestID != "req-clarify-01" {
		t.Errorf("RequestID = %q, want %q", resp.RequestID, "req-clarify-01")
	}
	if resp.Reply == "" {
		t.Error("Reply is empty, want the agent's reply text")
	}
	if resp.Clarified {
		t.Error("Clarified = true, want false for the fixture's in-progress conversation")
	}
	if len(resp.Matches) != 1 || resp.Matches[0].AssetID != "asset-01" {
		t.Errorf("Matches = %+v, want one match for asset-01", resp.Matches)
	}
	if resp.Usage.Model == "" || resp.Usage.InputTokens != 120 || resp.Usage.CachedInputTokens != 40 || resp.Usage.OutputTokens != 30 {
		t.Errorf("Usage = %+v, want the fixture's metered usage", resp.Usage)
	}
}

func TestClarifyPayloadsRejectUnknownFields(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		into any
	}{
		{
			name: "clarify request",
			raw:  `{"request_id":"r","description":"d","format":"csv","quality_bar":"","budget_micros":0,"spent_micros":0,"message":"m","history":[],"catalog":[],"extra":1}`,
			into: &contract.ClarifyRequest{},
		},
		{
			name: "clarify response",
			raw:  `{"request_id":"r","reply":"hi","clarified":false,"matches":[],"usage":{"model":"m","input_tokens":1,"cached_input_tokens":0,"output_tokens":1},"extra":1}`,
			into: &contract.ClarifyResponse{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(tt.raw), tt.into); err == nil {
				t.Error("payload accepted an unknown field, want rejection (additionalProperties: false)")
			}
		})
	}
}

func TestClarifyResponseValidateRejectsMalformedUsage(t *testing.T) {
	tests := []struct {
		name string
		resp contract.ClarifyResponse
	}{
		{
			name: "cached input exceeds input",
			resp: contract.ClarifyResponse{RequestID: "r", Reply: "hi", Usage: contract.MeteredUsage{Model: "m", InputTokens: 10, CachedInputTokens: 11, OutputTokens: 1}},
		},
		{
			name: "empty model",
			resp: contract.ClarifyResponse{RequestID: "r", Reply: "hi", Usage: contract.MeteredUsage{InputTokens: 10, OutputTokens: 1}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.resp.Validate(); err == nil {
				t.Error("Validate() = nil, want error for malformed usage")
			}
		})
	}
}
