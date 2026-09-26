package payments

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
)

// Service is the top-up front door: consumers initiate, gateways open
// sessions, and verified callbacks settle. Every credit posts through the
// Ledger — the store persists the settled movement inside its settlement
// transaction, so the claim on the top-up and the credit are one event;
// payments never touch credit balances directly.
type Service struct {
	store   TopUpStore
	config  func(ctx context.Context) (Config, error)
	gateway BuildGateway
}

// NewService returns a Service storing top-ups in store, reading the
// gateway config through config, and building gateways through gateway
// (a *Registry satisfies it).
func NewService(store TopUpStore, config func(ctx context.Context) (Config, error), gateway BuildGateway) *Service {
	return &Service{store: store, config: config, gateway: gateway}
}

// maxAmountMinor bounds one top-up at ten million currency units — a
// sanity cap against fat-fingered amounts, revisitable when the pilot
// outgrows it.
const maxAmountMinor = 10_000_000_00

// creditsFor converts gateway minor units into micro-credits at the
// configured rate. The multiplication is checked: an overflow would mint
// credits from arithmetic, not money.
func creditsFor(amountMinor int64, cfg Config) (int64, error) {
	if amountMinor <= 0 || amountMinor > maxAmountMinor {
		return 0, fmt.Errorf("payments: amount_minor must be between 1 and %d, got %d", maxAmountMinor, amountMinor)
	}
	if cfg.MicrosPerCent <= 0 {
		return 0, fmt.Errorf("payments: micros_per_cent must be positive, got %d", cfg.MicrosPerCent)
	}
	if amountMinor > math.MaxInt64/cfg.MicrosPerCent {
		return 0, fmt.Errorf("payments: %d minor units at %d µcr/cent overflows the credit range", amountMinor, cfg.MicrosPerCent)
	}
	return amountMinor * cfg.MicrosPerCent, nil
}

// Initiate opens a top-up: validate, open the gateway session, store the
// pending record. Nothing is credited here — settlement credits,
// initiation only opens a session. A gateway failure leaves no record.
func (s *Service) Initiate(ctx context.Context, accountID string, amountMinor int64, currency string) (TopUp, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return TopUp{}, fmt.Errorf("payments: load config: %w", err)
	}
	if !cfg.Enabled() {
		return TopUp{}, ErrDisabled
	}
	if currency != "" && currency != cfg.Currency {
		return TopUp{}, fmt.Errorf("payments: top-ups are charged in %s, not %s", cfg.Currency, currency)
	}

	gw, err := s.gateway(cfg)
	if err != nil {
		return TopUp{}, fmt.Errorf("payments: build gateway: %w", err)
	}

	// Derive the ledger side at initiation: the consumer sees exactly what
	// settlement will credit before they pay.
	creditsMicros, err := creditsFor(amountMinor, cfg)
	if err != nil {
		return TopUp{}, err
	}

	draft := TopUp{
		AccountID:     accountID,
		Provider:      cfg.Provider,
		AmountMinor:   amountMinor,
		Currency:      cfg.Currency,
		CreditsMicros: creditsMicros,
		Status:        StatusPending,
	}
	// The ID is minted here — before the gateway session opens — so it can
	// ride to the gateway (Stripe's client_reference_id) and come back on
	// the webhook, joining callback to top-up independent of the gateway's
	// own session id.
	id, err := newTopUpID()
	if err != nil {
		return TopUp{}, err
	}
	draft.ID = id
	sess, err := gw.Create(ctx, draft)
	if err != nil {
		return TopUp{}, fmt.Errorf("payments: open gateway session: %w", err)
	}
	if sess.Reference == "" {
		return TopUp{}, errors.New("payments: gateway returned an empty reference")
	}
	draft.Reference = sess.Reference
	draft.PaymentURL = sess.PaymentURL

	created, err := s.store.Create(ctx, draft)
	if err != nil {
		return TopUp{}, fmt.Errorf("payments: store top-up: %w", err)
	}
	return created, nil
}

// SettlementResult tells the caller what a callback did.
type SettlementResult string

const (
	// SettledNow: this callback closed the top-up and the Ledger movement
	// was posted by this call.
	SettledNow SettlementResult = "settled"
	// AlreadySettled: the top-up was already settled (a gateway replay) —
	// answered idempotently, nothing posted again.
	AlreadySettled SettlementResult = "already_settled"
	// MarkedFailed / MarkedCancelled: the terminal failure state was
	// recorded now; nothing was or will be credited.
	MarkedFailed    SettlementResult = "failed"
	MarkedCancelled SettlementResult = "cancelled"
)

