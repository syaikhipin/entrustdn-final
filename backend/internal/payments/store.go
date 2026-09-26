package payments

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
)

// TopUpStore is the persistence seam for top-ups and the gateway config.
// The postgres package implements it for the system of record;
// MemoryStore backs the Seam 1 tests.
//
// Money safety lives in SettlePaid: the store atomically claims the
// top-up as settled AND records the Ledger movement in one transaction.
// A replayed or concurrent callback therefore cannot post twice, and a
// crash cannot leave a claimed settlement without its movement.
type TopUpStore interface {
	// SaveConfig upserts the single gateway configuration row.
	SaveConfig(ctx context.Context, cfg Config) error
	// LoadConfig returns the stored configuration; a disabled platform
	// (no row) returns a zero Config and no error.
	LoadConfig(ctx context.Context) (Config, error)

	// Create inserts a pending top-up. The generated ID and timestamps
	// fill the returned copy.
	Create(ctx context.Context, tu TopUp) (TopUp, error)
	// ByReference loads one top-up by its gateway reference.
	ByReference(ctx context.Context, reference string) (TopUp, error)
	// History returns the account's top-ups, newest first.
	History(ctx context.Context, accountID string, limit int) ([]TopUp, error)

	// SettlePaid claims the top-up as settled and records the movement the
	// post callback builds — atomically with the claim. The callback is
	// BUILD-ONLY: it validates (ErrAmountMismatch refuses cleanly) and
	// constructs the balanced movement; the store persists it (postgres:
	// same transaction; memory: same mutex hold). The bool reports whether
	// this call won the settlement (true: movement recorded; false:
	// already settled — a replay, nothing posted, no error).
	//   - unknown reference → ErrNotFound
	//   - already terminal with a different outcome → ErrAlreadyTerminal
	SettlePaid(ctx context.Context, reference string, post func(TopUp) (credits.Movement, error)) (TopUp, bool, error)

	// MarkTerminal records a failed or cancelled outcome — never a ledger
	// posting. Replays of the same outcome return the stored top-up with
	// no error; a contradicting outcome returns ErrAlreadyTerminal.
	//   - unknown reference → ErrNotFound
	MarkTerminal(ctx context.Context, reference string, status TopUpStatus) (TopUp, error)
}

// ErrUnsupportedProvider is returned by a Registry missing the provider.
var ErrUnsupportedProvider = errors.New("payments: provider has no gateway implementation")

// Registry builds gateways from the configured provider. The api package
// registers the real constructors at boot; the Service resolves the
// current config's provider per call, so a config change takes effect
// without a restart. Registry satisfies the Service's gateway-builder
// function type.
type Registry struct {
	builders map[string]func(Config) (Gateway, error)
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{builders: map[string]func(Config) (Gateway, error){}} }

// Register adds a provider constructor. Registering the same provider
// twice overwrites — boot-time wiring stays declarative.
func (r *Registry) Register(provider string, build func(Config) (Gateway, error)) {
	r.builders[provider] = build
}

// GatewayFor builds the gateway for cfg's provider.
func (r *Registry) GatewayFor(cfg Config) (Gateway, error) {
	build, ok := r.builders[cfg.Provider]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedProvider, cfg.Provider)
	}
	return build(cfg)
}

// BuildGateway is the function shape the Service takes for gateway
// construction; *Registry satisfies it via GatewayFor.
type BuildGateway func(cfg Config) (Gateway, error)

// MemoryStore is the in-memory TopUpStore double used by Seam 1 tests.
// One mutex guards the top-up table and the config, so SettlePaid's
// claim-and-post is exactly as atomic as the postgres implementation's
// single transaction.
type MemoryStore struct {
	mu     sync.Mutex
	ledger CreditLedger
	nextID int
	config Config
	byRef  map[string]*TopUp
	order  []string // references in creation order
}

// Compile-time check: the memory store satisfies the seam.
var _ TopUpStore = (*MemoryStore)(nil)

// NewMemoryStore returns an empty store whose settlements post to ledger.
func NewMemoryStore(ledger CreditLedger) *MemoryStore {
	return &MemoryStore{ledger: ledger, byRef: map[string]*TopUp{}}
}

// SaveConfig upserts the single config row.
func (m *MemoryStore) SaveConfig(_ context.Context, cfg Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config = cfg
	return nil
}

// LoadConfig returns the stored configuration (zero Config when unset).
func (m *MemoryStore) LoadConfig(_ context.Context) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config, nil
}

// Create inserts a pending top-up.
func (m *MemoryStore) Create(_ context.Context, tu TopUp) (TopUp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tu.ID == "" {
		// The service always mints the ID before opening the gateway
		// session; this fallback keeps direct store users safe.
		m.nextID++
		tu.ID = fmt.Sprintf("top_%d", m.nextID)
	}
	now := time.Now().UTC()
	tu.CreatedAt = now
	tu.UpdatedAt = now
	stored := tu
	m.byRef[tu.Reference] = &stored
	m.order = append(m.order, tu.Reference)
	return stored, nil
}

// ByReference loads one top-up by gateway reference.
func (m *MemoryStore) ByReference(_ context.Context, reference string) (TopUp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tu, ok := m.byRef[reference]
	if !ok {
		return TopUp{}, ErrNotFound
	}
	return *tu, nil
}

// History returns the account's top-ups, newest first.
func (m *MemoryStore) History(_ context.Context, accountID string, limit int) ([]TopUp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []TopUp
	for i := len(m.order) - 1; i >= 0 && len(out) < limit; i-- {
		if tu := m.byRef[m.order[i]]; tu.AccountID == accountID {
			out = append(out, *tu)
		}
	}
	return out, nil
}

// SettlePaid claims the settlement under the store mutex and records the
// movement: the callback builds, the memory ledger posts, and the claim
// lands in one mutex hold — the memory counterpart of the postgres
// store's single transaction. A posting failure rolls the claim back: the
// top-up stays pending and the callback can be retried.
func (m *MemoryStore) SettlePaid(_ context.Context, reference string, post func(TopUp) (credits.Movement, error)) (TopUp, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tu, ok := m.byRef[reference]
	if !ok {
		return TopUp{}, false, ErrNotFound
	}
	if tu.Status.terminal() {
		if tu.Status == StatusSettled {
			return *tu, false, nil // replay: already credited
		}
		return *tu, false, ErrAlreadyTerminal
	}
	mov, err := post(*tu)
	if err != nil {
		return *tu, false, err // claim rolled back: still pending
	}
	mov.ID = "" // the ledger mints the movement id
	mov, err = m.ledger.PostMovement(context.Background(), mov)
	if err != nil {
		return *tu, false, fmt.Errorf("payments: post top-up movement: %w", err)
	}
	tu.Status = StatusSettled
	tu.MovementID = mov.ID
	tu.UpdatedAt = time.Now().UTC()
	return *tu, true, nil
}

// MarkTerminal records a failed or cancelled outcome.
func (m *MemoryStore) MarkTerminal(_ context.Context, reference string, status TopUpStatus) (TopUp, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tu, ok := m.byRef[reference]
	if !ok {
		return TopUp{}, ErrNotFound
	}
	if tu.Status.terminal() {
		if tu.Status == status {
			return *tu, nil // replay of the same terminal outcome
		}
		return *tu, ErrAlreadyTerminal
	}
	tu.Status = status
	tu.UpdatedAt = time.Now().UTC()
	return *tu, nil
}
