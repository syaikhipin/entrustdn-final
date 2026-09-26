package api

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/syaikhipin/entrustdn-final/backend/internal/payments"
)

// Payment gateway endpoints (ticket 09; ADR 0004). The Platform Admin
// configures the gateway — provider, credentials, exchange rate — as
// server-side platform config: credentials are stored server-side, shown
// masked, and never a Module. Data Consumers initiate top-ups and see
// their history; the gateway's callbacks settle them through the
// signature-verified webhook. Money-in only: Revenue Shares stay internal
// (ADR 0002).

// PaymentsDeps carries the collaborators the payment endpoints need.
type PaymentsDeps struct {
	Service *payments.Service
	// PublicBaseURL prefixes the gateway's success/cancel redirects; the
	// config endpoint requires it so sessions can send the payer home.
	PublicBaseURL string
}

// registerPaymentsRoutes wires the payment endpoints when configured.
// Membership guards (requireAdmin / requireConsumer) need the membership
// store, so without one the endpoints don't register — mirroring modules.
func (h *Handler) registerPaymentsRoutes(deps *PaymentsDeps) {
	if h.store == nil {
		return
	}
	h.payments = &paymentsHandlers{service: deps.Service, publicBaseURL: deps.PublicBaseURL}
	h.mux.HandleFunc("GET /api/v1/admin/payments/config", h.handleGetGatewayConfig)
	h.mux.HandleFunc("POST /api/v1/admin/payments/config", h.handleSaveGatewayConfig)
	h.mux.HandleFunc("POST /api/v1/me/topups", h.handleInitiateTopUp)
	h.mux.HandleFunc("GET /api/v1/me/topups", h.handleMyTopUps)
	h.mux.HandleFunc("POST /api/v1/payments/callback/{provider}", h.handlePaymentCallback)
}

// paymentsHandlers holds the resolved collaborators for payment routes.
type paymentsHandlers struct {
	service       *payments.Service
	publicBaseURL string
}

// gatewayConfigResponse renders the config with credentials masked: the
// admin sees which credentials are set, never their values.
type gatewayConfigResponse struct {
	Provider      string `json:"provider"`
	APIKeySet     bool   `json:"api_key_set"`
	SecretSet     bool   `json:"webhook_secret_set"`
	Currency      string `json:"currency"`
	MicrosPerCent int64  `json:"micros_per_cent"`
	ReturnBaseURL string `json:"return_base_url"`
	Enabled       bool   `json:"enabled"`
}

func maskGatewayConfig(cfg payments.Config) gatewayConfigResponse {
	return gatewayConfigResponse{
		Provider:      cfg.Provider,
		APIKeySet:     cfg.APIKey != "",
		SecretSet:     cfg.WebhookSecret != "",
		Currency:      cfg.Currency,
		MicrosPerCent: cfg.MicrosPerCent,
		ReturnBaseURL: cfg.ReturnBaseURL,
		Enabled:       cfg.Enabled(),
	}
}

// handleGetGatewayConfig serves the masked gateway configuration.
func (h *Handler) handleGetGatewayConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	cfg, err := h.payments.service.LoadConfigForAdmin(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to load the gateway configuration")
		return
	}
	writeJSON(w, http.StatusOK, maskGatewayConfig(cfg))
}

type gatewayConfigRequest struct {
	Provider      string `json:"provider"`
	APIKey        string `json:"api_key"`
	WebhookSecret string `json:"webhook_secret"`
	Currency      string `json:"currency"`
	MicrosPerCent int64  `json:"micros_per_cent"`
}

