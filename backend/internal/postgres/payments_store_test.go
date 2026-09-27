package postgres_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
	"github.com/syaikhipin/entrustdn-final/backend/internal/payments"
	"github.com/syaikhipin/entrustdn-final/backend/internal/postgres"
)

// The live Postgres implementation of payments.TopUpStore, exercised
// against a real database (ADR 0007). These complement the Seam 1 tests by
// proving the SQL layer keeps the settlement atomic: claim and ledger
// movement commit together, replays credit once, and the config row
// round-trips. A seed account row is required (top_ups.account_id has a
// foreign key), created through the membership store.

func newPaymentsStore(t *testing.T) (*postgres.PaymentsStore, *postgres.CreditsStore, string) {
	t.Helper()
	url := skipIfNoDatabase(t)
	pool := openCleanTestDB(t, url)

	// A real account row to satisfy the foreign key.
	store := postgres.NewStore(pool)
	acct := membership.Account{
		Email:       "topup-consumer@example.org",
		DisplayName: "Top-up Consumer",
		Role:        membership.RoleDataConsumer,
		Status:      membership.StatusActive,
	}
	if err := store.CreateAccount(t.Context(), &acct); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return postgres.NewPaymentsStore(pool), postgres.NewCreditsStore(pool), acct.ID
}

// seedPending opens one pending top-up directly in the store.
func seedPending(t *testing.T, ps *postgres.PaymentsStore, accountID string, amountMinor, creditsMicros int64) payments.TopUp {
	t.Helper()
	pendingSeq++
	tu, err := ps.Create(t.Context(), payments.TopUp{
		AccountID:     accountID,
		Reference:     "cs_test_" + accountID[:8] + "_" + t.Name() + "_" + string(rune('a'+pendingSeq)),
		Provider:      "stripe",
		AmountMinor:   amountMinor,
		Currency:      "eur",
		CreditsMicros: creditsMicros,
		Status:        payments.StatusPending,
	})
	if err != nil {
		t.Fatalf("seed pending top-up: %v", err)
	}
	return tu
}

var pendingSeq int

// buildMovement mirrors payments.Service's builder: the balanced top_up
// movement (account credited, treasury refilled).
func buildMovement(tu payments.TopUp, reference string) credits.Movement {
	return credits.Movement{
		Kind:      credits.KindTopUp,
		ActorID:   tu.AccountID,
		RequestID: reference,
		Memo:      "test top-up",
		Entries: []credits.Entry{
			{Scope: credits.AccountScope(tu.AccountID), AmountMicros: tu.CreditsMicros},
			{Scope: credits.Scope(credits.ScopeTreasury), AmountMicros: -tu.CreditsMicros},
		},
	}
}

func TestStoreTopUpConfigRoundTrip(t *testing.T) {
	ps, _, _ := newPaymentsStore(t)

	// No row: disabled, no error.
	cfg, err := ps.LoadConfig(t.Context())
	if err != nil || cfg.Enabled() {
		t.Fatalf("empty config = (%+v, %v), want disabled and nil", cfg, err)
	}
	want := payments.Config{
		Provider: "stripe", APIKey: "sk_live_x", WebhookSecret: "whsec_y",
		Currency: "eur", MicrosPerCent: 10_000, ReturnBaseURL: "https://thresh.example",
	}
	if err := ps.SaveConfig(t.Context(), want); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, err := ps.LoadConfig(t.Context())
	if err != nil || got != want {
		t.Errorf("round-tripped config = (%+v, %v), want %+v", got, err, want)
	}

	// The upsert overwrites.
	want.APIKey = "sk_live_rotated"
	if err := ps.SaveConfig(t.Context(), want); err != nil {
		t.Fatalf("SaveConfig overwrite: %v", err)
	}
	got, _ = ps.LoadConfig(t.Context())
	if got.APIKey != "sk_live_rotated" {
		t.Errorf("api key after rotate = %q", got.APIKey)
	}
}

