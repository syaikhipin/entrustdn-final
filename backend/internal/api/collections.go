package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/collections"
	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
)

// Data Collection endpoints (ticket 12). The Farmer Organization opens a
// Collection from a clarified Request — items materialize over its roster
// Members × the questions — and syncs it: the gathering step reads the
// Member conversations that carry the request ID, runs the quality
// triggers, opens re-asks, and finalizes (completed, or incomplete with
// the missing-data summary). The Data Consumer watches their collections
// and downloads the delivery: anonymized through the delivery cleaner
// (ADR 0005) and charged to their Ledger account as unique data.

// CollectionsDeps carries the collection service, wired by main with the
// real stores, the conversation opener, credits, and the cleaner.
type CollectionsDeps struct {
	Service *collections.Service
}

// collectionsHandlers holds the resolved collaborators for the collection
// routes.
type collectionsHandlers struct {
	svc *collections.Service
}

// registerCollectionsRoutes wires the collection endpoints.
func (h *Handler) registerCollectionsRoutes(deps *CollectionsDeps) {
	h.collections = &collectionsHandlers{svc: deps.Service}
	// The org's side: open, list, inspect, sync.
	h.mux.HandleFunc("POST /api/v1/collections", h.handleCreateCollection)
	h.mux.HandleFunc("GET /api/v1/collections", h.handleListCollections)
	h.mux.HandleFunc("GET /api/v1/collections/{id}", h.handleGetCollection)
	h.mux.HandleFunc("POST /api/v1/collections/{id}/sync", h.handleSyncCollection)
	// The consumer's side: watch and download.
	h.mux.HandleFunc("GET /api/v1/consumer/collections", h.handleListConsumerCollections)
	h.mux.HandleFunc("GET /api/v1/consumer/collections/{id}", h.handleGetConsumerCollection)
	h.mux.HandleFunc("GET /api/v1/consumer/collections/{id}/download", h.handleDownloadDelivery)
}

// collectionJSON renders one collection for its org: items carry
// per-member, per-question status, the gathering rounds, and member names
// — the org's own roster, so identities are its business.
func collectionJSON(c collections.Collection) map[string]any {
	items := make([]map[string]any, 0, len(c.Items))
	for _, it := range c.Items {
		rounds := make([]map[string]any, 0, len(it.Rounds))
		for _, r := range it.Rounds {
			rounds = append(rounds, map[string]any{
				"answer": r.Answer, "ok": r.OK, "reason": r.Reason,
				"at": r.At.Format(timeFormat),
			})
		}
		items = append(items, map[string]any{
			"member_id":    it.MemberID,
			"member_name":  it.MemberName,
			"question":     it.Question,
			"status":       string(it.Status),
			"accepted":     it.Accepted,
			"rounds":       rounds,
			"reasks":       it.Reasks,
			"blocked":      it.Blocked,
			"block_reason": it.BlockReason,
		})
	}
	missing := make([]map[string]any, 0, len(c.Missing))
	for _, m := range c.Missing {
		missing = append(missing, map[string]any{
			"member_name": m.MemberName, "question": m.Question, "reason": m.Reason,
		})
	}
	doc := map[string]any{
		"id":          c.ID,
		"org_id":      c.OrgID,
		"consumer_id": c.ConsumerID,
		"request_id":  c.RequestID,
		"status":      string(c.Status),
		"items":       items,
		"missing":     missing,
		"created_at":  c.CreatedAt.Format(timeFormat),
		"updated_at":  c.UpdatedAt.Format(timeFormat),
	}
	if c.Deadline != nil {
		doc["deadline"] = c.Deadline.Format(timeFormat)
	}
	return doc
}

// consumerCollectionJSON renders one collection for its Data Consumer:
// completeness without identities. Member names and the raw (uncleaned)
// answer rounds stay behind the delivery cleaner — the consumer sees how
// much is gathered, what's missing and why at the item level, never who
// said what before anonymization (ADR 0005).
func consumerCollectionJSON(c collections.Collection) map[string]any {
	doc := collectionJSON(c)
	items := doc["items"].([]map[string]any)
	for _, item := range items {
		delete(item, "member_name")
		delete(item, "rounds")
		// The accepted answer is only raw text too — the cleaned copy rides
		// the delivery, not this view.
		delete(item, "accepted")
	}
	// The missing summary names members; the consumer gets the tally.
	doc["missing_count"] = len(doc["missing"].([]map[string]any))
	doc["missing"] = []map[string]any{}
	return doc
}

type createCollectionRequest struct {
	RequestID string   `json:"request_id"`
	MemberIDs []string `json:"member_ids"`
	Questions []string `json:"questions"`
	Deadline  string   `json:"deadline"` // RFC 3339, optional
}

