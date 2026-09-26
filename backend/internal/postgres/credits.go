package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
)

// The Postgres implementation of credits.Store (ticket 03; ADR 0007: the
// system of record). Each movement + entries pair writes inside one
// transaction, so a posting is atomic and its overdraft check runs against
// a locked read of the affected scopes.

// CreditsStore implements credits.Store over the shared pool.
type CreditsStore struct {
	pool *pgxpool.Pool
}

// NewCreditsStore returns a Ledger store backed by the given pool.
func NewCreditsStore(pool *pgxpool.Pool) *CreditsStore { return &CreditsStore{pool: pool} }

// Compile-time check: the store satisfies the Ledger seam.
var _ credits.Store = (*CreditsStore)(nil)

// PostMovement writes the movement and its entries in one transaction,
// refusing unbalanced or overdrawn postings before anything is written.
// An inference charge carrying usage details records them in the same
// transaction, so the spend view can explain every charge down to tokens
// and rates.
func (s *CreditsStore) PostMovement(ctx context.Context, mov credits.Movement) (credits.Movement, error) {
	if len(mov.Entries) == 0 || !credits.Balanced(mov.Entries) {
		return credits.Movement{}, credits.ErrUnbalanced
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return credits.Movement{}, fmt.Errorf("begin movement: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Overdraft guard: for each user scope, its derived balance plus this
	// movement's delta may not go below zero. The guard takes a transaction
	// advisory lock per scope before summing: FOR UPDATE alone cannot save
	// us here, because entries are append-only and a blocked reader would
	// not see rows a concurrent poster INSERTed while it waited (phantom
	// read at READ COMMITTED). The advisory lock serializes posters on the
	// same scope whatever rows exist, and releases at commit. System scopes
	// are exempt (treasury issues credits, platform accumulates them).
	deltas := map[credits.Scope]int64{}
	for _, e := range mov.Entries {
		if credits.IsSystemScope(e.Scope) {
			continue
		}
		deltas[e.Scope] += e.AmountMicros
	}
	// Deterministic lock order: movements touching two scopes in opposite
	// orders must not deadlock.
	scopes := make([]string, 0, len(deltas))
	for scope := range deltas {
		scopes = append(scopes, string(scope))
	}
	sort.Strings(scopes)
	for _, scope := range scopes {
		if _, err := tx.Exec(ctx,
			`SELECT pg_advisory_xact_lock(hashtext($1))`, scope); err != nil {
			return credits.Movement{}, fmt.Errorf("lock scope %s: %w", scope, err)
		}
		var current int64
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(SUM(amount_micros), 0) FROM ledger_entries WHERE scope = $1`,
			scope).Scan(&current); err != nil {
			return credits.Movement{}, fmt.Errorf("sum balance for %s: %w", scope, err)
		}
		if current+deltas[credits.Scope(scope)] < 0 {
			return credits.Movement{}, credits.ErrInsufficientFunds
		}
	}

	movementID, createdAt, err := insertMovementTx(ctx, tx, mov)
	if err != nil {
		return credits.Movement{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return credits.Movement{}, fmt.Errorf("commit movement: %w", err)
	}
	return credits.Movement{
		ID:        movementID,
		Kind:      mov.Kind,
		ActorID:   mov.ActorID,
		RequestID: mov.RequestID,
		Memo:      mov.Memo,
		CreatedAt: createdAt,
		Entries:   append([]credits.Entry(nil), mov.Entries...),
	}, nil
}

// insertMovementTx writes one movement and its entries onto the given
// transaction — the shared posting path for the credits store and the
// payments store (whose settlement must claim the top-up and record the
// movement in one transaction). The caller owns validation (balance,
// overdraft); the credits store checks before calling, and the payments
// store's top-ups only credit. Inference details ride along when present.
func insertMovementTx(ctx context.Context, tx pgx.Tx, mov credits.Movement) (string, time.Time, error) {
	var movementID string
	var createdAt = mov.CreatedAt
	err := tx.QueryRow(ctx, `
		INSERT INTO ledger_movements (kind, actor_id, request_id, memo)
		VALUES ($1, NULLIF($2, '')::UUID, NULLIF($3, ''), $4)
		RETURNING id, created_at`,
		mov.Kind, mov.ActorID, mov.RequestID, mov.Memo).
		Scan(&movementID, &createdAt)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("insert movement: %w", err)
	}
	for _, e := range mov.Entries {
		if _, err := tx.Exec(ctx, `
			INSERT INTO ledger_entries (movement_id, scope, amount_micros, memo)
			VALUES ($1, $2, $3, $4)`,
			movementID, string(e.Scope), e.AmountMicros, e.Memo); err != nil {
			return "", time.Time{}, fmt.Errorf("insert entry for %s: %w", e.Scope, err)
		}
	}
	if mov.Inference != nil && mov.Kind == credits.KindInferenceCharge {
		d := mov.Inference
		if _, err := tx.Exec(ctx, `
			INSERT INTO inference_charge_details
				(movement_id, model, input_tokens, cached_input_tokens, output_tokens,
				 input_micros_per_1k, cached_input_micros_per_1k, output_micros_per_1k)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			movementID, d.Model, d.InputTokens, d.CachedInputTokens, d.OutputTokens,
			d.InputMicrosPer1K, d.CachedInputMicrosPer1K, d.OutputMicrosPer1K); err != nil {
			return "", time.Time{}, fmt.Errorf("insert inference charge details: %w", err)
		}
	}
	return movementID, createdAt, nil
}

// Balance derives one scope's balance: the sum of its entries. Nothing is
// stored — there is no mutable balance column to corrupt.
func (s *CreditsStore) Balance(ctx context.Context, scope credits.Scope) (int64, error) {
	var sum int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_micros), 0) FROM ledger_entries WHERE scope = $1`, string(scope)).
		Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("derive balance for %s: %w", scope, err)
	}
	return sum, nil
}

// Balances derives every scope's balance in one aggregate query.
func (s *CreditsStore) Balances(ctx context.Context) (map[credits.Scope]int64, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT scope, SUM(amount_micros) FROM ledger_entries GROUP BY scope`)
	if err != nil {
		return nil, fmt.Errorf("list balances: %w", err)
	}
	defer rows.Close()
	out := map[credits.Scope]int64{}
	for rows.Next() {
		var scope credits.Scope
		var sum int64
		if err := rows.Scan(&scope, &sum); err != nil {
			return nil, fmt.Errorf("scan balance: %w", err)
		}
		out[scope] = sum
	}
	return out, rows.Err()
}

