package payments_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/payments"
)

// Ticket 09 at Seam 1: the top-up flow with a fake gateway. Success credits
// the Ledger exactly once; failure and cancellation credit nothing; a replay
// of a settled callback credits nothing again; forged callbacks never reach
// settlement. The gateway is always the fake below — no test touches Stripe.

// fakeGateway is the test double for the Gateway seam: Create hands back a
// deterministic session; ParseCallback decodes a JSON body signed with the
// "ok" marker header — the cheapest honest stand-in for signature
// verification, which the real Stripe gateway proves separately.
type fakeGateway struct {
	mu        sync.Mutex
	created   []payments.TopUp
	sessions  int
	failNext  bool
	reference string
}

func newFakeGateway() *fakeGateway { return &fakeGateway{reference: "cs_test"} }

func (f *fakeGateway) Provider() string { return "stripe" }

func (f *fakeGateway) Create(_ context.Context, tu payments.TopUp) (payments.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		return payments.Session{}, errors.New("gateway unreachable")
	}
	f.created = append(f.created, tu)
	f.sessions++
	ref := f.reference
	f.reference = "cs_test_" + jsonNumber(f.sessions)
	return payments.Session{Reference: ref, PaymentURL: "https://pay.example/" + ref}, nil
}

func jsonNumber(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// callbackDoc is the fake's callback wire format: outcome and the amount the
// gateway believes the payment was for.
type callbackDoc struct {
	Reference   string `json:"reference"`
	Outcome     string `json:"outcome"`
	AmountMinor int64  `json:"amount_minor"`
}

func (f *fakeGateway) ParseCallback(_ context.Context, header http.Header, body []byte) (payments.Callback, error) {
	if header.Get("X-Fake-Signature") != "ok" {
		return payments.Callback{}, payments.ErrCallbackRejected
	}
	var doc callbackDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return payments.Callback{}, payments.ErrCallbackRejected
	}
	return payments.Callback{
		Reference:   doc.Reference,
		Outcome:     payments.Outcome(doc.Outcome),
		AmountMinor: doc.AmountMinor,
	}, nil
}

// fakeCallback builds the signed body for the fake gateway.
func fakeCallback(t *testing.T, ref string, outcome payments.Outcome, amountMinor int64) (http.Header, []byte) {
	t.Helper()
	body, err := json.Marshal(callbackDoc{Reference: ref, Outcome: string(outcome), AmountMinor: amountMinor})
	if err != nil {
		t.Fatalf("marshal fake callback: %v", err)
	}
	header := http.Header{}
	header.Set("X-Fake-Signature", "ok")
	return header, body
}

// newService returns a Service wired to the fake gateway, the in-memory
// payments store, and an in-memory Ledger. The rate is one credit per euro
// (10_000 µcr per cent). Two providers share the one fake — the gateway
// seam is provider-agnostic, and ticket 18's switch tests need a second
// registered name to switch to. The store's LoadConfig is the service's
// config loader (production wiring), pre-seeded with the stripe gateway
// enabled exactly as the admin API's config row enables it in production.
func newService(t *testing.T) (*payments.Service, *fakeGateway, *payments.MemoryStore, *credits.MemoryStore) {
	t.Helper()
	gw := newFakeGateway()
	ledger := credits.NewMemoryStore()
	store := payments.NewMemoryStore(ledger)
	if err := store.SaveConfig(context.Background(), stripeCfg()); err != nil {
		t.Fatalf("seed stripe config: %v", err)
	}
	reg := payments.NewRegistry()
	reg.Register("stripe", func(payments.Config) (payments.Gateway, error) { return gw, nil })
	reg.Register("paypal", func(payments.Config) (payments.Gateway, error) { return gw, nil })
	svc := payments.NewService(store, store.LoadConfig, reg.GatewayFor)
	return svc, gw, store, ledger
}

func testConfigLoader(context.Context) (payments.Config, error) {
	return payments.Config{
		Provider:      "stripe",
		APIKey:        "sk_test_1234",
		WebhookSecret: "whsec_test",
		Currency:      "eur",
		MicrosPerCent: 10_000, // 1 EUR = 1 credit
		ReturnBaseURL: "https://thresh.example",
	}, nil
}

