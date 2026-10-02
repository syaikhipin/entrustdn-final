// Package requests implements a Data Consumer's Request (ticket 07): a
// commission — data wanted, format, quality bar, budget in Credits — that
// the Agent clarifies through a web-chat conversation. Every clarification
// turn is metered and charged to the Ledger against the Request's budget;
// the budget can never be silently exceeded. Request production (the new
// Collection gathered from Farmer Members) is later tickets' work: this
// package owns the Request record, the conversation, and the metering.
package requests

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/modules"
)

// Status is where a Request sits in its life.
type Status string

const (
	// StatusClarifying: the Agent and Consumer are still shaping the need.
	StatusClarifying Status = "clarifying"
	// StatusClarified: the Agent judged the need fully specified.
	StatusClarified Status = "clarified"
	// StatusBudgetExhausted: the budget could not cover the last turn — the
	// Consumer must raise the budget (a later ticket's surface) or abandon
	// the Request. Surfaced, never silent.
	StatusBudgetExhausted Status = "budget_exhausted"
)

// ErrNotFound is returned by lookups that find nothing.
var ErrNotFound = errors.New("requests: not found")

// ErrForbidden is returned when the caller may not touch the Request. The
// API layer maps it to 403; a stranger's request reads as 404 there.
var ErrForbidden = errors.New("requests: not your request")

// ErrClosed is returned when a chat turn arrives for a Request that is no
// longer taking turns.
var ErrClosed = errors.New("requests: request is not open for clarification")

// ErrTooManySkills marks an attach that would exceed the request's skill
// cap — the caller's fault, so the API renders it 422.
var ErrTooManySkills = errors.New("requests: too many skills")

// ErrTooManyConnectors is the connectors-list twin of ErrTooManySkills
// (ticket 14).
var ErrTooManyConnectors = errors.New("requests: too many connectors")

// ErrBudgetExceeded wraps the refusal to charge a turn that would cross the
// request's budget (and, via credits.ErrInsufficientFunds inside it, the
// account's balance). It is always surfaced: status flips to
// budget_exhausted and the consumer sees the reason.
var ErrBudgetExceeded = errors.New("requests: budget exceeded")

// Message is one turn of the clarification conversation.
type Message struct {
	Role      string // "consumer" | "agent"
	Body      string
	CreatedAt time.Time
}

// Match is one existing Data Asset the Agent reported as already answering
// the need.
type Match struct {
	AssetID    string
	Name       string
	Reason     string
	ReportedAt time.Time
}

