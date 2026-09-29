package api

import (
	"net/http"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/stats"
)

// Admin stats endpoints (ticket 16): the Platform Admin's dashboard,
// assembled from the platform's real records through the stats.Source
// seam. One read, one document — the page renders it directly.

// statsWindow is how far back the over-time series reach. The status,
// flow, channel, and module counts are whole-history regardless.
const statsWindow = 30 * 24 * time.Hour

// StatsDeps carries the aggregation seam. Nil means the stats endpoint
// does not register.
type StatsDeps struct {
	Source stats.Source
}

// registerStatsRoutes wires the admin stats endpoint when configured.
func (h *Handler) registerStatsRoutes(deps *StatsDeps) {
	h.stats = &statsHandlers{deps: deps}
	h.mux.HandleFunc("GET /api/v1/admin/stats", h.handleAdminStats)
}

// statsHandlers holds the resolved collaborator for the stats route.
type statsHandlers struct {
	deps *StatsDeps
}

// handleAdminStats serves the dashboard snapshot. Admin-only: the numbers
// name accounts, channels, and money — platform-internal business.
func (h *Handler) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	snap, err := h.stats.deps.Source.Snapshot(r.Context(), statsWindow)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to aggregate platform stats")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}
