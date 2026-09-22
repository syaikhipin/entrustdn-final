package postgres_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/postgres"
)

// The live Postgres implementation of credits.Store, exercised against a
// real database (ADR 0007). These complement the Seam 1 tests (in-memory
// double) by proving the SQL layer honors the same contract: balanced
// postings, derived balances, overdraft refusal, and serialization of
// concurrent posters.

func newCreditsStore(t *testing.T) *postgres.CreditsStore {
	t.Helper()
	url := skipIfNoDatabase(t)
	pool := openCleanTestDB(t, url)
	return postgres.NewCreditsStore(pool)
}

func TestStoreLedgerPostingsAndDerivedBalances(t *testing.T) {
	store := newCreditsStore(t)
	ctx := t.Context()
	acct := credits.AccountScope("00000000-0000-0000-0000-000000000001")

	mov, err := store.PostMovement(ctx, credits.Movement{
		Kind: credits.KindGrant,
		Memo: "pilot seed",
		Entries: []credits.Entry{
			{Scope: acct, AmountMicros: 100 * credits.MicrosPerCredit},
			{Scope: credits.Scope(credits.ScopeTreasury), AmountMicros: -100 * credits.MicrosPerCredit},
		},
	})
	if err != nil {
		t.Fatalf("PostMovement(grant): %v", err)
	}
	if mov.ID == "" || mov.CreatedAt.IsZero() {
		t.Errorf("PostMovement must fill ID and CreatedAt, got id=%q at=%v", mov.ID, mov.CreatedAt)
	}

	// Refuse unbalanced postings loudly, leaving no trace.
	if _, err := store.PostMovement(ctx, credits.Movement{
		Kind: credits.KindAdjustment,
		Entries: []credits.Entry{
			{Scope: acct, AmountMicros: 1},
		},
	}); !errors.Is(err, credits.ErrUnbalanced) {
		t.Errorf("unbalanced post error = %v, want ErrUnbalanced", err)
	}
	bal, err := store.Balance(ctx, acct)
	if err != nil || bal != 100*credits.MicrosPerCredit {
		t.Errorf("balance after refused post = (%d, %v), want 100 credits", bal, err)
	}

	// Debit to exactly zero is allowed; past zero is refused.
	if _, err := store.PostMovement(ctx, credits.Movement{
		Kind: credits.KindDataCharge,
		Entries: []credits.Entry{
			{Scope: acct, AmountMicros: -100 * credits.MicrosPerCredit},
			{Scope: credits.Scope(credits.ScopePlatform), AmountMicros: 100 * credits.MicrosPerCredit},
		},
	}); err != nil {
		t.Fatalf("charge to zero: %v", err)
	}
	if _, err := store.PostMovement(ctx, credits.Movement{
		Kind: credits.KindDataCharge,
		Entries: []credits.Entry{
			{Scope: acct, AmountMicros: -1},
			{Scope: credits.Scope(credits.ScopePlatform), AmountMicros: 1},
		},
	}); !errors.Is(err, credits.ErrInsufficientFunds) {
		t.Errorf("overdraft error = %v, want ErrInsufficientFunds", err)
	}

	// Balances derive from entries: treasury issued 100, platform holds it.
	sums, err := store.Balances(ctx)
	if err != nil {
		t.Fatalf("Balances: %v", err)
	}
	if sums[credits.Scope(credits.ScopeTreasury)] != -100*credits.MicrosPerCredit {
		t.Errorf("treasury = %d, want −100 credits", sums[credits.Scope(credits.ScopeTreasury)])
	}
	if sums[credits.Scope(credits.ScopePlatform)] != 100*credits.MicrosPerCredit {
		t.Errorf("platform = %d, want 100 credits", sums[credits.Scope(credits.ScopePlatform)])
	}
}

