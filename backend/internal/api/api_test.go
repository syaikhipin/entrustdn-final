package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/agentclient"
	"github.com/syaikhipin/entrustdn-final/backend/internal/api"
	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
)

// Seam 1: the backend's public HTTP API. These tests drive the /api/v1/status
// endpoint at the HTTP boundary and assert only on observable behavior —
// status codes, JSON shape, and whether the agent round trip is reported
// honestly. The agent is always a contract fake; no test touches a real
// sidecar.

// fakeAgent reports a canned agent_version and echoes the request's nonce.
func fakeAgent(t *testing.T, agentVersion string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqEnv contract.Envelope
		_ = json.NewDecoder(r.Body).Decode(&reqEnv)
		var req contract.PingRequest
		_ = reqEnv.PayloadInto(&req)
		_ = json.NewEncoder(w).Encode(contract.Envelope{
			Version: contract.Version,
			Type:    contract.TypePingResponse,
			SentAt:  time.Now().UTC(),
			Payload: mustJSON(contract.PingResponse{
				Nonce:        req.Nonce,
				Pong:         true,
				AgentVersion: agentVersion,
			}),
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func getStatus(t *testing.T, srv *httptest.Server) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/v1/status")
	if err != nil {
		t.Fatalf("GET /api/v1/status: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("status response is not JSON: %v\nbody: %s", err, body)
	}
	return resp.StatusCode, doc
}

func TestStatusReportsHealthyWhenAgentAnswers(t *testing.T) {
	tests := []struct {
		name         string
		agentVersion string
		wantVersion  string
	}{
		{name: "agent v0.1.0", agentVersion: "0.1.0", wantVersion: "0.1.0"},
		{name: "agent v1.2.3", agentVersion: "1.2.3", wantVersion: "1.2.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := fakeAgent(t, tt.agentVersion)
			srv := httptest.NewServer(api.NewHandler(api.Deps{Agent: agentclient.New(agent.URL), Version: "test-backend"}))
			defer srv.Close()

			code, doc := getStatus(t, srv)
			if code != http.StatusOK {
				t.Fatalf("status code = %d, want %d", code, http.StatusOK)
			}
			if doc["status"] != "ok" {
				t.Errorf("status = %v, want ok", doc["status"])
			}
			if doc["service"] != "thresh-backend" {
				t.Errorf("service = %v, want thresh-backend", doc["service"])
			}
			if doc["version"] != "test-backend" {
				t.Errorf("version = %v, want test-backend (the backend's own version)", doc["version"])
			}
			agentDoc, ok := doc["agent"].(map[string]any)
			if !ok {
				t.Fatalf("agent is not an object: %v", doc["agent"])
			}
			if agentDoc["reachable"] != true {
				t.Errorf("agent.reachable = %v, want true", agentDoc["reachable"])
			}
			if agentDoc["version"] != tt.agentVersion {
				t.Errorf("agent.version = %v, want %v", agentDoc["version"], tt.agentVersion)
			}
		})
	}
}

func TestStatusReportsDegradedWhenAgentDown(t *testing.T) {
	// An httptest server that is closed before the status call: unreachable agent.
	agent := fakeAgent(t, "0.1.0")
	agent.Close()

	srv := httptest.NewServer(api.NewHandler(api.Deps{Agent: agentclient.New(agent.URL), Version: "test-backend"}))
	defer srv.Close()

	code, doc := getStatus(t, srv)
	if code != http.StatusOK {
		t.Fatalf("status code = %d, want %d (degraded, not down)", code, http.StatusOK)
	}
	if doc["status"] != "degraded" {
		t.Errorf("status = %v, want degraded", doc["status"])
	}
	agentDoc, ok := doc["agent"].(map[string]any)
	if !ok {
		t.Fatalf("agent is not an object: %v", doc["agent"])
	}
	if agentDoc["reachable"] != false {
		t.Errorf("agent.reachable = %v, want false", agentDoc["reachable"])
	}
}

func TestStatusAllowsBrowserCrossOriginFetch(t *testing.T) {
	// The web app (localhost:3000) fetches the backend (localhost:8080) from
	// the browser: a cross-origin request the backend must explicitly allow,
	// or the page shows "Failed to fetch". These cases pin the CORS surface.
	tests := []struct {
		name            string
		origin          string
		method          string
		wantAllowed     bool
		wantAllowOrigin string
	}{
		{
			name:            "GET from the dev web origin",
			origin:          "http://localhost:3000",
			method:          http.MethodGet,
			wantAllowed:     true,
			wantAllowOrigin: "http://localhost:3000",
		},
		{
			name:        "GET from an unknown origin",
			origin:      "http://evil.example.com",
			method:      http.MethodGet,
			wantAllowed: true,
			// status is public and readable, but no ACAO header for strangers
			wantAllowOrigin: "",
		},
		{
			name:            "preflight for POST from the dev web origin",
			origin:          "http://localhost:3000",
			method:          http.MethodOptions,
			wantAllowed:     true,
			wantAllowOrigin: "http://localhost:3000",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := fakeAgent(t, "0.1.0")
			srv := httptest.NewServer(api.NewHandler(api.Deps{Agent: agentclient.New(agent.URL), Version: "test-backend"}))
			defer srv.Close()

			req, _ := http.NewRequest(tt.method, srv.URL+"/api/v1/status", nil)
			req.Header.Set("Origin", tt.origin)
			if tt.method == http.MethodOptions {
				req.Header.Set("Access-Control-Request-Method", "POST")
				req.Header.Set("Access-Control-Request-Headers", "content-type")
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s: %v", tt.method, err)
			}
			defer resp.Body.Close()

			got := resp.Header.Get("Access-Control-Allow-Origin")
			if got != tt.wantAllowOrigin {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tt.wantAllowOrigin)
			}
		})
	}
}

func TestStatusIsJSONAndGETOnly(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		wantCode int
	}{
		{name: "GET allowed", method: http.MethodGet, wantCode: http.StatusOK},
		{name: "POST rejected", method: http.MethodPost, wantCode: http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := fakeAgent(t, "0.1.0")
			srv := httptest.NewServer(api.NewHandler(api.Deps{Agent: agentclient.New(agent.URL), Version: "test-backend"}))
			defer srv.Close()

			req, _ := http.NewRequest(tt.method, srv.URL+"/api/v1/status", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s: %v", tt.method, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantCode {
				t.Errorf("%s status = %d, want %d", tt.method, resp.StatusCode, tt.wantCode)
			}
			if tt.wantCode == http.StatusOK && !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
				t.Errorf("Content-Type = %q, want application/json", resp.Header.Get("Content-Type"))
			}
		})
	}
}
