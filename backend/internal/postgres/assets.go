package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/assets"
)

// The Postgres implementation of assets.Store (ticket 04; ADR 0007: the
// system of record). Blob bytes never come here — only metadata; the
// pipeline and provenance documents ride as JSONB.

// AssetsStore implements assets.Store over the shared pool.
type AssetsStore struct {
	pool *pgxpool.Pool
}

// NewAssetsStore returns an assets store backed by the given pool.
func NewAssetsStore(pool *pgxpool.Pool) *AssetsStore { return &AssetsStore{pool: pool} }

// Compile-time check: the store satisfies the assets seam.
var _ assets.Store = (*AssetsStore)(nil)

const assetColumns = `
	id, org_id, name, description, size_bytes, format, pipeline, provenance,
	object_key, created_at, updated_at`

func (s *AssetsStore) scanAsset(row pgx.Row) (assets.Asset, error) {
	var a assets.Asset
	var pipeline, provenance []byte
	err := row.Scan(&a.ID, &a.OrgID, &a.Name, &a.Description, &a.SizeBytes,
		&a.Format, &pipeline, &provenance, &a.ObjectKey, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return assets.Asset{}, err
	}
	if err := json.Unmarshal(pipeline, &a.Pipeline); err != nil {
		return assets.Asset{}, fmt.Errorf("decode pipeline for %s: %w", a.ID, err)
	}
	prov, err := decodeProvenance(provenance)
	if err != nil {
		return assets.Asset{}, fmt.Errorf("decode provenance for %s: %w", a.ID, err)
	}
	a.Provenance = prov
	return a, nil
}

// decodeProvenance reads the JSONB provenance document. CollectedAt rides
// inside it as an RFC 3339 string (or null); the scan leaves it unset, so
// decode here rather than through a nullable column.
func decodeProvenance(doc []byte) (assets.Provenance, error) {
	var raw struct {
		Source      string     `json:"source"`
		CollectedAt *time.Time `json:"collected_at"`
		Notes       string     `json:"notes"`
	}
	if len(doc) == 0 {
		return assets.Provenance{}, nil
	}
	if err := json.Unmarshal(doc, &raw); err != nil {
		return assets.Provenance{}, err
	}
	return assets.Provenance{Source: raw.Source, CollectedAt: raw.CollectedAt, Notes: raw.Notes}, nil
}

func encodeProvenance(p assets.Provenance) ([]byte, error) {
	return json.Marshal(struct {
		Source      string     `json:"source"`
		CollectedAt *time.Time `json:"collected_at"`
		Notes       string     `json:"notes"`
	}{
		Source: p.Source, CollectedAt: p.CollectedAt, Notes: p.Notes,
	})
}

// CreateAsset inserts one record. ID is the caller's (the service mints it
// before the blob write so object keys are stable); zero timestamps are
// filled in here and written back through the pointer, matching the
// Store contract — the caller serializes the record immediately after.
func (s *AssetsStore) CreateAsset(ctx context.Context, a *assets.Asset) error {
	prov, err := encodeProvenance(a.Provenance)
	if err != nil {
		return fmt.Errorf("encode provenance: %w", err)
	}
	pipeline, err := json.Marshal(a.Pipeline)
	if err != nil {
		return fmt.Errorf("encode pipeline: %w", err)
	}
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	if a.UpdatedAt.IsZero() {
		a.UpdatedAt = now
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO data_assets
			(id, org_id, name, description, size_bytes, format, pipeline, provenance, object_key, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		a.ID, a.OrgID, a.Name, a.Description, a.SizeBytes, a.Format,
		pipeline, prov, a.ObjectKey, a.CreatedAt, a.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert data asset: %w", err)
	}
	return nil
}

// AssetByID loads one record.
func (s *AssetsStore) AssetByID(ctx context.Context, id string) (assets.Asset, error) {
	a, err := s.scanAsset(s.pool.QueryRow(ctx,
		`SELECT `+assetColumns+` FROM data_assets WHERE id = $1`, id))
	return a, mapAssetErr(err, id)
}

// AssetsByOrg lists the org's records, newest first.
func (s *AssetsStore) AssetsByOrg(ctx context.Context, orgID string) ([]assets.Asset, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+assetColumns+` FROM data_assets WHERE org_id = $1
		ORDER BY created_at DESC, id`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list assets for org: %w", err)
	}
	defer rows.Close()

	var out []assets.Asset
	for rows.Next() {
		a, err := s.scanAsset(rows)
		if err != nil {
			return nil, fmt.Errorf("scan asset: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAssetMeta rewrites the editable fields; immutable columns (size,
// format, pipeline, object key) are never touched.
func (s *AssetsStore) UpdateAssetMeta(ctx context.Context, a assets.Asset) error {
	prov, err := encodeProvenance(a.Provenance)
	if err != nil {
		return fmt.Errorf("encode provenance: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE data_assets
		SET name = $2, description = $3, provenance = $4, updated_at = now()
		WHERE id = $1`,
		a.ID, a.Name, a.Description, prov)
	if err != nil {
		return fmt.Errorf("update data asset: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", assets.ErrNotFound, a.ID)
	}
	return nil
}

// DeleteAsset removes the record and returns it — the caller needs
// ObjectKey to delete the blob (ticket checklist: delete removes object
// and record).
func (s *AssetsStore) DeleteAsset(ctx context.Context, id string) (assets.Asset, error) {
	a, err := s.scanAsset(s.pool.QueryRow(ctx, `
		DELETE FROM data_assets WHERE id = $1
		RETURNING `+assetColumns, id))
	return a, mapAssetErr(err, id)
}

func mapAssetErr(err error, id string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", assets.ErrNotFound, id)
	}
	return err
}