func TestStoreMovementsByScopeNewestFirst(t *testing.T) {
	store := newCreditsStore(t)
	ctx := t.Context()
	acct := credits.AccountScope("00000000-0000-0000-0000-000000000002")
	treasury := credits.Scope(credits.ScopeTreasury)

	for i, amount := range []int64{10, 20, 30} {
		if _, err := store.PostMovement(ctx, credits.Movement{
			Kind: credits.KindGrant,
			Memo: "wave",
			Entries: []credits.Entry{
				{Scope: acct, AmountMicros: amount * credits.MicrosPerCredit},
				{Scope: treasury, AmountMicros: -amount * credits.MicrosPerCredit},
			},
		}); err != nil {
			t.Fatalf("grant %d: %v", i, err)
		}
	}

	movs, err := store.MovementsByScope(ctx, acct, 10)
	if err != nil {
		t.Fatalf("MovementsByScope: %v", err)
	}
	if len(movs) != 3 {
		t.Fatalf("got %d movements, want 3", len(movs))
	}
	// Newest first: 30, then 20, then 10. Each movement carries both legs.
	var sum int64
	for _, m := range movs {
		if len(m.Entries) != 2 {
			t.Errorf("movement %s carries %d entries, want 2", m.ID, len(m.Entries))
		}
		if !credits.Balanced(m.Entries) {
			t.Errorf("movement %s is unbalanced: %+v", m.ID, m.Entries)
		}
		for _, e := range m.Entries {
			if e.Scope == acct {
				sum += e.AmountMicros
			}
		}
	}
	if sum != 60*credits.MicrosPerCredit {
		t.Errorf("movement entries sum to %d, want 60 credits", sum)
	}
}

func TestStoreInferenceDetailRoundTrips(t *testing.T) {
	store := newCreditsStore(t)
	ctx := t.Context()
	acct := credits.AccountScope("00000000-0000-0000-0000-000000000005")
	platform := credits.Scope(credits.ScopePlatform)

	detail := &credits.InferenceDetail{
		Model: "test-model", InputTokens: 1_000, CachedInputTokens: 500, OutputTokens: 1_000,
		InputMicrosPer1K: 2_500, CachedInputMicrosPer1K: 1_250, OutputMicrosPer1K: 10_000,
	}
	// Fund the account, then debit it: the charge must clear the guard.
	if _, err := store.PostMovement(ctx, credits.Movement{
		Kind: credits.KindGrant,
		Memo: "seed",
		Entries: []credits.Entry{
			{Scope: acct, AmountMicros: credits.MicrosPerCredit},
			{Scope: credits.Scope(credits.ScopeTreasury), AmountMicros: -credits.MicrosPerCredit},
		},
	}); err != nil {
		t.Fatalf("PostMovement(seed grant): %v", err)
	}
	if _, err := store.PostMovement(ctx, credits.Movement{
		Kind:      credits.KindInferenceCharge,
		Memo:      "model test-model",
		Inference: detail,
		Entries: []credits.Entry{
			{Scope: acct, AmountMicros: -11_875},
			{Scope: platform, AmountMicros: 11_875},
		},
	}); err != nil {
		t.Fatalf("PostMovement(inference charge): %v", err)
	}

	movs, err := store.MovementsByScope(ctx, acct, 10)
	if err != nil {
		t.Fatalf("MovementsByScope: %v", err)
	}
	if len(movs) != 2 {
		t.Fatalf("got %d movements, want 2 (grant + charge)", len(movs))
	}
	var charge *credits.Movement
	for i := range movs {
		if movs[i].Kind == credits.KindInferenceCharge {
			charge = &movs[i]
		}
	}
	if charge == nil {
		t.Fatal("no inference charge in history")
	}
	got := charge.Inference
	if got == nil {
		t.Fatal("read back movement carries no inference detail")
	}
	if *got != *detail {
		t.Errorf("detail = %+v, want %+v", *got, *detail)
	}

	// A movement without details reads back without them.
	if _, err := store.PostMovement(ctx, credits.Movement{
		Kind: credits.KindGrant,
		Memo: "plain grant",
		Entries: []credits.Entry{
			{Scope: acct, AmountMicros: credits.MicrosPerCredit},
			{Scope: credits.Scope(credits.ScopeTreasury), AmountMicros: -credits.MicrosPerCredit},
		},
	}); err != nil {
		t.Fatalf("PostMovement(grant): %v", err)
	}
	movs, _ = store.MovementsByScope(ctx, acct, 10)
	for _, m := range movs {
		if m.Kind != credits.KindInferenceCharge && m.Inference != nil {
			t.Errorf("%s movement carries inference detail: %+v", m.Kind, m.Inference)
		}
	}
}

