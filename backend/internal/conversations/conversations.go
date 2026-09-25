// Package conversations implements the Agent's conversations with Farmer
// Members over Channels (ticket 11). The backend owns the durable record —
// thread, status, answers, and the Member's resumable token; the agent
// owns the LangGraph checkpoint. A conversation pauses for days
// (status awaiting_member, ADR 0001's core scenario) and resumes mid-thread
// when the Member replies — typed text or a transcribed voice note.
//
// The Member has no platform account: the resumable token is their
// capability. Over-survey protection caps how many questions one Member
// can be asked in one conversation (ticket 12's quality triggers build on
// this).
package conversations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
)

// MaxQuestionsPerConversation is the over-survey cap: after this many
// Member answers, the conversation stops — the agent may not keep a Member
// on the hook past this. Ticket 12's quality triggers may end sooner.
const MaxQuestionsPerConversation = 10

// stopWords are the Member replies that stop a conversation outright.
// Matching is case-insensitive on the trimmed whole message.
var stopWords = map[string]bool{
	"stop": true, "unsubscribe": true, "quit": true, "cancel": true,
}

// Status is where a conversation sits.
type Status string

const (
	// StatusAwaitingMember: the agent asked and is paused on a checkpoint.
	StatusAwaitingMember Status = "awaiting_member"
	// StatusCompleted: every question answered (or the Member said done).
	StatusCompleted Status = "completed"
	// StatusStopped: the Member declined to continue, or the cap was hit.
	StatusStopped Status = "stopped"
)

var (
	// ErrNotFound is returned for unknown conversation or token lookups.
	ErrNotFound = errors.New("conversations: not found")
	// ErrClosed is returned when a reply arrives for a conversation that
	// can take no more turns (completed or stopped).
	ErrClosed = errors.New("conversations: conversation is closed")
	// ErrOverSurveyed is returned when replying would exceed the
	// question cap — over-survey protection, surfaced to the Member.
	ErrOverSurveyed = errors.New("conversations: the question limit for this conversation was reached")
)

// Turn is one message on the thread.
type Turn struct {
	Role string // "agent" | "member"
	Body string
}

