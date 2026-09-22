package anonymize

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
)

// Map is the pseudonym-map seam: it hands out stable pseudonyms and erases
// them. Same (org, kind, value) maps to the same pseudonym for as long as
// the entry lives; EraseOrg removes an org's entries so the next use
// re-mints (the GDPR erasure surface, ADR 0005). The map lives in-platform
// only and is never exported: Entries exposes kind and pseudonym, never the
// raw identifier — the map is not a searchable dossier.
type Map interface {
	Pseudonym(ctx context.Context, orgID, kind, value string) (string, error)
	EraseOrg(ctx context.Context, orgID string) error
	// Entries pages the org's map (newest first) and reports the total.
	Entries(ctx context.Context, orgID string, limit, offset int) ([]Entry, int, error)
}

// Entry is one row of the pseudonym map as any viewer sees it: the
// identifier's kind and the opaque pseudonym standing in for it. Raw values
// are absent by construction.
type Entry struct {
	Kind      string
	Pseudonym string
}

// MemoryMap is the in-memory Map implementation (tests, dev).
type MemoryMap struct {
	mu    sync.Mutex
	byOrg map[string]*memOrg
}

// memOrg keeps one org's entries in mint order (newest = highest index).
type memOrg struct {
	entries []Entry
	// index maps kind\x00value → position in entries.
	index map[string]int
}

// NewMemoryMap returns an empty in-memory map.
func NewMemoryMap() *MemoryMap {
	return &MemoryMap{byOrg: map[string]*memOrg{}}
}

// mapKey namespaces one identifier within an org.
func mapKey(kind, value string) string { return kind + "\x00" + value }

// Pseudonym returns the stable pseudonym for one identifier, minting it on
// first use: 16 crypto/rand bytes, hex.
func (m *MemoryMap) Pseudonym(_ context.Context, orgID, kind, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("anonymize: refuse to map an empty identifier")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	org, ok := m.byOrg[orgID]
	if !ok {
		org = &memOrg{index: map[string]int{}}
		m.byOrg[orgID] = org
	}
	key := mapKey(kind, value)
	if i, ok := org.index[key]; ok {
		return org.entries[i].Pseudonym, nil
	}
	tok, err := newPseudonymToken()
	if err != nil {
		return "", err
	}
	org.index[key] = len(org.entries)
	org.entries = append(org.entries, Entry{Kind: kind, Pseudonym: tok})
	return tok, nil
}

// EraseOrg drops every entry for one org.
func (m *MemoryMap) EraseOrg(_ context.Context, orgID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byOrg, orgID)
	return nil
}

// Entries pages the org's map, newest first, plus the total count.
func (m *MemoryMap) Entries(_ context.Context, orgID string, limit, offset int) ([]Entry, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	org := m.byOrg[orgID]
	if org == nil {
		return []Entry{}, 0, nil
	}
	total := len(org.entries)
	out := []Entry{}
	// Walk the slice back to front: highest index = newest.
	for i := total - 1 - offset; i >= 0 && len(out) < limit; i-- {
		out = append(out, org.entries[i])
	}
	return out, total, nil
}

// newPseudonymToken mints one opaque pseudonym. A rand failure is returned,
// never papered over: reusing a token across identifiers would silently
// merge two farmers' data.
func newPseudonymToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("anonymize: mint pseudonym: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
