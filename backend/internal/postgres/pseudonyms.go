package postgres

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/anonymize"
)

// The Postgres implementation of anonymize.Map (ticket 05; ADR 0007). The
// map is the org-boundary dataset ADR 0005 protects: identifiers are HMAC
// (SHA-256, keyed server-side) before storage — enumeration of candidate
// phones or names cannot recover them, so the table is not a searchable
// dossier. The GDPR erasure surface is one DELETE per org.

// PseudonymMap implements anonymize.Map over the shared pool.
type PseudonymMap struct {
	pool *pgxpool.Pool
	key  []byte
}

// NewPseudonymMap returns a pseudonym map backed by the given pool. The
// key separates hash values from any stored copy of identifiers: it comes
// from PSEUDONYM_HASH_KEY (fallback: the database URL, so dev needs no
// extra setup). Rotating the key re-mints every pseudonym — the map
// degrades to fresh, not broken.
func NewPseudonymMap(pool *pgxpool.Pool, key string) *PseudonymMap {
	if key == "" {
		key = pool.Config().ConnString()
	}
	return &PseudonymMap{pool: pool, key: []byte(key)}
}

// Compile-time check: the store satisfies the anonymize seam.
var _ anonymize.Map = (*PseudonymMap)(nil)

// hashValue maps one raw identifier to its storage key. HMAC, not plain
// SHA-256: a keyed digest cannot be attacked by enumerating candidates
// (Irish mobiles are only ~10^7). The org scoping lives in the row's
// org_id, not the digest.
func (m *PseudonymMap) hashValue(value string) string {
	mac := hmac.New(sha256.New, m.key)
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

// Pseudonym returns the stable pseudonym for one (org, kind, value),
// inserting on first use. A concurrent insert is a read-back, not an error
// — two simultaneous uploads of the same farmer must land on one token.
func (m *PseudonymMap) Pseudonym(ctx context.Context, orgID, kind, value string) (string, error) {
	const q = `
		INSERT INTO pseudonym_maps (org_id, kind, value_hash, pseudonym)
		VALUES ($1, $2, $3, gen_random_uuid()::text)
		ON CONFLICT (org_id, kind, value_hash) DO NOTHING
		RETURNING pseudonym`
	var tok string
	err := m.pool.QueryRow(ctx, q, orgID, kind, m.hashValue(value)).Scan(&tok)
	if err == nil {
		return tok, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("insert pseudonym: %w", err)
	}
	// Conflict: another writer minted first; read the winner.
	err = m.pool.QueryRow(ctx,
		`SELECT pseudonym FROM pseudonym_maps WHERE org_id = $1 AND kind = $2 AND value_hash = $3`,
		orgID, kind, m.hashValue(value)).Scan(&tok)
	if err != nil {
		return "", fmt.Errorf("read pseudonym after conflict: %w", err)
	}
	return tok, nil
}

// EraseOrg drops every entry for one org (the GDPR erasure surface).
func (m *PseudonymMap) EraseOrg(ctx context.Context, orgID string) error {
	_, err := m.pool.Exec(ctx, `DELETE FROM pseudonym_maps WHERE org_id = $1`, orgID)
	if err != nil {
		return fmt.Errorf("erase pseudonyms for org: %w", err)
	}
	return nil
}

// Entries pages the org's map, newest first, with the total count. Raw
// identifiers do not exist here to return — only kind and pseudonym.
func (m *PseudonymMap) Entries(ctx context.Context, orgID string, limit, offset int) ([]anonymize.Entry, int, error) {
	var total int
	if err := m.pool.QueryRow(ctx,
		`SELECT count(*) FROM pseudonym_maps WHERE org_id = $1`, orgID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count pseudonyms: %w", err)
	}
	rows, err := m.pool.Query(ctx, `
		SELECT kind, pseudonym FROM pseudonym_maps WHERE org_id = $1
		ORDER BY created_at DESC, value_hash LIMIT $2 OFFSET $3`,
		orgID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list pseudonyms: %w", err)
	}
	defer rows.Close()

	out := []anonymize.Entry{}
	for rows.Next() {
		var e anonymize.Entry
		if err := rows.Scan(&e.Kind, &e.Pseudonym); err != nil {
			return nil, 0, fmt.Errorf("scan pseudonym: %w", err)
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}