// handleCreateCollection opens a Collection from a clarified Request over
// the org's roster members.
func (h *Handler) handleCreateCollection(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	var req createCollectionRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	req.RequestID = strings.TrimSpace(req.RequestID)
	if req.RequestID == "" {
		apiError(w, http.StatusBadRequest, "request_id is required")
		return
	}
	n := collections.NewCollection{
		RequestID: req.RequestID,
		MemberIDs: req.MemberIDs,
		Questions: req.Questions,
	}
	if dl := strings.TrimSpace(req.Deadline); dl != "" {
		t, err := time.Parse(time.RFC3339, dl)
		if err != nil {
			apiError(w, http.StatusBadRequest, "deadline must be an RFC 3339 timestamp")
			return
		}
		n.Deadline = &t
	}
	c, err := h.collections.svc.Create(r.Context(), org.ID, n)
	switch {
	case errors.Is(err, collections.ErrNotFound):
		apiError(w, http.StatusNotFound, "no such request")
	case err != nil:
		apiError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusCreated, map[string]any{"collection": collectionJSON(c)})
	}
}

// handleListCollections serves the org's collections, newest first.
func (h *Handler) handleListCollections(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	list, err := h.collections.svc.ByOrg(r.Context(), org.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to list collections")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": collectionsJSON(list)})
}

// handleGetCollection serves one collection — the org's gathering view.
func (h *Handler) handleGetCollection(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	c, err := h.collections.svc.ByID(r.Context(), r.PathValue("id"), org.ID)
	switch {
	case errors.Is(err, collections.ErrNotFound):
		// A stranger's collection reads as 404: existence is not disclosed.
		apiError(w, http.StatusNotFound, "no such collection")
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to load the collection")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"collection": collectionJSON(c)})
	}
}

// handleSyncCollection runs the gathering step: ingest new answers, open
// re-asks on bad data, finalize when the triggers say so.
func (h *Handler) handleSyncCollection(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	c, err := h.collections.svc.Sync(r.Context(), r.PathValue("id"), org.ID)
	switch {
	case errors.Is(err, collections.ErrNotFound):
		apiError(w, http.StatusNotFound, "no such collection")
	case errors.Is(err, collections.ErrClosed):
		// Already finalized: the record stands; hand it back.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":      "this collection is finalized",
			"collection": loadCollectionFor(w, h, r, org.ID, r.PathValue("id")),
		})
	default:
		if err != nil {
			apiError(w, http.StatusInternalServerError, "failed to sync the collection")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"collection": collectionJSON(c)})
	}
}

// loadCollectionFor reloads one collection for the conflict body; on
// failure the body carries no collection and the 409 still stands.
func loadCollectionFor(w http.ResponseWriter, h *Handler, r *http.Request, orgID, id string) map[string]any {
	c, err := h.collections.svc.ByID(r.Context(), id, orgID)
	if err != nil {
		return nil
	}
	return collectionJSON(c)
}

// handleListConsumerCollections serves the Data Consumer's collections —
// what's being gathered in their name, with each one's completeness.
func (h *Handler) handleListConsumerCollections(w http.ResponseWriter, r *http.Request) {
	consumer, ok := h.requireConsumer(w, r)
	if !ok {
		return
	}
	list, err := h.collections.svc.ByConsumer(r.Context(), consumer.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to list collections")
		return
	}
	docs := make([]map[string]any, 0, len(list))
	for _, c := range list {
		docs = append(docs, consumerCollectionJSON(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": docs})
}

// handleGetConsumerCollection serves one collection to its payer.
func (h *Handler) handleGetConsumerCollection(w http.ResponseWriter, r *http.Request) {
	consumer, ok := h.requireConsumer(w, r)
	if !ok {
		return
	}
	c, err := h.collections.svc.ByIDForConsumer(r.Context(), r.PathValue("id"), consumer.ID)
	switch {
	case errors.Is(err, collections.ErrNotFound):
		// Existence is not disclosed to strangers.
		apiError(w, http.StatusNotFound, "no such collection")
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to load the collection")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"collection": consumerCollectionJSON(c)})
	}
}

// handleDownloadDelivery streams the anonymized delivery to the payer:
// cleaned through the delivery cleaner (ADR 0005), charged to the Ledger.
func (h *Handler) handleDownloadDelivery(w http.ResponseWriter, r *http.Request) {
	consumer, ok := h.requireConsumer(w, r)
	if !ok {
		return
	}
	payload, err := h.collections.svc.Deliver(r.Context(), r.PathValue("id"), consumer.ID)
	switch {
	case errors.Is(err, collections.ErrNotFound):
		apiError(w, http.StatusNotFound, "no such collection")
	case errors.Is(err, collections.ErrClosed):
		apiError(w, http.StatusConflict, "this collection is still collecting — nothing to deliver yet")
	case errors.Is(err, credits.ErrInsufficientFunds):
		apiError(w, http.StatusPaymentRequired, "the account cannot cover this delivery")
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to prepare the delivery")
	default:
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition",
			`attachment; filename="collection-`+r.PathValue("id")+`.csv"`)
		w.WriteHeader(http.StatusOK)
		if _, werr := w.Write(payload); werr != nil {
			// The response already started; nothing more can be done.
			return
		}
	}
}

// collectionsJSON renders a list of collections.
func collectionsJSON(list []collections.Collection) []map[string]any {
	docs := make([]map[string]any, 0, len(list))
	for _, c := range list {
		docs = append(docs, collectionJSON(c))
	}
	return docs
}
