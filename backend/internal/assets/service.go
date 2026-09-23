package assets

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/objectstore"
	"github.com/syaikhipin/entrustdn-final/backend/internal/taxonomy"
)

// Service is the ingest front door (ticket 04): the only path bytes take
// from an upload request to a stored, cataloged Asset. Pipeline, blob
// storage, and metadata records are collaborators behind interfaces, so
// tests drive the service with in-memory doubles.
type Service struct {
	store    Store
	blobs    objectstore.Store
	pipeline *Pipeline
	// categorizer, when set, stamps taxonomy assignments at ingest
	// (ticket 06). Nil means uploads land uncategorized — the org corrects
	// or the admin seeds terms later.
	categorizer Categorizer
}

// Categorizer is the auto-categorization seam (ticket 06): given the
// record's classification text — name, description, provenance — it returns
// the assignments to stamp. *taxonomy.Classifier satisfies it; tests use a
// stub.
type Categorizer interface {
	Classify(ctx context.Context, text string) []taxonomy.Assignment
}

// NewService wires the ingest service: metadata store, blob store, and the
// ordered pipeline stages every upload passes through.
func NewService(store Store, blobs objectstore.Store, pipeline *Pipeline) *Service {
	return &Service{store: store, blobs: blobs, pipeline: pipeline}
}

// WithCategorizer returns the service with auto-categorization at ingest
// (ticket 06). Chain it off NewService before the service is shared.
func (s *Service) WithCategorizer(c Categorizer) *Service {
	s.categorizer = c
	return s
}

// IngestResult is what one upload produces.
type IngestResult struct {
	Asset Asset
}

// Ingest streams an upload through the pipeline into storage, then records
// the metadata. Order matters: bytes land first, record second — a failed
// record write leaves an orphan blob (cleaned by best-effort delete), while
// the reverse would point a record at bytes that never landed.
func (s *Service) Ingest(ctx context.Context, a *Asset, r io.Reader) (IngestResult, error) {
	if err := a.Validate(); err != nil {
		return IngestResult{}, err
	}
	if a.ID == "" {
		id, err := NewID()
		if err != nil {
			return IngestResult{}, err
		}
		a.ID = id
	}
	if a.ObjectKey == "" {
		a.ObjectKey = objectKey(a.OrgID, a.ID)
	}

	piped, stages, err := s.pipeline.RunFor(ctx, a.OrgID, a.Format, r)
	if err != nil {
		return IngestResult{}, err
	}
	// Bound what the server will read: one byte past the cap tells us the
	// file is oversized without draining the rest of the request body.
	n, err := s.blobs.Put(ctx, a.ObjectKey, contentTypeOf(a.Format), io.LimitReader(piped, MaxAssetBytes+1))
	if err != nil {
		return IngestResult{}, fmt.Errorf("assets: store blob: %w", err)
	}
	switch {
	case n == 0:
		// An empty upload is a mistake, not an Asset — nothing stored.
		if delErr := s.blobs.Delete(ctx, a.ObjectKey); delErr != nil {
			err = errors.Join(err, fmt.Errorf("assets: also failed to clean the empty blob: %w", delErr))
		}
		return IngestResult{}, fmt.Errorf("assets: file is empty")
	case n > MaxAssetBytes:
		// Oversized: storage wrote the capped bytes — remove them.
		if delErr := s.blobs.Delete(ctx, a.ObjectKey); delErr != nil {
			err = errors.Join(err, fmt.Errorf("assets: also failed to clean the oversized blob: %w", delErr))
		}
		return IngestResult{}, fmt.Errorf("assets: file exceeds the %d byte limit", MaxAssetBytes)
	}
	a.SizeBytes = n
	a.Pipeline = stages

	// Auto-categorize from the record's own metadata (ticket 06).
	// Classification is best-effort: the seam returns no error, and the
	// deterministic keyword classifier cannot fail — a classifier that could
	// (a remote one, later) belongs behind a seam that can report it, and
	// until then a miss simply leaves the asset for the org to correct.
	if s.categorizer != nil {
		text := classificationText(*a)
		if cats := s.categorizer.Classify(ctx, text); len(cats) > 0 {
			a.Categories = cats
		}
	}

	if err := s.store.CreateAsset(ctx, a); err != nil {
		// The record failed; don't strand the blob behind nothing.
		if delErr := s.blobs.Delete(ctx, a.ObjectKey); delErr != nil {
			err = errors.Join(err, fmt.Errorf("assets: also failed to clean the orphan blob: %w", delErr))
		}
		return IngestResult{}, err
	}
	return IngestResult{Asset: *a}, nil
}

