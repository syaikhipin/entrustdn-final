package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/modules"
)

// The Postgres implementation of modules.Store (ticket 08; ADR 0007: the
// system of record). One row per version; the module's identity fields are
// denormalized onto every row, so version history is a plain index scan
// and promotion is a module-wide UPDATE.

// ModulesStore implements modules.Store over the shared pool.
type ModulesStore struct {
	pool *pgxpool.Pool
}

// NewModulesStore returns a modules store backed by the given pool.
func NewModulesStore(pool *pgxpool.Pool) *ModulesStore { return &ModulesStore{pool: pool} }

// Compile-time check: the store satisfies the modules seam.
var _ modules.Store = (*ModulesStore)(nil)

const moduleColumns = `
	id, module_id, name, kind, author_id, version, capability, content,
	config, system_wide, deprecated, created_at, updated_at`

// scanModule decodes one row. The author ID is cast to text — the modules
// package speaks opaque author IDs, and account UUIDs are valid text IDs.
// pgx.Rows satisfies pgx.Row, so one scanner serves single and multi-row
// queries alike.
func (s *ModulesStore) scanModule(row pgx.Row) (modules.Module, error) {
	var m modules.Module
	var authorID string
	err := row.Scan(&m.ID, &m.ModuleID, &m.Name, &m.Kind, &authorID, &m.Version,
		&m.Capability, &m.Content, &m.Config, &m.SystemWide, &m.Deprecated,
		&m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return modules.Module{}, err
	}
	m.AuthorID = authorID
	return m, nil
}

// mapModuleErr converts driver misses into the seam's sentinel.
func mapModuleErr(err error, what, id string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s %s", modules.ErrNotFound, what, id)
	}
	return err
}

// CreateModule inserts the first version; zero timestamps are filled in
// here and written back through the pointer, matching the Store contract.
func (s *ModulesStore) CreateModule(ctx context.Context, m *modules.Module) error {
	now := time.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = now
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO modules
			(id, module_id, name, kind, author_id, version, capability, content,
			 config, system_wide, deprecated, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5::uuid, $6, $7, $8, $9, $10, $11, $12, $13)`,
		m.ID, m.ModuleID, m.Name, string(m.Kind), m.AuthorID, m.Version,
		m.Capability, m.Content, m.Config, m.SystemWide, m.Deprecated,
		m.CreatedAt, m.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert module: %w", err)
	}
	return nil
}

// CreateModuleVersion inserts a later version of an existing Module. The
// UNIQUE (module_id, version) constraint backstops the duplicate-version
// refusal with a database guarantee.
func (s *ModulesStore) CreateModuleVersion(ctx context.Context, m *modules.Module) error {
	now := time.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = now
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO modules
			(id, module_id, name, kind, author_id, version, capability, content,
			 config, system_wide, deprecated, created_at, updated_at)
		SELECT $1, m.module_id, m.name, m.kind, m.author_id, $2, $3, $4, $5,
		       FALSE, FALSE, $6, $6
		FROM modules m
		WHERE m.module_id = $7`,
		m.ID, m.Version, m.Capability, m.Content, m.Config, m.CreatedAt, m.ModuleID)
	if err != nil {
		return fmt.Errorf("insert module version: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: module %s", modules.ErrNotFound, m.ModuleID)
	}
	return nil
}

// ModuleByID loads one version record.
func (s *ModulesStore) ModuleByID(ctx context.Context, id string) (modules.Module, error) {
	m, err := s.scanModule(s.pool.QueryRow(ctx,
		`SELECT `+moduleColumns+` FROM modules WHERE id = $1`, id))
	return m, mapModuleErr(err, "module version", id)
}

// LatestModuleVersion loads the newest version of a Module (by created_at,
// then ID).
func (s *ModulesStore) LatestModuleVersion(ctx context.Context, moduleID string) (modules.Module, error) {
	m, err := s.scanModule(s.pool.QueryRow(ctx, `
		SELECT `+moduleColumns+` FROM modules WHERE module_id = $1
		ORDER BY created_at DESC, id DESC LIMIT 1`, moduleID))
	return m, mapModuleErr(err, "module", moduleID)
}

// ModuleVersions lists every version of a Module, oldest first.
func (s *ModulesStore) ModuleVersions(ctx context.Context, moduleID string) ([]modules.Module, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+moduleColumns+` FROM modules WHERE module_id = $1
		ORDER BY created_at, id`, moduleID)
	if err != nil {
		return nil, fmt.Errorf("list versions for module: %w", err)
	}
	defer rows.Close()
	return s.collectModules(rows)
}

