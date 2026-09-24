// The shared contract round trip: marshal a request envelope, POST /message,
// decode the response envelope, check its type, and hand back the typed,
// validated payload. Every message pair the client speaks (ping, clarify,
// later lifecycle pairs) is one call over this shape.
package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
)

// roundTrip sends env and returns the response envelope validated, with its
// type checked against wantType. The what string names the pair in errors
// ("ping", "clarify").
func (c *Client) roundTrip(ctx context.Context, env contract.Envelope, wantType string, what string) (contract.Envelope, error) {
	body, err := json.Marshal(env)
	if err != nil {
		return contract.Envelope{}, fmt.Errorf("failed to encode %s request: %w", what, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/message", bytes.NewReader(body))
	if err != nil {
		return contract.Envelope{}, fmt.Errorf("failed to build %s request: %w", what, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return contract.Envelope{}, fmt.Errorf("agent unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return contract.Envelope{}, fmt.Errorf("agent returned status %d", resp.StatusCode)
	}

	var replyEnv contract.Envelope
	if err := json.NewDecoder(resp.Body).Decode(&replyEnv); err != nil {
		return contract.Envelope{}, fmt.Errorf("agent response is not a contract envelope: %w", err)
	}
	if err := replyEnv.Validate(); err != nil {
		return contract.Envelope{}, fmt.Errorf("agent response failed validation: %w", err)
	}
	if replyEnv.Type != wantType {
		return contract.Envelope{}, fmt.Errorf("agent responded with %q, want %q", replyEnv.Type, wantType)
	}
	return replyEnv, nil
}
