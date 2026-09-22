package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
)

// Store implements membership.Store against Postgres (ADR 0007: the system
// of record). One row per domain fact; uniqueness constraints surface as
// membership.ErrConflict.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a membership store backed by the given pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Compile-time check: the postgres store satisfies the membership seam.
var _ membership.Store = (*Store)(nil)

func mapErr(err error, id string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", membership.ErrNotFound, id)
	}
	// Non-UUID lookups (client-supplied IDs) fail as a type error; callers
	// contract them as "not found".
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "22P02" { // invalid_text_representation
		return fmt.Errorf("%w: %s", membership.ErrNotFound, id)
	}
	return err
}

// --- Accounts ---

func (s *Store) CreateAccount(ctx context.Context, acct *membership.Account) error {
	var createdAt time.Time
	err := s.pool.QueryRow(ctx, `
		INSERT INTO accounts (email, password_hash, display_name, role, status)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (email) DO NOTHING
		RETURNING id, created_at`,
		acct.Email, acct.PasswordHash, acct.DisplayName, string(acct.Role), string(acct.Status)).
		Scan(&acct.ID, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return membership.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("insert account: %w", err)
	}
	acct.CreatedAt = createdAt
	return nil
}

func (s *Store) AccountByEmail(ctx context.Context, email string) (membership.Account, error) {
	acct, err := s.scanAccount(s.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, display_name, role, status, verified_at, created_at
		FROM accounts WHERE email = $1`, email))
	return acct, mapErr(err, email)
}

func (s *Store) AccountByID(ctx context.Context, id string) (membership.Account, error) {
	acct, err := s.scanAccount(s.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, display_name, role, status, verified_at, created_at
		FROM accounts WHERE id = $1`, id))
	return acct, mapErr(err, id)
}

func (s *Store) scanAccount(row pgx.Row) (membership.Account, error) {
	var a membership.Account
	var role, status string
	var verifiedAt *time.Time
	if err := row.Scan(&a.ID, &a.Email, &a.PasswordHash, &a.DisplayName, &role, &status, &verifiedAt, &a.CreatedAt); err != nil {
		return membership.Account{}, err
	}
	a.Role = membership.Role(role)
	a.Status = membership.Status(status)
	a.VerifiedAt = verifiedAt
	return a, nil
}

func (s *Store) SetAccountVerified(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE accounts SET verified_at = $2 WHERE id = $1`, id, at)
	if err != nil {
		return fmt.Errorf("verify account: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", membership.ErrNotFound, id)
	}
	return nil
}

func (s *Store) SetApplicationStatus(ctx context.Context, id string, status membership.Status) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE accounts SET status = $2 WHERE id = $1`, id, string(status))
	if err != nil {
		return fmt.Errorf("set application status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", membership.ErrNotFound, id)
	}
	return nil
}

func (s *Store) PendingOrganizations(ctx context.Context) ([]membership.Account, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, email, password_hash, display_name, role, status, verified_at, created_at
		FROM accounts
		WHERE role = 'farmer_organization' AND status = 'pending_approval'
		ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list pending organizations: %w", err)
	}
	defer rows.Close()

	var out []membership.Account
	for rows.Next() {
		var a membership.Account
		var role, status string
		var verifiedAt *time.Time
		if err := rows.Scan(&a.ID, &a.Email, &a.PasswordHash, &a.DisplayName, &role, &status, &verifiedAt, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan pending organization: %w", err)
		}
		a.Role = membership.Role(role)
		a.Status = membership.Status(status)
		a.VerifiedAt = verifiedAt
		out = append(out, a)
	}
	return out, rows.Err()
}

// --- Verification tokens ---

