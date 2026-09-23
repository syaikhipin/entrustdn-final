// Package taxonomy implements the categorization taxonomy (ticket 06): the
// Platform Admin's controlled vocabulary — crop, region, growth stage,
// intervention, outcome, data type — the classifier that tags Data Assets
// at ingest, and the corrections an owning organization can make. Terms are
// the facets the catalog searches on; assignments stamp provenance onto
// every categorized data item (who or what assigned it, when, how sure).
package taxonomy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ErrNotFound is returned by lookups that find nothing.
var ErrNotFound = errors.New("taxonomy: not found")

// ErrExists is returned when a term's (category, value) pair is taken.
var ErrExists = errors.New("taxonomy: term already exists")

// ErrInUse is returned when deleting a term that assets still carry — the
// assignments would keep the value alive as a ghost facet.
var ErrInUse = errors.New("taxonomy: term is assigned to assets")

// Category is one axis of the taxonomy. The six axes are the spec's: they
// are fixed vocabulary structure, not admin-configurable.
type Category string

const (
	CategoryCrop         Category = "crop"
	CategoryRegion       Category = "region"
	CategoryGrowthStage  Category = "growth_stage"
	CategoryIntervention Category = "intervention"
	CategoryOutcome      Category = "outcome"
	CategoryDataType     Category = "data_type"
)

// Categories lists every axis in display order.
var Categories = []Category{
	CategoryCrop,
	CategoryRegion,
	CategoryGrowthStage,
	CategoryIntervention,
	CategoryOutcome,
	CategoryDataType,
}

// Valid reports whether c is one of the six taxonomy axes.
func (c Category) Valid() bool {
	for _, known := range Categories {
		if c == known {
			return true
		}
	}
	return false
}

// Assignment sources. The classifier tags at ingest; an org correction is a
// human's word and always carries Confidence 1.
const (
	SourceClassifier = "classifier"
	SourceOrg        = "org"
)

// Term is one taxonomy entry: "dairy" in category crop, "co-galway" in
// region. Value is the stable slug facets filter on; Label is what humans
// read. Keywords feed the classifier (see classify.go).
type Term struct {
	ID        string
	Category  Category
	Value     string // slug: "dairy", "co-galway"
	Label     string // display: "Dairy", "County Galway"
	Keywords  []string
	CreatedAt time.Time
}

// Assignment is one category stamp on a data item: which term, how
// confident, and where it came from — the provenance of the categorization
// itself (CONTEXT.md: provenance is a trust dimension).
type Assignment struct {
	Category   Category  `json:"category"`
	TermID     string    `json:"term_id"`
	Value      string    `json:"value"`
	Label      string    `json:"label"`
	Confidence float64   `json:"confidence"`
	Source     string    `json:"source"` // classifier | org
	AssignedAt time.Time `json:"assigned_at"`
}

// NewID mints a term ID: 16 crypto/rand bytes, hex. A rand failure is
// returned, never papered over — an ID with less entropy than designed is a
// silent corruption of the taxonomy's keys.
func NewID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("taxonomy: mint term id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

var valuePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Validate refuses terms that would corrupt the vocabulary: unknown
// category, a value that is not a slug, or no label to render.
func (t *Term) Validate() error {
	if !t.Category.Valid() {
		return fmt.Errorf("taxonomy: unknown category %q", t.Category)
	}
	if !valuePattern.MatchString(t.Value) {
		return fmt.Errorf("taxonomy: value %q must be a lowercase slug (a-z, 0-9, dashes)", t.Value)
	}
	if strings.TrimSpace(t.Label) == "" {
		return fmt.Errorf("taxonomy: label is required")
	}
	return nil
}

// Slugify turns a free-text label into a slug: lowercase, non-alphanumerics
// to dashes, collapsed. An admin may omit the value and let the label
// provide it ("County Galway" → "county-galway").
func Slugify(label string) string {
	var b strings.Builder
	lastDash := true // collapses leading dashes too
	for _, r := range strings.ToLower(label) {
		switch {
		case unicode.IsLetter(r) && r < unicode.MaxLatin1 || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// DefaultKeywords derives classifier keywords for a term with none: the
// words of the label plus the value's dash-split parts. Seed terms carry
// curated keywords; admin-created ones fall back to this.
func (t Term) DefaultKeywords() []string {
	if len(t.Keywords) > 0 {
		return append([]string(nil), t.Keywords...)
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, w := range strings.FieldsFunc(t.Label, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		add(w)
	}
	for _, part := range strings.Split(t.Value, "-") {
		add(part)
	}
	return out
}

// Store is the persistence seam for taxonomy terms. The postgres package
// implements it for the system of record; MemoryStore backs tests.
type Store interface {
	// CreateTerm stores a new term, minting ID and CreatedAt when zero.
	// A taken (category, value) pair returns ErrExists.
	CreateTerm(ctx context.Context, t *Term) error
	// TermByID loads one term.
	TermByID(ctx context.Context, id string) (Term, error)
	// Terms lists every term, ordered by category then value.
	Terms(ctx context.Context) ([]Term, error)
	// UpdateTerm rewrites the editable fields (label, keywords). Category
	// and value are identity — they never change.
	UpdateTerm(ctx context.Context, t Term) error
	// DeleteTerm removes the term.
	DeleteTerm(ctx context.Context, id string) error
}

// Compile-time check that MemoryStore satisfies Store.
var _ Store = (*MemoryStore)(nil)

// MemoryStore is the in-memory Store double used by Seam 1 tests.
type MemoryStore struct {
	mu    sync.Mutex
	terms map[string]Term // by ID
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{terms: map[string]Term{}}
}

func (m *MemoryStore) CreateTerm(_ context.Context, t *Term) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t.ID == "" {
		id, err := NewID()
		if err != nil {
			return err
		}
		t.ID = id
	}
	for _, existing := range m.terms {
		if existing.Category == t.Category && existing.Value == t.Value {
			return fmt.Errorf("%w: %s/%s", ErrExists, t.Category, t.Value)
		}
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	m.terms[t.ID] = *t
	return nil
}

func (m *MemoryStore) TermByID(_ context.Context, id string) (Term, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.terms[id]
	if !ok {
		return Term{}, ErrNotFound
	}
	return t, nil
}

func (m *MemoryStore) Terms(_ context.Context) ([]Term, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Term, 0, len(m.terms))
	for _, t := range m.terms {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Value < out[j].Value
	})
	return out, nil
}

func (m *MemoryStore) UpdateTerm(_ context.Context, t Term) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.terms[t.ID]
	if !ok {
		return ErrNotFound
	}
	cur.Label = t.Label
	cur.Keywords = t.Keywords
	m.terms[t.ID] = cur
	return nil
}

func (m *MemoryStore) DeleteTerm(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.terms[id]; !ok {
		return ErrNotFound
	}
	delete(m.terms, id)
	return nil
}
