package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/payments"
)

// The Postgres implementation of payments.TopUpStore (ticket 09; ADR 0007:
// the system of record). Settlement is one SQL transaction: the UPDATE that
// claims the pending top-up and the ledger INSERT run together, so a replay
// or a crash can neither double-credit nor strand a claimed settlement.

// topUpColumns is the projection every top-up read shares — the order
// scanTopUp scans in.
const topUpColumns = "id, account_id, reference, provider, amount_minor, currency, credits_micros, " +
	"status, COALESCE(movement_id::TEXT, ''), created_at, updated_at"

// PaymentsStore implements payments.TopUpStore over the shared pool.
type PaymentsStore struct {
	pool *pgxpool.Pool
}

// NewPaymentsStore returns a top-up store backed by the given pool.
func NewPaymentsStore(pool *pgxpool.Pool) *PaymentsStore { return &PaymentsStore{pool: pool} }

// Compile-time check: the store satisfies the payments seam.
var _ payments.TopUpStore = (*PaymentsStore)(nil)

// SaveConfig upserts the single gateway configuration row.
func (s *PaymentsStore) SaveConfig(ctx context.Context, cfg payments.Config) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO payment_gateway_config
			(id, provider, api_key, webhook_secret, webhook_id, currency, micros_per_cent, return_base_url, updated_at)
		VALUES (1, $1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (id) DO UPDATE SET
			provider = EXCLUDED.provider,
			api_key = EXCLUDED.api_key,
			webhook_secret = EXCLUDED.webhook_secret,
			webhook_id = EXCLUDED.webhook_id,
			currency = EXCLUDED.currency,
			micros_per_cent = EXCLUDED.micros_per_cent,
			return_base_url = EXCLUDED.return_base_url,
			updated_at = now()`,
		cfg.Provider, cfg.APIKey, cfg.WebhookSecret, cfg.WebhookID, cfg.Currency, cfg.MicrosPerCent, cfg.ReturnBaseURL); err != nil {
		return fmt.Errorf("save payment gateway config: %w", err)
	}
	return nil
}

// LoadConfig returns the stored configuration; no row reads as a zero
// Config (top-ups disabled) with no error.
func (s *PaymentsStore) LoadConfig(ctx context.Context) (payments.Config, error) {
	var cfg payments.Config
	err := s.pool.QueryRow(ctx, `
		SELECT provider, api_key, webhook_secret, webhook_id, currency, micros_per_cent, return_base_url
		FROM payment_gateway_config WHERE id = 1`).
		Scan(&cfg.Provider, &cfg.APIKey, &cfg.WebhookSecret, &cfg.WebhookID, &cfg.Currency, &cfg.MicrosPerCent, &cfg.ReturnBaseURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return payments.Config{}, nil
	}
	if err != nil {
		return payments.Config{}, fmt.Errorf("load payment gateway config: %w", err)
	}
	return cfg, nil
}

// ConfigByProvider returns the last configuration saved for the named
// provider (ticket 18). The retention trigger on payment_gateway_config
// keeps the mirror current; a provider never configured reads as
// ErrNotFound so the webhook can 404 it before verification.
func (s *PaymentsStore) ConfigByProvider(ctx context.Context, provider string) (payments.Config, error) {
	var cfg payments.Config
	err := s.pool.QueryRow(ctx, `
		SELECT provider, api_key, webhook_secret, webhook_id, currency, micros_per_cent, return_base_url
		FROM payment_provider_configs WHERE provider = $1`, provider).
		Scan(&cfg.Provider, &cfg.APIKey, &cfg.WebhookSecret, &cfg.WebhookID, &cfg.Currency, &cfg.MicrosPerCent, &cfg.ReturnBaseURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return payments.Config{}, fmt.Errorf("%w: %s", payments.ErrUnknownProvider, provider)
	}
	if err != nil {
		return payments.Config{}, fmt.Errorf("load retained config for %s: %w", provider, err)
	}
	return cfg, nil
}

// Create inserts a pending top-up.
func (s *PaymentsStore) Create(ctx context.Context, tu payments.TopUp) (payments.TopUp, error) {
	if tu.ID == "" {
		// The service mints the ID before the gateway session opens (it
		// rides to the gateway as client_reference_id); direct store users
		// without one get a database-generated UUID.
		row := s.pool.QueryRow(ctx, `
			INSERT INTO top_ups (account_id, reference, provider, amount_minor, currency, credits_micros, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id, created_at, updated_at`,
			tu.AccountID, tu.Reference, tu.Provider, tu.AmountMinor, tu.Currency, tu.CreditsMicros, string(tu.Status))
		if err := row.Scan(&tu.ID, &tu.CreatedAt, &tu.UpdatedAt); err != nil {
			return payments.TopUp{}, fmt.Errorf("create top-up: %w", err)
		}
		return tu, nil
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO top_ups (id, account_id, reference, provider, amount_minor, currency, credits_micros, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at, updated_at`,
		tu.ID, tu.AccountID, tu.Reference, tu.Provider, tu.AmountMinor, tu.Currency, tu.CreditsMicros, string(tu.Status))
	if err := row.Scan(&tu.CreatedAt, &tu.UpdatedAt); err != nil {
		return payments.TopUp{}, fmt.Errorf("create top-up: %w", err)
	}
	return tu, nil
}