const consumerID = "00000000-0000-0000-0000-0000000000c9"

func TestInitiateCreatesPendingTopUpThroughTheGateway(t *testing.T) {
	svc, gw, _, ledger := newService(t)

	tu, err := svc.Initiate(t.Context(), consumerID, 2500, "eur")
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}
	if tu.Status != payments.StatusPending {
		t.Errorf("status = %q, want pending", tu.Status)
	}
	if tu.Reference == "" || tu.PaymentURL == "" {
		t.Errorf("gateway session missing: ref=%q url=%q", tu.Reference, tu.PaymentURL)
	}
	if tu.CreditsMicros != 25*credits.MicrosPerCredit {
		t.Errorf("credits_micros = %d, want 25 credits at 1 EUR/credit", tu.CreditsMicros)
	}
	if len(gw.created) != 1 || gw.created[0].AccountID != consumerID {
		t.Errorf("gateway saw %+v, want one session for the consumer", gw.created)
	}

	// Nothing is credited at initiation — only at settlement.
	if bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID)); bal != 0 {
		t.Errorf("balance after initiate = %d, want 0 (settlement credits, not initiation)", bal)
	}
}

func TestInitiateRefusesBadAmountsAndCurrency(t *testing.T) {
	svc, _, _, _ := newService(t)
	for _, tt := range []struct {
		name     string
		minor    int64
		currency string
	}{
		{name: "zero amount", minor: 0, currency: "eur"},
		{name: "negative amount", minor: -5, currency: "eur"},
		{name: "wrong currency", minor: 100, currency: "usd"},
		{name: "bad currency code", minor: 100, currency: "euros"},
		{name: "absurd amount", minor: 1 << 50, currency: "eur"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := svc.Initiate(t.Context(), consumerID, tt.minor, tt.currency); err == nil {
				t.Errorf("Initiate(%d, %q) succeeded, want refusal", tt.minor, tt.currency)
			}
		})
	}
}

func TestInitiateRefusesWithoutConfiguredGateway(t *testing.T) {
	gw := newFakeGateway()
	ledger := credits.NewMemoryStore()
	store := payments.NewMemoryStore(ledger)
	reg := payments.NewRegistry()
	reg.Register("stripe", func(payments.Config) (payments.Gateway, error) { return gw, nil })
	svc := payments.NewService(store, func(context.Context) (payments.Config, error) {
		return payments.Config{}, nil // disabled: no provider configured
	}, reg.GatewayFor)

	if _, err := svc.Initiate(t.Context(), consumerID, 1000, "eur"); !errors.Is(err, payments.ErrDisabled) {
		t.Errorf("Initiate without config = %v, want ErrDisabled", err)
	}
	if gw.sessions != 0 {
		t.Errorf("gateway saw %d sessions, want none", gw.sessions)
	}
}

func TestSettledCallbackPostsBalancedTopUpToTheLedger(t *testing.T) {
	svc, _, store, ledger := newService(t)
	tu, err := svc.Initiate(t.Context(), consumerID, 2500, "eur")
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}

	header, body := fakeCallback(t, tu.Reference, payments.OutcomePaid, 2500)
	got, result, err := svc.SettleCallback(t.Context(), "stripe", header, body)
	if err != nil {
		t.Fatalf("SettleCallback: %v", err)
	}
	if result != payments.SettledNow {
		t.Errorf("result = %q, want settled", result)
	}
	if got.Status != payments.StatusSettled || got.MovementID == "" {
		t.Errorf("top-up = %+v, want settled with a movement id", got)
	}

	// The Ledger holds a balanced top_up movement: the consumer credited,
	// the treasury refilled by the same amount.
	bal, err := ledger.Balance(t.Context(), credits.AccountScope(consumerID))
	if err != nil || bal != 25*credits.MicrosPerCredit {
		t.Errorf("consumer balance = (%d, %v), want 25 credits", bal, err)
	}
	treasury, _ := ledger.Balance(t.Context(), credits.Scope(credits.ScopeTreasury))
	if treasury != -25*credits.MicrosPerCredit {
		t.Errorf("treasury = %d, want −25 credits (the top-up refills it)", treasury)
	}
	movs, _ := ledger.MovementsByScope(t.Context(), credits.AccountScope(consumerID), 10)
	if len(movs) != 1 || movs[0].Kind != credits.KindTopUp {
		t.Fatalf("movements = %+v, want one top_up", movs)
	}
	if !credits.Balanced(movs[0].Entries) {
		t.Errorf("top_up movement is unbalanced: %+v", movs[0].Entries)
	}
	// The store's copy carries the same movement id — the record joins
	// the gateway reference to the ledger movement.
	stored, err := store.ByReference(t.Context(), tu.Reference)
	if err != nil || stored.MovementID != got.MovementID {
		t.Errorf("stored movement id = (%q, %v), want %q", stored.MovementID, err, got.MovementID)
	}
}

