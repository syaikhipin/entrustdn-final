package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
	"github.com/syaikhipin/entrustdn-final/backend/internal/modules"
)

// Module registry endpoints (ticket 08). Module Authors upload versioned
// Modules — manifest plus markdown and/or config — of exactly one kind;
// the manifest documents an A2A capability description that is shown, not
// run, and executable content is refused at the door. Modules are private
// to the author by default; the author grants and revokes access; the
// Platform Admin reviews content and promotes Modules system-wide.
// Any active account may be a Module Author — the role may hold any other
// role too — so the upload gate is "signed in, active, TOS current",
// stricter role gates stay on the admin promotion endpoint.

// ModulesDeps carries the collaborators the module endpoints need.
type ModulesDeps struct {
	Service *modules.Service
}

// registerModulesRoutes wires the module registry endpoints.
func (h *Handler) registerModulesRoutes(deps *ModulesDeps) {
	if h.store == nil {
		return // grants resolve accounts through the membership store
	}
	h.modules = &modulesHandlers{svc: deps.Service}
	h.mux.HandleFunc("POST /api/v1/modules", h.handleUploadModule)
	h.mux.HandleFunc("GET /api/v1/modules/mine", h.handleMyModules)
	h.mux.HandleFunc("GET /api/v1/modules/system", h.handleSystemModules)
	h.mux.HandleFunc("GET /api/v1/modules/{$}", h.handleEmptyModulesPath)
	h.mux.HandleFunc("GET /api/v1/modules/{module_id}/versions", h.handleModuleVersions)
	h.mux.HandleFunc("GET /api/v1/modules/{module_id}/versions/{version_id}", h.handleGetModuleVersion)
	h.mux.HandleFunc("POST /api/v1/modules/{module_id}/versions/{version_id}/deprecate", h.handleDeprecateModuleVersion)
	h.mux.HandleFunc("POST /api/v1/modules/{module_id}/versions", h.handlePublishModuleVersion)
	h.mux.HandleFunc("POST /api/v1/modules/{module_id}/grants", h.handleGrantModule)
	h.mux.HandleFunc("DELETE /api/v1/modules/{module_id}/grants/{email}", h.handleRevokeModule)
	h.mux.HandleFunc("GET /api/v1/modules/{module_id}/grants", h.handleListModuleGrants)
	h.mux.HandleFunc("POST /api/v1/admin/modules/{module_id}/promote", h.handlePromoteModule)
}

// modulesHandlers holds the resolved collaborators for the module routes.
type modulesHandlers struct {
	svc *modules.Service
}

// moduleJSON renders one stored version for the HTTP boundary: the
// manifest (identity, kind, version, A2A capability description) and the
// body. Nothing here is executable — it is shown, not run.
func moduleJSON(m modules.Module) map[string]any {
	return map[string]any{
		"id":          m.ID,
		"module_id":   m.ModuleID,
		"name":        m.Name,
		"kind":        string(m.Kind),
		"author_id":   m.AuthorID,
		"version":     m.Version,
		"capability":  m.Capability,
		"content":     m.Content,
		"config":      m.Config,
		"system_wide": m.SystemWide,
		"deprecated":  m.Deprecated,
		"created_at":  m.CreatedAt.Format(timeFormat),
		"updated_at":  m.UpdatedAt.Format(timeFormat),
	}
}

// grantJSON renders one access grant.
func grantJSON(g modules.Grant) map[string]any {
	return map[string]any{
		"module_id":  g.ModuleID,
		"account_id": g.AccountID,
		"granted_at": g.GrantedAt.Format(timeFormat),
	}
}

// requireAuthor resolves the caller and refuses anyone without an active,
// TOS-current account: every signed-in role may be a Module Author (the
// glossary: "may hold any other role").
func (h *Handler) requireAuthor(w http.ResponseWriter, r *http.Request) (membership.Account, bool) {
	sess, acct, ok := h.sessionAuth(w, r)
	if !ok {
		return membership.Account{}, false
	}
	if sess.RequiresReacceptance {
		apiError(w, http.StatusForbidden, "accept the current Terms of Service first (see /api/v1/tos/accept)")
		return membership.Account{}, false
	}
	if acct.Status != membership.StatusActive {
		apiError(w, http.StatusForbidden, "your account is not active")
		return membership.Account{}, false
	}
	return acct, true
}

type uploadModuleRequest struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Version    string `json:"version"`
	Capability string `json:"capability"`
	Content    string `json:"content"`
	Config     string `json:"config"`
}