func TestStoreSettlePaidIsAtomicAndIdempotent(t *testing.T) {
	ps, ledger, acctID := newPaymentsStore(t)
	ctx := t.Context()
	tu := seedPending(t, ps, acctID, 2500, 25*credits.MicrosPerCredit)

	got, won, err := ps.SettlePaid(ctx, tu.Reference, func(tu payments.TopUp) (credits.Movement, error) {
		return buildMovement(tu, tu.Reference), nil
	})
	if err != nil || !won {
		t.Fatalf("SettlePaid = (%+v, %v), want won", got, won)
	}
	if got.Status != payments.StatusSettled || got.MovementID == "" {
		t.Fatalf("settled top-up = %+v, want settled with a movement id", got)
	}

	// The ledger holds the balanced movement, derived balance agrees.
	bal, err := ledger.Balance(ctx, credits.AccountScope(acctID))
	if err != nil || bal != 25*credits.MicrosPerCredit {
		t.Errorf("balance = (%d, %v), want 25 credits", bal, err)
	}
	treasury, _ := ledger.Balance(ctx, credits.Scope(credits.ScopeTreasury))
	if treasury != -25*credits.MicrosPerCredit {
		t.Errorf("treasury = %d, want −25 credits", treasury)
	}

	// Replays credit nothing more.
	for range 2 {
		got, won, err = ps.SettlePaid(ctx, tu.Reference, func(tu payments.TopUp) (credits.Movement, error) {
			return buildMovement(tu, tu.Reference), nil
		})
		if err != nil || won {
			t.Fatalf("replay = (%+v, %v, %v), want won=false and nil error", got, won, err)
		}
	}
	bal, _ = ledger.Balance(ctx, credits.AccountScope(acctID))
	if bal != 25*credits.MicrosPerCredit {
		t.Errorf("balance after replay = %d, want exactly 25 credits", bal)
	}

	// The movement id survives the re-read.
	reread, err := ps.ByReference(ctx, tu.Reference)
	if err != nil || reread.MovementID == "" {
		t.Errorf("re-read = (%+v, %v), want the settled row with its movement", reread, err)
	}
}

func TestStoreSettlePaidRefusesUnknownAndContradicting(t *testing.T) {
	ps, _, acctID := newPaymentsStore(t)
	ctx := t.Context()

	if _, _, err := ps.SettlePaid(ctx, "cs_unknown", func(tu payments.TopUp) (credits.Movement, error) {
		return buildMovement(tu, tu.Reference), nil
	}); !errors.Is(err, payments.ErrNotFound) {
		t.Errorf("unknown reference = %v, want ErrNotFound", err)
	}

	tu := seedPending(t, ps, acctID, 1000, 10*credits.MicrosPerCredit)
	if _, err := ps.MarkTerminal(ctx, tu.Reference, payments.StatusFailed); err != nil {
		t.Fatalf("MarkTerminal(failed): %v", err)
	}
	// Paid after failed is a loud refusal, never a settlement.
	if _, _, err := ps.SettlePaid(ctx, tu.Reference, func(tu payments.TopUp) (credits.Movement, error) {
		return buildMovement(tu, tu.Reference), nil
	}); !errors.Is(err, payments.ErrAlreadyTerminal) {
		t.Errorf("paid-after-failed = %v, want ErrAlreadyTerminal", err)
	}
	// Failed again replays cleanly.
	if _, err := ps.MarkTerminal(ctx, tu.Reference, payments.StatusFailed); err != nil {
		t.Errorf("failed replay = %v, want nil", err)
	}
}

func TestStoreConcurrentSettlementsPostOnce(t *testing.T) {
	ps, ledger, acctID := newPaymentsStore(t)
	ctx := t.Context()
	tu := seedPending(t, ps, acctID, 1000, 10*credits.MicrosPerCredit)

	const workers = 8
	var wg sync.WaitGroup
	wonCount := make(chan bool, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, won, err := ps.SettlePaid(ctx, tu.Reference, func(tu payments.TopUp) (credits.Movement, error) {
				return buildMovement(tu, tu.Reference), nil
			})
			if err != nil {
				t.Errorf("concurrent settle: %v", err)
				wonCount <- false
				return
			}
			wonCount <- won
		}()
	}
	wg.Wait()
	close(wonCount)
	var wins int
	for w := range wonCount {
		if w {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("%d workers won the settlement, want exactly 1", wins)
	}
	bal, _ := ledger.Balance(ctx, credits.AccountScope(acctID))
	if bal != 10*credits.MicrosPerCredit {
		t.Errorf("balance = %d after %d winners, want exactly 10 credits", bal, wins)
	}
}

func TestStoreTopUpHistoryAndForeignKeys(t *testing.T) {
	ps, _, acctID := newPaymentsStore(t)
	ctx := t.Context()

	first := seedPending(t, ps, acctID, 100, 1*credits.MicrosPerCredit)
	second := seedPending(t, ps, acctID, 200, 2*credits.MicrosPerCredit)

	hist, err := ps.History(ctx, acctID, 10)
	if err != nil || len(hist) != 2 {
		t.Fatalf("history = (%d entries, %v), want 2", len(hist), err)
	}
	if hist[0].Reference != second.Reference {
		t.Errorf("newest = %q, want %q", hist[0].Reference, second.Reference)
	}
	if hist[1].Reference != first.Reference {
		t.Errorf("older = %q, want %q", hist[1].Reference, first.Reference)
	}

	// Unknown account: empty history, not an error.
	hist, err = ps.History(ctx, "00000000-0000-0000-0000-0000000000aa", 10)
	if err != nil || len(hist) != 0 {
		t.Errorf("stranger history = (%d, %v), want empty", len(hist), err)
	}
}