func TestFailedAndCancelledCallbacksNeverCredit(t *testing.T) {
	for _, tt := range []struct {
		name    string
		outcome payments.Outcome
		status  payments.TopUpStatus
		result  payments.SettlementResult
	}{
		{name: "failed payment", outcome: payments.OutcomeFailed, status: payments.StatusFailed, result: payments.MarkedFailed},
		{name: "cancelled checkout", outcome: payments.OutcomeCancelled, status: payments.StatusCancelled, result: payments.MarkedCancelled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _, ledger := newService(t)
			tu, err := svc.Initiate(t.Context(), consumerID, 1000, "eur")
			if err != nil {
				t.Fatalf("Initiate: %v", err)
			}

			header, body := fakeCallback(t, tu.Reference, tt.outcome, 1000)
			got, result, err := svc.SettleCallback(t.Context(), "stripe", header, body)
			if err != nil {
				t.Fatalf("SettleCallback(%s): %v", tt.outcome, err)
			}
			if result != tt.result {
				t.Errorf("result = %q, want %q", result, tt.result)
			}
			if got.Status != tt.status {
				t.Errorf("status = %q, want %q", got.Status, tt.status)
			}
			if bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID)); bal != 0 {
				t.Errorf("balance = %d after %s, want 0 — failures never credit", bal, tt.outcome)
			}
			movs, _ := ledger.MovementsByScope(t.Context(), credits.AccountScope(consumerID), 10)
			if len(movs) != 0 {
				t.Errorf("ledger holds %d movements after %s, want none", len(movs), tt.outcome)
			}
		})
	}
}

func TestReplayOfSettledCallbackCreditsNothingMore(t *testing.T) {
	svc, _, _, ledger := newService(t)
	tu, err := svc.Initiate(t.Context(), consumerID, 1000, "eur")
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}
	header, body := fakeCallback(t, tu.Reference, payments.OutcomePaid, 1000)
	if _, _, err := svc.SettleCallback(t.Context(), "stripe", header, body); err != nil {
		t.Fatalf("first settle: %v", err)
	}

	// Gateways retry webhooks; the same callback may arrive any number of
	// times. Each replay answers idempotently and credits nothing.
	for i := range 3 {
		got, result, err := svc.SettleCallback(t.Context(), "stripe", header, body)
		if err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
		if result != payments.AlreadySettled {
			t.Errorf("replay %d result = %q, want already_settled", i, result)
		}
		if got.Status != payments.StatusSettled {
			t.Errorf("replay %d status = %q, want settled", i, got.Status)
		}
	}
	bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID))
	if bal != 10*credits.MicrosPerCredit {
		t.Errorf("balance after replay = %d, want exactly 10 credits", bal)
	}
	movs, _ := ledger.MovementsByScope(t.Context(), credits.AccountScope(consumerID), 10)
	if len(movs) != 1 {
		t.Errorf("ledger holds %d movements after replay, want 1", len(movs))
	}
}