// Request is a Data Consumer's commission.
type Request struct {
	ID           string
	ConsumerID   string
	Description  string
	Format       string
	QualityBar   string
	BudgetMicros int64
	SpentMicros  int64
	Status       Status
	// Messages is the full web-chat conversation, oldest first.
	Messages []Message
	// Matches is every existing-Asset match the Agent has reported so far.
	Matches []Match
	// Template is the attached Process Template snapshot (ticket 13): the
	// spec rides the Request, so the Collection's questions and triggers
	// are frozen at attach time. Nil means the default flow.
	Template *TemplateAttachment
	// Skills lists the attached Agent Skills' Module IDs — the agent loads
	// their instructions into context for every clarify turn.
	Skills []string
	// Connectors lists the attached Connector Modules' IDs (ticket 14) —
	// resolved to connection facts at clarify-turn time through the
	// registry, so the latest version always rides the wire.
	Connectors []string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// TemplateAttachment is the frozen copy of a Process Template attached to
// a Request: which Module it came from, and the parsed spec as it stood
// when the consumer attached it.
type TemplateAttachment struct {
	ModuleID string               `json:"module_id"`
	Spec     modules.TemplateSpec `json:"spec"`
}

// NewRequest is what the Consumer submits when creating a Request.
type NewRequest struct {
	Description  string
	Format       string
	QualityBar   string
	BudgetMicros int64
}

// Validate refuses records that must never be stored: no description, no
// format, or a budget that could never fund a turn.
func (n NewRequest) Validate() error {
	if strings.TrimSpace(n.Description) == "" {
		return fmt.Errorf("requests: description is required")
	}
	if strings.TrimSpace(n.Format) == "" {
		return fmt.Errorf("requests: format is required")
	}
	if n.BudgetMicros <= 0 {
		return fmt.Errorf("requests: budget must be positive")
	}
	return nil
}

// NewID mints a request ID: 16 crypto/rand bytes, hex.
func NewID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("requests: mint request id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Store is the persistence seam for Requests. The postgres package
// implements it for the system of record; MemoryStore backs tests.
type Store interface {
	// CreateRequest stores a new record, filling timestamps when zero.
	CreateRequest(ctx context.Context, r *Request) error
	// RequestByID loads one record.
	RequestByID(ctx context.Context, id string) (Request, error)
	// RequestsByConsumer lists the consumer's requests, newest first.
	RequestsByConsumer(ctx context.Context, consumerID string) ([]Request, error)
	// FieldableRequests lists every clarified request, newest first — the
	// org-facing commissions.
	FieldableRequests(ctx context.Context) ([]Request, error)
	// UpdateRequest rewrites the whole record after a chat turn: messages,
	// matches, spend, status. The conversation is single-writer (the chat
	// path), so a whole-record update is the honest seam.
	UpdateRequest(ctx context.Context, r Request) error
}

// Compile-time check that MemoryStore satisfies Store.
var _ Store = (*MemoryStore)(nil)

// MemoryStore is the in-memory Store double used by tests.
type MemoryStore struct {
	mu       sync.Mutex
	requests map[string]Request
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{requests: map[string]Request{}}
}

func (m *MemoryStore) CreateRequest(_ context.Context, r *Request) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.requests[r.ID]; dup {
		return fmt.Errorf("requests: id %s already exists", r.ID)
	}
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now
	}
	m.requests[r.ID] = *r
	return nil
}

func (m *MemoryStore) RequestByID(_ context.Context, id string) (Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.requests[id]
	if !ok {
		return Request{}, ErrNotFound
	}
	return r, nil
}

func (m *MemoryStore) RequestsByConsumer(_ context.Context, consumerID string) ([]Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Request
	for _, r := range m.requests {
		if r.ConsumerID == consumerID {
			out = append(out, r)
		}
	}
	sortRequests(out)
	return out, nil
}

func (m *MemoryStore) FieldableRequests(_ context.Context) ([]Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Request
	for _, r := range m.requests {
		if r.Status == StatusClarified {
			out = append(out, r)
		}
	}
	sortRequests(out)
	return out, nil
}

func (m *MemoryStore) UpdateRequest(_ context.Context, r Request) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.requests[r.ID]; !ok {
		return ErrNotFound
	}
	r.UpdatedAt = time.Now().UTC()
	m.requests[r.ID] = r
	return nil
}

// sortRequests orders records newest first, ID as the tiebreak.
func sortRequests(a []Request) {
	sort.Slice(a, func(i, j int) bool {
		if !a[i].CreatedAt.Equal(a[j].CreatedAt) {
			return a[i].CreatedAt.After(a[j].CreatedAt)
		}
		return a[i].ID < a[j].ID
	})
}

// Clarifier is the Seam 2 collaborator: the agent, spoken to through the
// Go↔Agent contract. *agentclient.Client satisfies it; tests use a fake.
type Clarifier interface {
	Clarify(ctx context.Context, req contract.ClarifyRequest) (contract.ClarifyResponse, error)
}

