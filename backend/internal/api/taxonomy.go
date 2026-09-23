package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/assets"
	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/taxonomy"
)

// Taxonomy & catalog endpoints (ticket 06). Platform Admins manage the
// vocabulary; uploads auto-categorize at ingest; the owning org corrects
// its assets' categories; Data Consumers browse the catalog with taxonomy
// facets and cached pricing. The terms list is public — facets are
// meaningless without their labels; everything else is role-gated.

// timeFormat is the RFC 3339 stamp every timestamp in these documents uses.
const timeFormat = time.RFC3339

// TaxonomyDeps carries the collaborators the taxonomy endpoints need.
type TaxonomyDeps struct {
	Service *taxonomy.Service
	// Rules loads the price book for the catalog's cached-price display.
	Rules func(ctx context.Context) (credits.PricingRules, error)
}

// registerTaxonomyRoutes wires the taxonomy and catalog endpoints when
// configured.
func (h *Handler) registerTaxonomyRoutes(deps *TaxonomyDeps) {
	h.taxonomy = &taxonomyHandlers{deps: deps}
	// Public: facet labels render before login too.
	h.mux.HandleFunc("GET /api/v1/taxonomy/terms", h.handleListTerms)
	// Admin CRUD.
	h.mux.HandleFunc("POST /api/v1/admin/taxonomy/terms", h.handleCreateTerm)
	h.mux.HandleFunc("PATCH /api/v1/admin/taxonomy/terms/{id}", h.handleUpdateTerm)
	h.mux.HandleFunc("DELETE /api/v1/admin/taxonomy/terms/{id}", h.handleDeleteTerm)
	// Org corrections.
	h.mux.HandleFunc("PUT /api/v1/assets/{id}/categories", h.handleSetCategories)
	// Consumer catalog.
	h.mux.HandleFunc("GET /api/v1/catalog", h.handleCatalog)
}

// taxonomyHandlers holds the resolved collaborators for the taxonomy routes.
type taxonomyHandlers struct {
	deps *TaxonomyDeps
}

func (h *Handler) taxonomySvc() *taxonomy.Service { return h.taxonomy.deps.Service }

// termJSON renders one Term for the HTTP boundary.
func termJSON(t taxonomy.Term) map[string]any {
	keywords := t.Keywords
	if keywords == nil {
		keywords = []string{}
	}
	return map[string]any{
		"id":         t.ID,
		"category":   string(t.Category),
		"value":      t.Value,
		"label":      t.Label,
		"keywords":   keywords,
		"created_at": t.CreatedAt.Format(timeFormat),
	}
}

// handleListTerms serves the whole vocabulary — public, because the facets
// and correction UIs render it and nothing in a category label is private.
func (h *Handler) handleListTerms(w http.ResponseWriter, r *http.Request) {
	terms, err := h.taxonomySvc().Terms(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to list taxonomy terms")
		return
	}
	docs := make([]map[string]any, 0, len(terms))
	for _, t := range terms {
		docs = append(docs, termJSON(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"terms": docs})
}

type createTermRequest struct {
	Category string   `json:"category"`
	Value    string   `json:"value"`
	Label    string   `json:"label"`
	Keywords []string `json:"keywords"`
}

// handleCreateTerm adds a term to the vocabulary (admin).
func (h *Handler) handleCreateTerm(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	var req createTermRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	t := &taxonomy.Term{
		Category: taxonomy.Category(strings.TrimSpace(req.Category)),
		Value:    strings.TrimSpace(req.Value),
		Label:    strings.TrimSpace(req.Label),
		Keywords: cleanKeywords(req.Keywords),
	}
	if err := h.taxonomySvc().Create(r.Context(), t); err != nil {
		switch {
		case errors.Is(err, taxonomy.ErrExists):
			apiError(w, http.StatusConflict, "that category already has a term with this value")
		default:
			apiError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"term": termJSON(*t)})
}

type updateTermRequest struct {
	Label    string   `json:"label"`
	Keywords []string `json:"keywords"`
}

// handleUpdateTerm rewrites a term's label and keywords (admin). Category
// and value are identity — the service refuses changes to them.
func (h *Handler) handleUpdateTerm(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	var req updateTermRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	t, err := h.taxonomySvc().Update(r.Context(), r.PathValue("id"),
		strings.TrimSpace(req.Label), cleanKeywords(req.Keywords))
	switch {
	case errors.Is(err, taxonomy.ErrNotFound):
		apiError(w, http.StatusNotFound, "no such term")
	case err != nil:
		apiError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]any{"term": termJSON(t)})
	}
}