func TestForgedCallbacksAreRejectedBeforeSettlement(t *testing.T) {
	svc, _, _, ledger := newService(t)
	tu, err := svc.Initiate(t.Context(), consumerID, 1000, "eur")
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}

	// A forged callback: valid body, bad signature marker.
	body, _ := json.Marshal(callbackDoc{Reference: tu.Reference, Outcome: "paid", AmountMinor: 1000})
	header := http.Header{}
	header.Set("X-Fake-Signature", "forged")
	if _, _, err := svc.SettleCallback(t.Context(), "stripe", header, body); !errors.Is(err, payments.ErrCallbackRejected) {
		t.Fatalf("forged callback error = %v, want ErrCallbackRejected", err)
	}
	if bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID)); bal != 0 {
		t.Errorf("balance = %d after forgery, want 0", bal)
	}

	// A real callback after the forgery still settles normally — the
	// forgery never touched the top-up's state.
	goodHeader, goodBody := fakeCallback(t, tu.Reference, payments.OutcomePaid, 1000)
	if _, _, err := svc.SettleCallback(t.Context(), "stripe", goodHeader, goodBody); err != nil {
		t.Fatalf("settle after forgery: %v", err)
	}
	if bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID)); bal != 10*credits.MicrosPerCredit {
		t.Errorf("balance = %d, want 10 credits", bal)
	}
}

func TestCallbackAmountMismatchIsRefused(t *testing.T) {
	svc, _, _, ledger := newService(t)
	tu, err := svc.Initiate(t.Context(), consumerID, 1000, "eur")
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}

	// The gateway reports a different amount than the session was opened
	// for: refuse, credit nothing, leave the top-up pending.
	header, body := fakeCallback(t, tu.Reference, payments.OutcomePaid, 9999)
	if _, _, err := svc.SettleCallback(t.Context(), "stripe", header, body); !errors.Is(err, payments.ErrAmountMismatch) {
		t.Fatalf("mismatched callback error = %v, want ErrAmountMismatch", err)
	}
	if bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID)); bal != 0 {
		t.Errorf("balance = %d after mismatch, want 0", bal)
	}
	stored, err := svc.History(t.Context(), consumerID, 10)
	if err != nil || len(stored) != 1 || stored[0].Status != payments.StatusPending {
		t.Errorf("top-up after mismatch = (%+v, %v), want one still pending", stored, err)
	}

	// The honest callback then settles.
	header, body = fakeCallback(t, tu.Reference, payments.OutcomePaid, 1000)
	if _, _, err := svc.SettleCallback(t.Context(), "stripe", header, body); err != nil {
		t.Fatalf("settle after mismatch: %v", err)
	}
}

func TestUnknownReferenceAndUnverifiableBodyAreRefused(t *testing.T) {
	svc, _, _, ledger := newService(t)

	header, body := fakeCallback(t, "cs_unknown", payments.OutcomePaid, 1000)
	if _, _, err := svc.SettleCallback(t.Context(), "stripe", header, body); !errors.Is(err, payments.ErrNotFound) {
		t.Errorf("unknown reference = %v, want ErrNotFound", err)
	}
	if _, _, err := svc.SettleCallback(t.Context(), "stripe", header, []byte("not json")); !errors.Is(err, payments.ErrCallbackRejected) {
		t.Errorf("garbage body = %v, want ErrCallbackRejected", err)
	}
	if bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID)); bal != 0 {
		t.Errorf("balance = %d, want 0", bal)
	}
}

func TestPaidCallbackAfterTerminalStateIsRefusedLoudly(t *testing.T) {
	svc, _, _, ledger := newService(t)
	tu, err := svc.Initiate(t.Context(), consumerID, 1000, "eur")
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}

	// The payment fails, then the gateway reports paid anyway (an out-of-
	// order async pair): the failure was terminal, the paid callback must
	// not resurrect the payment — refuse loudly, credit nothing.
	failedHeader, failedBody := fakeCallback(t, tu.Reference, payments.OutcomeFailed, 1000)
	if _, _, err := svc.SettleCallback(t.Context(), "stripe", failedHeader, failedBody); err != nil {
		t.Fatalf("fail: %v", err)
	}
	paidHeader, paidBody := fakeCallback(t, tu.Reference, payments.OutcomePaid, 1000)
	if _, _, err := svc.SettleCallback(t.Context(), "stripe", paidHeader, paidBody); !errors.Is(err, payments.ErrAlreadyTerminal) {
		t.Errorf("paid-after-failed = %v, want ErrAlreadyTerminal", err)
	}
	if bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID)); bal != 0 {
		t.Errorf("balance = %d, want 0", bal)
	}
}

