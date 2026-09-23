package api

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/anonymize"
	"github.com/syaikhipin/entrustdn-final/backend/internal/assets"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
)

// Data Asset endpoints (ticket 04). Every byte moves through this backend:
// uploads stream from the request body through the ingest pipeline into
// object storage, downloads stream out after an entitlement check — no
// presigned URLs, no client bucket access (ADR 0006). The anonymization
// stage runs at ingest (ADR 0005); pass-through until ticket 05.

// AssetsDeps carries the collaborators the asset endpoints need.
type AssetsDeps struct {
	Service *assets.Service
	// Pseudonyms, when set, registers the org's pseudonym-map endpoints
	// (ticket 05): list what the map holds and erase it (the GDPR surface).
	Pseudonyms *PseudonymDeps
}

// PseudonymDeps carries the pseudonym-map collaborator (ticket 05).
type PseudonymDeps struct {
	Map anonymize.Map
}

// registerAssetsRoutes wires the asset endpoints when configured.
func (h *Handler) registerAssetsRoutes(deps *AssetsDeps) {
	h.assets = &assetsHandlers{deps: deps}
	h.mux.HandleFunc("POST /api/v1/assets", h.handleUploadAsset)
	h.mux.HandleFunc("GET /api/v1/assets", h.handleListAssets)
	h.mux.HandleFunc("GET /api/v1/assets/{id}", h.handleGetAsset)
	h.mux.HandleFunc("GET /api/v1/assets/{id}/content", h.handleDownloadAsset)
	h.mux.HandleFunc("PATCH /api/v1/assets/{id}", h.handleUpdateAsset)
	h.mux.HandleFunc("DELETE /api/v1/assets/{id}", h.handleDeleteAsset)
	if deps.Pseudonyms != nil {
		h.mux.HandleFunc("GET /api/v1/pseudonyms", h.handleListPseudonyms)
		h.mux.HandleFunc("DELETE /api/v1/pseudonyms", h.handleErasePseudonyms)
	}
}

// assetsHandlers holds the resolved collaborators for the asset routes.
type assetsHandlers struct {
	deps *AssetsDeps
}

func (h *Handler) assetsSvc() *assets.Service { return h.assets.deps.Service }

// requireOrg resolves the caller, refuses non-organizations, unapproved
// orgs, and orgs whose session is still flagged for TOS re-acceptance (the
// same contract-gates-the-surface rule requireAdmin applies). It writes the
// error response and returns false when the caller may not proceed.
func (h *Handler) requireOrg(w http.ResponseWriter, r *http.Request) (membership.Account, bool) {
	sess, acct, ok := h.sessionAuth(w, r)
	if !ok {
		return membership.Account{}, false
	}
	if sess.RequiresReacceptance {
		apiError(w, http.StatusForbidden, "accept the current Terms of Service first (see /api/v1/tos/accept)")
		return membership.Account{}, false
	}
	if acct.Role != membership.RoleFarmerOrganization {
		apiError(w, http.StatusForbidden, "this endpoint is for Farmer Organizations")
		return membership.Account{}, false
	}
	if acct.Status != membership.StatusActive {
		apiError(w, http.StatusForbidden, "your organization's application has not been approved")
		return membership.Account{}, false
	}
	return acct, true
}

// uploadMeta is the multipart text fields that ride alongside the file,
// plus the open file part the handler streams through the pipeline.
type uploadMeta struct {
	Name        string
	Description string
	Source      string
	CollectedAt *time.Time
	Notes       string
	Filename    string
	ContentType string
	filePart    *multipart.Part
	foundFile   bool
}

