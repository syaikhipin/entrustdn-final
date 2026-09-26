package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/collections"
	"github.com/syaikhipin/entrustdn-final/backend/internal/requests"
)

// The Postgres implementation of collections.Store (ticket 12; ADR 0007).
// The items — with their gathering rounds — and the missing-data summary
// ride as JSONB: Sync is the single writer and always rewrites the whole
// record, the same pattern as data_requests and member_conversations. The
// consumer_id is a plain column because delivery's entitlement check and
// the consumer's list both filter on it.

// CollectionsStore implements collections.Store over the shared pool.
type CollectionsStore struct {
	pool *pgxpool.Pool
}

// NewCollectionsStore returns a collections store backed by the pool.
func NewCollectionsStore(pool *pgxpool.Pool) *CollectionsStore {
	return &CollectionsStore{pool: pool}
}

// Compile-time check: the store satisfies the collections seam.
var _ collections.Store = (*CollectionsStore)(nil)

const collectionColumns = `
	id, org_id, consumer_id, request_id, deadline, status, items, missing,
	template, created_at, updated_at`

func scanCollection(row pgx.Row) (collections.Collection, error) {
	var c collections.Collection
	var orgID, consumerID string
	var items, missing, template []byte
	var deadline *time.Time
	err := row.Scan(&c.ID, &orgID, &consumerID, &c.RequestID, &deadline,
		&c.Status, &items, &missing, &template, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return collections.Collection{}, err
	}
	c.OrgID = orgID
	c.ConsumerID = consumerID
	c.Deadline = deadline
	if len(items) > 0 {
		if err := json.Unmarshal(items, &c.Items); err != nil {
			return collections.Collection{}, fmt.Errorf("decode items for %s: %w", c.ID, err)
		}
	}
	if len(missing) > 0 {
		if err := json.Unmarshal(missing, &c.Missing); err != nil {
			return collections.Collection{}, fmt.Errorf("decode missing for %s: %w", c.ID, err)
		}
	}
	if len(template) > 0 {
		var att requests.TemplateAttachment
		if err := json.Unmarshal(template, &att); err != nil {
			return collections.Collection{}, fmt.Errorf("decode template for %s: %w", c.ID, err)
		}
		c.Template = &att
	}
	return c, nil
}

// CreateCollection stores the record, filling zero timestamps through the
// pointer, matching the Store contract.
func (s *CollectionsStore) CreateCollection(ctx context.Context, c *collections.Collection) error {
	items, err := encodeJSON(c.Items)
	if err != nil {
		return fmt.Errorf("encode items: %w", err)
	}
	missing, err := encodeJSON(c.Missing)
	if err != nil {
		return fmt.Errorf("encode missing: %w", err)
	}
	// Checked against the concrete pointer: a typed-nil inside any would
	// slip past a nil-interface check and store the JSON literal null.
	var template []byte
	if c.Template != nil {
		if template, err = encodeJSON(c.Template); err != nil {
			return fmt.Errorf("encode template: %w", err)
		}
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = now
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO data_collections
			(id, org_id, consumer_id, request_id, deadline, status, items, missing,
			 template, created_at, updated_at)
		VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9, $10, $11)`,
		c.ID, c.OrgID, c.ConsumerID, c.RequestID, c.Deadline, string(c.Status),
		items, missing, template, c.CreatedAt, c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert data collection: %w", err)
	}
	return nil
}

// CollectionByID loads one record.
func (s *CollectionsStore) CollectionByID(ctx context.Context, id string) (collections.Collection, error) {
	c, err := scanCollection(s.pool.QueryRow(ctx,
		`SELECT `+collectionColumns+` FROM data_collections WHERE id = $1`, id))
	return c, mapCollectionErr(err, id)
}

// CollectionsByOrg lists the org's collections, newest first.
func (s *CollectionsStore) CollectionsByOrg(ctx context.Context, orgID string) ([]collections.Collection, error) {
	return s.list(ctx, `WHERE org_id = $1::uuid`, orgID)
}

// CollectionsByConsumer lists the Data Consumer's collections, newest
// first.
func (s *CollectionsStore) CollectionsByConsumer(ctx context.Context, consumerID string) ([]collections.Collection, error) {
	return s.list(ctx, `WHERE consumer_id = $1::uuid`, consumerID)
}

func (s *CollectionsStore) list(ctx context.Context, where string, arg any) ([]collections.Collection, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+collectionColumns+` FROM data_collections `+where+`
		 ORDER BY created_at DESC, id DESC`, arg)
	if err != nil {
		return nil, fmt.Errorf("list data collections: %w", err)
	}
	defer rows.Close()
	var out []collections.Collection
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return nil, fmt.Errorf("scan data collection: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateCollection rewrites the whole record after a Sync — the
// single-writer whole-record pattern.
func (s *CollectionsStore) UpdateCollection(ctx context.Context, c collections.Collection) error {
	items, err := encodeJSON(c.Items)
	if err != nil {
		return fmt.Errorf("encode items: %w", err)
	}
	missing, err := encodeJSON(c.Missing)
	if err != nil {
		return fmt.Errorf("encode missing: %w", err)
	}
	var template []byte
	if c.Template != nil {
		if template, err = encodeJSON(c.Template); err != nil {
			return fmt.Errorf("encode template: %w", err)
		}
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE data_collections
		SET request_id = $2, deadline = $3, status = $4, items = $5,
		    missing = $6, template = $7, updated_at = now()
		WHERE id = $1`,
		c.ID, c.RequestID, c.Deadline, string(c.Status), items, missing, template)
	if err != nil {
		return fmt.Errorf("update data collection: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", collections.ErrNotFound, c.ID)
	}
	return nil
}

func mapCollectionErr(err error, id string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", collections.ErrNotFound, id)
	}
	return err
}
