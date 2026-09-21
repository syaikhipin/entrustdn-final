// Package agentclient is the backend's client for the agent sidecar. It speaks
// the Go↔Agent contract (internal/contract) and is the only code in the
// backend that talks to the agent process. Tests drive it against a
// contract-fake agent (Seam 2, backend side).
package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
)

// Client sends typed contract messages to the agent sidecar.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client pointed at the agent's base URL.
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Ping sends a ping.request and validates the ping.response.
func (c *Client) Ping(ctx context.Context, nonce string) (contract.PingResponse, error) {
	env, err := contract.NewPingRequest(nonce, time.Now().UTC())
	if err != nil {
		return contract.PingResponse{}, fmt.Errorf("failed to build ping request: %w", err)
	}
	body, err := json.Marshal(env)
	if err != nil {
		return contract.PingResponse{}, fmt.Errorf("failed to encode ping request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/message", bytes.NewReader(body))
	if err != nil {
		return contract.PingResponse{}, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return contract.PingResponse{}, fmt.Errorf("agent unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return contract.PingResponse{}, fmt.Errorf("agent returned status %d", resp.StatusCode)
	}

	var replyEnv contract.Envelope
	if err := json.NewDecoder(resp.Body).Decode(&replyEnv); err != nil {
		return contract.PingResponse{}, fmt.Errorf("agent response is not a contract envelope: %w", err)
	}
	if err := replyEnv.Validate(); err != nil {
		return contract.PingResponse{}, fmt.Errorf("agent response failed validation: %w", err)
	}
	if replyEnv.Type != contract.TypePingResponse {
		return contract.PingResponse{}, fmt.Errorf("agent responded with %q, want %q", replyEnv.Type, contract.TypePingResponse)
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