// ModuleSource is the ticket-13 seam to the Module registry: the request
// service asks it who may use a Module (attach-time authorization) and
// loads an attached Agent Skill's content / Connector Module's spec
// (turn-time, so the latest version always rides the wire).
// *modules.Service satisfies it; tests use a fake. Nil means no Modules
// are attached to anything — the default collection flow, no loaded
// skills, no connector facts, exactly the pre-ticket-13 shape.
type ModuleSource interface {
	Accessible(ctx context.Context, callerID, moduleID string) error
	Skill(ctx context.Context, callerID, moduleID string) (modules.SkillContent, error)
	// Connector is the ticket-14 half: the parsed connection fact of a
	// Connector Module (endpoint, transport, query — never credentials).
	Connector(ctx context.Context, callerID, moduleID string) (modules.ConnectorSpec, error)
}

// CatalogSource hands the agent the current catalog snapshot each turn —
// "checks the catalog first" means every turn sees the live inventory.
type CatalogSource func(ctx context.Context) ([]contract.CatalogAsset, error)

// Service is the Request front door: creation, the clarification chat loop,
// metering, and the budget guard.
type Service struct {
	store   Store
	agent   Clarifier
	catalog CatalogSource
	credits *credits.Service
	// mods is the Module registry seam (ticket 13); nil disables module
	// attachment entirely.
	mods ModuleSource

	// memProviders lists the platform's Memory Providers (ticket 14) — the
	// admin registry read per turn so config changes land without a
	// restart. Nil (or empty) means no recall context rides the wire.
	memProviders func(ctx context.Context) ([]contract.MemoryProvider, error)

	// turns guards the chat path's read-check-charge-update against
	// concurrent turns on one request. The guard's cap is only as honest as
	// its serialization: without this, two racing turns both read the same
	// spend and both pass. (Cross-process writers share the DB row; the
	// single-process pilot has one backend, so this lock is the honest seam
	// until that changes.)
	turns sync.Map // request ID → *sync.Mutex
}

// NewService wires the clarification service.
func NewService(store Store, agent Clarifier, catalog CatalogSource, credSvc *credits.Service) *Service {
	return &Service{store: store, agent: agent, catalog: catalog, credits: credSvc}
}

// SetModuleSource attaches the Module registry seam after construction —
// the API layer wires it late (the registry service is built beside the
// request handlers, not before them).
func SetModuleSource(s *Service, m ModuleSource) { s.mods = m }

// SetMemoryProviders attaches the Memory Provider source (ticket 14): a
// function reading the admin registry per turn. The contract type is the
// wire shape; the API layer adapts memoryprov.Provider onto it.
func SetMemoryProviders(s *Service, src func(ctx context.Context) ([]contract.MemoryProvider, error)) {
	s.memProviders = src
}