func TestConcurrentSettlementsCreditExactlyOnce(t *testing.T) {
	svc, _, _, ledger := newService(t)
	tu, err := svc.Initiate(t.Context(), consumerID, 1000, "eur")
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}
	header, body := fakeCallback(t, tu.Reference, payments.OutcomePaid, 1000)

	const workers = 8
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := svc.SettleCallback(t.Context(), "stripe", header, body)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Errorf("concurrent settle: %v", err)
		}
	}
	// Every worker sees settled or already_settled; exactly one credit posts.
	bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID))
	if bal != 10*credits.MicrosPerCredit {
		t.Errorf("balance after %d concurrent settles = %d, want exactly 10 credits", workers, bal)
	}
}

func TestGatewayFailureLeavesNoTopUpRow(t *testing.T) {
	svc, gw, store, _ := newService(t)
	gw.failNext = true
	if _, err := svc.Initiate(t.Context(), consumerID, 1000, "eur"); err == nil {
		t.Fatal("Initiate against a failing gateway succeeded, want error")
	}
	history, err := svc.History(t.Context(), consumerID, 10)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("history holds %d top-ups after gateway failure, want none (nothing was opened)", len(history))
	}
	if _, err := store.ByReference(t.Context(), "cs_test"); !errors.Is(err, payments.ErrNotFound) {
		t.Errorf("orphan reference lookup = %v, want ErrNotFound", err)
	}
}

func TestHistoryReturnsNewestFirst(t *testing.T) {
	svc, _, _, _ := newService(t)
	refs := []string{}
	for range 3 {
		tu, err := svc.Initiate(t.Context(), consumerID, 100, "eur")
		if err != nil {
			t.Fatalf("Initiate: %v", err)
		}
		refs = append(refs, tu.Reference)
	}
	history, err := svc.History(t.Context(), consumerID, 10)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("history = %d entries, want 3", len(history))
	}
	if history[0].Reference != refs[len(refs)-1] {
		t.Errorf("newest = %q, want %q", history[0].Reference, refs[len(refs)-1])
	}
}

// --- Ticket 18: a provider switch must not strand outstanding top-ups ---

// TestSettleCallbackUsesTheCallbackProviderNotTheCurrentConfig drives the
// retention contract: a top-up opened under paypal settles even after the
// platform's current config says stripe — the callback names its provider
// and the service verifies against that provider's retained config.
func TestSettleCallbackUsesTheCallbackProviderNotTheCurrentConfig(t *testing.T) {
	svc, _, store, ledger := newService(t)

	// Top-up opened while paypal was configured.
	if err := store.SaveConfig(t.Context(), paypalCfg); err != nil {
		t.Fatalf("save paypal config: %v", err)
	}
	tu, err := svc.Initiate(t.Context(), consumerID, 1000, "eur")
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}

	// The platform switches to stripe. If verification used the current
	// config, this callback would be untestable and the money stranded.
	if err := store.SaveConfig(t.Context(), stripeCfg()); err != nil {
		t.Fatalf("save stripe config: %v", err)
	}

	header, body := fakeCallback(t, tu.Reference, payments.OutcomePaid, 1000)
	got, result, err := svc.SettleCallback(t.Context(), "paypal", header, body)
	if err != nil {
		t.Fatalf("SettleCallback after switch: %v", err)
	}
	if result != payments.SettledNow || got.Status != payments.StatusSettled {
		t.Errorf("settlement = (%q, %s), want settled", result, got.Status)
	}
	if bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID)); bal != 10*credits.MicrosPerCredit {
		t.Errorf("balance = %d, want 10 credits — the switch must not strand the top-up", bal)
	}
}

// TestSettleCallbackRefusesProviderThePlatformNeverKnew pins the 404 path:
// a callback claiming a provider with no retained config is refused before
// any verification work — the webhook can answer 404.
func TestSettleCallbackRefusesProviderThePlatformNeverKnew(t *testing.T) {
	svc, _, _, ledger := newService(t)

	header, body := fakeCallback(t, "cs_test", payments.OutcomePaid, 1000)
	if _, _, err := svc.SettleCallback(t.Context(), "unheardof", header, body); !errors.Is(err, payments.ErrUnknownProvider) {
		t.Fatalf("unknown provider = %v, want ErrUnknownProvider", err)
	}
	if bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID)); bal != 0 {
		t.Errorf("balance = %d, want 0", bal)
	}
}

