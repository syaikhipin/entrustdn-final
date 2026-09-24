package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/requests"
)

// The Postgres implementation of requests.Store (ticket 07; ADR 0007: the
// system of record). Messages and matches ride as JSONB arrays — the chat
// path is the single writer and always rewrites the whole record, so the
// document shape never needs row-level querying.

// RequestsStore implements requests.Store over the shared pool.
type RequestsStore struct {
	pool *pgxpool.Pool
}

// NewRequestsStore returns a requests store backed by the given pool.
func NewRequestsStore(pool *pgxpool.Pool) *RequestsStore { return &RequestsStore{pool: pool} }

// Compile-time check: the store satisfies the requests seam.
var _ requests.Store = (*RequestsStore)(nil)

const requestColumns = `
	id, consumer_id, description, format, quality_bar, budget_micros,
	spent_micros, status, messages, matches, created_at, updated_at`

// scanRequest decodes one row. The consumer ID is cast to text — the
// requests package speaks opaque consumer IDs, and account UUIDs are valid
// text IDs.
func (s *RequestsStore) scanRequest(row pgx.Row) (requests.Request, error) {
	var r requests.Request
	var messages, matches []byte
	var consumerID string
	err := row.Scan(&r.ID, &consumerID, &r.Description, &r.Format, &r.QualityBar,
		&r.BudgetMicros, &r.SpentMicros, &r.Status, &messages, &matches,
		&r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return requests.Request{}, err
	}
	r.ConsumerID = consumerID
	if err := json.Unmarshal(messages, &r.Messages); err != nil {
		return requests.Request{}, fmt.Errorf("decode messages for %s: %w", r.ID, err)
	}
	if err := json.Unmarshal(matches, &r.Matches); err != nil {
		return requests.Request{}, fmt.Errorf("decode matches for %s: %w", r.ID, err)
	}
	return r, nil
}

func encodeMessages(msgs []requests.Message) ([]byte, error) {
	if len(msgs) == 0 {
		return []byte("[]"), nil
	}
	return json.Marshal(msgs)
}

func encodeMatches(ms []requests.Match) ([]byte, error) {
	if len(ms) == 0 {
		return []byte("[]"), nil
	}
	return json.Marshal(ms)
}

// CreateRequest inserts the commission; zero timestamps are filled in here
// and written back through the pointer, matching the Store contract.
func (s *RequestsStore) CreateRequest(ctx context.Context, r *requests.Request) error {
	messages, err := encodeMessages(r.Messages)
	if err != nil {
		return fmt.Errorf("encode messages: %w", err)
	}
	matches, err := encodeMatches(r.Matches)
	if err != nil {
		return fmt.Errorf("encode matches: %w", err)
	}
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO data_requests
			(id, consumer_id, description, format, quality_bar, budget_micros,
			 spent_micros, status, messages, matches, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		r.ID, r.ConsumerID, r.Description, r.Format, r.QualityBar,
		r.BudgetMicros, r.SpentMicros, string(r.Status), messages, matches,
		r.CreatedAt, r.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert data request: %w", err)
	}
	return nil
}

// RequestByID loads one record.
func (s *RequestsStore) RequestByID(ctx context.Context, id string) (requests.Request, error) {
	r, err := s.scanRequest(s.pool.QueryRow(ctx,
		`SELECT `+requestColumns+` FROM data_requests WHERE id = $1`, id))
	return r, mapRequestErr(err, id)
}

// RequestsByConsumer lists the consumer's requests, newest first.
func (s *RequestsStore) RequestsByConsumer(ctx context.Context, consumerID string) ([]requests.Request, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+requestColumns+` FROM data_requests WHERE consumer_id = $1::uuid
		ORDER BY created_at DESC, id DESC`, consumerID)
	if err != nil {
		return nil, fmt.Errorf("list requests for consumer: %w", err)
	}
	defer rows.Close()

	var out []requests.Request
	for rows.Next() {
		r, err := s.scanRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("scan request: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateRequest rewrites the whole record after a chat turn: messages,
// matches, spend, status.
func (s *RequestsStore) UpdateRequest(ctx context.Context, r requests.Request) error {
	messages, err := encodeMessages(r.Messages)
	if err != nil {
		return fmt.Errorf("encode messages: %w", err)
	}
	matches, err := encodeMatches(r.Matches)
	if err != nil {
		return fmt.Errorf("encode matches: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE data_requests
		SET description = $2, format = $3, quality_bar = $4, budget_micros = $5,
		    spent_micros = $6, status = $7, messages = $8, matches = $9,
		    updated_at = now()
		WHERE id = $1`,
		r.ID, r.Description, r.Format, r.QualityBar, r.BudgetMicros,
		r.SpentMicros, string(r.Status), messages, matches)
	if err != nil {
		return fmt.Errorf("update data request: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", requests.ErrNotFound, r.ID)
	}
	return nil
}

func mapRequestErr(err error, id string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", requests.ErrNotFound, id)
	}
	return err
}
