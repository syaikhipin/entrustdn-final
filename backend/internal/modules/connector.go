// ConnectorSpec (ticket 14): a Connector Module's config as the platform
// understands it. A connector's config is the connection fact the agent
// needs to reach an external data source — endpoint, transport, query.
// Parsing is strict: unknown fields are refused, because the request
// service hands the parsed result to the agent as a contract field and the
// agent must be able to trust it. Connection facts only — never
// credentials: a config that names an api_key or password field is
// refused at the door (unknown fields are), and nothing in the shape has a
// place to smuggle one.
package modules

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrUnusableConnector marks a connector config (or module kind) that
// cannot drive the agent's queries — the caller's fault, so the API renders
// it 422 with the reason, distinct from store failures (5xx) and access
// refusals (404).
var ErrUnusableConnector = errors.New("modules: unusable connector")

// Transport is how the agent talks to a Connector Module's source.
type Transport string

const (
	// TransportMCP: the endpoint is an MCP server (Streamable HTTP); the
	// agent speaks the MCP wire protocol to it.
	TransportMCP Transport = "mcp"
	// TransportAPI: the endpoint is a plain HTTP API; the agent GETs
	// endpoint/query and carries the body verbatim.
	TransportAPI Transport = "api"
)

// ConnectorSpec is the parsed body of a Connector Module: the connection
// fact handed to the agent on every clarify turn. Query is transport-meaning:
// for MCP it is the tool name (default "query"); for API it is the path
// under the endpoint (default ""). Empty means the transport's default.
type ConnectorSpec struct {
	Endpoint  string    `json:"endpoint"`
	Transport Transport `json:"transport,omitempty"`
	Query     string    `json:"query,omitempty"`
}

// ParseConnectorSpec decodes and validates a connector's config JSON,
// strictly — unknown fields are refused, so a future api_key field cannot
// ride in quietly and credentials can never become part of the shape. The
// request service may trust what this returns.
func ParseConnectorSpec(config []byte) (ConnectorSpec, error) {
	dec := json.NewDecoder(bytes.NewReader(config))
	dec.DisallowUnknownFields()
	var spec ConnectorSpec
	if err := dec.Decode(&spec); err != nil {
		return ConnectorSpec{}, fmt.Errorf("%w: config is not a valid connector: %w", ErrUnusableConnector, err)
	}
	if strings.TrimSpace(spec.Endpoint) == "" {
		return ConnectorSpec{}, fmt.Errorf("%w: endpoint is required", ErrUnusableConnector)
	}
	u, err := url.Parse(spec.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ConnectorSpec{}, fmt.Errorf("%w: endpoint must be an http(s) URL, got %q", ErrUnusableConnector, spec.Endpoint)
	}
	// Trimmed endpoint is the connection fact that rides the contract.
	spec.Endpoint = strings.TrimSpace(spec.Endpoint)
	if spec.Transport == "" {
		spec.Transport = TransportMCP
	}
	if spec.Transport != TransportMCP && spec.Transport != TransportAPI {
		return ConnectorSpec{}, fmt.Errorf("%w: transport must be mcp or api, got %q", ErrUnusableConnector, spec.Transport)
	}
	return spec, nil
}
