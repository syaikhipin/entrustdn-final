package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/memoryprov"
)

// The Postgres implementation of memoryprov.Store (ticket 14; ADR 0007:
// the system of record). Providers are configuration rows: connection
// facts only, no credentials. The UNIQUE (name) constraint is the
// registry's integrity, surfaced as memoryprov.ErrExists.

// MemoryProvidersStore implements memoryprov.Store over the shared pool.
type MemoryProvidersStore struct {
	pool *pgxpool.Pool
}

// NewMemoryProvidersStore returns a provider registry backed by the given
// pool.
func NewMemoryProvidersStore(pool *pgxpool.Pool) *MemoryProvidersStore {
	return &MemoryProvidersStore{pool: pool}
}

// Compile-time check: the store satisfies the memoryprov seam.
var _ memoryprov.Store = (*MemoryProvidersStore)(nil)

const providerColumns = `id::TEXT, name, endpoint, created_at, updated_at`

// Create inserts one provider. The caller's ID wins when set (mirrors the
// other registries); a duplicate name surfaces as memoryprov.ErrExists.
func (s *MemoryProvidersStore) Create(ctx context.Context, p *memoryprov.Provider) error {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO memory_providers (id, name, endpoint)
		VALUES (COALESCE(NULLIF($1, '')::UUID, gen_random_uuid()), $2, $3)
		RETURNING id::TEXT, created_at, updated_at`,
		p.ID, p.Name, p.Endpoint).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("%w: %s", memoryprov.ErrExists, p.Name)
		}
		return fmt.Errorf("insert memory provider: %w", err)
	}
	return nil
}

// List returns the configured providers, newest first.
func (s *MemoryProvidersStore) List(ctx context.Context) ([]memoryprov.Provider, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+providerColumns+` FROM memory_providers ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list memory providers: %w", err)
	}
	defer rows.Close()
	out := []memoryprov.Provider{}
	for rows.Next() {
		var p memoryprov.Provider
		if err := rows.Scan(&p.ID, &p.Name, &p.Endpoint, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan memory provider: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list memory providers: %w", err)
	}
	return out, nil
}

// Delete removes one provider by ID. An unknown ID is memoryprov.ErrNotFound.
func (s *MemoryProvidersStore) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM memory_providers WHERE id = $1::UUID`, id)
	if err != nil {
		return fmt.Errorf("delete memory provider: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", memoryprov.ErrNotFound, id)
	}
	return nil
}
