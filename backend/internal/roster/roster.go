// Package roster implements the Farmer Organization's Member roster
// (ticket 11): contact points for Farmer Members, no Member accounts. A
// Farmer Member never logs in — the Agent reaches them on their contact
// point's channel, and the resumable link (ticket 11) is their capability.
package roster

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
)

// ErrNotFound is returned by lookups that find nothing.
var ErrNotFound = errors.New("roster: not found")

// ErrForbidden is returned when the caller may not touch the member. The
// API layer maps it to 403 (a stranger's roster reads as 404 there).
var ErrForbidden = errors.New("roster: not your member")

// Member is one roster entry: a person the Agent can reach on a Channel.
type Member struct {
	ID          string
	OrgID       string
	DisplayName string
	// Contact is channel-qualified: 'whatsapp:+353860000001',
	// 'telegram:12345', 'email:member@farm.ie'.
	Contact   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewMember is what the org submits when adding a member.
type NewMember struct {
	DisplayName string
	Contact     string
}

// ValidateContact enforces the channel-qualified contact-point format.
// The channel set matches the agent's outbound adapters (Seam 3).
func ValidateContact(contact string) error {
	channel, address, found := strings.Cut(contact, ":")
	if !found || channel == "" || address == "" {
		return fmt.Errorf("roster: contact must be 'channel:address' (e.g. 'whatsapp:+353860000001')")
	}
	switch channel {
	case "whatsapp", "telegram", "email", "web":
	default:
		return fmt.Errorf("roster: contact names unknown channel %q", channel)
	}
	return nil
}

// Validate refuses roster entries that must never be stored.
func (n NewMember) Validate() error {
	if strings.TrimSpace(n.DisplayName) == "" {
		return fmt.Errorf("roster: member name is required")
	}
	return ValidateContact(n.Contact)
}

// NewID mints a member ID: 16 crypto/rand bytes, hex.
func NewID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("roster: mint member id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Store is the persistence seam for the roster. The postgres package
// implements it for the system of record; MemoryStore backs tests.
type Store interface {
	CreateMember(ctx context.Context, m *Member) error
	MemberByID(ctx context.Context, id string) (Member, error)
	MembersByOrg(ctx context.Context, orgID string) ([]Member, error)
	UpdateMember(ctx context.Context, m Member) error
	DeleteMember(ctx context.Context, id string) error
}

// Compile-time check that MemoryStore satisfies Store.
var _ Store = (*MemoryStore)(nil)

// MemoryStore is the in-memory Store double used by tests.
type MemoryStore struct {
	mu      sync.Mutex
	members map[string]Member
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{members: map[string]Member{}}
}

func (m *MemoryStore) CreateMember(_ context.Context, mem *Member) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.members[mem.ID]; dup {
		return fmt.Errorf("roster: id %s already exists", mem.ID)
	}
	now := time.Now().UTC()
	if mem.CreatedAt.IsZero() {
		mem.CreatedAt = now
	}
	if mem.UpdatedAt.IsZero() {
		mem.UpdatedAt = now
	}
	m.members[mem.ID] = *mem
	return nil
}

func (m *MemoryStore) MemberByID(_ context.Context, id string) (Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem, ok := m.members[id]
	if !ok {
		return Member{}, ErrNotFound
	}
	return mem, nil
}

func (m *MemoryStore) MembersByOrg(_ context.Context, orgID string) ([]Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Member
	for _, mem := range m.members {
		if mem.OrgID == orgID {
			out = append(out, mem)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (m *MemoryStore) UpdateMember(_ context.Context, mem Member) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.members[mem.ID]; !ok {
		return ErrNotFound
	}
	mem.UpdatedAt = time.Now().UTC()
	m.members[mem.ID] = mem
	return nil
}

func (m *MemoryStore) DeleteMember(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.members[id]; !ok {
		return ErrNotFound
	}
	delete(m.members, id)
	return nil
}

// Service is the roster front door, scoped to the owning org.
type Service struct {
	store Store
}

// NewService wires the roster service.
func NewService(store Store) *Service { return &Service{store: store} }

// Add records a new member for the org.
func (s *Service) Add(ctx context.Context, orgID string, n NewMember) (Member, error) {
	if err := n.Validate(); err != nil {
		return Member{}, err
	}
	if orgID == "" {
		return Member{}, fmt.Errorf("roster: org is required")
	}
	id, err := NewID()
	if err != nil {
		return Member{}, err
	}
	m := &Member{
		ID:          id,
		OrgID:       orgID,
		DisplayName: strings.TrimSpace(n.DisplayName),
		Contact:     strings.TrimSpace(n.Contact),
	}
	if err := s.store.CreateMember(ctx, m); err != nil {
		return Member{}, err
	}
	return *m, nil
}

// List returns the org's roster, oldest first.
func (s *Service) List(ctx context.Context, orgID string) ([]Member, error) {
	return s.store.MembersByOrg(ctx, orgID)
}

// ByID returns one member after the entitlement check.
func (s *Service) ByID(ctx context.Context, id, orgID string) (Member, error) {
	m, err := s.store.MemberByID(ctx, id)
	if err != nil {
		return Member{}, err
	}
	if m.OrgID != orgID {
		return Member{}, ErrForbidden
	}
	return m, nil
}

// Update rewrites one member's name and contact point.
func (s *Service) Update(ctx context.Context, id, orgID string, n NewMember) (Member, error) {
	if err := n.Validate(); err != nil {
		return Member{}, err
	}
	existing, err := s.ByID(ctx, id, orgID) // entitlement check
	if err != nil {
		return Member{}, err
	}
	existing.DisplayName = strings.TrimSpace(n.DisplayName)
	existing.Contact = strings.TrimSpace(n.Contact)
	if err := s.store.UpdateMember(ctx, existing); err != nil {
		return Member{}, err
	}
	return existing, nil
}

// Remove deletes one member from the org's roster.
func (s *Service) Remove(ctx context.Context, id, orgID string) error {
	if _, err := s.ByID(ctx, id, orgID); err != nil {
		return err
	}
	return s.store.DeleteMember(ctx, id)
}
