// Package api exposes the backend's public HTTP API (Seam 1). Handlers take
// collaborators via constructor injection; tests drive them at the HTTP
// boundary with the agent replaced by a contract fake and the store by an
// in-memory double.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
	"github.com/syaikhipin/entrustdn-final/backend/internal/mailsink"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
)

// Pinger is the slice of the agent client the status endpoint needs.
// *agentclient.Client satisfies it; tests can substitute any fake.
type Pinger interface {
	Ping(ctx context.Context, nonce string) (contract.PingResponse, error)
}

// AgentClient is Pinger plus the clarification turns the request endpoints
// (ticket 07) drive over the contract. *agentclient.Client satisfies it.
type AgentClient interface {
	Pinger
	Clarify(ctx context.Context, req contract.ClarifyRequest) (contract.ClarifyResponse, error)
}

// Deps carries the collaborators the API needs. Non-nil dependencies are
// required by NewHandler; store and mail are mandatory for the membership
// endpoints to register. Credits is optional: when nil, the credits
// endpoints (ticket 03) do not register. Assets is optional: when nil, the
// Data Asset endpoints (ticket 04) do not register. Taxonomy is optional:
// when nil, the taxonomy/catalog endpoints (ticket 06) do not register.
// Requests is optional: when nil, the request endpoints (ticket 07) do not
// register — and it needs Agent to be a full AgentClient, since every chat
// turn round-trips the Go↔Agent contract. Modules is optional: when nil,
// the module registry endpoints (ticket 08) do not register. Roster is
// optional: when nil, the Member roster endpoints (ticket 11) do not
// register. Conversations is optional: when nil, the conversation
// endpoints (ticket 11) do not register — and it needs Roster (a
// conversation opens with a roster Member) and the Agent to speak
// conversation turns.
type Deps struct {
	Agent         Pinger
	Version       string
	Store         membership.Store
	Mail          mailsink.Sink
	Credits       *CreditsDeps
	Assets        *AssetsDeps
	Taxonomy      *TaxonomyDeps
	Requests      *RequestsDeps
	Modules       *ModulesDeps
	Roster        *RosterDeps
	Conversations *ConversationsDeps
	Collections   *CollectionsDeps
	// Earnings is optional: when nil (or when Roster is missing — members
	// name the earnings lines), the org earnings endpoints (ticket 15) do
	// not register.
	Earnings *EarningsDeps
	// Payments is optional: when nil, the payment gateway endpoints
	// (ticket 09) do not register.
	Payments *PaymentsDeps
	// MemoryProviders is optional: when nil, the admin Memory Provider
	// endpoints (ticket 14) do not register.
	MemoryProviders *MemoryProvidersDeps
}

// Handler serves the backend API.
type Handler struct {
	agent         Pinger
	version       string
	store         membership.Store
	mail          mailsink.Sink
	credits       *creditsHandlers
	assets        *assetsHandlers
	taxonomy      *taxonomyHandlers
	requests      *requestsHandlers
	modules       *modulesHandlers
	roster        *rosterHandlers
	conversations *conversationsHandlers
	collections   *collectionsHandlers
	earnings      *earningsHandlers
	payments      *paymentsHandlers
	// memoryProviders is the ticket-14 admin registry; nil means the
	// endpoints are not registered.
	memoryProviders *memoryProviderHandlers
	mux             *http.ServeMux
}

// allowedOrigins lists the browser origins allowed to call this API across
// origins. Dev default covers the Nuxt dev server; production origins get
// added via config as deployment lands.
var allowedOrigins = map[string]bool{
	"http://localhost:3000": true,
}

// NewHandler wires routes and returns the backend's HTTP handler.
func NewHandler(deps Deps) http.Handler {
	h := &Handler{
		agent:   deps.Agent,
		version: deps.Version,
		store:   deps.Store,
		mail:    deps.Mail,
		mux:     http.NewServeMux(),
	}
	h.mux.HandleFunc("GET /api/v1/status", h.handleStatus)
	h.mux.HandleFunc("POST /api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	if h.store != nil {
		h.mux.HandleFunc("POST /api/v1/register", h.handleRegister)
		h.mux.HandleFunc("POST /api/v1/verify", h.handleVerify)
		h.mux.HandleFunc("POST /api/v1/login", h.handleLogin)
		h.mux.HandleFunc("POST /api/v1/logout", h.handleLogout)
		h.mux.HandleFunc("GET /api/v1/me", h.handleMe)
		h.mux.HandleFunc("POST /api/v1/tos/accept", h.handleTOSAccept)
		h.mux.HandleFunc("GET /api/v1/tos/current", h.handleCurrentTOS)
		h.mux.HandleFunc("GET /api/v1/admin/applications", h.handleListApplications)
		h.mux.HandleFunc("POST /api/v1/admin/applications/decide", h.handleApplicationDecision)
		h.mux.HandleFunc("POST /api/v1/admin/tos", h.handlePublishTOS)
	}

	if deps.Credits != nil {
		h.registerCreditsRoutes(deps.Credits)
	}

	if deps.Assets != nil {
		h.registerAssetsRoutes(deps.Assets)
	}

	if deps.Taxonomy != nil {
		h.registerTaxonomyRoutes(deps.Taxonomy)
	}

	if deps.Requests != nil {
		h.registerRequestsRoutes(deps.Requests)
	}

	if deps.Modules != nil {
		h.registerModulesRoutes(deps.Modules)
	}

	if deps.Roster != nil {
		h.registerRosterRoutes(deps.Roster)
	}

	if deps.Conversations != nil {
		h.registerConversationRoutes(deps.Conversations)
	}

	if deps.Collections != nil {
		h.registerCollectionsRoutes(deps.Collections)
	}

	// The earnings view needs the Ledger and the roster (members name the
	// lines); the split percentage rides the credits price book, resolved
	// here once from the credits deps.
	if deps.Earnings != nil && deps.Credits != nil && deps.Roster != nil {
		h.registerEarningsRoutes(deps.Earnings, deps.Credits.Rules)
	}

	if deps.Payments != nil {
		h.registerPaymentsRoutes(deps.Payments)
	}

	if deps.MemoryProviders != nil {
		h.registerMemoryProviderRoutes(deps.MemoryProviders)
	}

	return withCORS(h.mux)
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

type agentStatus struct {
	Reachable bool   `json:"reachable"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
}

type statusResponse struct {
	Status  string      `json:"status"`
	Service string      `json:"service"`
	Version string      `json:"version"`
	Agent   agentStatus `json:"agent"`
}

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	nonce := newNonce()
	agent := agentStatus{}
	resp, err := h.agent.Ping(ctx, nonce)
	switch {
	case err != nil:
		agent.Reachable = false
		agent.Error = err.Error()
	default:
		agent.Reachable = true
		agent.Version = resp.AgentVersion
	}

	status := "ok"
	if !agent.Reachable {
		status = "degraded"
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(statusResponse{
		Status:  status,
		Service: "thresh-backend",
		Version: h.version,
		Agent:   agent,
	}); err != nil {
		log.Printf("api: failed to encode status response: %v", err)
	}
}

// withCORS wraps a handler with the browser cross-origin policy: known
// origins get Access-Control-Allow-Origin on every response, and preflight
// (OPTIONS) requests are answered directly. Unknown origins get no CORS
// headers, so browsers block them.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && allowedOrigins[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}
		if r.Method == http.MethodOptions && origin != "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// newNonce returns a random hex string for ping requests.
func newNonce() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(buf)
}
