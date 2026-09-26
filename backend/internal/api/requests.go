package api

import (
	"errors"
	"net/http"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/modules"
	"github.com/syaikhipin/entrustdn-final/backend/internal/requests"
)

// Request endpoints (ticket 07): a Data Consumer creates a Request — data
// wanted, format, quality bar, budget in Credits — and clarifies it with
// the Agent through a web-chat conversation. Every turn round-trips the
// Go↔Agent contract, is metered, and is charged to the Ledger against the
// Request's budget; the budget cannot be silently exceeded. The consumer's
// status view shows the conversation, the reported existing-Asset matches,
// and the spend.

// RequestsDeps carries the collaborators the request endpoints need.
// Catalog hands the agent the live catalog snapshot each turn (the
// assets-service catalog, mapped to contract shapes); it is a func so tests
// can stub the inventory. Modules is the registry front door — nil when the
// module routes are unconfigured, in which case template/skill attachment
// is refused.
type RequestsDeps struct {
	Store   requests.Store
	Catalog requests.CatalogSource
	Modules *modules.Service
}

// registerRequestsRoutes wires the request endpoints when configured.
// Requires the credits endpoints to be configured too — metering posts
// through the same Ledger — and the Agent dependency to speak clarify turns
// (a full AgentClient, not just a Pinger); otherwise the routes don't
// register.
func (h *Handler) registerRequestsRoutes(deps *RequestsDeps) {
	if h.credits == nil || h.agent == nil {
		return
	}
	ac, ok := h.agent.(AgentClient)
	if !ok {
		return
	}
	credSvc := credits.NewService(h.credits.deps.Store, h.credits.deps.Rules)
	// ac already satisfies requests.Clarifier structurally — the backend
	// speaks to the agent only through the contract, no adapter needed.
	h.requests = &requestsHandlers{svc: requests.NewService(deps.Store, ac, deps.Catalog, credSvc), mods: deps.Modules}
	if deps.Modules != nil {
		// Ticket 13: the registry authorizes every template/skill attach —
		// private modules reach only their author and grantees.
		requests.SetModuleSource(h.requests.svc, deps.Modules)
	}
	h.mux.HandleFunc("POST /api/v1/requests", h.handleCreateRequest)
	h.mux.HandleFunc("GET /api/v1/requests", h.handleListRequests)
	h.mux.HandleFunc("GET /api/v1/requests/{id}", h.handleGetRequest)
	h.mux.HandleFunc("POST /api/v1/requests/{id}/chat", h.handleRequestChat)
	h.mux.HandleFunc("PUT /api/v1/requests/{id}/template", h.handleAttachTemplate)
	h.mux.HandleFunc("DELETE /api/v1/requests/{id}/template", h.handleDetachTemplate)
	h.mux.HandleFunc("POST /api/v1/requests/{id}/skills", h.handleAttachSkill)
	h.mux.HandleFunc("DELETE /api/v1/requests/{id}/skills/{module_id}", h.handleDetachSkill)
}

// requestsHandlers holds the resolved collaborators for the request routes.
// mods is nil when the module routes are unconfigured.
type requestsHandlers struct {
	svc  *requests.Service
	mods *modules.Service
}

// requestJSON renders one Request for the HTTP boundary: the commission,
// the conversation, the reported matches, and the budget math.
func requestJSON(r requests.Request) map[string]any {
	msgs := make([]map[string]any, 0, len(r.Messages))
	for _, m := range r.Messages {
		msgs = append(msgs, map[string]any{
			"role":       m.Role,
			"body":       m.Body,
			"created_at": m.CreatedAt.Format(timeFormat),
		})
	}
	matches := make([]map[string]any, 0, len(r.Matches))
	for _, m := range r.Matches {
		matches = append(matches, map[string]any{
			"asset_id":    m.AssetID,
			"name":        m.Name,
			"reason":      m.Reason,
			"reported_at": m.ReportedAt.Format(timeFormat),
		})
	}
	doc := map[string]any{
		"id":            r.ID,
		"description":   r.Description,
		"format":        r.Format,
		"quality_bar":   r.QualityBar,
		"budget_micros": r.BudgetMicros,
		"spent_micros":  r.SpentMicros,
		"status":        string(r.Status),
		"messages":      msgs,
		"matches":       matches,
		"skills":        r.Skills,
		"created_at":    r.CreatedAt.Format(timeFormat),
		"updated_at":    r.UpdatedAt.Format(timeFormat),
	}
	if r.Template != nil {
		// The attachment's identity rides; the parsed spec stays internal —
		// the collection carries the snapshot that actually runs.
		doc["template"] = map[string]any{"module_id": r.Template.ModuleID}
	}
	return doc
}

