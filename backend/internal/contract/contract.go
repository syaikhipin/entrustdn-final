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
	TypePingRequest     = "ping.request"
	TypePingResponse    = "ping.response"
	TypeClarifyRequest  = "request.clarify.request"
	TypeClarifyResponse = "request.clarify.response"
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

// CatalogAsset is one catalog row handed to the agent (Seam 2, ticket 07):
// the public face of an existing Data Asset the agent checks before
// fielding, so it can tell the Consumer when the catalog already answers
// the need.
type CatalogAsset struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	Categories        []CategoryStamp `json:"categories"`
	CachedPriceMicros int64           `json:"cached_price_micros"`
}

// CategoryStamp is one taxonomy assignment as the contract carries it:
// which axis, which term.
type CategoryStamp struct {
	Category string `json:"category"`
	Value    string `json:"value"`
	Label    string `json:"label"`
}

// HistoryTurn is one prior turn of a clarification conversation.
type HistoryTurn struct {
	Role string `json:"role"` // "consumer" | "agent"
	Body string `json:"body"`
}

// SkillModule is one Agent Skill loaded into the agent's context (ticket
// 13): the skill's name and its markdown instructions. Instructions are
// data the agent reads, never code it runs.
type SkillModule struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// ClarifyRequest is one clarification turn sent backend→agent: the Request
// so far, the Consumer's new message, the conversation history, the catalog
// snapshot to check first, and any attached Agent Skills to load into
// context (ticket 13).
type ClarifyRequest struct {
	RequestID    string         `json:"request_id"`
	Description  string         `json:"description"`
	Format       string         `json:"format"`
	QualityBar   string         `json:"quality_bar"`
	BudgetMicros int64          `json:"budget_micros"`
	SpentMicros  int64          `json:"spent_micros"`
	Message      string         `json:"message"`
	History      []HistoryTurn  `json:"history"`
	Catalog      []CatalogAsset `json:"catalog"`
	Skills       []SkillModule  `json:"skills"`
}

// UnmarshalJSON decodes strictly: unknown fields are contract violations
// (additionalProperties: false in the JSON Schema).
func (r *ClarifyRequest) UnmarshalJSON(data []byte) error {
	type plain ClarifyRequest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode((*plain)(r)); err != nil {
		return err
	}
	return nil
}

// ClarifyResponse is the agent's answer for one clarification turn: the
// reply text, whether the Request is fully clarified, any existing-Asset
// matches, and the metered usage to post to the Ledger.
type ClarifyResponse struct {
	RequestID string         `json:"request_id"`
	Reply     string         `json:"reply"`
	Clarified bool           `json:"clarified"`
	Matches   []CatalogMatch `json:"matches"`
	Usage     MeteredUsage   `json:"usage"`
}

// UnmarshalJSON decodes strictly: unknown fields are contract violations
// (additionalProperties: false in the JSON Schema).
func (r *ClarifyResponse) UnmarshalJSON(data []byte) error {
	type plain ClarifyResponse
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode((*plain)(r)); err != nil {
		return err
	}
	return nil
}

// CatalogMatch is one existing Asset the agent reports as already
// answering the need.
type CatalogMatch struct {
	AssetID string `json:"asset_id"`
	Name    string `json:"name"`
	Reason  string `json:"reason"`
}

