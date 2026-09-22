package membership

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Role is one of the three platform roles (ticket 02). New accounts may hold
// only the first two; Platform Admins are provisioned out of band.
type Role string

const (
	RoleDataConsumer       Role = "data_consumer"
	RoleFarmerOrganization Role = "farmer_organization"
	RolePlatformAdmin      Role = "platform_admin"
)

// Status is an account's lifecycle state. Farmer Organizations start
// pending_approval until a Platform Admin decides; consumers start active.
type Status string

const (
	StatusActive          Status = "active"
	StatusPendingApproval Status = "pending_approval"
	StatusRejected        Status = "rejected"
)

// ErrNotFound is returned by lookups that find nothing.
var ErrNotFound = errors.New("membership: not found")

// ErrConflict is returned when a uniqueness constraint fails (e.g. the email
// is already registered).
var ErrConflict = errors.New("membership: already exists")

// Account is a registered person or organization. PasswordHash is the
// membership-package hash string; it never crosses the HTTP boundary.
type Account struct {
	ID           string
	Email        string
	PasswordHash string
	DisplayName  string
	Role         Role
	Status       Status
	VerifiedAt   *time.Time
	CreatedAt    time.Time
}

// Public strips credential material for the HTTP boundary.
func (a Account) Public() PublicAccount {
	return PublicAccount{
		ID:          a.ID,
		Email:       a.Email,
		DisplayName: a.DisplayName,
		Role:        a.Role,
		Status:      a.Status,
		Verified:    a.VerifiedAt != nil,
		CreatedAt:   a.CreatedAt,
	}
}

// PublicAccount is the account as the API renders it — no password hash.
type PublicAccount struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	Role        Role      `json:"role"`
	Status      Status    `json:"status"`
	Verified    bool      `json:"verified"`
	CreatedAt   time.Time `json:"created_at"`
}

// TOSVersion is one published version of the Terms of Service.
type TOSVersion struct {
	Version     string
	Body        string
	PublishedAt time.Time
}

// TOSAcceptance records which version an account accepted, and when — the
// per-account proof story 7 asks for.
type TOSAcceptance struct {
	AccountID  string
	Version    string
	AcceptedAt time.Time
}

// Session is an authenticated browser session. RequiresReacceptance marks a
// session created while a newer TOS version exists than the account last
// accepted: the holder may act only on the TOS re-acceptance flow.
type Session struct {
	Token                string
	AccountID            string
	CreatedAt            time.Time
	RequiresReacceptance bool
}

// Store is the persistence seam the API handlers code against. The postgres
// package implements it for the system of record; MemoryStore backs tests.
type Store interface {
	// Accounts. CreateAccount fills acct.ID and CreatedAt.
	CreateAccount(ctx context.Context, acct *Account) error
	AccountByEmail(ctx context.Context, email string) (Account, error)
	AccountByID(ctx context.Context, id string) (Account, error)
	SetAccountVerified(ctx context.Context, id string, at time.Time) error
	SetApplicationStatus(ctx context.Context, id string, status Status) error
	PendingOrganizations(ctx context.Context) ([]Account, error)

	// Verification tokens (email verification)
	CreateVerificationToken(ctx context.Context, token string, accountID string, expiresAt time.Time) error
	ConsumeVerificationToken(ctx context.Context, token string, now time.Time) (string, error)

	// Terms of Service
	PublishTOS(ctx context.Context, v TOSVersion) error
	CurrentTOS(ctx context.Context) (TOSVersion, error)
	TOSVersions(ctx context.Context) ([]TOSVersion, error)
	RecordAcceptance(ctx context.Context, acc TOSAcceptance) error
	LatestAcceptance(ctx context.Context, accountID string) (TOSAcceptance, error)

	// Sessions
	CreateSession(ctx context.Context, s Session) error
	SessionByToken(ctx context.Context, token string) (Session, error)
	UpdateSession(ctx context.Context, s Session) error
	DeleteSession(ctx context.Context, token string) error
}

// Compile-time check that MemoryStore satisfies Store.
var _ Store = (*MemoryStore)(nil)

// MemoryStore is the in-memory Store double used by Seam 1 tests.
type MemoryStore struct {
	mu          sync.Mutex
	accounts    map[string]Account // by ID
	byEmail     map[string]string  // email → ID
	verifTokens map[string]struct {
		accountID string
		expiresAt time.Time
		used      bool
	}
	tos        map[string]TOSVersion // version → doc
	acceptance map[string]TOSAcceptance
	sessions   map[string]Session
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		accounts:    map[string]Account{},
		byEmail:     map[string]string{},
		verifTokens: map[string]struct {
			accountID string
			expiresAt time.Time
			used      bool
		}{},
		tos:        map[string]TOSVersion{},
		acceptance: map[string]TOSAcceptance{},
		sessions:   map[string]Session{},
	}
}

