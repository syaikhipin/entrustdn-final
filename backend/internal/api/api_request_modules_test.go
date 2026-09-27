package api_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/agentclient"
	"github.com/syaikhipin/entrustdn-final/backend/internal/api"
	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/mailsink"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
	"github.com/syaikhipin/entrustdn-final/backend/internal/modules"
	"github.com/syaikhipin/entrustdn-final/backend/internal/requests"
)

// Ticket 13 at Seam 1: a consumer attaches a private Process Template and
// Agent Skills to their Request — through endpoints that authorize every
// attach against the registry, refuse strangers without disclosing
// existence, and freeze the template onto the Request at attach time. The
// skills' markdown must ride the clarify payload into the agent's context.

// newRequestModuleServer boots the API with the requests and modules
// endpoints sharing one registry service, plus the fake clarifier agent.
// Returns the server, admin token, mail buffer, and the fake agent (tests
// assert what rode the wire).
func newRequestModuleServer(t *testing.T) (*httptest.Server, string, *bytes.Buffer, *fakeClarifierAgent) {
	t.Helper()
	reply := contract.ClarifyResponse{
		Reply:     "Which counties?",
		Clarified: false,
		Usage:     contract.MeteredUsage{Model: "test-model", InputTokens: 80, CachedInputTokens: 40, OutputTokens: 30},
	}
	agent := newClarifierAgent(t, reply)
	memStore := membership.NewMemoryStore()
	if err := memStore.PublishTOS(t.Context(), membership.TOSVersion{Version: "1.0", Body: "Be good."}); err != nil {
		t.Fatalf("seed TOS: %v", err)
	}
	provisionAdmin(t, memStore, "admin@thresh.dev")
	mail := &bytes.Buffer{}
	// One registry behind both dependency slots: uploads through the
	// modules endpoints must be visible to the requests attach endpoints.
	modSvc := modules.NewService(modules.NewMemoryStore())
	srv := httptest.NewServer(api.NewHandler(api.Deps{
		Agent:   agentclient.New(agent.server.URL),
		Version: "test-backend",
		Store:   memStore,
		Mail:    mailsink.NewLogSink(mail),
		Credits: &api.CreditsDeps{
			Store: credits.NewMemoryStore(),
			Rules: func(context.Context) (credits.PricingRules, error) { return testPricingRules(), nil },
		},
		Requests: &api.RequestsDeps{
			Store:   requests.NewMemoryStore(),
			Modules: modSvc,
			Catalog: func(_ context.Context) ([]contract.CatalogAsset, error) { return nil, nil },
		},
		Modules: &api.ModulesDeps{Service: modSvc},
	}))
	t.Cleanup(srv.Close)
	token := loginWith(t, srv, "admin@thresh.dev", "admin-pass-2026")
	return srv, token, mail, agent
}

// uploadModule runs the author's upload call and returns the module ID.
func uploadModule(t *testing.T, srv *httptest.Server, token string, body map[string]any) string {
	t.Helper()
	code, doc := postWithToken(t, srv, "/api/v1/modules", token, body)
	if code != http.StatusCreated {
		t.Fatalf("module upload = %d (doc: %v)", code, doc)
	}
	mod, _ := doc["module"].(map[string]any)
	if mod == nil || mod["module_id"] == "" {
		t.Fatalf("upload response has no module_id: %v", doc)
	}
	return mod["module_id"].(string)
}

// templateUploadBody is a private process template with its own question
// list, one trigger, and a one-re-ask budget.
func templateUploadBody() map[string]any {
	return map[string]any{
		"name":       "barley-survey",
		"kind":       "process_template",
		"version":    "1.0.0",
		"capability": "Runs a spring-barley yield survey workflow for Requests.",
		"content":    "# Barley survey",
		"config":     `{"questions": ["Which county is your farm in?"], "follow_up": {"max_reasks": 1, "topic": "A quick follow-up"}, "triggers": [{"type": "require_any", "values": ["carlow", "hectares"]}]}`,
	}
}

// skillUploadBody is a private agent skill with markdown instructions.
func skillUploadBody() map[string]any {
	return map[string]any{
		"name":       "teagasc-barley-conventions",
		"kind":       "agent_skill",
		"version":    "1.0.0",
		"capability": "Conventions for reading Irish barley answers.",
		"content":    "# Reading Irish barley answers\n\n- Yields are quoted in t/ha.",
	}
}