// TestSettleCallbackRefusesProviderMismatch pins the cross-check: a verified
// callback whose URL provider disagrees with the top-up's own provider
// column refuses without touching state — the two must name the same
// gateway or the reference is being spoofed across gateways.
func TestSettleCallbackRefusesProviderMismatch(t *testing.T) {
	svc, _, store, ledger := newService(t)
	tu, err := svc.Initiate(t.Context(), consumerID, 1000, "eur") // provider: stripe
	if err != nil {
		t.Fatalf("Initiate: %v", err)
	}

	// Both providers retained, both share the fake — only the mismatch
	// check stands between this callback and a settlement.
	if err := store.SaveConfig(t.Context(), paypalCfg); err != nil {
		t.Fatalf("save paypal config: %v", err)
	}

	header, body := fakeCallback(t, tu.Reference, payments.OutcomePaid, 1000)
	if _, _, err := svc.SettleCallback(t.Context(), "paypal", header, body); !errors.Is(err, payments.ErrProviderMismatch) {
		t.Fatalf("provider mismatch = %v, want ErrProviderMismatch", err)
	}
	if bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID)); bal != 0 {
		t.Errorf("balance = %d after mismatch, want 0", bal)
	}
	// The top-up stays pending — a corrected callback can still settle it.
	stored, err := store.ByReference(t.Context(), tu.Reference)
	if err != nil || stored.Status != payments.StatusPending {
		t.Errorf("top-up after mismatch = (%+v, %v), want still pending", stored, err)
	}
}

// TestRetainedConfigsSurviveDisableAndSwitch drives the store contract the
// settlement path leans on: each provider's last config stays readable by
// provider name through switches and a full disable.
func TestRetainedConfigsSurviveDisableAndSwitch(t *testing.T) {
	_, _, store, _ := newService(t)
	ctx := t.Context()

	// A provider the platform never knew has nothing retained. (The shared
	// store arrives pre-seeded with stripe, so this checks a fresh one.)
	if _, err := payments.NewMemoryStore(credits.NewMemoryStore()).ConfigByProvider(ctx, "paypal"); !errors.Is(err, payments.ErrNotFound) {
		t.Errorf("never-configured provider ConfigByProvider = %v, want ErrNotFound", err)
	}

	if err := store.SaveConfig(ctx, stripeCfg()); err != nil {
		t.Fatalf("save stripe: %v", err)
	}
	if err := store.SaveConfig(ctx, paypalCfg); err != nil {
		t.Fatalf("save paypal: %v", err)
	}
	// Disable the gateway entirely (the admin path clears the current row).
	if err := store.SaveConfig(ctx, payments.Config{}); err != nil {
		t.Fatalf("disable: %v", err)
	}

	// Current config: disabled. Retained: both providers still settle-able.
	cfg, err := store.LoadConfig(ctx)
	if err != nil || cfg.Enabled() {
		t.Errorf("LoadConfig after disable = (%+v, %v), want disabled", cfg, err)
	}
	for provider, wantSecret := range map[string]string{"stripe": "whsec_test", "paypal": "whsec_paypal"} {
		got, err := store.ConfigByProvider(ctx, provider)
		if err != nil {
			t.Fatalf("ConfigByProvider(%s): %v", provider, err)
		}
		if got.Provider != provider || got.WebhookSecret != wantSecret {
			t.Errorf("retained %s = (%s/%s), want provider kept and secret %s", provider, got.Provider, got.WebhookSecret, wantSecret)
		}
	}
}

// stripeCfg mirrors testConfigLoader as a plain value — the switch tests
// save configs directly through the store, which takes a Config, not a loader.
func stripeCfg() payments.Config {
	cfg, err := testConfigLoader(context.Background())
	if err != nil {
		panic(err) // the loader is a fixture; it cannot fail
	}
	return cfg
}

// paypalCfg is the second provider's configuration: same shape, its own
// secret — proof that retention is per provider, not one shared row.
var paypalCfg = payments.Config{
	Provider:      "paypal",
	APIKey:        "pp_test",
	WebhookSecret: "whsec_paypal",
	Currency:      "eur",
	MicrosPerCent: 10_000,
	ReturnBaseURL: "https://thresh.example",
}