// MovementsByScope returns the movements touching the scope, newest first.
func (s *CreditsStore) MovementsByScope(ctx context.Context, scope credits.Scope, limit int) ([]credits.Movement, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.kind, COALESCE(m.actor_id::TEXT, ''), COALESCE(m.request_id, ''),
		       m.memo, m.created_at, e.scope, e.amount_micros, e.memo,
		       COALESCE(d.model, ''), COALESCE(d.input_tokens, 0), COALESCE(d.cached_input_tokens, 0),
		       COALESCE(d.output_tokens, 0), COALESCE(d.input_micros_per_1k, 0),
		       COALESCE(d.cached_input_micros_per_1k, 0), COALESCE(d.output_micros_per_1k, 0)
		FROM ledger_movements m
		JOIN ledger_entries e ON e.movement_id = m.id
		LEFT JOIN inference_charge_details d ON d.movement_id = m.id
		WHERE m.id IN (
			SELECT e2.movement_id
			FROM ledger_entries e2
			JOIN ledger_movements m2 ON m2.id = e2.movement_id
			WHERE e2.scope = $1
			GROUP BY e2.movement_id
			ORDER BY max(m2.created_at) DESC
			LIMIT $2
		)
		ORDER BY m.created_at DESC, m.id DESC, e.scope`,
		string(scope), limit)
	if err != nil {
		return nil, fmt.Errorf("list movements for %s: %w", scope, err)
	}
	defer rows.Close()

	byID := map[string]*credits.Movement{}
	var order []string
	for rows.Next() {
		var id string
		var e credits.Entry
		var mov credits.Movement
		var detail credits.InferenceDetail
		if err := rows.Scan(&id, &mov.Kind, &mov.ActorID, &mov.RequestID,
			&mov.Memo, &mov.CreatedAt, &e.Scope, &e.AmountMicros, &e.Memo,
			&detail.Model, &detail.InputTokens, &detail.CachedInputTokens,
			&detail.OutputTokens, &detail.InputMicrosPer1K,
			&detail.CachedInputMicrosPer1K, &detail.OutputMicrosPer1K); err != nil {
			return nil, fmt.Errorf("scan movement row: %w", err)
		}
		if _, seen := byID[id]; !seen {
			m := mov
			m.Entries = []credits.Entry{}
			if detail.Model != "" {
				d := detail
				m.Inference = &d
			}
			byID[id] = &m
			order = append(order, id)
		}
		byID[id].Entries = append(byID[id].Entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate movements: %w", err)
	}
	out := make([]credits.Movement, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// RevenueShareReceived sums the scope's positive entries across revenue_share
// movements — one aggregate over the whole ledger, so an org's gross
// receipts never truncate at a history-page boundary.
func (s *CreditsStore) RevenueShareReceived(ctx context.Context, scope credits.Scope) (int64, error) {
	var total int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(e.amount_micros), 0)
		FROM ledger_entries e
		JOIN ledger_movements m ON m.id = e.movement_id
		WHERE e.scope = $1 AND m.kind = 'revenue_share' AND e.amount_micros > 0`,
		string(scope)).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum revenue share for %s: %w", scope, err)
	}
	return total, nil
}

