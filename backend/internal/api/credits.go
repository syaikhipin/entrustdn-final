package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
)

// Credits endpoints (ticket 03): the Platform Admin's grant, adjustment,
// and test-charge paths plus the price book, and every account's own
// balance + entry history view. Every movement posts through the Ledger
// (the credits.Service front door) — nothing writes around it.

// CreditsDeps carries the collaborators the credits endpoints need.
// Rules loads the current price book; SaveRules persists an admin's
// rewritten one. Both are satisfied by the postgres package in production
// and by closures over the in-memory double in tests.
type CreditsDeps struct {
	Store     credits.Store
	Rules     func(ctx context.Context) (credits.PricingRules, error)
	SaveRules func(ctx context.Context, rules credits.PricingRules) error
}

// registerCreditsRoutes wires the credits endpoints when configured.
func (h *Handler) registerCreditsRoutes(deps *CreditsDeps) {
	h.mux.HandleFunc("POST /api/v1/admin/credits/grant", h.handleAdminGrant)
	h.mux.HandleFunc("POST /api/v1/admin/credits/adjust", h.handleAdminAdjust)
	h.mux.HandleFunc("POST /api/v1/admin/credits/charge", h.handleAdminCharge)
	h.mux.HandleFunc("GET /api/v1/admin/pricing", h.handleGetPricing)
	h.mux.HandleFunc("POST /api/v1/admin/pricing", h.handleSavePricing)
	h.mux.HandleFunc("GET /api/v1/me/credits", h.handleMyCredits)
	h.credits = &creditsHandlers{deps: deps, svc: credits.NewService(deps.Store, deps.Rules)}
}

// creditsHandlers holds the resolved collaborators for the credits routes.
type creditsHandlers struct {
	deps *CreditsDeps
	svc  *credits.Service
}

func (h *Handler) creditsSvc() *credits.Service { return h.credits.svc }

// resolveAccount loads the target account for an admin credit operation.
func (h *Handler) resolveAccount(w http.ResponseWriter, r *http.Request, accountID string) (membership.Account, bool) {
	acct, err := h.store.AccountByID(r.Context(), accountID)
	if err != nil {
		apiError(w, http.StatusNotFound, "no such account")
		return membership.Account{}, false
	}
	return acct, true
}

type amountRequest struct {
	AccountID    string `json:"account_id"`
	AmountMicros int64  `json:"amount_micros"`
	Memo         string `json:"memo"`
}

// handleAdminGrant credits an account from the treasury (issue #3: grants
// for pilot participants and exceptions). The amount must be positive; the
// signed path is adjustment.
func (h *Handler) handleAdminGrant(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var req amountRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.AmountMicros <= 0 {
		apiError(w, http.StatusBadRequest, "amount_micros must be positive (adjustment is the signed path)")
		return
	}
	if _, ok := h.resolveAccount(w, r, req.AccountID); !ok {
		return
	}
	mov, err := h.creditsSvc().Grant(r.Context(), credits.AccountScope(req.AccountID), req.AmountMicros, admin.ID, strings.TrimSpace(req.Memo))
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to post the grant")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"movement": mov})
}

// handleAdminAdjust posts a signed adjustment (the admin override path).
// An adjustment that would overdraw the account is refused with 409.
func (h *Handler) handleAdminAdjust(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var req amountRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.AmountMicros == 0 {
		apiError(w, http.StatusBadRequest, "amount_micros must not be zero")
		return
	}
	if _, ok := h.resolveAccount(w, r, req.AccountID); !ok {
		return
	}
	mov, err := h.creditsSvc().Adjust(r.Context(), credits.AccountScope(req.AccountID), req.AmountMicros, admin.ID, strings.TrimSpace(req.Memo))
	if err != nil {
		if errors.Is(err, credits.ErrInsufficientFunds) {
			apiError(w, http.StatusConflict, "this adjustment would overdraw the account")
			return
		}
		apiError(w, http.StatusInternalServerError, "failed to post the adjustment")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"movement": mov})
}