func (m *MemoryStore) CreateAccount(_ context.Context, acct *Account) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.byEmail[acct.Email]; dup {
		return ErrConflict
	}
	if acct.ID == "" {
		acct.ID = newTokenID("acct")
	}
	if acct.CreatedAt.IsZero() {
		acct.CreatedAt = time.Now().UTC()
	}
	m.accounts[acct.ID] = *acct
	m.byEmail[acct.Email] = acct.ID
	return nil
}

func (m *MemoryStore) AccountByEmail(_ context.Context, email string) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byEmail[email]
	if !ok {
		return Account{}, ErrNotFound
	}
	return m.accounts[id], nil
}

func (m *MemoryStore) AccountByID(_ context.Context, id string) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	acct, ok := m.accounts[id]
	if !ok {
		return Account{}, ErrNotFound
	}
	return acct, nil
}

func (m *MemoryStore) SetAccountVerified(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	acct, ok := m.accounts[id]
	if !ok {
		return ErrNotFound
	}
	acct.VerifiedAt = &at
	m.accounts[id] = acct
	return nil
}

func (m *MemoryStore) SetApplicationStatus(_ context.Context, id string, status Status) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	acct, ok := m.accounts[id]
	if !ok {
		return ErrNotFound
	}
	acct.Status = status
	m.accounts[id] = acct
	return nil
}

func (m *MemoryStore) PendingOrganizations(_ context.Context) ([]Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Account
	for _, acct := range m.accounts {
		if acct.Role == RoleFarmerOrganization && acct.Status == StatusPendingApproval {
			out = append(out, acct)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryStore) CreateVerificationToken(_ context.Context, token, accountID string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.accounts[accountID]; !ok {
		return ErrNotFound
	}
	m.verifTokens[token] = struct {
		accountID string
		expiresAt time.Time
		used      bool
	}{accountID: accountID, expiresAt: expiresAt}
	return nil
}

func (m *MemoryStore) ConsumeVerificationToken(_ context.Context, token string, now time.Time) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	vt, ok := m.verifTokens[token]
	if !ok || vt.used || now.After(vt.expiresAt) {
		return "", ErrNotFound
	}
	vt.used = true
	m.verifTokens[token] = vt
	return vt.accountID, nil
}

func (m *MemoryStore) PublishTOS(_ context.Context, v TOSVersion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v.PublishedAt.IsZero() {
		v.PublishedAt = time.Now().UTC()
	}
	m.tos[v.Version] = v
	return nil
}

func (m *MemoryStore) CurrentTOS(_ context.Context) (TOSVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	versions := make([]TOSVersion, 0, len(m.tos))
	for _, v := range m.tos {
		versions = append(versions, v)
	}
	if len(versions) == 0 {
		return TOSVersion{}, ErrNotFound
	}
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].PublishedAt.After(versions[j].PublishedAt)
	})
	return versions[0], nil
}

func (m *MemoryStore) TOSVersions(_ context.Context) ([]TOSVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TOSVersion, 0, len(m.tos))
	for _, v := range m.tos {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].PublishedAt.After(out[j].PublishedAt)
	})
	return out, nil
}

func (m *MemoryStore) RecordAcceptance(_ context.Context, acc TOSAcceptance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if acc.AcceptedAt.IsZero() {
		acc.AcceptedAt = time.Now().UTC()
	}
	m.acceptance[acc.AccountID] = acc // latest wins
	return nil
}

func (m *MemoryStore) LatestAcceptance(_ context.Context, accountID string) (TOSAcceptance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	acc, ok := m.acceptance[accountID]
	if !ok {
		return TOSAcceptance{}, ErrNotFound
	}
	return acc, nil
}

func (m *MemoryStore) CreateSession(_ context.Context, s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	m.sessions[s.Token] = s
	return nil
}

func (m *MemoryStore) SessionByToken(_ context.Context, token string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[token]
	if !ok {
		return Session{}, ErrNotFound
	}
	return s, nil
}

func (m *MemoryStore) UpdateSession(_ context.Context, s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[s.Token]; !ok {
		return ErrNotFound
	}
	m.sessions[s.Token] = s
	return nil
}

func (m *MemoryStore) DeleteSession(_ context.Context, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, token)
	return nil
}
