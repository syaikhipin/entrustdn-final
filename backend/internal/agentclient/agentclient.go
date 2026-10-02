// Package agentclient is the backend's client for the agent sidecar. It speaks
// the Go↔Agent contract (internal/contract) and is the only code in the
// backend that talks to the agent process. Tests drive it against a
// contract-fake agent (Seam 2, backend side).
package agentclient

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
)

// Client sends typed contract messages to the agent sidecar.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client pointed at the agent's base URL. The HTTP timeout
// defaults to 120s and rides THRESH_AGENT_TIMEOUT_S: a clarify or
// conversation turn through a real model gateway takes 10–30s (often more
// with long threads), and the old 10s default killed every slow turn at
// the charging step ("agent could not be reached").
func New(baseURL string) *Client {
	timeout := 120 * time.Second
	if v := os.Getenv("THRESH_AGENT_TIMEOUT_S"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			timeout = time.Duration(parsed) * time.Second
		}
	}
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: timeout},
	}
}

// Ping sends a ping.request and validates the ping.response.
func (c *Client) Ping(ctx context.Context, nonce string) (contract.PingResponse, error) {
	env, err := contract.NewPingRequest(nonce, time.Now().UTC())
	if err != nil {
		return contract.PingResponse{}, fmt.Errorf("failed to build ping request: %w", err)
	}
	replyEnv, err := c.roundTrip(ctx, env, contract.TypePingResponse, "ping")
	if err != nil {
		return contract.PingResponse{}, err
	}

	var reply contract.PingResponse
	if err := replyEnv.PayloadInto(&reply); err != nil {
		return contract.PingResponse{}, fmt.Errorf("agent response payload invalid: %w", err)
	}
	if err := reply.Validate(); err != nil {
		return contract.PingResponse{}, fmt.Errorf("agent response invalid: %w", err)
	}
	if reply.Nonce != nonce {
		return reply, fmt.Errorf("agent echoed nonce %q, want %q: nonce mismatch", reply.Nonce, nonce)
	}
	return reply, nil
}

// Clarify sends one request.clarify.request and validates the
// request.clarify.response — the ticket 07 round trip. The reply must name
// the request it answers and carry usable metered usage; anything else is
// refused before the caller can charge on it.
func (c *Client) Clarify(ctx context.Context, req contract.ClarifyRequest) (contract.ClarifyResponse, error) {
	env, err := contract.NewClarifyRequest(req, time.Now().UTC())
	if err != nil {
		return contract.ClarifyResponse{}, fmt.Errorf("failed to build clarify request: %w", err)
	}
	replyEnv, err := c.roundTrip(ctx, env, contract.TypeClarifyResponse, "clarify")
	if err != nil {
		return contract.ClarifyResponse{}, err
	}

	var reply contract.ClarifyResponse
	if err := replyEnv.PayloadInto(&reply); err != nil {
		return contract.ClarifyResponse{}, fmt.Errorf("agent response payload invalid: %w", err)
	}
	if err := reply.Validate(); err != nil {
		return contract.ClarifyResponse{}, fmt.Errorf("agent response invalid: %w", err)
	}
	if reply.RequestID != req.RequestID {
		return reply, fmt.Errorf("agent answered request %q, want %q", reply.RequestID, req.RequestID)
	}
	return reply, nil
}