// classificationText concatenates everything the classifier may read: the
// uploader's own metadata. The bytes themselves stay unread — ingest
// streams them to storage without parsing, and re-reading would couple the
// pipeline to content inspection.
func classificationText(a Asset) string {
	prov := a.Provenance.Source + " " + a.Provenance.Notes
	if a.Provenance.CollectedAt != nil {
		prov += " " + a.Provenance.CollectedAt.Format(time.RFC3339)
	}
	return strings.Join([]string{a.Name, a.Description, prov}, " ")
}

// Get returns one Asset record — the entitlement check reads this before
// any byte moves.
func (s *Service) Get(ctx context.Context, id string) (Asset, error) {
	return s.store.AssetByID(ctx, id)
}

// ByOrg lists the org's Assets, newest first.
func (s *Service) ByOrg(ctx context.Context, orgID string) ([]Asset, error) {
	return s.store.AssetsByOrg(ctx, orgID)
}

// ErrForbidden is returned when the caller may not touch the Asset. The API
// layer maps it to 403; the caller's identity decides, never the object.
var ErrForbidden = errors.New("assets: not your asset")

// Open streams the Asset's bytes out of storage after re-checking
// entitlement against the current record — the record could have changed
// hands since the caller last saw the dashboard.
func (s *Service) Open(ctx context.Context, id string, orgID string) (Asset, Blob, error) {
	a, err := s.store.AssetByID(ctx, id)
	if err != nil {
		return Asset{}, Blob{}, err
	}
	if a.OrgID != orgID {
		return Asset{}, Blob{}, ErrForbidden
	}
	blob, err := s.blobs.Get(ctx, a.ObjectKey)
	if err != nil {
		return Asset{}, Blob{}, err
	}
	return a, blob, nil
}

// UpdateMeta rewrites an Asset's editable fields after the entitlement
// check. Size, format, pipeline, and the blob itself are immutable.
func (s *Service) UpdateMeta(ctx context.Context, id, orgID string, name, description string, prov Provenance) (Asset, error) {
	a, err := s.store.AssetByID(ctx, id)
	if err != nil {
		return Asset{}, err
	}
	if a.OrgID != orgID {
		return Asset{}, ErrForbidden
	}
	a.Name = name
	a.Description = description
	a.Provenance = prov
	if err := a.Validate(); err != nil {
		return Asset{}, err
	}
	if err := s.store.UpdateAssetMeta(ctx, a); err != nil {
		return Asset{}, err
	}
	return a, nil
}

// ErrBadCategory wraps a correction the taxonomy cannot honor — unknown
// term, term in another category, or a duplicated category. It keeps the
// underlying taxonomy error for errors.Is; the message is what the API maps
// to 422: a misconfiguration-shaped refusal, not a server fault.
type ErrBadCategory struct{ err error }

func (e ErrBadCategory) Error() string { return e.err.Error() }
func (e ErrBadCategory) Unwrap() error { return e.err }

// IsBadCategory reports whether err is an ErrBadCategory.
func IsBadCategory(err error) bool {
	var bad ErrBadCategory
	return errors.As(err, &bad)
}

// Corrector is the correction seam (ticket 06): the taxonomy-side half of
// SetCategories. *taxonomy.Service satisfies it; the interface keeps this
// service testable without the concrete package wiring.
type Corrector interface {
	ApplyCorrections(ctx context.Context, corrections []taxonomy.Correction) ([]taxonomy.Assignment, error)
}

// SetCategories applies the owning org's corrections (ticket 06): the
// corrector validates the correction set against the current vocabulary and
// returns the stamped assignments, which replace whatever the record
// carried. Corrections are the truth, not a patch — the API sends the full
// desired set.
func (s *Service) SetCategories(ctx context.Context, id, orgID string, corrector Corrector, corrections []taxonomy.Correction) (Asset, error) {
	a, err := s.store.AssetByID(ctx, id)
	if err != nil {
		return Asset{}, err
	}
	if a.OrgID != orgID {
		return Asset{}, ErrForbidden
	}
	assigned, err := corrector.ApplyCorrections(ctx, corrections)
	if err != nil {
		return Asset{}, ErrBadCategory{err: fmt.Errorf("taxonomy refuses the correction: %w", err)}
	}
	a.Categories = assigned
	if err := s.store.UpdateAssetMeta(ctx, a); err != nil {
		return Asset{}, err
	}
	return a, nil
}

// CatalogEntry is one row of the consumer catalog (ticket 06): the asset's
// public face — no owner ID, no object key — with its category facets.
type CatalogEntry struct {
	ID          string
	Name        string
	Description string
	SizeBytes   int64
	Format      string
	Pipeline    []string
	Provenance  Provenance
	Categories  []taxonomy.Assignment
	// CachedPriceMicros is what one download bills at the current price
	// book — a display value for the catalog, not a charge.
	CachedPriceMicros int64
	CreatedAt         time.Time
}