// ByReference loads one top-up by its gateway reference.
func (s *PaymentsStore) ByReference(ctx context.Context, reference string) (payments.TopUp, error) {
	tu, err := scanTopUp(s.pool.QueryRow(ctx,
		"SELECT "+topUpColumns+" FROM top_ups WHERE reference = $1", reference))
	if errors.Is(err, pgx.ErrNoRows) {
		return payments.TopUp{}, payments.ErrNotFound
	}
	return tu, err
}

// History returns the account's top-ups, newest first.
func (s *PaymentsStore) History(ctx context.Context, accountID string, limit int) ([]payments.TopUp, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		"SELECT "+topUpColumns+" FROM top_ups WHERE account_id = $1 "+
			"ORDER BY created_at DESC, id DESC LIMIT $2", accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("list top-ups for %s: %w", accountID, err)
	}
	defer rows.Close()
	var out []payments.TopUp
	for rows.Next() {
		tu, err := scanTopUp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tu)
	}
	return out, rows.Err()
}

// PendingOlderThan returns pending top-ups created before the cutoff,
// oldest first (ticket 18's admin detection surface). The service clamps
// the limit.
func (s *PaymentsStore) PendingOlderThan(ctx context.Context, cutoff time.Time, limit int) ([]payments.TopUp, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT "+topUpColumns+" FROM top_ups WHERE status = 'pending' AND created_at < $1 "+
			"ORDER BY created_at ASC, id ASC LIMIT $2", cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending top-ups older than %s: %w", cutoff.Format(time.RFC3339), err)
	}
	defer rows.Close()
	var out []payments.TopUp
	for rows.Next() {
		tu, err := scanTopUp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tu)
	}
	return out, rows.Err()
}

func scanTopUp(row pgx.Row) (payments.TopUp, error) {
	var tu payments.TopUp
	var status string
	err := row.Scan(&tu.ID, &tu.AccountID, &tu.Reference, &tu.Provider, &tu.AmountMinor,
		&tu.Currency, &tu.CreditsMicros, &status, &tu.MovementID, &tu.CreatedAt, &tu.UpdatedAt)
	if err != nil {
		return payments.TopUp{}, err
	}
	tu.Status = payments.TopUpStatus(status)
	return tu, nil
}

// SettlePaid claims the top-up as settled and records the ledger movement
// in one transaction. The claim is a conditional UPDATE under a row lock:
// a replay or a concurrent callback finds the row already settled and
// reads it back as a clean idempotent no-op — never posting twice. A
// posting failure rolls the whole transaction back, so the top-up stays
// pending and the callback can be retried.
func (s *PaymentsStore) SettlePaid(ctx context.Context, reference string, post func(payments.TopUp) (credits.Movement, error)) (payments.TopUp, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return payments.TopUp{}, false, fmt.Errorf("begin settlement: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the row (whether or not it exists as pending) to serialize
	// concurrent settlements of the same reference.
	current, err := scanTopUp(tx.QueryRow(ctx,
		"SELECT "+topUpColumns+" FROM top_ups WHERE reference = $1 FOR UPDATE", reference))
	if errors.Is(err, pgx.ErrNoRows) {
		return payments.TopUp{}, false, payments.ErrNotFound
	}
	if err != nil {
		return payments.TopUp{}, false, fmt.Errorf("lock top-up %s: %w", reference, err)
	}
	if current.Status.Terminal() {
		if current.Status == payments.StatusSettled {
			return current, false, nil // replay: already credited
		}
		return current, false, payments.ErrAlreadyTerminal
	}

	mov, err := post(current)
	if err != nil {
		return current, false, err // nothing written; the claim rolls back
	}

	// post builds the balanced movement; insertMovementTx writes it onto
	// the SAME transaction as the claim — that is the whole point of this
	// method. Claim and credit commit together or not at all.
	movementID, _, err := insertMovementTx(ctx, tx, mov)
	if err != nil {
		return payments.TopUp{}, false, fmt.Errorf("post top-up movement for %s: %w", reference, err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE top_ups SET status = 'settled', movement_id = $2, updated_at = now()
		WHERE reference = $1 AND status = 'pending'`,
		reference, movementID); err != nil {
		return payments.TopUp{}, false, fmt.Errorf("claim settlement for %s: %w", reference, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return payments.TopUp{}, false, fmt.Errorf("commit settlement for %s: %w", reference, err)
	}
	current.Status = payments.StatusSettled
	current.MovementID = movementID
	return current, true, nil
}

// MarkTerminal records a failed or cancelled outcome. Replays of the same
// outcome read back clean; a contradicting outcome refuses loudly.
func (s *PaymentsStore) MarkTerminal(ctx context.Context, reference string, status payments.TopUpStatus) (payments.TopUp, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE top_ups SET status = $2, updated_at = now()
		WHERE reference = $1 AND status = 'pending'`,
		reference, string(status))
	if err != nil {
		return payments.TopUp{}, fmt.Errorf("mark top-up %s: %w", reference, err)
	}
	if tag.RowsAffected() == 1 {
		return s.ByReference(ctx, reference)
	}
	// Either unknown or already terminal: distinguish for the caller.
	tu, err := s.ByReference(ctx, reference)
	if err != nil {
		return payments.TopUp{}, err
	}
	if tu.Status == status {
		return tu, nil // replay of the same terminal outcome
	}
	return tu, payments.ErrAlreadyTerminal
}