// parseUploadForm walks the multipart body part by part. Streaming
// (MultipartReader, not ParseMultipartForm) keeps large uploads off the
// heap — the file bytes flow straight into the pipeline (ADR 0006: the
// backend streams, never buffers whole objects). Text fields are bounded
// reads; the file part is returned open, unconsumed.
func parseUploadForm(w http.ResponseWriter, r *http.Request) (uploadMeta, bool) {
	mr, err := r.MultipartReader()
	if err != nil {
		apiError(w, http.StatusBadRequest, "upload must be multipart/form-data with a file part")
		return uploadMeta{}, false
	}
	var form uploadMeta
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			apiError(w, http.StatusBadRequest, "malformed multipart body")
			return uploadMeta{}, false
		}
		if part.FileName() == "" {
			// A text field: bounded read — metadata, not payload.
			val, err := readBounded(part, maxFieldBytes)
			if err != nil {
				apiError(w, http.StatusBadRequest, "unreadable form field")
				return uploadMeta{}, false
			}
			switch part.FormName() {
			case "name":
				form.Name = strings.TrimSpace(val)
			case "description":
				form.Description = strings.TrimSpace(val)
			case "source":
				form.Source = strings.TrimSpace(val)
			case "notes":
				form.Notes = strings.TrimSpace(val)
			case "collected_at":
				if strings.TrimSpace(val) != "" {
					t, err := time.Parse(time.RFC3339, strings.TrimSpace(val))
					if err != nil {
						apiError(w, http.StatusBadRequest, "collected_at must be an RFC 3339 timestamp")
						return uploadMeta{}, false
					}
					form.CollectedAt = &t
				}
			}
			part.Close()
			continue
		}
		// The file part: hand it back open and unconsumed. Walking on is not
		// an option — NextPart would drain this part to find the next
		// boundary — so anything after the file part is ignored; the web
		// client therefore appends its text fields before the file.
		form.Filename = part.FileName()
		form.ContentType = part.Header.Get("Content-Type")
		form.filePart = part
		form.foundFile = true
		return form, true
	}
	if !form.foundFile {
		apiError(w, http.StatusBadRequest, "a file part is required")
		return uploadMeta{}, false
	}
	return form, true
}

// maxFieldBytes bounds each multipart text field. These are metadata, not
// payloads; a bigger "description" is a mistake or an attack.
const maxFieldBytes = 64 << 10

// readBounded reads at most maxFieldBytes bytes from r.
func readBounded(r io.Reader, limit int64) (string, error) {
	limited := io.LimitReader(r, limit+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return "", err
	}
	if int64(len(raw)) > limit {
		return "", fmt.Errorf("form field exceeds %d bytes", limit)
	}
	return string(raw), nil
}

// handleUploadAsset streams an upload through the ingest pipeline and
// records the Asset against the caller's organization.
func (h *Handler) handleUploadAsset(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	if !strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
		apiError(w, http.StatusBadRequest, "upload must be multipart/form-data")
		return
	}
	form, ok := parseUploadForm(w, r)
	if !ok {
		return
	}
	if form.Name == "" {
		form.Name = form.Filename
	}

	asset := &assets.Asset{
		OrgID:       org.ID,
		Name:        form.Name,
		Description: form.Description,
		Format:      assets.DetectFormat(form.Filename, form.ContentType),
		Provenance: assets.Provenance{
			Source:      form.Source,
			CollectedAt: form.CollectedAt,
			Notes:       form.Notes,
		},
	}
	res, err := h.assetsSvc().Ingest(r.Context(), asset, form.filePart)
	defer form.filePart.Close()
	if err != nil {
		// Ingest refusals are caller errors (empty file, oversized, bad
		// metadata); storage failures are also folded here — rare enough at
		// pilot that the 400/500 split would be speculative.
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"asset": assetJSON(res.Asset)})
}

// assetJSON renders one Asset for the HTTP boundary. ObjectKey is
// deliberately absent — the bucket layout is server-side only (ADR 0006).
func assetJSON(a assets.Asset) map[string]any {
	pipeline := a.Pipeline
	if pipeline == nil {
		pipeline = []string{}
	}
	cats := make([]map[string]any, 0, len(a.Categories))
	for _, c := range a.Categories {
		cats = append(cats, assignmentJSON(c))
	}
	return map[string]any{
		"id":          a.ID,
		"name":        a.Name,
		"description": a.Description,
		"size_bytes":  a.SizeBytes,
		"format":      a.Format,
		"pipeline":    pipeline,
		"provenance":  provenanceJSON(a.Provenance),
		"categories":  cats,
		"created_at":  a.CreatedAt.Format(timeFormat),
		"updated_at":  a.UpdatedAt.Format(timeFormat),
	}
}

// provenanceJSON renders the origin metadata; collected_at appears only
// when known.
func provenanceJSON(p assets.Provenance) map[string]any {
	prov := map[string]any{
		"source": p.Source,
		"notes":  p.Notes,
	}
	if p.CollectedAt != nil {
		prov["collected_at"] = p.CollectedAt.Format(timeFormat)
	}
	return prov
}

// handleListAssets shows the org's shared-data dashboard contents.
func (h *Handler) handleListAssets(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	list, err := h.assetsSvc().ByOrg(r.Context(), org.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to list assets")
		return
	}
	docs := make([]map[string]any, 0, len(list))
	for _, a := range list {
		docs = append(docs, assetJSON(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"assets": docs})
}

// assetFromRequest loads one Asset and checks it belongs to the caller —
// the entitlement check every single-asset route runs before anything else.
// A stranger's asset reads as 404: existence is not disclosed. A store
// failure is not a 404 — only a genuinely missing record is.
func (h *Handler) assetFromRequest(w http.ResponseWriter, r *http.Request, org membership.Account) (assets.Asset, bool) {
	a, err := h.assetsSvc().Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, assets.ErrNotFound) {
			apiError(w, http.StatusNotFound, "no such asset")
		} else {
			apiError(w, http.StatusInternalServerError, "failed to load the asset")
		}
		return assets.Asset{}, false
	}
	if a.OrgID != org.ID {
		apiError(w, http.StatusNotFound, "no such asset")
		return assets.Asset{}, false
	}
	return a, true
}

