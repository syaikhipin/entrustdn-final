package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/conversations"
)

// The Postgres implementation of conversations.Store (ticket 11; ADR 0007).
// The thread, answers, and question list ride as JSONB arrays — the reply
// path is the single writer and always rewrites the whole record, the same
// pattern as data_requests. The resumable token is UNIQUE: it is the
// Member's capability and names at most one conversation.

// ConversationsStore implements conversations.Store over the shared pool.
type ConversationsStore struct {
	pool *pgxpool.Pool
}

// NewConversationsStore returns a conversations store backed by the pool.
func NewConversationsStore(pool *pgxpool.Pool) *ConversationsStore {
	return &ConversationsStore{pool: pool}
}

// Compile-time check: the store satisfies the conversations seam.
var _ conversations.Store = (*ConversationsStore)(nil)

const conversationColumns = `
	id, org_id, request_id, member_id, member_name, contact, topic,
	questions, resume_token, thread, answers, status, created_at, updated_at`

func scanConversation(row pgx.Row) (conversations.Conversation, error) {
	var c conversations.Conversation
	var orgID string
	var questions, thread, answers []byte
	err := row.Scan(&c.ID, &orgID, &c.RequestID, &c.MemberID, &c.MemberName,
		&c.Contact, &c.Topic, &questions, &c.ResumeToken, &thread, &answers,
		&c.Status, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return conversations.Conversation{}, err
	}
	c.OrgID = orgID
	if err := json.Unmarshal(questions, &c.Questions); err != nil {
		return conversations.Conversation{}, fmt.Errorf("decode questions for %s: %w", c.ID, err)
	}
	if err := json.Unmarshal(thread, &c.Thread); err != nil {
		return conversations.Conversation{}, fmt.Errorf("decode thread for %s: %w", c.ID, err)
	}
	if err := json.Unmarshal(answers, &c.Answers); err != nil {
		return conversations.Conversation{}, fmt.Errorf("decode answers for %s: %w", c.ID, err)
	}
	return c, nil
}

func encodeJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// CreateConversation stores the record, filling zero timestamps through
// the pointer, matching the Store contract.
func (s *ConversationsStore) CreateConversation(ctx context.Context, c *conversations.Conversation) error {
	questions, err := encodeJSON(c.Questions)
	if err != nil {
		return fmt.Errorf("encode questions: %w", err)
	}
	thread, err := encodeJSON(c.Thread)
	if err != nil {
		return fmt.Errorf("encode thread: %w", err)
	}
	answers, err := encodeJSON(c.Answers)
	if err != nil {
		return fmt.Errorf("encode answers: %w", err)
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = now
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO member_conversations
			(id, org_id, request_id, member_id, member_name, contact, topic,
			 questions, resume_token, thread, answers, status, created_at, updated_at)
		VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		c.ID, c.OrgID, c.RequestID, c.MemberID, c.MemberName, c.Contact,
		c.Topic, questions, c.ResumeToken, thread, answers, string(c.Status),
		c.CreatedAt, c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert member conversation: %w", err)
	}
	return nil
}

// ConversationByID loads one record.
func (s *ConversationsStore) ConversationByID(ctx context.Context, id string) (conversations.Conversation, error) {
	c, err := scanConversation(s.pool.QueryRow(ctx,
		`SELECT `+conversationColumns+` FROM member_conversations WHERE id = $1`, id))
	return c, mapConversationErr(err, id)
}

// ConversationByToken resolves the Member's resumable token.
func (s *ConversationsStore) ConversationByToken(ctx context.Context, token string) (conversations.Conversation, error) {
	c, err := scanConversation(s.pool.QueryRow(ctx,
		`SELECT `+conversationColumns+` FROM member_conversations WHERE resume_token = $1`, token))
	return c, mapConversationErr(err, "token")
}

// ConversationsByOrg lists the org's conversations, newest first.
func (s *ConversationsStore) ConversationsByOrg(ctx context.Context, orgID string) ([]conversations.Conversation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+conversationColumns+` FROM member_conversations WHERE org_id = $1::uuid
		ORDER BY created_at DESC, id DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list member conversations: %w", err)
	}
	defer rows.Close()
	var out []conversations.Conversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan member conversation: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateConversation rewrites the whole record after a reply turn.
func (s *ConversationsStore) UpdateConversation(ctx context.Context, c conversations.Conversation) error {
	questions, err := encodeJSON(c.Questions)
	if err != nil {
		return fmt.Errorf("encode questions: %w", err)
	}
	thread, err := encodeJSON(c.Thread)
	if err != nil {
		return fmt.Errorf("encode thread: %w", err)
	}
	answers, err := encodeJSON(c.Answers)
	if err != nil {
		return fmt.Errorf("encode answers: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE member_conversations
		SET request_id = $2, member_id = $3, member_name = $4, contact = $5,
		    topic = $6, questions = $7, thread = $8, answers = $9,
		    status = $10, updated_at = now()
		WHERE id = $1`,
		c.ID, c.RequestID, c.MemberID, c.MemberName, c.Contact, c.Topic,
		questions, thread, answers, string(c.Status))
	if err != nil {
		return fmt.Errorf("update member conversation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", conversations.ErrNotFound, c.ID)
	}
	return nil
}

func mapConversationErr(err error, id string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", conversations.ErrNotFound, id)
	}
	return err
}