// lockRequest returns the per-request turn mutex, minting it on first use.
func (s *Service) lockRequest(id string) *sync.Mutex {
	mu, _ := s.turns.LoadOrStore(id, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// Create records the Consumer's commission: status clarifying, nothing
// spent yet.
func (s *Service) Create(ctx context.Context, consumerID string, n NewRequest) (Request, error) {
	if err := n.Validate(); err != nil {
		return Request{}, err
	}
	if consumerID == "" {
		return Request{}, fmt.Errorf("requests: consumer is required")
	}
	id, err := NewID()
	if err != nil {
		return Request{}, err
	}
	r := &Request{
		ID:           id,
		ConsumerID:   consumerID,
		Description:  strings.TrimSpace(n.Description),
		Format:       strings.TrimSpace(n.Format),
		QualityBar:   strings.TrimSpace(n.QualityBar),
		BudgetMicros: n.BudgetMicros,
		Status:       StatusClarifying,
		// Empty slices, never nil: the JSON boundary renders [] for the
		// skill and connector lists of a fresh request, not null.
		Skills:     []string{},
		Connectors: []string{},
	}
	if err := s.store.CreateRequest(ctx, r); err != nil {
		return Request{}, err
	}
	return *r, nil
}

// ByConsumer lists the consumer's requests, newest first.
func (s *Service) ByConsumer(ctx context.Context, consumerID string) ([]Request, error) {
	return s.store.RequestsByConsumer(ctx, consumerID)
}

// Fieldable lists every clarified Request — the commissions an approved
// Farmer Organization may turn into a Collection. The org-facing view;
// the consumer's private chat is stripped at the API layer, not here.
func (s *Service) Fieldable(ctx context.Context) ([]Request, error) {
	return s.store.FieldableRequests(ctx)
}

// ByID returns one request after the entitlement check — a stranger's
// request is ErrForbidden, which the API renders as 404.
func (s *Service) ByID(ctx context.Context, id, consumerID string) (Request, error) {
	r, err := s.store.RequestByID(ctx, id)
	if err != nil {
		return Request{}, err
	}
	if r.ConsumerID != consumerID {
		return Request{}, ErrForbidden
	}
	return r, nil
}

// Turn is the outcome of one clarification turn: the updated request, the
// agent's reply envelope, and what the turn charged to the Ledger.
type Turn struct {
	Request       Request
	Reply         contract.ClarifyResponse
	ChargedMicros int64
}

// Chat runs one clarification turn: the consumer's message goes to the
// agent with the Request so far and a live catalog snapshot; the reply is
// recorded, the reported usage is priced and charged to the Ledger, and
// the budget guard has the last word.
func (s *Service) Chat(ctx context.Context, id, consumerID, message string) (Turn, error) {
	// One turn at a time per request: the read-check-charge-update below
	// is not atomic, so racing turns are serialized here — the budget guard
	// depends on it (see Service.turns).
	mu := s.lockRequest(id)
	mu.Lock()
	defer mu.Unlock()

	var turn Turn
	r, err := s.ByID(ctx, id, consumerID)
	if err != nil {
		return Turn{}, err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return Turn{}, fmt.Errorf("requests: message is empty")
	}
	if r.Status != StatusClarifying {
		return Turn{}, ErrClosed
	}

	// Catalog first (ticket checklist): the snapshot rides with every turn
	// so the agent can report existing assets that answer the need.
	catalog, err := s.catalog(ctx)
	if err != nil {
		return Turn{}, fmt.Errorf("requests: load catalog: %w", err)
	}

	req := contract.ClarifyRequest{
		RequestID:    r.ID,
		Description:  r.Description,
		Format:       r.Format,
		QualityBar:   r.QualityBar,
		BudgetMicros: r.BudgetMicros,
		SpentMicros:  r.SpentMicros,
		Message:      message,
		// Empty slices, never nil: the contract's arrays must serialize as
		// [], not null (the schema and the Python side both refuse null).
		History: []contract.HistoryTurn{},
		Catalog: []contract.CatalogAsset{},
	}
	for _, m := range r.Messages {
		req.History = append(req.History, contract.HistoryTurn{Role: m.Role, Body: m.Body})
	}
	req.Catalog = catalog
	if req.Catalog == nil {
		req.Catalog = []contract.CatalogAsset{}
	}
	// Attached Agent Skills ride every turn (ticket 13): the agent loads
	// their instructions into context before answering.
	skills, err := s.loadSkills(ctx, r)
	if err != nil {
		return Turn{}, err
	}
	req.Skills = skills

	// Memory Providers (ticket 14): the admin registry read per turn, so a
	// provider added or removed lands on the next turn without a restart.
	// A registry read failure fails the turn — the config is platform
	// state, and guessing (omit? stale copy?) would be worse than surfacing.
	if s.memProviders != nil {
		providers, err := s.memProviders(ctx)
		if err != nil {
			return Turn{}, fmt.Errorf("requests: load memory providers: %w", err)
		}
		req.MemoryProviders = providers
	}
	if req.MemoryProviders == nil {
		req.MemoryProviders = []contract.MemoryProvider{}
	}

	// Attached Connector Modules ride every turn (ticket 14): the resolved
	// connection facts, latest version, so the agent can query live
	// sources during the turn.
	connectors, err := s.loadConnectors(ctx, r)
	if err != nil {
		return Turn{}, err
	}
	req.Connectors = connectors

	resp, err := s.agent.Clarify(ctx, req)
	if err != nil {
		return Turn{}, fmt.Errorf("requests: agent clarify: %w", err)
	}
	if resp.RequestID != r.ID {
		return Turn{}, fmt.Errorf("requests: agent answered request %q, want %q", resp.RequestID, r.ID)
	}
	if err := resp.Validate(); err != nil {
		return Turn{}, fmt.Errorf("requests: agent response invalid: %w", err)
	}

	// Record the turn before charging: the conversation is the record.
	now := time.Now().UTC()
	r.Messages = append(r.Messages,
		Message{Role: "consumer", Body: message, CreatedAt: now},
		Message{Role: "agent", Body: resp.Reply, CreatedAt: now},
	)
	for _, m := range resp.Matches {
		r.Matches = append(r.Matches, Match{AssetID: m.AssetID, Name: m.Name, Reason: m.Reason, ReportedAt: now})
	}
	if resp.Clarified {
		r.Status = StatusClarified
	}

	// Metering and the budget guard in one posting (ticket checklist): the
	// turn is priced and charged in a single capped step — cap = budget −
	// spend — so the budget can never be silently exceeded, and a pricing
	// rule change between looking and charging cannot slip a larger amount
	// through the guard.
	usage := credits.InferenceUsage{
		InputTokens:       resp.Usage.InputTokens,
		CachedInputTokens: resp.Usage.CachedInputTokens,
		OutputTokens:      resp.Usage.OutputTokens,
	}
	charge := credits.Charge{
		Scope:     credits.AccountScope(consumerID),
		Model:     resp.Usage.Model,
		RequestID: r.ID,
		ActorID:   consumerID,
		Memo:      "request clarification",
	}
	mov, err := s.credits.ChargeInferenceCapped(ctx, usage, charge, r.BudgetMicros-r.SpentMicros)
	switch {
	case err == nil:
		// The consumer's entry is the negative leg; the spend is its size.
		charged := -sumScope(mov.Entries, credits.AccountScope(consumerID))
		r.SpentMicros += charged
		turn.ChargedMicros = charged
	case errors.Is(err, credits.ErrCapExceeded):
		// The turn's price crossed what the budget has left: refuse, mark
		// the request budget_exhausted (surfaced, never silent), post
		// nothing, and carry the marked record out so callers see the new
		// status without re-reading.
		r.Status = StatusBudgetExhausted
		if uerr := s.store.UpdateRequest(ctx, r); uerr != nil {
			return Turn{}, uerr
		}
		return Turn{Request: r}, fmt.Errorf("%w: this turn would exceed the request's budget (%d µcr spent of %d µcr)",
			ErrBudgetExceeded, r.SpentMicros, r.BudgetMicros)
	case errors.Is(err, credits.ErrInsufficientFunds):
		// Account exhausted: surface it — the turn is refused, the request
		// is marked, and nothing was charged.
		r.Status = StatusBudgetExhausted
		if uerr := s.store.UpdateRequest(ctx, r); uerr != nil {
			return Turn{}, uerr
		}
		return Turn{Request: r}, fmt.Errorf("%w: this turn would exceed the request's budget", ErrBudgetExceeded)
	default:
		return Turn{}, fmt.Errorf("requests: post inference charge: %w", err)
	}

	if err := s.store.UpdateRequest(ctx, r); err != nil {
		return Turn{}, err
	}
	turn.Request = r
	turn.Reply = resp
	return turn, nil
}

// sumScope sums the entry amounts for one scope within a movement.
func sumScope(entries []credits.Entry, scope credits.Scope) int64 {
	var sum int64
	for _, e := range entries {
		if e.Scope == scope {
			sum += e.AmountMicros
		}
	}
	return sum
}
