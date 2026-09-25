package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/roster"
)

// The Postgres implementation of roster.Store (ticket 11; ADR 0007: the
// system of record). One row per Farmer Member contact point.

// RosterStore implements roster.Store over the shared pool.
type RosterStore struct {
	pool *pgxpool.Pool
}

// NewRosterStore returns a roster store backed by the given pool.
func NewRosterStore(pool *pgxpool.Pool) *RosterStore { return &RosterStore{pool: pool} }

// Compile-time check: the store satisfies the roster seam.
var _ roster.Store = (*RosterStore)(nil)

const rosterColumns = `id, org_id, display_name, contact, created_at, updated_at`

func scanMember(row pgx.Row) (roster.Member, error) {
	var m roster.Member
	var orgID string
	err := row.Scan(&m.ID, &orgID, &m.DisplayName, &m.Contact, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return roster.Member{}, err
	}
	m.OrgID = orgID
	return m, nil
}

// CreateMember inserts the contact point; zero timestamps are filled in
// here and written back through the pointer, matching the Store contract.
func (s *RosterStore) CreateMember(ctx context.Context, m *roster.Member) error {
	now := time.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = now
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO roster_members (id, org_id, display_name, contact, created_at, updated_at)
		VALUES ($1, $2::uuid, $3, $4, $5, $6)`,
		m.ID, m.OrgID, m.DisplayName, m.Contact, m.CreatedAt, m.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert roster member: %w", err)
	}
	return nil
}

// MemberByID loads one contact point.
func (s *RosterStore) MemberByID(ctx context.Context, id string) (roster.Member, error) {
	m, err := scanMember(s.pool.QueryRow(ctx,
		`SELECT `+rosterColumns+` FROM roster_members WHERE id = $1`, id))
	return m, mapRosterErr(err, id)
}

// MembersByOrg lists the org's roster, oldest first.
func (s *RosterStore) MembersByOrg(ctx context.Context, orgID string) ([]roster.Member, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+rosterColumns+` FROM roster_members WHERE org_id = $1::uuid
		ORDER BY created_at, id`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list roster members: %w", err)
	}
	defer rows.Close()
	var out []roster.Member
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, fmt.Errorf("scan roster member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpdateMember rewrites one contact point.
func (s *RosterStore) UpdateMember(ctx context.Context, m roster.Member) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE roster_members
		SET display_name = $2, contact = $3, updated_at = now()
		WHERE id = $1`,
		m.ID, m.DisplayName, m.Contact)
	if err != nil {
		return fmt.Errorf("update roster member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", roster.ErrNotFound, m.ID)
	}
	return nil
}

// DeleteMember removes one contact point.
func (s *RosterStore) DeleteMember(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM roster_members WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete roster member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", roster.ErrNotFound, id)
	}
	return nil
}

func mapRosterErr(err error, id string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", roster.ErrNotFound, id)
	}
	return err
}
