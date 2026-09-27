// Package memoryprov implements ticket 14's Memory Provider registry: the
// admin-configured external recall services (mem0, Hindsight, supermemory,
// Honcho — ADR 0003) the agent connects to over MCP. Like the payment
// gateway they are platform configuration, never a Module: third parties
// do not author platform infrastructure. A provider record is a connection
// fact — name and endpoint — and nothing else; the agent speaks MCP to the
// endpoint with no credentials, so the shape has nowhere to hide them.
package memoryprov

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Sentinel errors. ErrExists: the name is taken. ErrNotFound: the ID does
// not match any provider. ErrInvalid: the record failed validation — the
// caller's fault, so the API renders it 422 rather than 5xx.
var (
	ErrExists   = errors.New("memoryprov: a provider with that name already exists")
	ErrNotFound = errors.New("memoryprov: no such provider")
	ErrInvalid  = errors.New("memoryprov: invalid provider")
)

// namePattern: lowercase slugs — the name rides the contract as the
// provider's identifier and shows in admin UIs; it is an ID, not marketing
// copy.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// maxProviders caps the registry. Every configured provider is dialed on
// every clarify turn; an unbounded list would turn recall into an
// outage-shaped latency tax.
const maxProviders = 5

// Provider is one configured Memory Provider: the connection fact the
// backend hands the agent on every clarify turn (contract field
// memory_providers). No credentials — MCP over the endpoint is the whole
// story.
type Provider struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Endpoint  string    `json:"endpoint"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate refuses records that cannot work: blank or malformed name, or
// an endpoint that is not an http(s) URL. The endpoint must be reachable
// is the agent's discovery, not this check.
func (p Provider) Validate() error {
	if !namePattern.MatchString(p.Name) {
		return fmt.Errorf("memoryprov: name must be a lowercase slug (letters, digits, dashes)")
	}
	if strings.TrimSpace(p.Endpoint) == "" {
		return fmt.Errorf("memoryprov: endpoint is required")
	}
	u, err := url.Parse(p.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("memoryprov: endpoint must be an http(s) URL, got %q", p.Endpoint)
	}
	return nil
}

// Store is the persistence seam: the postgres package implements it for
// the system of record; MemoryStore backs tests.
type Store interface {
	Create(ctx context.Context, p *Provider) error
	List(ctx context.Context) ([]Provider, error)
	Delete(ctx context.Context, id string) error
}

// Compile-time check that MemoryStore satisfies Store.
var _ Store = (*MemoryStore)(nil)

// MemoryStore is the in-memory Store double used by tests.
type MemoryStore struct {
	mu        sync.Mutex
	byID      map[string]Provider
	byName    map[string]string // name → ID
	createdAt map[string]time.Time
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		byID:      map[string]Provider{},
		byName:    map[string]string{},
		createdAt: map[string]time.Time{},
	}
}

func (m *MemoryStore) Create(_ context.Context, p *Provider) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.byName[p.Name]; taken {
		return ErrExists
	}
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = now
	}
	m.byID[p.ID] = *p
	m.byName[p.Name] = p.ID
	m.createdAt[p.ID] = p.CreatedAt
	return nil
}

func (m *MemoryStore) List(_ context.Context) ([]Provider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Provider, 0, len(m.byID))
	for _, p := range m.byID {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func (m *MemoryStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.byID[id]
	if !ok {
		return ErrNotFound
	}
	delete(m.byID, id)
	delete(m.byName, p.Name)
	delete(m.createdAt, id)
	return nil
}

// Service is the registry front door: create, list, delete. Every mutation
// validates before it reaches the store, so an unusable provider can never
// ride a clarify turn.
type Service struct {
	store Store
}

// NewService wires the registry over its store.
func NewService(store Store) *Service { return &Service{store: store} }

// Create validates and stores one provider. The name is trimmed and
// checked against the slug pattern; the endpoint must be http(s).
func (s *Service) Create(ctx context.Context, name, endpoint string) (Provider, error) {
	p := Provider{
		Name:     strings.TrimSpace(name),
		Endpoint: strings.TrimSpace(endpoint),
	}
	if err := p.Validate(); err != nil {
		return Provider{}, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	id, err := newProviderID()
	if err != nil {
		return Provider{}, err
	}
	p.ID = id
	if err := s.store.Create(ctx, &p); err != nil {
		return Provider{}, err
	}
	return p, nil
}

// List returns the configured providers, newest first. Nil is never
// returned — an empty registry is an empty slice.
func (s *Service) List(ctx context.Context) ([]Provider, error) {
	return s.store.List(ctx)
}

// Delete removes one provider by ID. An unknown or malformed ID is
// ErrNotFound — a malformed one is the caller's fault, so it renders 404,
// and the store never sees an ID that cannot be its key.
func (s *Service) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if !providerIDPattern.MatchString(strings.ReplaceAll(id, "-", "")) {
		return ErrNotFound
	}
	return s.store.Delete(ctx, id)
}

// providerIDPattern: 32 hex chars — the shape newProviderID mints; the
// dashes are stripped first because Postgres renders UUID text with them.
var providerIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Count reports how many providers are configured (the cap check rides
// here so the API and the Chat wiring share one limit).
func (s *Service) Count(ctx context.Context) (int, error) {
	providers, err := s.store.List(ctx)
	if err != nil {
		return 0, err
	}
	return len(providers), nil
}

// MaxProviders is the registry cap, exported for the API layer's error
// message.
const MaxProviders = maxProviders

// newProviderID mints an ID: 16 crypto/rand bytes, hex — the other
// registries' convention.
func newProviderID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("memoryprov: mint id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
