package taxonomy

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// AssignmentCounter is what the Service needs to know whether terms are
// still in use: the asset side reports how many live assignments reference
// each term ID. The assets package satisfies it; tests use a map.
type AssignmentCounter interface {
	// AssignmentsByTerm counts, across all assets, the assignments naming
	// each term ID.
	AssignmentsByTerm(ctx context.Context) (map[string]int, error)
}

// Counters adapts a map to AssignmentCounter — what tests and small
// deployments use.
type Counters map[string]int

// AssignmentsByTerm implements AssignmentCounter.
func (c Counters) AssignmentsByTerm(context.Context) (map[string]int, error) {
	return map[string]int(c), nil
}

// Service is the taxonomy front door: the Platform Admin's CRUD, the
// org-correction path, and the classifier construction. Every mutation
// validates before it reaches the store, so the vocabulary can never hold
// a malformed term.
type Service struct {
	store    Store
	counters AssignmentCounter
}

// NewService wires the taxonomy service over its store and the assignment
// counter that guards deletes.
func NewService(store Store, counters AssignmentCounter) *Service {
	return &Service{store: store, counters: counters}
}

// Create adds a term. An omitted value is derived from the label
// ("County Galway" → "county-galway"); a value the category already has
// returns ErrExists.
func (s *Service) Create(ctx context.Context, t *Term) error {
	t.Value = strings.TrimSpace(t.Value)
	if t.Value == "" {
		t.Value = Slugify(t.Label)
	}
	t.Label = strings.TrimSpace(t.Label)
	if err := t.Validate(); err != nil {
		return err
	}
	if err := s.store.CreateTerm(ctx, t); err != nil {
		return err
	}
	return nil
}

// Terms lists the whole vocabulary, category order then value.
func (s *Service) Terms(ctx context.Context) ([]Term, error) {
	return s.store.Terms(ctx)
}

// TermByID loads one term.
func (s *Service) TermByID(ctx context.Context, id string) (Term, error) {
	return s.store.TermByID(ctx, id)
}

// Update rewrites a term's label and keywords. Category and value are
// identity: an update that changes either is refused — facets stored on
// assets key on them, and rewriting identity would orphan the assignments.
func (s *Service) Update(ctx context.Context, id string, label string, keywords []string) (Term, error) {
	cur, err := s.store.TermByID(ctx, id)
	if err != nil {
		return Term{}, err
	}
	cur.Label = strings.TrimSpace(label)
	cur.Keywords = keywords
	if err := cur.Validate(); err != nil {
		return Term{}, err
	}
	if err := s.store.UpdateTerm(ctx, cur); err != nil {
		return Term{}, err
	}
	return cur, nil
}

// Delete removes a term no asset references. An in-use term is refused with
// ErrInUse: its slug lives on assigned assets, and deleting it would turn
// those assignments into ghosts the catalog cannot render. Corrections come
// first — once no asset carries the term, it deletes cleanly.
func (s *Service) Delete(ctx context.Context, id string) error {
	if _, err := s.store.TermByID(ctx, id); err != nil {
		return err
	}
	usage, err := s.counters.AssignmentsByTerm(ctx)
	if err != nil {
		return fmt.Errorf("taxonomy: count assignments: %w", err)
	}
	if usage[id] > 0 {
		return fmt.Errorf("%w: %d asset(s) still carry it", ErrInUse, usage[id])
	}
	return s.store.DeleteTerm(ctx, id)
}

// Classify stamps assignments from the live vocabulary: terms load from the
// store on every call, so an admin's new term or keyword edit reaches the
// next upload without a restart — the manage-the-vocabulary,
// classify-at-ingest loop stays closed at runtime. The method satisfies the
// assets.Categorizer seam, so the ingest service can hold the taxonomy
// service itself as its classifier.
func (s *Service) Classify(ctx context.Context, text string) []Assignment {
	terms, err := s.store.Terms(ctx)
	if err != nil {
		// Classification is best-effort (the Categorizer seam carries no
		// error): the upload proceeds uncategorized rather than lost.
		log.Printf("taxonomy: load terms for classification: %v", err)
		return nil
	}
	return NewClassifier(terms).Classify(ctx, text)
}

// Correction is the org's word on one category: which term, and (optionally)
// the value/label echoes the client read back — they are re-derived from the
// stored term so a stale client cannot stamp divergent denormalized data.
type Correction struct {
	Category Category
	TermID   string
}

// ApplyCorrections replaces the org-correctable assignments on an asset
// record: the given corrections (source "org", confidence 1) are validated
// against the store, stamped, and handed back for the asset side to persist.
// Empty corrections clear the assignments — an org may withdraw a category
// entirely. Classifier assignments the org does not repeat are replaced too:
// the correction set is the truth, not a patch.
func (s *Service) ApplyCorrections(ctx context.Context, corrections []Correction) ([]Assignment, error) {
	now := time.Now().UTC()
	next := make([]Assignment, 0, len(corrections))
	seen := map[Category]bool{}
	for _, c := range corrections {
		if !c.Category.Valid() {
			return nil, fmt.Errorf("taxonomy: unknown category %q", c.Category)
		}
		if seen[c.Category] {
			return nil, fmt.Errorf("taxonomy: two corrections for category %q", c.Category)
		}
		seen[c.Category] = true
		term, err := s.store.TermByID(ctx, c.TermID)
		if err != nil {
			return nil, fmt.Errorf("taxonomy: term %s: %w", c.TermID, err)
		}
		if term.Category != c.Category {
			return nil, fmt.Errorf("taxonomy: term %s is %s, not %s", c.TermID, term.Category, c.Category)
		}
		next = append(next, Assignment{
			Category:   c.Category,
			TermID:     term.ID,
			Value:      term.Value,
			Label:      term.Label,
			Confidence: 1,
			Source:     SourceOrg,
			AssignedAt: now,
		})
	}
	SortAssignments(next)
	return next, nil
}