// SettleCallback verifies, claims, and settles one gateway callback. The
// pipeline is: ParseCallback (signature verification — forged payloads die
// here, state untouched) → SettlePaid / MarkTerminal (atomic claim; the
// amount match runs inside the claim, so state cannot move between check
// and post; replays answer idempotently; contradictions refuse loudly).
// Only the claim winner's transaction records the Ledger movement.
func (s *Service) SettleCallback(ctx context.Context, header http.Header, body []byte) (TopUp, SettlementResult, error) {
	cb, err := s.parseCallback(ctx, header, body)
	if err != nil {
		return TopUp{}, "", err
	}

	switch cb.Outcome {
	case OutcomePaid:
		tu, won, err := s.store.SettlePaid(ctx, cb.Reference, func(tu TopUp) (credits.Movement, error) {
			// Build-only: the store persists this inside its own
			// settlement transaction. The captured amount must match the
			// session here — a verified callback claiming a different
			// amount refuses and the top-up stays pending.
			if cb.AmountMinor != tu.AmountMinor {
				return credits.Movement{}, fmt.Errorf("%w: callback %d vs top-up %d %s",
					ErrAmountMismatch, cb.AmountMinor, tu.AmountMinor, tu.Currency)
			}
			return buildTopUpMovement(tu, cb.Reference), nil
		})
		switch {
		case errors.Is(err, ErrAmountMismatch):
			return tu, "", err // typed through: mismatch, state untouched
		case errors.Is(err, ErrAlreadyTerminal):
			return tu, "", fmt.Errorf("%w: cannot settle %s as paid", ErrAlreadyTerminal, cb.Reference)
		case errors.Is(err, ErrNotFound):
			return TopUp{}, "", fmt.Errorf("%w: reference %s", ErrNotFound, cb.Reference)
		case err != nil:
			return TopUp{}, "", fmt.Errorf("payments: settle %s: %w", cb.Reference, err)
		}
		if !won {
			return tu, AlreadySettled, nil // replay: already credited once
		}
		return tu, SettledNow, nil
	case OutcomeFailed:
		tu, err := s.store.MarkTerminal(ctx, cb.Reference, StatusFailed)
		switch {
		case errors.Is(err, ErrNotFound):
			return TopUp{}, "", fmt.Errorf("%w: reference %s", ErrNotFound, cb.Reference)
		case errors.Is(err, ErrAlreadyTerminal):
			// Same-status replays come back clean; only contradictions
			// land here — refuse loudly (nothing was credited anyway).
			return tu, "", fmt.Errorf("%w: cannot mark %s as failed", ErrAlreadyTerminal, cb.Reference)
		case err != nil:
			return TopUp{}, "", fmt.Errorf("payments: mark %s failed: %w", cb.Reference, err)
		}
		return tu, MarkedFailed, nil
	case OutcomeCancelled:
		tu, err := s.store.MarkTerminal(ctx, cb.Reference, StatusCancelled)
		switch {
		case errors.Is(err, ErrNotFound):
			return TopUp{}, "", fmt.Errorf("%w: reference %s", ErrNotFound, cb.Reference)
		case errors.Is(err, ErrAlreadyTerminal):
			return tu, "", fmt.Errorf("%w: cannot mark %s as cancelled", ErrAlreadyTerminal, cb.Reference)
		case err != nil:
			return TopUp{}, "", fmt.Errorf("payments: mark %s cancelled: %w", cb.Reference, err)
		}
		return tu, MarkedCancelled, nil
	default:
		return TopUp{}, "", fmt.Errorf("%w: unknown outcome %q", ErrCallbackRejected, cb.Outcome)
	}
}

func (s *Service) parseCallback(ctx context.Context, header http.Header, body []byte) (Callback, error) {
	cfg, err := s.config(ctx)
	if err != nil {
		return Callback{}, fmt.Errorf("payments: load config: %w", err)
	}
	gw, err := s.gateway(cfg)
	if err != nil {
		return Callback{}, fmt.Errorf("payments: build gateway: %w", err)
	}
	cb, err := gw.ParseCallback(ctx, header, body)
	if err != nil {
		return Callback{}, err // payments-flavored: ErrCallbackRejected / errUnhandledEvent
	}
	if cb.Reference == "" {
		return Callback{}, fmt.Errorf("%w: callback carries no reference", ErrCallbackRejected)
	}
	return cb, nil
}

// newTopUpID mints a top-up ID: 16 crypto/rand bytes, hex (the requests
// package's idiom). The postgres store accepts it as the row's primary key;
// the memory store honors it too.
func newTopUpID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("payments: mint top-up id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// buildTopUpMovement builds the balanced top_up movement: the account
// credited, the treasury refilled. The gateway reference rides as
// RequestID so the ledger movement joins to the gateway's
// own records. Building is pure — the store persists it inside the
// settlement transaction.
func buildTopUpMovement(tu TopUp, reference string) credits.Movement {
	return credits.Movement{
		Kind:      credits.KindTopUp,
		ActorID:   tu.AccountID,
		RequestID: reference,
		Memo:      fmt.Sprintf("top-up %s %d.%02d via %s", strings.ToUpper(tu.Currency), tu.AmountMinor/100, tu.AmountMinor%100, tu.Provider),
		Entries: []credits.Entry{
			{Scope: credits.AccountScope(tu.AccountID), AmountMicros: tu.CreditsMicros},
			{Scope: credits.Scope(credits.ScopeTreasury), AmountMicros: -tu.CreditsMicros},
		},
	}
}

// History returns the account's top-ups, newest first.
func (s *Service) History(ctx context.Context, accountID string, limit int) ([]TopUp, error) {
	tops, err := s.store.History(ctx, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("payments: load history: %w", err)
	}
	return tops, nil
}

// LoadConfigForAdmin returns the raw configuration (unmasked credentials —
// the admin surface masks before rendering, never stores the mask).
func (s *Service) LoadConfigForAdmin(ctx context.Context) (Config, error) {
	return s.config(ctx)
}

// SaveConfig validates and persists the gateway configuration. It proves
// the provider is one the registry can actually build before storing — a
// typo'd provider never reaches the database.
func (s *Service) SaveConfig(ctx context.Context, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if _, err := s.gateway(cfg); err != nil {
		return fmt.Errorf("payments: provider %q is not registered: %w", cfg.Provider, err)
	}
	return s.store.SaveConfig(ctx, cfg)
}

// Disable clears the gateway configuration: top-ups are off and the
// webhook answers 404.
func (s *Service) Disable(ctx context.Context) error {
	return s.store.SaveConfig(ctx, Config{})
}

// ProviderName returns the configured provider ("", "stripe", …) — the
// webhook route checks the URL's provider against it so a callback for an
// unconfigured gateway reads as 404 before any verification work.
func (s *Service) ProviderName() string {
	cfg, err := s.config(context.Background())
	if err != nil {
		return ""
	}
	return cfg.Provider
}