// handleDeleteTerm removes a term nothing references (admin). An in-use
// term is refused with 409: correct the assets first.
func (h *Handler) handleDeleteTerm(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	err := h.taxonomySvc().Delete(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, taxonomy.ErrNotFound):
		apiError(w, http.StatusNotFound, "no such term")
	case errors.Is(err, taxonomy.ErrInUse):
		apiError(w, http.StatusConflict, "assets still carry this term — correct their categories first")
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to delete the term")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	}
}

// cleanKeywords trims, lowercases, and drops empties — keyword matching is
// on tokenized lowercase text.
func cleanKeywords(in []string) []string {
	out := make([]string, 0, len(in))
	for _, k := range in {
		k = strings.ToLower(strings.TrimSpace(k))
		if k != "" {
			out = append(out, k)
		}
	}
	return out
}

type setCategoriesRequest struct {
	Categories []struct {
		Category string `json:"category"`
		TermID   string `json:"term_id"`
	} `json:"categories"`
}

// handleSetCategories applies the owning org's corrections: the full
// desired category set replaces what the record carried (classifier
// suggestions included — a correction is the truth, not a patch).
func (h *Handler) handleSetCategories(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	var req setCategoriesRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	corrections := make([]taxonomy.Correction, 0, len(req.Categories))
	for _, c := range req.Categories {
		corrections = append(corrections, taxonomy.Correction{
			Category: taxonomy.Category(strings.TrimSpace(c.Category)),
			TermID:   strings.TrimSpace(c.TermID),
		})
	}
	a, err := h.assetsSvc().SetCategories(r.Context(), r.PathValue("id"), org.ID,
		h.taxonomySvc(), corrections)
	switch {
	case errors.Is(err, assets.ErrNotFound), errors.Is(err, assets.ErrForbidden):
		apiError(w, http.StatusNotFound, "no such asset")
	case assets.IsBadCategory(err):
		apiError(w, http.StatusUnprocessableEntity, err.Error())
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to set categories")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"asset": assetJSON(a)})
	}
}

// catalogEntryJSON renders one catalog row. No owner ID, no object key —
// the consumer sees the asset's public face (ADR 0006: the bucket layout
// is server-side only).
func catalogEntryJSON(e assets.CatalogEntry) map[string]any {
	cats := make([]map[string]any, 0, len(e.Categories))
	for _, c := range e.Categories {
		cats = append(cats, assignmentJSON(c))
	}
	return map[string]any{
		"id":                  e.ID,
		"name":                e.Name,
		"description":         e.Description,
		"size_bytes":          e.SizeBytes,
		"format":              e.Format,
		"pipeline":            e.Pipeline,
		"provenance":          provenanceJSON(e.Provenance),
		"categories":          cats,
		"cached_price_micros": e.CachedPriceMicros,
		"created_at":          e.CreatedAt.Format(timeFormat),
	}
}

// assignmentJSON renders one taxonomy assignment with its provenance.
func assignmentJSON(a taxonomy.Assignment) map[string]any {
	return map[string]any{
		"category":    string(a.Category),
		"term_id":     a.TermID,
		"value":       a.Value,
		"label":       a.Label,
		"confidence":  a.Confidence,
		"source":      a.Source,
		"assigned_at": a.AssignedAt.Format(timeFormat),
	}
}

// facetsJSON renders the facet directory: category → [{value, label, count}].
func facetsJSON(f assets.Facets) map[string]any {
	out := map[string]any{}
	for cat, vals := range f {
		arr := make([]map[string]any, 0, len(vals))
		for _, v := range vals {
			arr = append(arr, map[string]any{
				"term_id": v.TermID,
				"value":   v.Value,
				"label":   v.Label,
				"count":   v.Count,
			})
		}
		out[string(cat)] = arr
	}
	return out
}

// handleCatalog serves the consumer catalog: every org's assets with
// category facets and the cached price. The catalog describes what buying
// costs at the current price book; entitlement (buying) arrives with
// Requests.
func (h *Handler) handleCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireConsumer(w, r); !ok {
		return
	}
	cachedMicros := int64(0)
	if rules, err := h.taxonomy.deps.Rules(r.Context()); err == nil {
		cachedMicros = rules.Data.CachedAssetMicrosPerUnit
	}
	// An unpriced catalog still renders — the price column shows "—"; a
	// missing price book is a normal pilot state, not a catalog failure.
	entries, facets, err := h.assetsSvc().Catalog(r.Context(), cachedMicros)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to build the catalog")
		return
	}
	docs := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		docs = append(docs, catalogEntryJSON(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"assets": docs,
		"facets": facetsJSON(facets),
	})
}
