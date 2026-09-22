// Package assets implements Data Assets (ticket 04): datasets or documents
// a Farmer Organization uploads as inventory. Blobs live in object storage
// behind the objectstore seam (ADR 0006: server-side only, clients never
// touch the bucket); this package owns the metadata record and the ingest
// pipeline (ADR 0005: anonymization runs as a stage at ingest).
package assets

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MaxAssetBytes caps one upload. The API enforces it while streaming, so an
// oversized file is refused without ever being fully buffered or stored.
const MaxAssetBytes int64 = 256 << 20 // 256 MiB

// ErrNotFound is returned by lookups that find nothing.
var ErrNotFound = errors.New("assets: not found")

// Asset is one uploaded dataset or document. ObjectKey names the blob in
// object storage; it never crosses the HTTP boundary — downloads stream
// through the backend after an entitlement check (ADR 0006).
type Asset struct {
	ID          string
	OrgID       string // the owning Farmer Organization's account ID
	Name        string
	Description string
	SizeBytes   int64
	Format      string // short content tag: "csv", "pdf", …
	// Pipeline lists the ingest stages the bytes passed through, in order —
	// the record of ADR 0005 enforcement ("which cleaning touched this data").
	Pipeline []string
	// Provenance is the verifiable origin metadata (CONTEXT.md): where the
	// data came from and when it was collected.
	Provenance Provenance
	ObjectKey  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Provenance records an Asset's origin. CollectedAt is nil when unknown.
type Provenance struct {
	Source      string     // where the data came from, free text
	CollectedAt *time.Time // when it was gathered, if known
	Notes       string     // anything else a buyer should know
}

// NewID mints an asset ID: 16 crypto/rand bytes, hex. A rand failure is
// returned, never papered over — an ID with less entropy than designed is a
// silent corruption of the catalog's keys.
func NewID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("assets: mint asset id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Validate refuses records that must never be stored: no name or no owning
// organization. Size is not checked here — at ingest it is only known after
// streaming, where Ingest refuses an empty upload itself.
func (a *Asset) Validate() error {
	if strings.TrimSpace(a.Name) == "" {
		return fmt.Errorf("assets: name is required")
	}
	if a.OrgID == "" {
		return fmt.Errorf("assets: owning organization is required")
	}
	return nil
}

// DetectFormat derives the short format tag from the upload's filename and
// declared content type: the extension wins, then the content-type subtype,
// then "bin".
func DetectFormat(filename, contentType string) string {
	if idx := strings.LastIndex(filename, "."); idx >= 0 {
		ext := strings.ToLower(strings.TrimSpace(filename[idx+1:]))
		if ext != "" && len(ext) <= 8 && isPlain(ext) {
			return ext
		}
	}
	if idx := strings.LastIndex(contentType, "/"); idx >= 0 {
		sub := strings.ToLower(strings.TrimSpace(contentType[idx+1:]))
		if sub != "" && sub != "octet-stream" && isPlain(sub) {
			// Strip parameters: "text/csv; charset=utf-8" was already cut by
			// the slash split, but "vnd.ms-excel+zip" style subtypes keep
			// their shape as-is.
			return sub
		}
	}
	return "bin"
}

// isPlain reports whether s is a reasonable short lowercase tag: letters,
// digits, dash, dot, plus.
func isPlain(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.', r == '+':
		default:
			return false
		}
	}
	return s != ""
}

// Store is the persistence seam for Asset metadata. The postgres package
// implements it for the system of record; MemoryStore backs tests.
type Store interface {
	// CreateAsset stores a new record, filling CreatedAt/UpdatedAt when zero.
	// The caller mints ID and uploads the blob first, so a failed record
	// write can be cleaned by deleting the object.
	CreateAsset(ctx context.Context, a *Asset) error
	// AssetByID loads one record.
	AssetByID(ctx context.Context, id string) (Asset, error)
	// AssetsByOrg lists the org's records, newest first.
	AssetsByOrg(ctx context.Context, orgID string) ([]Asset, error)
	// UpdateAssetMeta rewrites name, description, and provenance, bumping
	// UpdatedAt. The blob, size, and format never change after ingest.
	UpdateAssetMeta(ctx context.Context, a Asset) error
	// DeleteAsset removes the record and returns what it was — the caller
	// needs ObjectKey to clean the blob out of storage.
	DeleteAsset(ctx context.Context, id string) (Asset, error)
}

// Compile-time check that MemoryStore satisfies Store.
var _ Store = (*MemoryStore)(nil)

// MemoryStore is the in-memory Store double used by Seam 1 tests.
type MemoryStore struct {
	mu     sync.Mutex
	assets map[string]Asset // by ID
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{assets: map[string]Asset{}}
}

func (m *MemoryStore) CreateAsset(_ context.Context, a *Asset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.ID == "" {
		id, err := NewID()
		if err != nil {
			return err
		}
		a.ID = id
	}
	if _, dup := m.assets[a.ID]; dup {
		return fmt.Errorf("assets: id %s already exists", a.ID)
	}
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	if a.UpdatedAt.IsZero() {
		a.UpdatedAt = now
	}
	m.assets[a.ID] = *a
	return nil
}

func (m *MemoryStore) AssetByID(_ context.Context, id string) (Asset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.assets[id]
	if !ok {
		return Asset{}, ErrNotFound
	}
	return a, nil
}

func (m *MemoryStore) AssetsByOrg(_ context.Context, orgID string) ([]Asset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Asset
	for _, a := range m.assets {
		if a.OrgID == orgID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (m *MemoryStore) UpdateAssetMeta(_ context.Context, a Asset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.assets[a.ID]
	if !ok {
		return ErrNotFound
	}
	cur.Name = a.Name
	cur.Description = a.Description
	cur.Provenance = a.Provenance
	cur.UpdatedAt = time.Now().UTC()
	m.assets[a.ID] = cur
	return nil
}

func (m *MemoryStore) DeleteAsset(_ context.Context, id string) (Asset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.assets[id]
	if !ok {
		return Asset{}, ErrNotFound
	}
	delete(m.assets, id)
	return a, nil
}