// handleGetAsset serves one Asset's metadata.
func (h *Handler) handleGetAsset(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	a, ok := h.assetFromRequest(w, r, org)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"asset": assetJSON(a)})
}

type updateAssetRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	CollectedAt string `json:"collected_at"`
	Notes       string `json:"notes"`
}

// handleUpdateAsset rewrites an Asset's metadata; bytes, size, format, and
// the pipeline record are immutable after ingest.
func (h *Handler) handleUpdateAsset(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	var req updateAssetRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	var collected *time.Time
	if strings.TrimSpace(req.CollectedAt) != "" {
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(req.CollectedAt))
		if err != nil {
			apiError(w, http.StatusBadRequest, "collected_at must be an RFC 3339 timestamp")
			return
		}
		collected = &t
	}
	a, err := h.assetsSvc().UpdateMeta(r.Context(), r.PathValue("id"), org.ID,
		strings.TrimSpace(req.Name), strings.TrimSpace(req.Description),
		assets.Provenance{Source: strings.TrimSpace(req.Source), CollectedAt: collected, Notes: strings.TrimSpace(req.Notes)})
	switch {
	case errors.Is(err, assets.ErrNotFound), errors.Is(err, assets.ErrForbidden):
		apiError(w, http.StatusNotFound, "no such asset")
	case err != nil:
		apiError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]any{"asset": assetJSON(a)})
	}
}

// handleDeleteAsset removes the record and the blob (ticket checklist:
// delete removes object and record).
func (h *Handler) handleDeleteAsset(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	// Existence + entitlement first, so a stranger's delete is a clean 404
	// and never reaches the service's delete path.
	if _, ok := h.assetFromRequest(w, r, org); !ok {
		return
	}
	if err := h.assetsSvc().Delete(r.Context(), r.PathValue("id"), org.ID); err != nil {
		apiError(w, http.StatusInternalServerError, "failed to delete the asset")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// handleDownloadAsset streams the Asset's bytes through the backend after
// the entitlement check — the delivery half of ADR 0006.
func (h *Handler) handleDownloadAsset(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	a, blob, err := h.assetsSvc().Open(r.Context(), r.PathValue("id"), org.ID)
	if err != nil {
		switch {
		case errors.Is(err, assets.ErrNotFound), errors.Is(err, assets.ErrForbidden):
			apiError(w, http.StatusNotFound, "no such asset")
		default:
			apiError(w, http.StatusInternalServerError, "failed to read the asset")
		}
		return
	}
	defer blob.Body.Close()
	w.Header().Set("Content-Type", blob.ContentType)
	if blob.Size > 0 {
		w.Header().Set("Content-Length", fmt.Sprint(blob.Size))
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.Name}))
	w.WriteHeader(http.StatusOK)
	// Stream, never buffer (ADR 0006).
	_, _ = io.Copy(w, blob.Body)
}

// handleListPseudonyms shows the org's pseudonym map: kinds and pseudonyms
// only — raw identifiers are not stored in readable form and the map is
// never exported (ADR 0005).
func (h *Handler) handleListPseudonyms(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	m := h.assets.deps.Pseudonyms.Map
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			apiError(w, http.StatusBadRequest, "limit must be an integer between 1 and 500")
			return
		}
		limit = n
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			apiError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return
		}
		offset = n
	}
	entries, total, err := m.Entries(r.Context(), org.ID, limit, offset)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to list the pseudonym map")
		return
	}
	docs := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		docs = append(docs, map[string]any{"kind": e.Kind, "pseudonym": e.Pseudonym})
	}
	writeJSON(w, http.StatusOK, map[string]any{"pseudonyms": docs, "total": total})
}

// handleErasePseudonyms wipes the org's whole pseudonym map — the GDPR
// erasure surface (ADR 0005). Every erased identifier re-mints on next use,
// so previously delivered pseudonyms no longer link to anything.
func (h *Handler) handleErasePseudonyms(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	m := h.assets.deps.Pseudonyms.Map
	if err := m.EraseOrg(r.Context(), org.ID); err != nil {
		apiError(w, http.StatusInternalServerError, "failed to erase the pseudonym map")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"erased": true})
}