type chargeRequest struct {
	AccountID string `json:"account_id"`
	Inference *struct {
		Model             string `json:"model"`
		InputTokens       int64  `json:"input_tokens"`
		CachedInputTokens int64  `json:"cached_input_tokens"`
		OutputTokens      int64  `json:"output_tokens"`
	} `json:"inference"`
	Data *struct {
		Class string `json:"class"`
		Units int64  `json:"units"`
	} `json:"data"`
	Memo string `json:"memo"`
}

// handleAdminCharge posts a test charge priced automatically by the current
// price book — the demo path for ticket 03, and the seam the agent's
// metering (ticket 07+) and delivery pricing will call through.
func (h *Handler) handleAdminCharge(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var req chargeRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if _, ok := h.resolveAccount(w, r, req.AccountID); !ok {
		return
	}
	if (req.Inference == nil) == (req.Data == nil) {
		apiError(w, http.StatusBadRequest, "give exactly one of inference or data")
		return
	}

	ch := credits.Charge{Scope: credits.AccountScope(req.AccountID), ActorID: admin.ID, Memo: strings.TrimSpace(req.Memo)}
	var (
		mov credits.Movement
		err error
	)
	switch {
	case req.Inference != nil:
		mov, err = h.creditsSvc().ChargeInference(r.Context(), credits.InferenceUsage{
			InputTokens:       req.Inference.InputTokens,
			CachedInputTokens: req.Inference.CachedInputTokens,
			OutputTokens:      req.Inference.OutputTokens,
		}, credits.Charge{Scope: ch.Scope, Model: req.Inference.Model, ActorID: admin.ID, Memo: ch.Memo})
	case req.Data != nil:
		class := credits.DataClass(req.Data.Class)
		if class != credits.DataCached && class != credits.DataUnique {
			apiError(w, http.StatusBadRequest, `data class must be "cached" or "unique"`)
			return
		}
		mov, err = h.creditsSvc().ChargeData(r.Context(), credits.DataUsage{Class: class, Units: req.Data.Units}, ch)
	}
	if err != nil {
		// An unpriced model or class fails loudly with 422 — a pricing
		// misconfiguration must not read as a server fault.
		if errors.Is(err, credits.ErrNoPricingRule) {
			apiError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if errors.Is(err, credits.ErrInsufficientFunds) {
			apiError(w, http.StatusConflict, "the account cannot cover this charge")
			return
		}
		if errors.Is(err, credits.ErrUnbalanced) {
			apiError(w, http.StatusBadRequest, "the usage prices to nothing to post")
			return
		}
		apiError(w, http.StatusInternalServerError, "failed to post the charge")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"movement":       mov,
		"charged_micros": -sumScope(mov.Entries, credits.AccountScope(req.AccountID)),
	})
}

// sumScope sums the entry amounts for one scope within a movement.
func sumScope(entries []credits.Entry, scope credits.Scope) int64 {
	var sum int64
	for _, e := range entries {
		if e.Scope == scope {
			sum += e.AmountMicros
		}
	}
	return sum
}

// handleGetPricing serves the current price book.
func (h *Handler) handleGetPricing(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	rules, err := h.credits.deps.Rules(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to load pricing rules")
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

// handleSavePricing validates and saves a rewritten price book. Future
// charges use it immediately; posted movements are never rewritten.
func (h *Handler) handleSavePricing(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	var rules credits.PricingRules
	if !decodeJSONBody(w, r, &rules) {
		return
	}
	if err := rules.Validate(); err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.credits.deps.SaveRules(r.Context(), rules); err != nil {
		apiError(w, http.StatusInternalServerError, "failed to save pricing rules")
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

// handleMyCredits serves the caller's balance and entry history — the
// consumer-facing spend view (issue #3; orgs see the same for their account).
func (h *Handler) handleMyCredits(w http.ResponseWriter, r *http.Request) {
	_, acct, ok := h.sessionAuth(w, r)
	if !ok {
		return
	}
	scope := credits.AccountScope(acct.ID)
	balance, err := h.credits.deps.Store.Balance(r.Context(), scope)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to derive the balance")
		return
	}
	movs, err := h.credits.deps.Store.MovementsByScope(r.Context(), scope, 100)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to list ledger movements")
		return
	}
	if movs == nil {
		movs = []credits.Movement{} // an empty history is an array, not null
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"balance_micros": balance,
		"movements":      movs,
	})
}