// Ticket 18: the retained-config mirror and the pending-top-up sweep.

func TestStoreConfigByProviderRetainsAcrossSwitchAndDisable(t *testing.T) {
	ps, _, _ := newPaymentsStore(t)
	ctx := t.Context()

	// Never configured: the 404 path.
	if _, err := ps.ConfigByProvider(ctx, "stripe"); !errors.Is(err, payments.ErrUnknownProvider) {
		t.Fatalf("never-configured provider = %v, want ErrUnknownProvider", err)
	}

	stripe := payments.Config{
		Provider: "stripe", APIKey: "sk_live_x", WebhookSecret: "whsec_y",
		Currency: "eur", MicrosPerCent: 10_000, ReturnBaseURL: "https://thresh.example",
	}
	if err := ps.SaveConfig(ctx, stripe); err != nil {
		t.Fatalf("SaveConfig stripe: %v", err)
	}
	paypal := stripe
	paypal.Provider, paypal.APIKey, paypal.WebhookSecret = "paypal", "pp_live", "whsec_pp"
	if err := ps.SaveConfig(ctx, paypal); err != nil {
		t.Fatalf("SaveConfig paypal: %v", err)
	}
	// Disable: the current row clears, the mirror must not.
	if err := ps.SaveConfig(ctx, payments.Config{}); err != nil {
		t.Fatalf("SaveConfig disable: %v", err)
	}

	cfg, err := ps.LoadConfig(ctx)
	if err != nil || cfg.Enabled() {
		t.Errorf("LoadConfig after disable = (%+v, %v), want disabled", cfg, err)
	}
	got, err := ps.ConfigByProvider(ctx, "stripe")
	if err != nil || got != stripe {
		t.Errorf("retained stripe = (%+v, %v), want %+v", got, err, stripe)
	}
	got, err = ps.ConfigByProvider(ctx, "paypal")
	if err != nil || got != paypal {
		t.Errorf("retained paypal = (%+v, %v), want %+v", got, err, paypal)
	}

	// Re-configuring a provider rotates its mirror.
	rotated := stripe
	rotated.APIKey = "sk_live_rotated"
	if err := ps.SaveConfig(ctx, rotated); err != nil {
		t.Fatalf("SaveConfig rotated stripe: %v", err)
	}
	got, _ = ps.ConfigByProvider(ctx, "stripe")
	if got.APIKey != "sk_live_rotated" {
		t.Errorf("rotated retained api key = %q, want sk_live_rotated", got.APIKey)
	}
}

func TestStorePendingOlderThanSweepsOldestFirst(t *testing.T) {
	url := skipIfNoDatabase(t)
	pool := openCleanTestDB(t, url)
	ps := postgres.NewPaymentsStore(pool)

	// A real account row to satisfy the foreign key.
	store := postgres.NewStore(pool)
	acct := membership.Account{
		Email:       "pending-sweep@example.org",
		DisplayName: "Pending Sweep",
		Role:        membership.RoleDataConsumer,
		Status:      membership.StatusActive,
	}
	if err := store.CreateAccount(t.Context(), &acct); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	ctx := t.Context()

	old, new := seedPending(t, ps, acct.ID, 100, 1*credits.MicrosPerCredit), seedPending(t, ps, acct.ID, 200, 2*credits.MicrosPerCredit)
	// The seeds land within the same clock tick; force the age gap the
	// cutoff needs. The pool is in scope here, so raw SQL is available.
	if _, err := pool.Exec(ctx, "UPDATE top_ups SET created_at = created_at - interval '48 hours' WHERE reference = $1", old.Reference); err != nil {
		t.Fatalf("age the first top-up: %v", err)
	}
	if _, won, err := ps.SettlePaid(ctx, new.Reference, func(tu payments.TopUp) (credits.Movement, error) {
		return buildMovement(tu, tu.Reference), nil
	}); err != nil || !won {
		t.Fatalf("settle the second top-up: (%v, %v)", won, err)
	}

	got, err := ps.PendingOlderThan(ctx, time.Now().Add(-24*time.Hour), 200)
	if err != nil {
		t.Fatalf("PendingOlderThan: %v", err)
	}
	if len(got) != 1 || got[0].Reference != old.Reference {
		t.Errorf("pending = %+v, want exactly the aged %q", got, old.Reference)
	}

	// A cutoff in the future catches the settled one? No — status gates it.
	got, err = ps.PendingOlderThan(ctx, time.Now().Add(time.Hour), 200)
	if err != nil || len(got) != 0 {
		t.Errorf("future cutoff = (%+v, %v), want empty (settled never lists)", got, err)
	}
}
