package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/syaikhipin/entrustdn-final/backend/internal/memoryprov"
)

// Memory Provider endpoints (ticket 14; ADR 0003). The Platform Admin
// registers the external recall services the agent connects to over MCP —
// connection facts only (name + endpoint), never credentials. Providers
// are platform configuration, not Modules: third parties do not author
// platform infrastructure.

// MemoryProvidersDeps carries the collaborator the provider endpoints need.
type MemoryProvidersDeps struct {
	Service *memoryprov.Service
}

// registerMemoryProviderRoutes wires the provider endpoints when
// configured. The membership guards need the membership store, so without
// one the endpoints don't register — mirroring payments.
func (h *Handler) registerMemoryProviderRoutes(deps *MemoryProvidersDeps) {
	if h.store == nil {
		return
	}
	h.memoryProviders = &memoryProviderHandlers{service: deps.Service}
	h.mux.HandleFunc("GET /api/v1/admin/memory-providers", h.handleListMemoryProviders)
	h.mux.HandleFunc("POST /api/v1/admin/memory-providers", h.handleCreateMemoryProvider)
	h.mux.HandleFunc("DELETE /api/v1/admin/memory-providers/{id}", h.handleDeleteMemoryProvider)
}

type memoryProviderHandlers struct {
	service *memoryprov.Service
}

// handleListMemoryProviders serves the configured providers, newest first.
func (h *Handler) handleListMemoryProviders(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	providers, err := h.memoryProviders.service.List(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to load the memory providers")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"memory_providers": providers,
		"max":              memoryprov.MaxProviders,
	})
}

type createMemoryProviderRequest struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
}

// handleCreateMemoryProvider validates and stores one provider.
func (h *Handler) handleCreateMemoryProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	var req createMemoryProviderRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	count, err := h.memoryProviders.service.Count(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to load the memory providers")
		return
	}
	if count >= memoryprov.MaxProviders {
		apiError(w, http.StatusUnprocessableEntity,
			"at most "+strconv.Itoa(memoryprov.MaxProviders)+" memory providers may be configured")
		return
	}
	p, err := h.memoryProviders.service.Create(r.Context(), req.Name, req.Endpoint)
	switch {
	case errors.Is(err, memoryprov.ErrExists):
		apiError(w, http.StatusConflict, "a memory provider with that name already exists")
	case errors.Is(err, memoryprov.ErrInvalid):
		apiError(w, http.StatusUnprocessableEntity, err.Error())
	case err != nil:
		// A store failure is the platform's fault, not the caller's.
		apiError(w, http.StatusInternalServerError, "failed to store the memory provider")
	default:
		writeJSON(w, http.StatusCreated, map[string]any{"memory_provider": p})
	}
}

// handleDeleteMemoryProvider removes one provider by ID.
func (h *Handler) handleDeleteMemoryProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if err := h.memoryProviders.service.Delete(r.Context(), id); err != nil {
		if errors.Is(err, memoryprov.ErrNotFound) {
			apiError(w, http.StatusNotFound, "no such memory provider")
			return
		}
		// Anything else is a store failure — the platform's fault, not a
		// missing provider. (A malformed ID is mapped to ErrNotFound inside
		// the service, so it reads as 404 above.)
		apiError(w, http.StatusInternalServerError, "failed to delete the memory provider")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}
