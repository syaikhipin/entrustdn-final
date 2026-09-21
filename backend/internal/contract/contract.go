// Package contract holds the Go-side types for the Thresh Go↔Agent message
// contract. The single source of truth is the JSON Schema and golden fixtures
// in /contract at the repo root; internal/contract/contract_test.go pins these
// types to those fixtures so the two cannot drift silently.
package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// Version is the contract version every message must carry. Both sides reject
// envelopes carrying anything else.
const Version = 1

// Message types carried by the envelope's type field.
const (
	TypePingRequest  = "ping.request"
	TypePingResponse = "ping.response"
)

// Envelope is the outer shell of every backend↔agent message.
type Envelope struct {
	Version int             `json:"version"`
	Type    string          `json:"type"`
	SentAt  time.Time       `json:"sent_at"`
	Payload json.RawMessage `json:"payload"`

	// extra catches fields outside the contract (schema: additionalProperties
	// false). Populated only by strict decoding; see UnmarshalJSON.
	extra []string
}

// UnmarshalJSON decodes strictly: unknown fields are contract violations.
func (e *Envelope) UnmarshalJSON(data []byte) error {
	type plain Envelope
	var p plain
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return err
	}
	*e = Envelope(p)
	return nil
}

// Validate checks the invariants shared by every message type.
func (e Envelope) Validate() error {
	if e.Version != Version {
		return fmt.Errorf("contract version %d, want %d", e.Version, Version)
	}
	if e.Type == "" {
		return fmt.Errorf("message type is empty")
	}
	if e.SentAt.IsZero() {
		return fmt.Errorf("sent_at is missing")
	}
	return nil
}

// PayloadInto decodes the envelope payload into a message-typed struct.
func (e Envelope) PayloadInto(v any) error {
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return fmt.Errorf("payload does not decode as %T: %w", v, err)
	}
	return nil
}

// PingRequest is the backend→agent liveness probe payload.
type PingRequest struct {
	Nonce string `json:"nonce"`
}

// PingResponse is the agent's answer to a PingRequest.
type PingResponse struct {
	Nonce        string `json:"nonce"`
	Pong         bool   `json:"pong"`
	AgentVersion string `json:"agent_version"`
}

// Validate checks the response invariants the fixtures pin down.
func (r PingResponse) Validate() error {
	if r.Nonce == "" {
		return fmt.Errorf("nonce is empty")
	}
	if !r.Pong {
		return fmt.Errorf("pong is not true")
	}
	if r.AgentVersion == "" {
		return fmt.Errorf("agent_version is empty")
	}
	return nil
}

// NewPingRequest builds an envelope around a PingRequest payload.
func NewPingRequest(nonce string, sentAt time.Time) (Envelope, error) {
	payload, err := json.Marshal(PingRequest{Nonce: nonce})
	if err != nil {
		return Envelope{}, fmt.Errorf("failed to marshal ping request: %w", err)
	}
	return Envelope{
		Version: Version,
		Type:    TypePingRequest,
		SentAt:  sentAt,
		Payload: payload,
	}, nil
}