func (s *Store) CreateVerificationToken(ctx context.Context, token, accountID string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO verification_tokens (token, account_id, expires_at)
		VALUES ($1, $2, $3)`, token, accountID, expiresAt)
	if err != nil {
		return fmt.Errorf("insert verification token: %w", err)
	}
	return nil
}

func (s *Store) ConsumeVerificationToken(ctx context.Context, token string, now time.Time) (string, error) {
	var accountID string
	err := s.pool.QueryRow(ctx, `
		UPDATE verification_tokens
		SET used_at = now()
		WHERE token = $1 AND used_at IS NULL AND expires_at > $2
		RETURNING account_id`, token, now).Scan(&accountID)
	return accountID, mapErr(err, token)
}

// --- Terms of Service ---

func (s *Store) PublishTOS(ctx context.Context, v membership.TOSVersion) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tos_versions (version, body, published_at)
		VALUES ($1, $2, COALESCE(NULLIF($3, '0001-01-01 00:00:00+00'::timestamptz), now()))
		ON CONFLICT (version) DO UPDATE SET body = EXCLUDED.body`,
		v.Version, v.Body, v.PublishedAt)
	if err != nil {
		return fmt.Errorf("publish TOS version: %w", err)
	}
	return nil
}

func (s *Store) scanTOS(row pgx.Row) (membership.TOSVersion, error) {
	var v membership.TOSVersion
	if err := row.Scan(&v.Version, &v.Body, &v.PublishedAt); err != nil {
		return membership.TOSVersion{}, err
	}
	return v, nil
}

func (s *Store) CurrentTOS(ctx context.Context) (membership.TOSVersion, error) {
	v, err := s.scanTOS(s.pool.QueryRow(ctx, `
		SELECT version, body, published_at FROM tos_versions
		ORDER BY published_at DESC LIMIT 1`))
	return v, mapErr(err, "current TOS")
}

func (s *Store) TOSVersions(ctx context.Context) ([]membership.TOSVersion, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT version, body, published_at FROM tos_versions
		ORDER BY published_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list TOS versions: %w", err)
	}
	defer rows.Close()

	var out []membership.TOSVersion
	for rows.Next() {
		var v membership.TOSVersion
		if err := rows.Scan(&v.Version, &v.Body, &v.PublishedAt); err != nil {
			return nil, fmt.Errorf("scan TOS version: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) RecordAcceptance(ctx context.Context, acc membership.TOSAcceptance) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tos_acceptances (account_id, tos_version, accepted_at)
		VALUES ($1, $2, COALESCE(NULLIF($3, '0001-01-01 00:00:00+00'::timestamptz), now()))`,
		acc.AccountID, acc.Version, acc.AcceptedAt)
	if err != nil {
		return fmt.Errorf("record TOS acceptance: %w", err)
	}
	return nil
}

func (s *Store) LatestAcceptance(ctx context.Context, accountID string) (membership.TOSAcceptance, error) {
	var acc membership.TOSAcceptance
	err := s.pool.QueryRow(ctx, `
		SELECT account_id, tos_version, accepted_at FROM tos_acceptances
		WHERE account_id = $1
		ORDER BY accepted_at DESC LIMIT 1`, accountID).
		Scan(&acc.AccountID, &acc.Version, &acc.AcceptedAt)
	return acc, mapErr(err, accountID)
}

// --- Sessions ---

func (s *Store) CreateSession(ctx context.Context, sess membership.Session) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (token, account_id, created_at, requires_reacceptance)
		VALUES ($1, $2, COALESCE(NULLIF($3, '0001-01-01 00:00:00+00'::timestamptz), now()), $4)`,
		sess.Token, sess.AccountID, sess.CreatedAt, sess.RequiresReacceptance)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

func (s *Store) SessionByToken(ctx context.Context, token string) (membership.Session, error) {
	var sess membership.Session
	err := s.pool.QueryRow(ctx, `
		SELECT token, account_id, created_at, requires_reacceptance FROM sessions
		WHERE token = $1`, token).
		Scan(&sess.Token, &sess.AccountID, &sess.CreatedAt, &sess.RequiresReacceptance)
	return sess, mapErr(err, "session")
}

func (s *Store) UpdateSession(ctx context.Context, sess membership.Session) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE sessions SET requires_reacceptance = $2 WHERE token = $1`,
		sess.Token, sess.RequiresReacceptance)
	if err != nil {
		return fmt.Errorf("update session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: session", membership.ErrNotFound)
	}
	return nil
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token = $1`, token)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}