// ModulesByAuthor lists the latest version of each of the author's Modules,
// newest first. DISTINCT ON keeps one row per module_id at the newest
// created_at.
func (s *ModulesStore) ModulesByAuthor(ctx context.Context, authorID string) ([]modules.Module, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (module_id) `+moduleColumns+` FROM modules
		WHERE author_id = $1::uuid
		ORDER BY module_id, created_at DESC, id DESC`, authorID)
	if err != nil {
		return nil, fmt.Errorf("list modules for author: %w", err)
	}
	defer rows.Close()
	out, err := s.collectModules(rows)
	if err != nil {
		return nil, err
	}
	sortModulesDesc(out)
	return out, nil
}

// SystemWideModules lists the latest version of every system-wide Module,
// newest first.
func (s *ModulesStore) SystemWideModules(ctx context.Context) ([]modules.Module, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (module_id) `+moduleColumns+` FROM modules
		WHERE system_wide
		ORDER BY module_id, created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list system-wide modules: %w", err)
	}
	defer rows.Close()
	out, err := s.collectModules(rows)
	if err != nil {
		return nil, err
	}
	sortModulesDesc(out)
	return out, nil
}

// SetSystemWide flips a Module's promotion flag across every version row —
// promotion is module-level state.
func (s *ModulesStore) SetSystemWide(ctx context.Context, moduleID string, systemWide bool) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE modules SET system_wide = $2, updated_at = now()
		WHERE module_id = $1`, moduleID, systemWide)
	if err != nil {
		return fmt.Errorf("set system_wide: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: module %s", modules.ErrNotFound, moduleID)
	}
	return nil
}

// SetVersionDeprecated flips one version's deprecation flag.
func (s *ModulesStore) SetVersionDeprecated(ctx context.Context, id string, deprecated bool) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE modules SET deprecated = $2, updated_at = now()
		WHERE id = $1`, id, deprecated)
	if err != nil {
		return fmt.Errorf("set deprecated: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: module version %s", modules.ErrNotFound, id)
	}
	return nil
}

// AddGrant records one account's read access to a Module.
func (s *ModulesStore) AddGrant(ctx context.Context, g modules.Grant) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO module_grants (module_id, account_id, granted_at)
		VALUES ($1, $2::uuid, $3)`, g.ModuleID, g.AccountID, g.GrantedAt)
	if err != nil {
		return fmt.Errorf("add module grant: %w", err)
	}
	return nil
}

// RemoveGrant deletes one account's read access.
func (s *ModulesStore) RemoveGrant(ctx context.Context, moduleID, accountID string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM module_grants WHERE module_id = $1 AND account_id = $2::uuid`,
		moduleID, accountID)
	if err != nil {
		return fmt.Errorf("remove module grant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: grant for %s", modules.ErrNotFound, accountID)
	}
	return nil
}

// GrantExists reports whether the account holds read access.
func (s *ModulesStore) GrantExists(ctx context.Context, moduleID, accountID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM module_grants WHERE module_id = $1 AND account_id = $2::uuid)`,
		moduleID, accountID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("check module grant: %w", err)
	}
	return ok, nil
}

// GrantsForModule lists a Module's grants, oldest first.
func (s *ModulesStore) GrantsForModule(ctx context.Context, moduleID string) ([]modules.Grant, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT module_id, account_id::text, granted_at FROM module_grants
		WHERE module_id = $1 ORDER BY granted_at`, moduleID)
	if err != nil {
		return nil, fmt.Errorf("list module grants: %w", err)
	}
	defer rows.Close()

	var out []modules.Grant
	for rows.Next() {
		var g modules.Grant
		if err := rows.Scan(&g.ModuleID, &g.AccountID, &g.GrantedAt); err != nil {
			return nil, fmt.Errorf("scan module grant: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *ModulesStore) collectModules(rows pgx.Rows) ([]modules.Module, error) {
	var out []modules.Module
	for rows.Next() {
		m, err := s.scanModule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan module: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// sortModulesDesc orders latest-version listings newest first, module_id as
// the tiebreak.
func sortModulesDesc(a []modules.Module) {
	sort.Slice(a, func(i, j int) bool {
		if !a[i].CreatedAt.Equal(a[j].CreatedAt) {
			return a[i].CreatedAt.After(a[j].CreatedAt)
		}
		return a[i].ModuleID < a[j].ModuleID
	})
}
