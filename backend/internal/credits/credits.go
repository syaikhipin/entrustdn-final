// Package credits implements the double-entry Ledger (ticket 03): every
// credit movement — grant, adjustment, inference charge, data charge, top-up,
// Revenue Share posting — is a balanced set of entries, and balances are
// derived from entries, never stored as a mutable number (ADR 0002: credits,
// not payments, at pilot).
//
// Amounts are int64 micro-credits: one credit is 1e6 micro-credits. Integer
// money keeps pricing math exact and the balanced-posting check unambiguous.
package credits

import (
	"context"
	"errors"
	"sync"
	"time"
)

// MicrosPerCredit is how many micro-credits one credit is worth.
const MicrosPerCredit int64 = 1_000_000

// SystemScope identifies a platform-internal accounting scope: not an
// account, and therefore allowed to hold a negative balance.
type SystemScope string

const (
	// ScopeTreasury is where credits come from: grants draw it down, and
	// top-ups (ticket 09) will refill it. Negative balance = issued credits.
	ScopeTreasury SystemScope = "sys:treasury"
	// ScopePlatform accumulates the platform's 20% Revenue Share (default,
	// admin-tunable) and the platform side of charge adjustments.
	ScopePlatform SystemScope = "sys:platform"
)

// Scope is where a ledger entry lands: an account ("acct:<id>") or a system
// scope ("sys:…").
type Scope string

// AccountScope is the ledger scope of the account with the given ID.
func AccountScope(accountID string) Scope { return Scope("acct:" + accountID) }

// Movement kinds. Each names who moved and why; the pricing engine picks
// KindInferenceCharge and KindDataCharge, admins use KindGrant and
// KindAdjustment, and KindTopUp / KindRevenueShare reserve the vocabulary
// for tickets 09 and 15 — posted through the same Ledger when they land.
const (
	KindGrant           = "grant"
	KindAdjustment      = "adjustment"
	KindInferenceCharge = "inference_charge"
	KindDataCharge      = "data_charge"
	KindTopUp           = "top_up"
	KindRevenueShare    = "revenue_share"
)

// ErrUnbalanced is returned when a movement's entries do not sum to zero.
// The double-entry invariant is enforced everywhere postings happen.
var ErrUnbalanced = errors.New("credits: movement entries are not balanced")

// ErrNotFound is returned by lookups that find nothing.
var ErrNotFound = errors.New("credits: not found")

// ErrInsufficientFunds is returned when a debit would push a user-scoped
// scope below zero. System scopes may go negative (treasury issues credits).
var ErrInsufficientFunds = errors.New("credits: insufficient funds")

// ErrCapExceeded is returned when a capped charge's price would cross the
// caller-approved cap (ticket 07: the request budget guard). Priced and
// posted in one step, so the cap and the posted amount can never disagree.
var ErrCapExceeded = errors.New("credits: charge would exceed the approved cap")

// Entry is one leg of a movement: a scope and a signed amount. Positive
// credits the scope, negative debits it.
type Entry struct {
	Scope        Scope  `json:"scope"`
	AmountMicros int64  `json:"amount_micros"`
	Memo         string `json:"memo,omitempty"`
}

// Balanced reports whether the entries sum to exactly zero.
func Balanced(entries []Entry) bool {
	var sum int64
	for _, e := range entries {
		sum += e.AmountMicros
	}
	return sum == 0
}