// handleUploadModule stores the first version of a Module, private to its
// author. Validation errors are 422: the client sent a manifest the
// platform will never accept — wrong kind, malformed fields, or executable
// content.
func (h *Handler) handleUploadModule(w http.ResponseWriter, r *http.Request) {
	author, ok := h.requireAuthor(w, r)
	if !ok {
		return
	}
	var req uploadModuleRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	m, err := h.modules.svc.Upload(r.Context(), author.ID, modules.NewVersion{
		Name:       req.Name,
		Kind:       modules.Kind(req.Kind),
		Version:    req.Version,
		Capability: req.Capability,
		Content:    req.Content,
		Config:     req.Config,
	})
	if err != nil {
		apiError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"module": moduleJSON(m)})
}

// handleMyModules serves the caller's own registry view: their Modules,
// latest version each, newest first — private ones included.
func (h *Handler) handleMyModules(w http.ResponseWriter, r *http.Request) {
	author, ok := h.requireAuthor(w, r)
	if !ok {
		return
	}
	list, err := h.modules.svc.ByAuthor(r.Context(), author.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to list your modules")
		return
	}
	docs := make([]map[string]any, 0, len(list))
	for _, m := range list {
		docs = append(docs, moduleJSON(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"modules": docs})
}

// handleEmptyModulesPath makes GET /api/v1/modules a friendly pointer at
// the two real listings.
func (h *Handler) handleEmptyModulesPath(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"hint": "list your modules at /api/v1/modules/mine, or the system-wide registry at /api/v1/modules/system",
	})
}