// Conversation is the durable record of one Member conversation.
type Conversation struct {
	ID         string
	OrgID      string
	RequestID  string
	MemberID   string
	MemberName string
	Contact    string
	Topic      string
	Questions  []string
	// ResumeToken is the Member's capability: it rides their resumable
	// link and identifies the conversation on reply. No account.
	ResumeToken string
	// Thread is the full conversation, oldest first.
	Thread []Turn
	// Answers holds the Member's answer to each question, in order.
	Answers   []string
	Status    Status
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Start is what the caller submits to open a conversation.
type Start struct {
	OrgID      string
	RequestID  string
	MemberID   string
	MemberName string
	Contact    string
	Topic      string
	Questions  []string
}

// Agent is the Seam 2 collaborator: the agent sidecar, spoken to through
// the contract. *agentclient.Client satisfies it; tests use a fake.
type Agent interface {
	Start(ctx context.Context, req contract.ConversationStartRequest) (contract.ConversationResponse, error)
	Reply(ctx context.Context, req contract.ConversationReplyRequest) (contract.ConversationResponse, error)
}

// Store is the persistence seam for conversations. Postgres implements it
// for the system of record; MemoryStore backs tests.
type Store interface {
	CreateConversation(ctx context.Context, c *Conversation) error
	ConversationByID(ctx context.Context, id string) (Conversation, error)
	ConversationByToken(ctx context.Context, token string) (Conversation, error)
	ConversationsByOrg(ctx context.Context, orgID string) ([]Conversation, error)
	UpdateConversation(ctx context.Context, c Conversation) error
}

// Compile-time check that MemoryStore satisfies Store.
var _ Store = (*MemoryStore)(nil)

// MemoryStore is the in-memory Store double used by tests.
type MemoryStore struct {
	mu     sync.Mutex
	convs  map[string]Conversation
	tokens map[string]string // resume token → conversation ID
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{convs: map[string]Conversation{}, tokens: map[string]string{}}
}

func (m *MemoryStore) CreateConversation(_ context.Context, c *Conversation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.convs[c.ID]; dup {
		return fmt.Errorf("conversations: id %s already exists", c.ID)
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = now
	}
	m.convs[c.ID] = *c
	m.tokens[c.ResumeToken] = c.ID
	return nil
}

func (m *MemoryStore) ConversationByID(_ context.Context, id string) (Conversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.convs[id]
	if !ok {
		return Conversation{}, ErrNotFound
	}
	return c, nil
}

func (m *MemoryStore) ConversationByToken(_ context.Context, token string) (Conversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.tokens[token]
	if !ok {
		return Conversation{}, ErrNotFound
	}
	return m.convs[id], nil
}

func (m *MemoryStore) ConversationsByOrg(_ context.Context, orgID string) ([]Conversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Conversation
	for _, c := range m.convs {
		if c.OrgID == orgID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (m *MemoryStore) UpdateConversation(_ context.Context, c Conversation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.convs[c.ID]; !ok {
		return ErrNotFound
	}
	c.UpdatedAt = time.Now().UTC()
	m.convs[c.ID] = c
	return nil
}

// Service is the conversation front door.
type Service struct {
	store Store
	agent Agent
	// PublicBaseURL prefixes every resumable link. Empty means the
	// deployment has no public URL: links still carry the token, the
	// member just sees no URL on the channel message.
	publicBaseURL string
}

// NewService wires the conversation service.
func NewService(store Store, agent Agent, publicBaseURL string) *Service {
	return &Service{store: store, agent: agent, publicBaseURL: publicBaseURL}
}

// NewID mints a conversation ID.
func NewID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("conversations: mint id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// newToken mints a resumable token: 24 crypto/rand bytes, hex.
func newToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("conversations: mint resume token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// ResumeURL builds the Member's resumable link for a token — the
// redemption surface the backend itself serves.
func (s *Service) ResumeURL(token string) string {
	if s.publicBaseURL == "" {
		return ""
	}
	return strings.TrimRight(s.publicBaseURL, "/") + "/api/v1/member/resume/" + token
}

// contractThread converts the stored thread to contract shapes.
func contractThread(c Conversation) []contract.ThreadMessage {
	out := make([]contract.ThreadMessage, 0, len(c.Thread))
	for _, t := range c.Thread {
		out = append(out, contract.ThreadMessage{Role: t.Role, Body: t.Body})
	}
	return out
}

// Start opens a conversation: the agent asks the first question over the
// Member's Channel, then pauses. The record — thread, token, status — is
// durable before Start returns.
func (s *Service) Start(ctx context.Context, st Start) (Conversation, error) {
	if strings.TrimSpace(st.MemberName) == "" {
		return Conversation{}, fmt.Errorf("conversations: member name is required")
	}
	if len(st.Questions) == 0 {
		return Conversation{}, fmt.Errorf("conversations: at least one question is required")
	}
	id, err := NewID()
	if err != nil {
		return Conversation{}, err
	}
	token, err := newToken()
	if err != nil {
		return Conversation{}, err
	}

	c := Conversation{
		ID:          id,
		OrgID:       st.OrgID,
		RequestID:   st.RequestID,
		MemberID:    st.MemberID,
		MemberName:  st.MemberName,
		Contact:     st.Contact,
		Topic:       st.Topic,
		Questions:   st.Questions,
		ResumeToken: token,
		Thread:      []Turn{},
		Answers:     []string{},
		Status:      StatusAwaitingMember,
	}
	resp, err := s.agent.Start(ctx, contract.ConversationStartRequest{
		ConversationID: id,
		RequestID:      st.RequestID,
		MemberName:     st.MemberName,
		Contact:        st.Contact,
		Topic:          st.Topic,
		Questions:      st.Questions,
		ResumeURL:      s.ResumeURL(token),
	})
	if err != nil {
		return Conversation{}, fmt.Errorf("conversations: agent start: %w", err)
	}
	if !resp.Delivered {
		return Conversation{}, fmt.Errorf("conversations: agent did not deliver the opening message on %q", st.Contact)
	}
	// The contract's thread is the full conversation: adopt it wholesale.
	c.Thread = c.Thread[:0]
	for _, t := range resp.Thread {
		c.Thread = append(c.Thread, Turn{Role: t.Role, Body: t.Body})
	}
	// The answers ride the same rule: the agent reports all of them so far.
	c.Answers = resp.Answers
	c.Status = Status(resp.Status)
	if err := s.store.CreateConversation(ctx, &c); err != nil {
		return Conversation{}, err
	}
	return c, nil
}

// ByID returns one conversation after the entitlement check.
func (s *Service) ByID(ctx context.Context, id, orgID string) (Conversation, error) {
	c, err := s.store.ConversationByID(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	if c.OrgID != orgID {
		return Conversation{}, ErrNotFound // existence is not disclosed
	}
	return c, nil
}

// ByOrg lists the org's conversations.
func (s *Service) ByOrg(ctx context.Context, orgID string) ([]Conversation, error) {
	return s.store.ConversationsByOrg(ctx, orgID)
}

// ByToken redeems a resumable link: the token is the Member's whole
// capability, so no org check applies — whoever holds the token may read
// the conversation's record (questions asked, answers given).
func (s *Service) ByToken(ctx context.Context, token string) (Conversation, error) {
	return s.store.ConversationByToken(ctx, token)
}

// MemberReply resumes a conversation by the Member's resumable token: the
// message (typed, or a transcribed voice note) rides to the agent, the
// agent resumes from its checkpoint, and the thread grows. Over-survey
// protection has the last word: stop words stop the conversation, the
// question cap closes it, a closed conversation takes nothing.
func (s *Service) MemberReply(ctx context.Context, token, message string) (Conversation, error) {
	c, err := s.store.ConversationByToken(ctx, token)
	if err != nil {
		return Conversation{}, err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return Conversation{}, fmt.Errorf("conversations: message is empty")
	}
	switch c.Status {
	case StatusCompleted, StatusStopped:
		return c, ErrClosed
	case StatusAwaitingMember:
	default:
		return c, ErrClosed
	}

	// Over-survey protection, member side: a stop word stops the
	// conversation immediately — the record keeps the opt-out.
	if stopWords[strings.ToLower(message)] {
		c.Thread = append(c.Thread, Turn{Role: "member", Body: message})
		c.Status = StatusStopped
		if err := s.store.UpdateConversation(ctx, c); err != nil {
			return Conversation{}, err
		}
		return c, nil
	}

	// Over-survey protection, agent side: past the cap, the Member is not
	// asked anything more — the reply is accepted, the conversation stops.
	if len(c.Answers) >= MaxQuestionsPerConversation {
		c.Thread = append(c.Thread, Turn{Role: "member", Body: message})
		c.Status = StatusStopped
		if err := s.store.UpdateConversation(ctx, c); err != nil {
			return Conversation{}, err
		}
		return c, fmt.Errorf("%w: thank you — this conversation has ended", ErrOverSurveyed)
	}

	resp, err := s.agent.Reply(ctx, contract.ConversationReplyRequest{
		ConversationID: c.ID,
		Message:        message,
		Thread:         contractThread(c),
		Contact:        c.Contact,
		Questions:      c.Questions,
		ResumeURL:      s.ResumeURL(c.ResumeToken),
	})
	if err != nil {
		return c, fmt.Errorf("conversations: agent reply: %w", err)
	}
	if !resp.Delivered {
		return c, fmt.Errorf("conversations: agent did not deliver its message on %q", c.Contact)
	}
	// The contract's thread is the full conversation: adopt it wholesale
	// (the agent's LangGraph state, including the checkpoint marker, is
	// the source of truth for the turn sequence).
	c.Thread = c.Thread[:0]
	for _, t := range resp.Thread {
		c.Thread = append(c.Thread, Turn{Role: t.Role, Body: t.Body})
	}
	// The answers ride the same rule: the agent reports all of them so far.
	c.Answers = resp.Answers
	c.Status = Status(resp.Status)
	if err := s.store.UpdateConversation(ctx, c); err != nil {
		return Conversation{}, err
	}
	return c, nil
}
