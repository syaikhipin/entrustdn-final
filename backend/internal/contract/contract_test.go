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