// handleSaveGatewayConfig validates and saves the gateway configuration.
// Blank api_key / webhook_secret fields keep the stored values — rotating
// one credential must not require re-typing the other. A stored config is
// then enabled immediately; saving a blank provider disables top-ups.
func (h *Handler) handleSaveGatewayConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	var req gatewayConfigRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	current, err := h.payments.service.LoadConfigForAdmin(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to load the gateway configuration")
		return
	}
	if req.APIKey == "" {
		req.APIKey = current.APIKey // blank keeps the stored credential
	}
	if req.WebhookSecret == "" {
		req.WebhookSecret = current.WebhookSecret
	}
	cfg := payments.Config{
		Provider:      strings.ToLower(strings.TrimSpace(req.Provider)),
		APIKey:        strings.TrimSpace(req.APIKey),
		WebhookSecret: strings.TrimSpace(req.WebhookSecret),
		Currency:      strings.ToLower(strings.TrimSpace(req.Currency)),
		MicrosPerCent: req.MicrosPerCent,
		ReturnBaseURL: h.payments.publicBaseURL,
	}
	if cfg.Provider == "" {
		// Explicit disable: clear the row.
		if err := h.payments.service.Disable(r.Context()); err != nil {
			apiError(w, http.StatusInternalServerError, "failed to disable the gateway")
			return
		}
		writeJSON(w, http.StatusOK, maskGatewayConfig(payments.Config{}))
		return
	}
	if err := cfg.Validate(); err != nil {
		apiError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := h.payments.service.SaveConfig(r.Context(), cfg); err != nil {
		apiError(w, http.StatusInternalServerError, "failed to save the gateway configuration")
		return
	}
	writeJSON(w, http.StatusOK, maskGatewayConfig(cfg))
}

type initiateTopUpRequest struct {
	// AmountMinor is the charge in the gateway's smallest currency unit
	// (cents for EUR): 2500 = €25.00.
	AmountMinor int64 `json:"amount_minor"`
}

// handleInitiateTopUp opens a top-up session for the signed-in Data
// Consumer: the response carries the hosted payment URL and what the
// settlement will credit. Nothing is credited at initiation.
func (h *Handler) handleInitiateTopUp(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.requireConsumer(w, r)
	if !ok {
		return
	}
	var req initiateTopUpRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	tu, err := h.payments.service.Initiate(r.Context(), acct.ID, req.AmountMinor, "")
	switch {
	case errors.Is(err, payments.ErrDisabled):
		apiError(w, http.StatusConflict, "top-ups are not enabled on this platform")
	case err != nil:
		apiError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		writeJSON(w, http.StatusCreated, map[string]any{"top_up": tu})
	}
}

// handleMyTopUps serves the caller's top-up history, newest first.
func (h *Handler) handleMyTopUps(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.requireConsumer(w, r)
	if !ok {
		return
	}
	tops, err := h.payments.service.History(r.Context(), acct.ID, 50)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to load your top-ups")
		return
	}
	if tops == nil {
		tops = []payments.TopUp{} // an empty history is an array, not null
	}
	writeJSON(w, http.StatusOK, map[string]any{"top_ups": tops})
}

// handlePaymentCallback is the gateway webhook: verify the signature
// before trusting anything, then settle. Forged callbacks get 400 with no
// state change; verified settlements 200; Stripe's verified-but-irrelevant
// events also 200 so it stops retrying them.
func (h *Handler) handlePaymentCallback(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if provider != h.payments.service.ProviderName() {
		apiError(w, http.StatusNotFound, "no such payment provider")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		apiError(w, http.StatusBadRequest, "unreadable callback body")
		return
	}
	_, result, err := h.payments.service.SettleCallback(r.Context(), r.Header, body)
	switch {
	case errors.Is(err, payments.ErrCallbackRejected):
		apiError(w, http.StatusBadRequest, "callback verification failed")
	case errors.Is(err, payments.ErrNotFound):
		apiError(w, http.StatusNotFound, "no top-up for this reference")
	case payments.IsUnhandledEvent(err):
		writeJSON(w, http.StatusOK, map[string]any{"handled": false})
	case errors.Is(err, payments.ErrAmountMismatch), errors.Is(err, payments.ErrAlreadyTerminal):
		// A verified callback we refuse: 409 so the operator's monitoring
		// sees it, but the top-up's state answers idempotent retries.
		apiError(w, http.StatusConflict, err.Error())
	case err != nil:
		apiError(w, http.StatusInternalServerError, "failed to settle the callback")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"result": result})
	}
}
