package api_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/agentclient"
	"github.com/syaikhipin/entrustdn-final/backend/internal/api"
	"github.com/syaikhipin/entrustdn-final/backend/internal/mailsink"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
	"github.com/syaikhipin/entrustdn-final/backend/internal/stats"
)

// Admin stats dashboard (ticket 16) at Seam 1: the endpoint aggregates the
// platform's real records through the stats.Source seam — here an
// in-memory fake — and serves the document to Platform Admins only.

// fakeStatsSource returns a canned snapshot, recording the window it was
// asked for.
type fakeStatsSource struct {
	got    time.Duration
	result stats.Snapshot
	err    error
}

func (f *fakeStatsSource) Snapshot(_ context.Context, window time.Duration) (stats.Snapshot, error) {
	f.got = window
	return f.result, f.err
}

func newStatsServer(t *testing.T, src stats.Source) (*httptest.Server, *bytes.Buffer) {
	t.Helper()
	agent := fakeAgent(t, "0.1.0")
	store := membership.NewMemoryStore()
	if err := store.PublishTOS(t.Context(), membership.TOSVersion{Version: "1.0", Body: "Be good."}); err != nil {
		t.Fatalf("seed TOS: %v", err)
	}
	provisionAdmin(t, store, "admin@thresh.dev")
	mail := &bytes.Buffer{}
	srv := httptest.NewServer(api.NewHandler(api.Deps{
		Agent:   agentclient.New(agent.URL),
		Version: "test-backend",
		Store:   store,
		Mail:    mailsink.NewLogSink(mail),
		Stats:   &api.StatsDeps{Source: src},
	}))
	t.Cleanup(srv.Close)
	return srv, mail
}

func adminLoginToken(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	return loginWith(t, srv, "admin@thresh.dev", "admin-pass-2026")
}

// registerVerified registers and verifies a plain account of the given
// role, returning a login token.
func registerVerified(t *testing.T, srv *httptest.Server, mail *bytes.Buffer, email, role string) string {
	t.Helper()
	if code, doc := postJSON(t, srv, "/api/v1/register", map[string]any{
		"email": email, "password": "harvest-2026",
		"display_name": "Stats Fixture", "role": role, "tos_version": "1.0",
	}); code != http.StatusCreated {
		t.Fatalf("register %s: %d (doc: %v)", role, code, doc)
	}
	if code, _ := postJSON(t, srv, "/api/v1/verify", map[string]any{"token": verificationTokenFor(t, mail, email)}); code != http.StatusOK {
		t.Fatal("verify failed")
	}
	return loginWith(t, srv, email, "harvest-2026")
}

func TestAdminStatsServesDashboardDocument(t *testing.T) {
	src := &fakeStatsSource{result: stats.Snapshot{
		RequestsOverTime:   []stats.DayCount{{Day: "2026-09-28", Count: 3}},
		RequestStatuses:    []stats.StatusCount{{Status: "clarifying", Count: 2}, {Status: "clarified", Count: 1}},
		CollectionStatuses: []stats.StatusCount{{Status: "completed", Count: 1}},
		CreditsFlow: []stats.KindFlow{
			{Kind: "top_up", InMicros: 100_000_000, Movements: 1},
			{Kind: "inference_charge", OutMicros: 4_000_000, Movements: 1},
		},
		Channels: []stats.ChannelActivity{{Channel: "whatsapp", Conversations: 2, Turns: 9}},
		Modules:  stats.ModuleCounts{AgentSkill: 2, ProcessTemplate: 1, Connector: 1, SystemWide: 1, Private: 3},
	}}
	srv, _ := newStatsServer(t, src)
	token := adminLoginToken(t, srv)

	code, doc := getWithToken(t, srv, "/api/v1/admin/stats", token)
	if code != http.StatusOK {
		t.Fatalf("admin stats = %d (doc: %v)", code, doc)
	}
	if src.got != 30*24*time.Hour {
		t.Errorf("window = %v, want 30d", src.got)
	}
	if got := len(doc["requests_over_time"].([]any)); got != 1 {
		t.Errorf("requests_over_time = %d entries, want 1", got)
	}
	flows := doc["credits_flow"].([]any)
	if len(flows) != 2 {
		t.Fatalf("credits_flow = %d entries, want 2", len(flows))
	}
	topUp := flows[0].(map[string]any)
	if topUp["kind"] != "top_up" || topUp["in_micros"].(float64) != 100_000_000 {
		t.Errorf("first flow = %v, want top_up in 100 cr", topUp)
	}
	mods := doc["modules"].(map[string]any)
	if mods["agent_skill"].(float64) != 2 || mods["system_wide"].(float64) != 1 {
		t.Errorf("modules = %v, want agent_skill 2, system_wide 1", mods)
	}
}

func TestAdminStatsRefusesNonAdmins(t *testing.T) {
	src := &fakeStatsSource{}
	srv, mail := newStatsServer(t, src)

	consumerToken := registerVerified(t, srv, mail, "stats-consumer@example.org", "data_consumer")
	if code, _ := getWithToken(t, srv, "/api/v1/admin/stats", consumerToken); code != http.StatusForbidden {
		t.Errorf("consumer stats = %d, want 403", code)
	}
	if code, _ := getWithToken(t, srv, "/api/v1/admin/stats", ""); code != http.StatusUnauthorized {
		t.Errorf("anonymous stats = %d, want 401", code)
	}
	// The source is never consulted for refused callers.
	if src.got != 0 {
		t.Errorf("source consulted for refused caller (window %v)", src.got)
	}
}

func TestAdminStatsWithoutSourceDoesNotRegister(t *testing.T) {
	agent := fakeAgent(t, "0.1.0")
	store := membership.NewMemoryStore()
	if err := store.PublishTOS(t.Context(), membership.TOSVersion{Version: "1.0", Body: "Be good."}); err != nil {
		t.Fatalf("seed TOS: %v", err)
	}
	provisionAdmin(t, store, "admin@thresh.dev")
	mail := &bytes.Buffer{}
	srv := httptest.NewServer(api.NewHandler(api.Deps{
		Agent: agentclient.New(agent.URL), Version: "t", Store: store, Mail: mailsink.NewLogSink(mail),
	}))
	defer srv.Close()

	token := adminLoginToken(t, srv)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/admin/stats", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("stats without deps = %d, want 404", resp.StatusCode)
	}
}