// handleSystemModules serves the system-wide registry — every Module the
// Platform Admin has promoted, latest version each. Any signed-in account
// may browse what the platform offers.
func (h *Handler) handleSystemModules(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAuthor(w, r); !ok {
		return
	}
	list, err := h.modules.svc.SystemWide(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to list system-wide modules")
		return
	}
	docs := make([]map[string]any, 0, len(list))
	for _, m := range list {
		docs = append(docs, moduleJSON(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"modules": docs})
}

// handleGetModuleVersion serves one stored version after the visibility
// check — a stranger's private Module reads as 404: existence is not
// disclosed.
func (h *Handler) handleGetModuleVersion(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.requireAuthor(w, r)
	if !ok {
		return
	}
	m, err := h.modules.svc.Get(r.Context(), caller.ID, r.PathValue("module_id"), r.PathValue("version_id"))
	switch {
	case errors.Is(err, modules.ErrNotFound):
		apiError(w, http.StatusNotFound, "no such module version")
	case errors.Is(err, modules.ErrForbidden):
		apiError(w, http.StatusNotFound, "no such module version")
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to load the module version")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"module": moduleJSON(m)})
	}
}

// handleModuleVersions serves a Module's version history, oldest first.
func (h *Handler) handleModuleVersions(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.requireAuthor(w, r)
	if !ok {
		return
	}
	versions, err := h.modules.svc.Versions(r.Context(), caller.ID, r.PathValue("module_id"))
	switch {
	case errors.Is(err, modules.ErrNotFound):
		apiError(w, http.StatusNotFound, "no such module")
	case errors.Is(err, modules.ErrForbidden):
		apiError(w, http.StatusNotFound, "no such module")
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to list the module's versions")
	default:
		docs := make([]map[string]any, 0, len(versions))
		for _, m := range versions {
			docs = append(docs, moduleJSON(m))
		}
		writeJSON(w, http.StatusOK, map[string]any{"versions": docs})
	}
}

type publishVersionRequest struct {
	Version    string `json:"version"`
	Capability string `json:"capability"`
	Content    string `json:"content"`
	Config     string `json:"config"`
}

// handlePublishModuleVersion adds a version to an existing Module (author
// only). Name and kind are identity — a new version cannot rename the
// module or change its kind.
func (h *Handler) handlePublishModuleVersion(w http.ResponseWriter, r *http.Request) {
	author, ok := h.requireAuthor(w, r)
	if !ok {
		return
	}
	var req publishVersionRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	m, err := h.modules.svc.Publish(r.Context(), author.ID, r.PathValue("module_id"), modules.NewVersion{
		Version:    req.Version,
		Capability: req.Capability,
		Content:    req.Content,
		Config:     req.Config,
	})
	switch {
	case errors.Is(err, modules.ErrNotFound):
		apiError(w, http.StatusNotFound, "no such module")
	case errors.Is(err, modules.ErrForbidden):
		apiError(w, http.StatusForbidden, "only the author may publish versions")
	case errors.Is(err, modules.ErrExists):
		apiError(w, http.StatusConflict, err.Error())
	case err != nil:
		apiError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		writeJSON(w, http.StatusCreated, map[string]any{"module": moduleJSON(m)})
	}
}

type grantModuleRequest struct {
	Email string `json:"email"`
}

// handleGrantModule gives one account read access to a private Module.
// The grantee is addressed by email — grants happen between people who
// know each other's address, not their opaque IDs.
func (h *Handler) handleGrantModule(w http.ResponseWriter, r *http.Request) {
	author, ok := h.requireAuthor(w, r)
	if !ok {
		return
	}
	var req grantModuleRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	grantee, err := h.store.AccountByEmail(r.Context(), strings.ToLower(strings.TrimSpace(req.Email)))
	if err != nil {
		apiError(w, http.StatusNotFound, "no account with that email")
		return
	}
	if err := h.modules.svc.Grant(r.Context(), author.ID, r.PathValue("module_id"), grantee.ID); err != nil {
		switch {
		case errors.Is(err, modules.ErrNotFound):
			apiError(w, http.StatusNotFound, "no such module")
		case errors.Is(err, modules.ErrForbidden):
			apiError(w, http.StatusForbidden, "only the author may grant access")
		case errors.Is(err, modules.ErrExists):
			apiError(w, http.StatusConflict, "that account already has access")
		default:
			apiError(w, http.StatusUnprocessableEntity, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"granted": true, "account_id": grantee.ID})
}

// handleRevokeModule removes one account's read access. The path value is
// the grantee's email or account ID — the author's grants list carries
// account IDs, and their address book carries emails; both revoke.
func (h *Handler) handleRevokeModule(w http.ResponseWriter, r *http.Request) {
	author, ok := h.requireAuthor(w, r)
	if !ok {
		return
	}
	who := strings.TrimSpace(r.PathValue("email"))
	grantee, err := h.store.AccountByEmail(r.Context(), strings.ToLower(who))
	if err != nil {
		// Not an email we know — try it as an account ID.
		grantee, err = h.store.AccountByID(r.Context(), who)
	}
	if err != nil {
		apiError(w, http.StatusNotFound, "no account with that email or id")
		return
	}
	if err := h.modules.svc.Revoke(r.Context(), author.ID, r.PathValue("module_id"), grantee.ID); err != nil {
		switch {
		case errors.Is(err, modules.ErrNotFound):
			apiError(w, http.StatusNotFound, "no such grant")
		case errors.Is(err, modules.ErrForbidden):
			apiError(w, http.StatusForbidden, "only the author may revoke access")
		default:
			apiError(w, http.StatusInternalServerError, "failed to revoke access")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
}

// handleListModuleGrants serves a Module's access grants (author only).
func (h *Handler) handleListModuleGrants(w http.ResponseWriter, r *http.Request) {
	author, ok := h.requireAuthor(w, r)
	if !ok {
		return
	}
	grants, err := h.modules.svc.Grants(r.Context(), author.ID, r.PathValue("module_id"))
	switch {
	case errors.Is(err, modules.ErrNotFound):
		apiError(w, http.StatusNotFound, "no such module")
	case errors.Is(err, modules.ErrForbidden):
		apiError(w, http.StatusForbidden, "only the author may inspect grants")
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to list grants")
	default:
		docs := make([]map[string]any, 0, len(grants))
		for _, g := range grants {
			docs = append(docs, grantJSON(g))
		}
		writeJSON(w, http.StatusOK, map[string]any{"grants": docs})
	}
}

type promoteModuleRequest struct {
	SystemWide bool `json:"system_wide"`
}

// handlePromoteModule is the Platform Admin's review verdict: promote a
// reviewed Module system-wide, or demote it again. Promotion is reversible
// while the pilot is small.
func (h *Handler) handlePromoteModule(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	var req promoteModuleRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	m, err := h.modules.svc.Promote(r.Context(), true, r.PathValue("module_id"), req.SystemWide)
	switch {
	case errors.Is(err, modules.ErrNotFound):
		apiError(w, http.StatusNotFound, "no such module")
	case errors.Is(err, modules.ErrForbidden):
		apiError(w, http.StatusForbidden, "this endpoint is for Platform Admins")
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to set the module's reach")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"module": moduleJSON(m)})
	}
}

type deprecateModuleRequest struct {
	Deprecated bool `json:"deprecated"`
}

// handleDeprecateModuleVersion flags one version retired — the author may
// deprecate their own versions, an admin any version at all. The version
// stays in the history, flagged.
func (h *Handler) handleDeprecateModuleVersion(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.requireAuthor(w, r)
	if !ok {
		return
	}
	var req deprecateModuleRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	isAdmin := h.callerIsAdmin(w, r)
	m, err := h.modules.svc.Deprecate(r.Context(), caller.ID, isAdmin, r.PathValue("module_id"), r.PathValue("version_id"), req.Deprecated)
	switch {
	case errors.Is(err, modules.ErrNotFound):
		apiError(w, http.StatusNotFound, "no such module version")
	case errors.Is(err, modules.ErrForbidden):
		apiError(w, http.StatusNotFound, "no such module version")
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to set the version's deprecation")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"module": moduleJSON(m)})
	}
}