func TestConsumerAttachesTemplateAndSkillsToTheirRequest(t *testing.T) {
	srv, _, mail, _ := newRequestModuleServer(t)

	// The consumer authors both modules — an author may always use their own.
	_, consToken := newConsumer(t, srv, mail, "author-consumer@example.org")
	tplID := uploadModule(t, srv, consToken, templateUploadBody())
	skillID := uploadModule(t, srv, consToken, skillUploadBody())

	code, doc := postWithToken(t, srv, "/api/v1/requests", consToken, map[string]any{
		"description": "Spring barley yields for Leinster", "format": "csv", "budget_micros": 5_000_000,
	})
	if code != http.StatusCreated {
		t.Fatalf("create request = %d (doc: %v)", code, doc)
	}
	reqID := doc["request"].(map[string]any)["id"].(string)

	// Attach the template; the request carries its module id.
	code, doc = putWithToken(t, srv, fmt.Sprintf("/api/v1/requests/%s/template", reqID), consToken, map[string]any{"module_id": tplID})
	if code != http.StatusOK {
		t.Fatalf("attach template = %d (doc: %v)", code, doc)
	}
	attached := doc["request"].(map[string]any)
	tpl, _ := attached["template"].(map[string]any)
	if tpl == nil || tpl["module_id"] != tplID {
		t.Errorf("request.template = %v, want the attached module id", attached["template"])
	}

	// Attach the skill; the request's skills list shows it.
	code, doc = postWithToken(t, srv, fmt.Sprintf("/api/v1/requests/%s/skills", reqID), consToken, map[string]any{"module_id": skillID})
	if code != http.StatusOK {
		t.Fatalf("attach skill = %d (doc: %v)", code, doc)
	}
	skills := doc["request"].(map[string]any)["skills"].([]any)
	if len(skills) != 1 || skills[0] != skillID {
		t.Errorf("skills = %v, want [%s]", skills, skillID)
	}

	// Detach the template — the request falls back to the default flow.
	code, doc = doWithToken(t, http.MethodDelete, srv, fmt.Sprintf("/api/v1/requests/%s/template", reqID), consToken, nil)
	if code != http.StatusOK {
		t.Fatalf("detach template = %d (doc: %v)", code, doc)
	}
	if _, has := doc["request"].(map[string]any)["template"]; has {
		t.Errorf("detached template still rendered: %v", doc["request"])
	}

	// Detach the skill.
	code, _ = doWithToken(t, http.MethodDelete, srv, fmt.Sprintf("/api/v1/requests/%s/skills/%s", reqID, skillID), consToken, nil)
	if code != http.StatusOK {
		t.Fatalf("detach skill = %d", code)
	}
}

func TestAttachRefusesAStrangersPrivateModule(t *testing.T) {
	srv, _, mail, _ := newRequestModuleServer(t)

	// A stranger authors a private template; the requester never got a grant.
	_, tplToken := newConsumer(t, srv, mail, "module-author@example.org")
	tplID := uploadModule(t, srv, tplToken, templateUploadBody())

	_, reqToken := newConsumer(t, srv, mail, "requester@example.org")
	code, doc := postWithToken(t, srv, "/api/v1/requests", reqToken, map[string]any{
		"description": "yields", "format": "csv", "budget_micros": 1_000_000,
	})
	if code != http.StatusCreated {
		t.Fatalf("create request = %d (doc: %v)", code, doc)
	}
	reqID := doc["request"].(map[string]any)["id"].(string)

	// Both attaches read as 404: a stranger's private module's existence is
	// not disclosed.
	code, _ = putWithToken(t, srv, fmt.Sprintf("/api/v1/requests/%s/template", reqID), reqToken, map[string]any{"module_id": tplID})
	if code != http.StatusNotFound {
		t.Errorf("stranger template attach = %d, want 404 (existence not disclosed)", code)
	}
	code, _ = postWithToken(t, srv, fmt.Sprintf("/api/v1/requests/%s/skills", reqID), reqToken, map[string]any{"module_id": tplID})
	if code != http.StatusNotFound {
		t.Errorf("stranger skill attach = %d, want 404", code)
	}

	// The author attaching their own module works — authorization is on the
	// module, not the endpoint.
	code, _ = putWithToken(t, srv, fmt.Sprintf("/api/v1/requests/%s/template", reqID), tplToken, map[string]any{"module_id": tplID})
	if code != http.StatusNotFound {
		t.Errorf("non-owner's attach to someone else's request = %d, want 404", code)
	}
}

func TestSkillsRideTheWireIntoTheAgentContext(t *testing.T) {
	srv, adminToken, mail, agent := newRequestModuleServer(t)

	consID, consToken := newConsumer(t, srv, mail, "skill-consumer@example.org")
	fundConsumer(t, srv, adminToken, consID)
	skillID := uploadModule(t, srv, consToken, skillUploadBody())

	code, doc := postWithToken(t, srv, "/api/v1/requests", consToken, map[string]any{
		"description": "Spring barley yields", "format": "csv", "budget_micros": 5_000_000,
	})
	if code != http.StatusCreated {
		t.Fatalf("create request = %d (doc: %v)", code, doc)
	}
	reqID := doc["request"].(map[string]any)["id"].(string)

	code, _ = postWithToken(t, srv, fmt.Sprintf("/api/v1/requests/%s/skills", reqID), consToken, map[string]any{"module_id": skillID})
	if code != http.StatusOK {
		t.Fatalf("attach skill = %d", code)
	}

	code, doc = postWithToken(t, srv, fmt.Sprintf("/api/v1/requests/%s/chat", reqID), consToken, map[string]any{"message": "hello"})
	if code != http.StatusOK {
		t.Fatalf("chat = %d (doc: %v)", code, doc)
	}
	if len(agent.got) != 1 {
		t.Fatalf("agent saw %d clarify payloads, want 1", len(agent.got))
	}
	got := agent.got[0]
	if len(got.Skills) != 1 {
		t.Fatalf("clarify payload skills = %+v, want one loaded skill", got.Skills)
	}
	if got.Skills[0].Name != "teagasc-barley-conventions" || !strings.Contains(got.Skills[0].Content, "t/ha") {
		t.Errorf("skill on the wire = %+v, want the markdown content loaded", got.Skills[0])
	}
}