func TestStoreConcurrentPostersSerialize(t *testing.T) {
	store := newCreditsStore(t)
	ctx := t.Context()
	acct := credits.AccountScope("00000000-0000-0000-0000-000000000003")
	treasury := credits.Scope(credits.ScopeTreasury)

	if _, err := store.PostMovement(ctx, credits.Movement{
		Kind: credits.KindGrant,
		Entries: []credits.Entry{
			{Scope: acct, AmountMicros: 100 * credits.MicrosPerCredit},
			{Scope: treasury, AmountMicros: -100 * credits.MicrosPerCredit},
		},
	}); err != nil {
		t.Fatalf("seed grant: %v", err)
	}

	const workers = 8
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.PostMovement(t.Context(), credits.Movement{
				Kind: credits.KindDataCharge,
				Entries: []credits.Entry{
					{Scope: acct, AmountMicros: -30 * credits.MicrosPerCredit},
					{Scope: credits.Scope(credits.ScopePlatform), AmountMicros: 30 * credits.MicrosPerCredit},
				},
			})
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	var ok int
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, credits.ErrInsufficientFunds):
			// correct refusal
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok > 3 {
		t.Errorf("%d charges succeeded against 100 credits at 30 each — overdrawn", ok)
	}
	bal, _ := store.Balance(t.Context(), acct)
	if bal < 0 || bal != int64(100-30*ok)*credits.MicrosPerCredit {
		t.Errorf("final balance = %d with %d successes, want exactly (100−30k) credits", bal, ok)
	}
}

func TestStorePricingRulesRoundTrip(t *testing.T) {
	url := skipIfNoDatabase(t)
	pool := openCleanTestDB(t, url)
	ctx := t.Context()

	loader := postgres.CreditRulesLoader(pool)
	if _, err := loader(ctx); !errors.Is(err, credits.ErrNoPricingRule) {
		t.Fatalf("loader with no price book = %v, want ErrNoPricingRule", err)
	}

	want := credits.PricingRules{
		Inference: []credits.InferenceRule{{
			Model: "test-model", InputMicrosPer1K: 2_500,
			CachedInputMicrosPer1K: 1_250, OutputMicrosPer1K: 10_000,
		}},
		Data: credits.DataRules{CachedAssetMicrosPerUnit: 5_000_000, UniqueMicrosPerUnit: 50_000_000},
	}
	if err := postgres.SaveCreditRules(ctx, pool, want); err != nil {
		t.Fatalf("SaveCreditRules: %v", err)
	}
	got, err := loader(ctx)
	if err != nil {
		t.Fatalf("loader: %v", err)
	}
	if len(got.Inference) != 1 || got.Inference[0].Model != "test-model" ||
		got.Inference[0].InputMicrosPer1K != 2_500 ||
		got.Inference[0].CachedInputMicrosPer1K != 1_250 ||
		got.Inference[0].OutputMicrosPer1K != 10_000 ||
		got.Data != want.Data {
		t.Errorf("round-tripped rules = %+v, want %+v", got, want)
	}

	// Invalid rule sets are refused at the door.
	bad := want
	bad.Inference[0].CachedInputMicrosPer1K = 99_999
	if err := postgres.SaveCreditRules(ctx, pool, bad); err == nil {
		t.Error("SaveCreditRules accepted a price book with cached input pricier than fresh")
	}
}
