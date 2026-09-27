package modules_test

import (
	"strings"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/modules"
)

// Ticket 14: a Connector Module's config is the connection fact the agent
// needs to reach an external source — endpoint, transport, query. Parsed
// strictly like a template spec: unknown fields are refused, because the
// request service hands the parsed result to the agent as a contract field
// and the agent must be able to trust it. Connection facts only — never
// credentials.

func validConnectorJSON() string {
	return `{
		"endpoint": "http://connector.local:9090/mcp",
		"transport": "mcp",
		"query": "search_reports"
	}`
}

func TestParseConnectorSpecAcceptsAWellFormedSpec(t *testing.T) {
	spec, err := modules.ParseConnectorSpec([]byte(validConnectorJSON()))
	if err != nil {
		t.Fatalf("ParseConnectorSpec: %v", err)
	}
	if spec.Endpoint != "http://connector.local:9090/mcp" {
		t.Errorf("endpoint = %q", spec.Endpoint)
	}
	if spec.Transport != modules.TransportMCP {
		t.Errorf("transport = %q, want %q", spec.Transport, modules.TransportMCP)
	}
	if spec.Query != "search_reports" {
		t.Errorf("query = %q", spec.Query)
	}
}

func TestParseConnectorSpecDefaultsTransportToMCP(t *testing.T) {
	// The contract's Python side defaults transport to mcp when absent;
	// the Go parser speaks the same language.
	spec, err := modules.ParseConnectorSpec([]byte(`{"endpoint": "https://api.local/mcp"}`))
	if err != nil {
		t.Fatalf("ParseConnectorSpec: %v", err)
	}
	if spec.Transport != modules.TransportMCP {
		t.Errorf("transport = %q, want %q", spec.Transport, modules.TransportMCP)
	}
}

func TestParseConnectorSpecAcceptsAPITransport(t *testing.T) {
	spec, err := modules.ParseConnectorSpec([]byte(`{
		"endpoint": "https://api.local",
		"transport": "api",
		"query": "reports/2025"
	}`))
	if err != nil {
		t.Fatalf("ParseConnectorSpec: %v", err)
	}
	if spec.Transport != modules.TransportAPI {
		t.Errorf("transport = %q, want %q", spec.Transport, modules.TransportAPI)
	}
}

func TestParseConnectorSpecRefusesUnusableConfigs(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name:    "not JSON at all",
			config:  "point at the teagasc server",
			wantErr: "connector",
		},
		{
			name:    "unknown field",
			config:  `{"endpoint": "http://x.local", "api_key": "hunter2"}`,
			wantErr: "unknown field",
		},
		{
			name:    "no endpoint",
			config:  `{"transport": "mcp"}`,
			wantErr: "endpoint is required",
		},
		{
			name:    "blank endpoint",
			config:  `{"endpoint": "   "}`,
			wantErr: "endpoint is required",
		},
		{
			name:    "endpoint without scheme",
			config:  `{"endpoint": "connector.local:9090/mcp"}`,
			wantErr: "http(s)",
		},
		{
			name:    "endpoint with a non-http scheme",
			config:  `{"endpoint": "ftp://connector.local/pub"}`,
			wantErr: "http(s)",
		},
		{
			name:    "unknown transport",
			config:  `{"endpoint": "http://x.local", "transport": "carrier-pigeon"}`,
			wantErr: "transport",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := modules.ParseConnectorSpec([]byte(tt.config))
			if err == nil {
				t.Fatalf("ParseConnectorSpec accepted %s", tt.config)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q, want it to contain %q", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), "unusable connector") {
				t.Errorf("error %q, want the unusable-connector marker", err)
			}
		})
	}
}