// connectorUploadBody is a private Connector Module: an MCP endpoint fact
// — connection only, never credentials (ticket 14).
func connectorUploadBody() map[string]any {
	return map[string]any{
		"name":       "teagasc-reports",
		"kind":       "connector",
		"version":    "1.0.0",
		"capability": "Answers barley-report questions from the Teagasc source.",
		"config":     `{"endpoint": "http://connector.local:9090/mcp", "transport": "mcp", "query": "search_reports"}`,
	}
}

func TestConsumerAttachesConnectorToTheirRequest(t *testing.T) {
	srv, adminToken, mail, agent := newRequestModuleServer(t)

	consID, consToken := newConsumer(t, srv, mail, "connector-consumer@example.org")
	fundConsumer(t, srv, adminToken, consID)
	connID := uploadModule(t, srv, consToken, connectorUploadBody())

	code, doc := postWithToken(t, srv, "/api/v1/requests", consToken, map[string]any{
		"description": "Spring barley yields", "format": "csv", "budget_micros": 5_000_000,
	})
	if code != http.StatusCreated {
		t.Fatalf("create request = %d (doc: %v)", code, doc)
	}
	reqID := doc["request"].(map[string]any)["id"].(string)

	// Attach the connector; the request's connectors list shows it.
	code, doc = postWithToken(t, srv, fmt.Sprintf("/api/v1/requests/%s/connectors", reqID), consToken, map[string]any{"module_id": connID})
	if code != http.StatusOK {
		t.Fatalf("attach connector = %d (doc: %v)", code, doc)
	}
	conns := doc["request"].(map[string]any)["connectors"].([]any)
	if len(conns) != 1 || conns[0] != connID {
		t.Errorf("connectors = %v, want [%s]", conns, connID)
	}

	// The resolved connection fact rides the clarify wire.
	code, doc = postWithToken(t, srv, fmt.Sprintf("/api/v1/requests/%s/chat", reqID), consToken, map[string]any{"message": "hello"})
	if code != http.StatusOK {
		t.Fatalf("chat = %d (doc: %v)", code, doc)
	}
	if len(agent.got) != 1 {
		t.Fatalf("agent saw %d clarify payloads, want 1", len(agent.got))
	}
	got := agent.got[0]
	if len(got.Connectors) != 1 {
		t.Fatalf("clarify payload connectors = %+v, want one", got.Connectors)
	}
	c := got.Connectors[0]
	if c.Endpoint != "http://connector.local:9090/mcp" || c.Transport != "mcp" || c.Query != "search_reports" {
		t.Errorf("connector on the wire = %+v, want the parsed connection fact", c)
	}

	// Detach.
	code, doc = doWithToken(t, http.MethodDelete, srv, fmt.Sprintf("/api/v1/requests/%s/connectors/%s", reqID, connID), consToken, nil)
	if code != http.StatusOK {
		t.Fatalf("detach connector = %d (doc: %v)", code, doc)
	}
	conns, _ = doc["request"].(map[string]any)["connectors"].([]any)
	if len(conns) != 0 {
		t.Errorf("connectors after detach = %v, want none", conns)
	}
}

func TestConnectorAttachRefusesAStrangersPrivateModule(t *testing.T) {
	srv, _, mail, _ := newRequestModuleServer(t)

	_, authorToken := newConsumer(t, srv, mail, "conn-author@example.org")
	connID := uploadModule(t, srv, authorToken, connectorUploadBody())

	_, otherToken := newConsumer(t, srv, mail, "conn-stranger@example.org")
	code, doc := postWithToken(t, srv, "/api/v1/requests", otherToken, map[string]any{
		"description": "Other data", "format": "csv", "budget_micros": 5_000_000,
	})
	if code != http.StatusCreated {
		t.Fatalf("create request = %d (doc: %v)", code, doc)
	}
	reqID := doc["request"].(map[string]any)["id"].(string)

	code, _ = postWithToken(t, srv, fmt.Sprintf("/api/v1/requests/%s/connectors", reqID), otherToken, map[string]any{"module_id": connID})
	if code != http.StatusNotFound {
		t.Errorf("stranger connector attach = %d, want 404", code)
	}
}