// Movement is one credit movement: a kind, the actor responsible, and the
// balanced entries. Created and ID are filled by the store on post.
type Movement struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	ActorID   string    `json:"actor_id,omitempty"` // account responsible, if any
	RequestID string    `json:"request_id,omitempty"`
	Memo      string    `json:"memo,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Entries   []Entry   `json:"entries"`
	// Inference carries the metered usage and applied rates of an
	// inference charge — the spend view's explanation of the charge. The
	// store persists it (inference_charge_details) and returns it on reads.
	Inference *InferenceDetail `json:"inference,omitempty"`
}

// Store is the persistence seam for the Ledger. The postgres package
// implements it for the system of record; MemoryStore backs tests.
type Store interface {
	// PostMovement stores one movement atomically, refusing it unless the
	// entries balance and no user-scoped entry would overdraw its scope.
	PostMovement(ctx context.Context, mov Movement) (Movement, error)
	// Balance returns the derived balance for one scope.
	Balance(ctx context.Context, scope Scope) (int64, error)
	// Balances returns derived balances for every scope.
	Balances(ctx context.Context) (map[Scope]int64, error)
	// MovementsByScope returns a scope's movements, newest first.
	MovementsByScope(ctx context.Context, scope Scope, limit int) ([]Movement, error)
	// RevenueShareReceived sums the scope's positive entries across
	// revenue_share movements — an organization's gross receipts, derived
	// from the whole ledger, never truncated by a history window.
	RevenueShareReceived(ctx context.Context, scope Scope) (int64, error)
}

// Compile-time check that MemoryStore satisfies Store.
var _ Store = (*MemoryStore)(nil)

// MemoryStore is the in-memory Store double used by Seam 1 tests.
type MemoryStore struct {
	mu        sync.Mutex
	movements []Movement
}

// NewMemoryStore returns an empty ledger.
func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

// PostMovement validates the double-entry invariant and appends. User
// scopes may not go negative; system scopes (treasury, platform) may.
func (m *MemoryStore) PostMovement(_ context.Context, mov Movement) (Movement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// An empty movement posts nothing and records nothing — refuse it with
	// the unbalanced posting, since it moves no value.
	if len(mov.Entries) == 0 || !Balanced(mov.Entries) {
		return Movement{}, ErrUnbalanced
	}
	// Overdraft check per user scope, summing the movement's own entries so
	// a single movement debiting one scope twice is judged on its total.
	deltas := map[Scope]int64{}
	for _, e := range mov.Entries {
		if isSystemScope(e.Scope) {
			continue
		}
		deltas[e.Scope] += e.AmountMicros
	}
	for scope, delta := range deltas {
		var current int64
		for _, past := range m.movements {
			for _, e := range past.Entries {
				if e.Scope == scope {
					current += e.AmountMicros
				}
			}
		}
		if current+delta < 0 {
			return Movement{}, ErrInsufficientFunds
		}
	}

	if mov.ID == "" {
		var err error
		mov.ID, err = newMovementID()
		if err != nil {
			return Movement{}, err
		}
	}
	if mov.CreatedAt.IsZero() {
		mov.CreatedAt = time.Now().UTC()
	}
	stored := Movement{
		ID:        mov.ID,
		Kind:      mov.Kind,
		ActorID:   mov.ActorID,
		RequestID: mov.RequestID,
		Memo:      mov.Memo,
		CreatedAt: mov.CreatedAt,
		Inference: mov.Inference,
		Entries:   append([]Entry(nil), mov.Entries...),
	}
	m.movements = append(m.movements, stored)
	return stored, nil
}

// Balance derives the scope's balance: the sum of its entries. No balance is
// stored anywhere — there is no mutable number to corrupt.
func (m *MemoryStore) Balance(_ context.Context, scope Scope) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var sum int64
	for _, mov := range m.movements {
		for _, e := range mov.Entries {
			if e.Scope == scope {
				sum += e.AmountMicros
			}
		}
	}
	return sum, nil
}

// Balances derives every scope's balance in one pass.
func (m *MemoryStore) Balances(_ context.Context) (map[Scope]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[Scope]int64{}
	for _, mov := range m.movements {
		for _, e := range mov.Entries {
			out[e.Scope] += e.AmountMicros
		}
	}
	return out, nil
}

// MovementsByScope returns the movements touching the scope, newest first.
func (m *MemoryStore) MovementsByScope(_ context.Context, scope Scope, limit int) ([]Movement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Movement
	for i := len(m.movements) - 1; i >= 0 && len(out) < limit; i-- {
		mov := m.movements[i]
		for _, e := range mov.Entries {
			if e.Scope == scope {
				out = append(out, mov)
				break
			}
		}
	}
	return out, nil
}

// RevenueShareReceived sums the scope's positive entries across revenue_share
// movements — the org's gross receipts, before any member distribution. The
// distribution back out to members rides separate entries and must not
// shrink the total.
func (m *MemoryStore) RevenueShareReceived(_ context.Context, scope Scope) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var total int64
	for _, mov := range m.movements {
		if mov.Kind != KindRevenueShare {
			continue
		}
		for _, e := range mov.Entries {
			if e.Scope == scope && e.AmountMicros > 0 {
				total += e.AmountMicros
			}
		}
	}
	return total, nil
}

func isSystemScope(s Scope) bool {
	return string(s) == string(ScopeTreasury) || string(s) == string(ScopePlatform)
}

// IsSystemScope reports whether the scope is platform-internal (treasury or
// platform) — such scopes may hold negative balances.
func IsSystemScope(s Scope) bool { return isSystemScope(s) }

func newMovementID() (string, error) {
	suffix, err := randomSuffix()
	if err != nil {
		return "", err
	}
	return "mov_" + time.Now().UTC().Format("20060102T150405.000000000") + "_" + suffix, nil
}