type createRequestRequest struct {
	Description  string `json:"description"`
	Format       string `json:"format"`
	QualityBar   string `json:"quality_bar"`
	BudgetMicros int64  `json:"budget_micros"`
}

// handleCreateRequest records the Data Consumer's commission.
func (h *Handler) handleCreateRequest(w http.ResponseWriter, r *http.Request) {
	consumer, ok := h.requireConsumer(w, r)
	if !ok {
		return
	}
	var req createRequestRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	created, err := h.requests.svc.Create(r.Context(), consumer.ID, requests.NewRequest{
		Description:  req.Description,
		Format:       req.Format,
		QualityBar:   req.QualityBar,
		BudgetMicros: req.BudgetMicros,
	})
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"request": requestJSON(created)})
}

// handleListRequests serves the consumer's requests, newest first.
func (h *Handler) handleListRequests(w http.ResponseWriter, r *http.Request) {
	consumer, ok := h.requireConsumer(w, r)
	if !ok {
		return
	}
	list, err := h.requests.svc.ByConsumer(r.Context(), consumer.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to list requests")
		return
	}
	docs := make([]map[string]any, 0, len(list))
	for _, req := range list {
		docs = append(docs, requestJSON(req))
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": docs})
}

// handleGetRequest serves one request — the consumer's status view.
func (h *Handler) handleGetRequest(w http.ResponseWriter, r *http.Request) {
	consumer, ok := h.requireConsumer(w, r)
	if !ok {
		return
	}
	req, err := h.requests.svc.ByID(r.Context(), r.PathValue("id"), consumer.ID)
	switch {
	case errors.Is(err, requests.ErrNotFound), errors.Is(err, requests.ErrForbidden):
		// A stranger's request reads as 404: existence is not disclosed.
		apiError(w, http.StatusNotFound, "no such request")
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to load the request")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"request": requestJSON(req)})
	}
}

type chatRequest struct {
	Message string `json:"message"`
}

// handleRequestChat runs one clarification turn: the consumer's message
// goes to the agent over the contract; the reply, the metered charge, and
// the budget guard's verdict come back as the updated request. A refused
// turn is 402 with the reason and the request's new status — surfaced,
// never silent.
func (h *Handler) handleRequestChat(w http.ResponseWriter, r *http.Request) {
	consumer, ok := h.requireConsumer(w, r)
	if !ok {
		return
	}
	var req chatRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	turn, err := h.requests.svc.Chat(r.Context(), r.PathValue("id"), consumer.ID, req.Message)
	switch {
	case errors.Is(err, requests.ErrNotFound), errors.Is(err, requests.ErrForbidden):
		apiError(w, http.StatusNotFound, "no such request")
	case errors.Is(err, requests.ErrClosed):
		apiError(w, http.StatusConflict, "this request is no longer open for clarification")
	case errors.Is(err, requests.ErrBudgetExceeded), errors.Is(err, credits.ErrInsufficientFunds):
		writeJSON(w, http.StatusPaymentRequired, map[string]any{
			"error":   err.Error(),
			"request": requestJSON(turn.Request),
		})
	case errors.Is(err, credits.ErrNoPricingRule):
		apiError(w, http.StatusUnprocessableEntity, err.Error())
	case err != nil:
		apiError(w, http.StatusBadGateway, "the agent could not be reached for this turn")
	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"request":        requestJSON(turn.Request),
			"reply":          turn.Reply.Reply,
			"clarified":      turn.Reply.Clarified,
			"charged_micros": turn.ChargedMicros,
		})
	}
}

type attachTemplateRequest struct {
	ModuleID string `json:"module_id"`
}