// MeteredUsage is one metered model call as the gateway reports it.
// CachedInputTokens rides inside InputTokens (the OpenAI convention) and
// bills at the cheaper cached rate.
type MeteredUsage struct {
	Model             string `json:"model"`
	InputTokens       int64  `json:"input_tokens"`
	CachedInputTokens int64  `json:"cached_input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
}

// Validate checks the response invariants the fixtures pin down: a reply
// must be present and the metered usage well-formed (metering math runs on
// it, so a malformed report must be refused before it prices).
func (r ClarifyResponse) Validate() error {
	if r.RequestID == "" {
		return fmt.Errorf("request_id is empty")
	}
	if r.Reply == "" {
		return fmt.Errorf("reply is empty")
	}
	if r.Usage.Model == "" {
		return fmt.Errorf("usage.model is empty")
	}
	if r.Usage.InputTokens < 0 || r.Usage.CachedInputTokens < 0 || r.Usage.OutputTokens < 0 {
		return fmt.Errorf("usage carries a negative token count")
	}
	if r.Usage.CachedInputTokens > r.Usage.InputTokens {
		return fmt.Errorf("usage cached input tokens (%d) exceed input tokens (%d)", r.Usage.CachedInputTokens, r.Usage.InputTokens)
	}
	return nil
}

// NewClarifyRequest builds an envelope around a ClarifyRequest payload.
func NewClarifyRequest(req ClarifyRequest, sentAt time.Time) (Envelope, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return Envelope{}, fmt.Errorf("failed to marshal clarify request: %w", err)
	}
	return Envelope{
		Version: Version,
		Type:    TypeClarifyRequest,
		SentAt:  sentAt,
		Payload: payload,
	}, nil
}

// Conversation message types (ticket 11): the agent converses with Farmer
// Members over Channels; the backend opens conversations and delivers
// Member replies; the agent answers with delivery acknowledgement, status,
// thread, and answers.
const (
	TypeConversationStartRequest = "conversation.start.request"
	TypeConversationReplyRequest = "conversation.reply.request"
	TypeConversationResponse     = "conversation.response"
)

// Conversation status values carried by ConversationResponse.
const (
	// ConversationAwaitingMember: paused on a checkpoint, waiting for the
	// Member's reply — possibly for days (ADR 0001).
	ConversationAwaitingMember = "awaiting_member"
	// ConversationCompleted: all questions answered (or the Member said done).
	ConversationCompleted = "completed"
	// ConversationStopped: the Member declined to continue.
	ConversationStopped = "stopped"
)

// ThreadMessage is one turn of a Member conversation.
type ThreadMessage struct {
	Role string `json:"role"` // "agent" | "member"
	Body string `json:"body"`
}

// ConversationStartRequest is sent backend→agent to open a conversation
// with one Farmer Member over a Channel.
type ConversationStartRequest struct {
	ConversationID string   `json:"conversation_id"`
	RequestID      string   `json:"request_id"`
	MemberName     string   `json:"member_name"`
	Contact        string   `json:"contact"`
	Topic          string   `json:"topic"`
	Questions      []string `json:"questions"`
	ResumeURL      string   `json:"resume_url"`
}

// UnmarshalJSON decodes strictly: unknown fields are contract violations
// (additionalProperties: false in the JSON Schema).
func (r *ConversationStartRequest) UnmarshalJSON(data []byte) error {
	type plain ConversationStartRequest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode((*plain)(r))
}

// ConversationReplyRequest is sent backend→agent when a Member's reply
// arrives — possibly days later, possibly after an agent restart. The
// reply is typed text (Message) or a voice note (Audio, transcribed by
// the agent's env-keyed STT client) — exactly one of the two. Thread,
// contact, questions, and the resumable link ride along: the backend owns
// the durable record, and re-sending them lets a cold agent rebuild
// context, deliver on the right channel (re-offering the link after
// partial answers), and pace the same question list.
type ConversationReplyRequest struct {
	ConversationID string             `json:"conversation_id"`
	Message        string             `json:"message,omitempty"`
	Audio          *ConversationAudio `json:"audio,omitempty"`
	Thread         []ThreadMessage    `json:"thread"`
	Contact        string             `json:"contact"`
	Questions      []string           `json:"questions"`
	ResumeURL      string             `json:"resume_url,omitempty"`
}

// ConversationAudio is a Member's voice note: base64-encoded bytes plus a
// format hint that rides the transcription upload.
type ConversationAudio struct {
	Data   string `json:"data"` // base64
	Format string `json:"format"`
}

// Validate enforces the exactly-one-of rule: a reply carries text or
// audio, never both, never neither.
func (r ConversationReplyRequest) Validate() error {
	hasText := r.Message != ""
	hasAudio := r.Audio != nil
	switch {
	case hasText && hasAudio:
		return fmt.Errorf("a reply carries message or audio, not both")
	case !hasText && !hasAudio:
		return fmt.Errorf("a reply needs message or audio")
	}
	return nil
}

// UnmarshalJSON decodes strictly: unknown fields are contract violations
// (additionalProperties: false in the JSON Schema), and the
// message-or-audio rule is enforced on decode.
func (r *ConversationReplyRequest) UnmarshalJSON(data []byte) error {
	type plain ConversationReplyRequest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode((*plain)(r)); err != nil {
		return err
	}
	return r.Validate()
}

// ConversationResponse is the agent→backend answer for any conversation
// turn: delivery acknowledgement, status, the full thread, and the answers
// gathered so far.
type ConversationResponse struct {
	ConversationID string          `json:"conversation_id"`
	Delivered      bool            `json:"delivered"`
	Status         string          `json:"status"`
	Thread         []ThreadMessage `json:"thread"`
	Answers        []string        `json:"answers"`
}

// UnmarshalJSON decodes strictly: unknown fields are contract violations
// (additionalProperties: false in the JSON Schema).
func (r *ConversationResponse) UnmarshalJSON(data []byte) error {
	type plain ConversationResponse
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode((*plain)(r)); err != nil {
		return err
	}
	switch r.Status {
	case ConversationAwaitingMember, ConversationCompleted, ConversationStopped:
	default:
		return fmt.Errorf("conversation status %q is not one of the contract statuses", r.Status)
	}
	return nil
}

// Validate checks the response invariants the fixtures pin down.
func (r ConversationResponse) Validate() error {
	if r.ConversationID == "" {
		return fmt.Errorf("conversation_id is empty")
	}
	switch r.Status {
	case ConversationAwaitingMember, ConversationCompleted, ConversationStopped:
	default:
		return fmt.Errorf("conversation status %q is not one of the contract statuses", r.Status)
	}
	return nil
}

// NewConversationStartRequest builds an envelope around a
// ConversationStartRequest payload.
func NewConversationStartRequest(req ConversationStartRequest, sentAt time.Time) (Envelope, error) {
	if len(req.Questions) == 0 {
		return Envelope{}, fmt.Errorf("conversation start needs at least one question")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return Envelope{}, fmt.Errorf("failed to marshal conversation start: %w", err)
	}
	return Envelope{
		Version: Version,
		Type:    TypeConversationStartRequest,
		SentAt:  sentAt,
		Payload: payload,
	}, nil
}

// NewConversationReplyRequest builds an envelope around a
// ConversationReplyRequest payload.
func NewConversationReplyRequest(req ConversationReplyRequest, sentAt time.Time) (Envelope, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return Envelope{}, fmt.Errorf("failed to marshal conversation reply: %w", err)
	}
	return Envelope{
		Version: Version,
		Type:    TypeConversationReplyRequest,
		SentAt:  sentAt,
		Payload: payload,
	}, nil
}