// Catalog lists every org's assets in the consumer's view: category facets
// computed from the live inventory and one entry per asset with its cached
// price. cachedPriceMicros is resolved by the caller from the current price
// book (credits.DataRules.CachedAssetMicrosPerUnit) — the catalog displays
// it, it never charges. The facets always describe the whole catalog, so
// counts stay stable while the consumer filters the entries client-side.
func (s *Service) Catalog(ctx context.Context, cachedPriceMicros int64) ([]CatalogEntry, Facets, error) {
	all, err := s.store.AllAssets(ctx)
	if err != nil {
		return nil, Facets{}, err
	}
	entries := make([]CatalogEntry, 0, len(all))
	for _, a := range all {
		entries = append(entries, CatalogEntry{
			ID:                a.ID,
			Name:              a.Name,
			Description:       a.Description,
			SizeBytes:         a.SizeBytes,
			Format:            a.Format,
			Pipeline:          a.Pipeline,
			Provenance:        a.Provenance,
			Categories:        a.Categories,
			CachedPriceMicros: cachedPriceMicros,
			CreatedAt:         a.CreatedAt,
		})
	}
	return entries, BuildFacets(all), nil
}

// Facets is the catalog's filter vocabulary: per category, the distinct
// values present with their counts.
type Facets map[taxonomy.Category]FacetValues

// FacetValues is one category's values, count-ordered.
type FacetValues []FacetValue

// FacetValue is one facet value and how many catalog assets carry it.
// Value is the slug the consumer filters on; TermID identifies the term.
type FacetValue struct {
	TermID string
	Value  string
	Label  string
	Count  int
}

// BuildFacets computes the facet directory from live assets: every
// assignment's term appears with the number of assets carrying it. Terms
// no asset uses are absent — facets describe the catalog, not the taxonomy.
func BuildFacets(all []Asset) Facets {
	type agg struct {
		value string
		label string
		count int
	}
	perCategory := map[taxonomy.Category]map[string]agg{} // category → termID → agg
	for _, a := range all {
		for _, as := range a.Categories {
			vals := perCategory[as.Category]
			if vals == nil {
				vals = map[string]agg{}
				perCategory[as.Category] = vals
			}
			v := vals[as.TermID]
			v.value = as.Value
			v.label = as.Label
			v.count++
			vals[as.TermID] = v
		}
	}
	facets := make(Facets, len(perCategory))
	for cat, vals := range perCategory {
		out := make(FacetValues, 0, len(vals))
		for termID, v := range vals {
			out = append(out, FacetValue{TermID: termID, Value: v.value, Label: v.label, Count: v.count})
		}
		// Counts descending, value as the stable tiebreak.
		sort.Slice(out, func(i, j int) bool {
			if out[i].Count != out[j].Count {
				return out[i].Count > out[j].Count
			}
			return out[i].Value < out[j].Value
		})
		facets[cat] = out
	}
	return facets
}

// Delete removes the record and the blob after the entitlement check.
// Record first, blob second: a blob that outlives its record is invisible
// to the API (the record is the only door), while a record without its
// blob would 500 on download. DeleteAsset returns the record — the caller
// path needs its ObjectKey for the blob cleanup.
func (s *Service) Delete(ctx context.Context, id, orgID string) error {
	a, err := s.store.AssetByID(ctx, id)
	if err != nil {
		return err
	}
	if a.OrgID != orgID {
		return ErrForbidden
	}
	if _, err := s.store.DeleteAsset(ctx, id); err != nil {
		return err
	}
	if err := s.blobs.Delete(ctx, a.ObjectKey); err != nil {
		return fmt.Errorf("assets: record deleted but the blob delete failed (orphan blob %s): %w", a.ObjectKey, err)
	}
	return nil
}

// Blob is the read side of a stored object (objectstore.Blob, aliased so
// callers of this package need not import objectstore).
type Blob = objectstore.Blob

func objectKey(orgID, assetID string) string {
	return "assets/" + orgID + "/" + assetID
}

// contentTypeOf maps a format tag to a MIME type for storage and download.
func contentTypeOf(format string) string {
	switch format {
	case "csv":
		return "text/csv"
	case "json":
		return "application/json"
	case "pdf":
		return "application/pdf"
	case "txt", "md":
		return "text/plain"
	case "xls":
		return "application/vnd.ms-excel"
	case "xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "zip":
		return "application/zip"
	case "parquet":
		return "application/vnd.apache.parquet"
	default:
		return "application/octet-stream"
	}
}
