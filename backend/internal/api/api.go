// Package api exposes the backend's public HTTP API (Seam 1). Handlers take
// collaborators via constructor injection; tests drive them at the HTTP
// boundary with the agent replaced by a contract fake.
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
)

// Pinger is the slice of the agent client the status endpoint needs.
// *agentclient.Client satisfies it; tests can substitute any fake.
type Pinger interface {
	Ping(ctx context.Context, nonce string) (contract.PingResponse, error)
}

// Handler serves the backend API.
type Handler struct {
	agent   Pinger
	version string
	mux     *http.ServeMux
}

// allowedOrigins lists the browser origins allowed to call this API across
// origins. Dev default covers the Nuxt dev server; production origins get
// added via config as deployment lands.
var allowedOrigins = map[string]bool{
	"http://localhost:3000": true,
}

// NewHandler wires routes and returns the backend's HTTP handler.
func NewHandler(agent Pinger, version string) http.Handler {
	h := &Handler{
		agent:   agent,
		version: version,
		mux:     http.NewServeMux(),
	}
	h.mux.HandleFunc("GET /api/v1/status", h.handleStatus)
	h.mux.HandleFunc("POST /api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
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
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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
