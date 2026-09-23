package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/taxonomy"
)

// The Postgres implementation of taxonomy.Store (ticket 06; ADR 0007: the
// system of record). Terms are rows; the UNIQUE (category, value) pair is
// the vocabulary's integrity constraint, surfaced as taxonomy.ErrExists.

// TaxonomyStore implements taxonomy.Store over the shared pool.
type TaxonomyStore struct {
	pool *pgxpool.Pool
}

// NewTaxonomyStore returns a taxonomy store backed by the given pool.
func NewTaxonomyStore(pool *pgxpool.Pool) *TaxonomyStore { return &TaxonomyStore{pool: pool} }

// Compile-time check: the store satisfies the taxonomy seam.
var _ taxonomy.Store = (*TaxonomyStore)(nil)

const termColumns = `id, category, value, label, keywords, created_at`

func scanTerm(row pgx.Row) (taxonomy.Term, error) {
	var t taxonomy.Term
	err := row.Scan(&t.ID, &t.Category, &t.Value, &t.Label, &t.Keywords, &t.CreatedAt)
	return t, err
}

// CreateTerm inserts one term. The caller's ID wins when set (the seed
// path mints through the domain package); uniqueness violations surface as
// taxonomy.ErrExists.
func (s *TaxonomyStore) CreateTerm(ctx context.Context, t *taxonomy.Term) error {
	if t.ID == "" {
		id, err := taxonomy.NewID()
		if err != nil {
			return err
		}
		t.ID = id
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO taxonomy_terms (id, category, value, label, keywords)
		VALUES ($1, $2, $3, $4, $5)`,
		t.ID, string(t.Category), t.Value, t.Label, t.Keywords)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("%w: %s/%s", taxonomy.ErrExists, t.Category, t.Value)
		}
		return fmt.Errorf("insert taxonomy term: %w", err)
	}
	return nil
}

// TermByID loads one term.
func (s *TaxonomyStore) TermByID(ctx context.Context, id string) (taxonomy.Term, error) {
	t, err := scanTerm(s.pool.QueryRow(ctx,
		`SELECT `+termColumns+` FROM taxonomy_terms WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return taxonomy.Term{}, fmt.Errorf("%w: term %s", taxonomy.ErrNotFound, id)
	}
	return t, err
}

// Terms lists the vocabulary, category order then value.
func (s *TaxonomyStore) Terms(ctx context.Context) ([]taxonomy.Term, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+termColumns+` FROM taxonomy_terms ORDER BY category, value`)
	if err != nil {
		return nil, fmt.Errorf("list taxonomy terms: %w", err)
	}
	defer rows.Close()
	var out []taxonomy.Term
	for rows.Next() {
		t, err := scanTerm(rows)
		if err != nil {
			return nil, fmt.Errorf("scan taxonomy term: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateTerm rewrites the editable fields; category and value are identity.
func (s *TaxonomyStore) UpdateTerm(ctx context.Context, t taxonomy.Term) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE taxonomy_terms SET label = $2, keywords = $3 WHERE id = $1`,
		t.ID, t.Label, t.Keywords)
	if err != nil {
		return fmt.Errorf("update taxonomy term: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: term %s", taxonomy.ErrNotFound, t.ID)
	}
	return nil
}

// DeleteTerm removes one term.
func (s *TaxonomyStore) DeleteTerm(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM taxonomy_terms WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete taxonomy term: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: term %s", taxonomy.ErrNotFound, id)
	}
	return nil
}

// SeedTaxonomyIfEmpty loads the Irish-agriculture starter vocabulary when
// the store holds no terms (ticket 06: counties; dairy/beef/tillage
// profile). Idempotent — later boots leave the admin's edits alone.
func SeedTaxonomyIfEmpty(ctx context.Context, pool *pgxpool.Pool) error {
	store := NewTaxonomyStore(pool)
	if _, err := taxonomy.SeedIfEmpty(ctx, store); err != nil {
		return err
	}
	return nil
}
