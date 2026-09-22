package credits_test

import (
	"errors"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
)

// The Ledger's core invariants (ticket 03): every posting is balanced — its
// entries sum to zero — or it is refused loudly, and balances are derived
// from entries (a sum), never stored as a mutable number.

func TestPostRefusesUnbalancedPostings(t *testing.T) {
	tests := []struct {
		name    string
		entries []credits.Entry
	}{
		{
			name:    "single lone entry",
			entries: []credits.Entry{{Scope: credits.AccountScope("acct-1"), AmountMicros: 5_000_000}},
		},
		{
			name: "entries over-credited by 100 micros",
			entries: []credits.Entry{
				{Scope: credits.AccountScope("acct-1"), AmountMicros: 10_000_100},
				{Scope: credits.Scope(credits.ScopeTreasury), AmountMicros: -10_000_000},
			},
		},
		{
			name: "entries under-credited by 1 micro",
			entries: []credits.Entry{
				{Scope: credits.AccountScope("acct-1"), AmountMicros: -9_999_999},
				{Scope: credits.Scope(credits.ScopePlatform), AmountMicros: 10_000_000},
			},
		},
		{
			name:    "no entries at all",
			entries: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := credits.NewMemoryStore()
			_, err := store.PostMovement(t.Context(), credits.Movement{
				Kind:    credits.KindAdjustment,
				Entries: tt.entries,
			})
			if !errors.Is(err, credits.ErrUnbalanced) {
				t.Fatalf("PostMovement error = %v, want ErrUnbalanced", err)
			}
			// A refused posting must leave no trace: the would-be balance is
			// unchanged, so corruption cannot hide behind a failed post.
			bal, err := store.Balance(t.Context(), credits.AccountScope("acct-1"))
			if err != nil || bal != 0 {
				t.Errorf("Balance after refused post = (%d, %v), want (0, nil)", bal, err)
			}
		})
	}
}

func TestBalancedPostingStoresEntriesAndDerivesBalances(t *testing.T) {
	store := credits.NewMemoryStore()
	mov, err := store.PostMovement(t.Context(), credits.Movement{
		Kind: credits.KindGrant,
		Memo: "pilot seed",
		Entries: []credits.Entry{
			{Scope: credits.AccountScope("acct-1"), AmountMicros: 100_000_000},
			{Scope: credits.Scope(credits.ScopeTreasury), AmountMicros: -100_000_000},
		},
	})
	if err != nil {
		t.Fatalf("PostMovement() error = %v", err)
	}
	if mov.ID == "" {
		t.Error("PostMovement must fill the movement ID")
	}
	if mov.CreatedAt.IsZero() {
		t.Error("PostMovement must fill CreatedAt")
	}
	if len(mov.Entries) != 2 {
		t.Errorf("stored movement carries %d entries, want 2", len(mov.Entries))
	}

	// Balances are derived per scope from the very same entries.
	acct, err := store.Balance(t.Context(), credits.AccountScope("acct-1"))
	if err != nil || acct != 100_000_000 {
		t.Errorf("account balance = (%d, %v), want (100000000, nil)", acct, err)
	}
	treasury, err := store.Balance(t.Context(), credits.Scope(credits.ScopeTreasury))
	if err != nil || treasury != -100_000_000 {
		t.Errorf("treasury balance = (%d, %v), want (-100000000, nil)", treasury, err)
	}
}

func TestBalanceIsAlwaysTheSumOfTheEntries(t *testing.T) {
	// Derivation, not storage: whatever the posting sequence, the reported
	// balance equals the sum of the account's entries.
	store := credits.NewMemoryStore()
	post := func(entries ...credits.Entry) {
		t.Helper()
		if _, err := store.PostMovement(t.Context(), credits.Movement{Kind: credits.KindAdjustment, Entries: entries}); err != nil {
			t.Fatalf("PostMovement: %v", err)
		}
	}
	post(
		credits.Entry{Scope: credits.AccountScope("acct-1"), AmountMicros: 100_000_000},
		credits.Entry{Scope: credits.Scope(credits.ScopeTreasury), AmountMicros: -100_000_000},
	)
	post(
		credits.Entry{Scope: credits.AccountScope("acct-1"), AmountMicros: -30_000_000},
		credits.Entry{Scope: credits.Scope(credits.ScopePlatform), AmountMicros: 30_000_000},
	)
	post(
		credits.Entry{Scope: credits.AccountScope("acct-1"), AmountMicros: 500_000},
		credits.Entry{Scope: credits.Scope(credits.ScopeTreasury), AmountMicros: -500_000},
	)

	bal, err := store.Balance(t.Context(), credits.AccountScope("acct-1"))
	if err != nil || bal != 70_500_000 {
		t.Errorf("balance = (%d, %v), want (70500000, nil) — the sum of the entries", bal, err)
	}
}