// handleAttachTemplate pins a Process Template onto the Request (ticket
// 13): its questions, follow-up rules, and triggers drive the collection
// conversation instead of the defaults. The registry authorizes first — a
// private module reaches only its author and grantees, and a stranger's
// refusal reads as 404 so existence is not disclosed. Attach only while the
// request is still clarifying.
func (h *Handler) handleAttachTemplate(w http.ResponseWriter, r *http.Request) {
	consumer, ok := h.requireConsumer(w, r)
	if !ok {
		return
	}
	var req attachTemplateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if h.requests.mods == nil {
		// No registry configured: nothing may attach (surfaced, never a
		// nil-registry panic).
		apiError(w, http.StatusNotFound, "no such request or module")
		return
	}
	// The registry parses and authorizes in one step. Kinds of failure map
	// separately: a stranger's module reads as 404 so existence is not
	// disclosed; an unusable config or wrong kind is the caller's fault
	// (422, message from the registry); a store failure is 5xx with the
	// internals kept back.
	spec, terr := h.requests.mods.Template(r.Context(), consumer.ID, req.ModuleID)
	if terr != nil {
		switch {
		case errors.Is(terr, modules.ErrNotFound), errors.Is(terr, modules.ErrForbidden):
			apiError(w, http.StatusNotFound, "no such request or module")
		case errors.Is(terr, modules.ErrUnusableTemplate):
			apiError(w, http.StatusUnprocessableEntity, terr.Error())
		default:
			apiError(w, http.StatusInternalServerError, "failed to load the template")
		}
		return
	}
	updated, err := h.requests.svc.AttachTemplate(r.Context(), r.PathValue("id"), consumer.ID, req.ModuleID, spec)
	switch {
	case errors.Is(err, requests.ErrNotFound), errors.Is(err, requests.ErrForbidden):
		apiError(w, http.StatusNotFound, "no such request or module")
	case errors.Is(err, requests.ErrClosed):
		apiError(w, http.StatusConflict, "this request is no longer open for clarification")
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to attach the template")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"request": requestJSON(updated)})
	}
}

// handleDetachTemplate removes the attached template; the request falls
// back to caller-supplied questions at collection time.
func (h *Handler) handleDetachTemplate(w http.ResponseWriter, r *http.Request) {
	consumer, ok := h.requireConsumer(w, r)
	if !ok {
		return
	}
	updated, err := h.requests.svc.DetachTemplate(r.Context(), r.PathValue("id"), consumer.ID)
	switch {
	case errors.Is(err, requests.ErrNotFound), errors.Is(err, requests.ErrForbidden):
		apiError(w, http.StatusNotFound, "no such request")
	case errors.Is(err, requests.ErrClosed):
		apiError(w, http.StatusConflict, "this request is no longer open for clarification")
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to detach the template")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"request": requestJSON(updated)})
	}
}

type attachSkillRequest struct {
	ModuleID string `json:"module_id"`
}

// handleAttachSkill adds an Agent Skill to the request's context set
// (ticket 13): its markdown loads into the agent's context on every clarify
// turn. Idempotent; capped; authorized through the registry like templates.
func (h *Handler) handleAttachSkill(w http.ResponseWriter, r *http.Request) {
	consumer, ok := h.requireConsumer(w, r)
	if !ok {
		return
	}
	var req attachSkillRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	updated, err := h.requests.svc.AttachSkill(r.Context(), r.PathValue("id"), consumer.ID, req.ModuleID)
	switch {
	case errors.Is(err, requests.ErrNotFound), errors.Is(err, requests.ErrForbidden):
		apiError(w, http.StatusNotFound, "no such request or module")
	case errors.Is(err, requests.ErrClosed):
		apiError(w, http.StatusConflict, "this request is no longer open for clarification")
	case errors.Is(err, requests.ErrTooManySkills):
		apiError(w, http.StatusUnprocessableEntity, err.Error())
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to attach the skill")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"request": requestJSON(updated)})
	}
}

// handleDetachSkill removes one skill from the request's set; detaching a
// skill that is not attached is not an error.
func (h *Handler) handleDetachSkill(w http.ResponseWriter, r *http.Request) {
	consumer, ok := h.requireConsumer(w, r)
	if !ok {
		return
	}
	updated, err := h.requests.svc.DetachSkill(r.Context(), r.PathValue("id"), consumer.ID, r.PathValue("module_id"))
	switch {
	case errors.Is(err, requests.ErrNotFound), errors.Is(err, requests.ErrForbidden):
		apiError(w, http.StatusNotFound, "no such request")
	case errors.Is(err, requests.ErrClosed):
		apiError(w, http.StatusConflict, "this request is no longer open for clarification")
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to detach the skill")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"request": requestJSON(updated)})
	}
}