// CreditRulesLoader reads the admin-configured price book: the `rules`
// function the credits Service takes. An unconfigured or invalid price book
// reads as ErrNoPricingRule — charges fail loudly instead of guessing.
func CreditRulesLoader(pool *pgxpool.Pool) func(context.Context) (credits.PricingRules, error) {
	return func(ctx context.Context) (credits.PricingRules, error) {
		var raw []byte
		err := pool.QueryRow(ctx, `SELECT document FROM pricing_rules WHERE id = 1`).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return credits.PricingRules{}, fmt.Errorf("%w: no price book configured", credits.ErrNoPricingRule)
		}
		if err != nil {
			return credits.PricingRules{}, fmt.Errorf("load pricing rules: %w", err)
		}
		var rules credits.PricingRules
		if err := json.Unmarshal(raw, &rules); err != nil {
			return credits.PricingRules{}, fmt.Errorf("stored pricing rules are not valid JSON: %w", err)
		}
		if err := rules.Validate(); err != nil {
			return credits.PricingRules{}, fmt.Errorf("stored pricing rules are invalid: %w", err)
		}
		return rules, nil
	}
}

// SaveCreditRules validates and upserts the price book — the admin surface's
// writer. Invalid rule sets never reach the database.
func SaveCreditRules(ctx context.Context, pool *pgxpool.Pool, rules credits.PricingRules) error {
	if err := rules.Validate(); err != nil {
		return fmt.Errorf("refusing invalid pricing rules: %w", err)
	}
	doc, err := json.Marshal(rules)
	if err != nil {
		return fmt.Errorf("encode pricing rules: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO pricing_rules (id, document, updated_at)
		VALUES (1, $1, now())
		ON CONFLICT (id) DO UPDATE SET document = EXCLUDED.document, updated_at = now()`, doc); err != nil {
		return fmt.Errorf("save pricing rules: %w", err)
	}
	return nil
}
