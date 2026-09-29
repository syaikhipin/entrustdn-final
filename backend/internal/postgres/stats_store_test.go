package postgres_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/collections"
	"github.com/syaikhipin/entrustdn-final/backend/internal/conversations"
	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
	"github.com/syaikhipin/entrustdn-final/backend/internal/modules"
	"github.com/syaikhipin/entrustdn-final/backend/internal/postgres"
	"github.com/syaikhipin/entrustdn-final/backend/internal/requests"
	"github.com/syaikhipin/entrustdn-final/backend/internal/roster"
	"github.com/syaikhipin/entrustdn-final/backend/internal/stats"
)

// The live Postgres implementation of stats.Source (ticket 16), exercised
// against a real database like the other stores. Seed rows land through the
// same stores production uses — ledger postings, requests, collections,
// conversations, roster, modules — and the snapshot must report exactly
// what was seeded: derived from real platform data, nothing invented.

func newStatsSource(t *testing.T) (*postgres.StatsSource, *pgxpool.Pool) {
	t.Helper()
	url := skipIfNoDatabase(t)
	pool := openCleanTestDB(t, url)
	return postgres.NewStatsSource(pool), pool
}

func TestStatsSnapshotEmptyPlatform(t *testing.T) {
	src, _ := newStatsSource(t)
	snap, err := src.Snapshot(t.Context(), 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Snapshot(): %v", err)
	}
	if len(snap.RequestsOverTime) != 0 {
		t.Errorf("requests_over_time = %v, want empty", snap.RequestsOverTime)
	}
	if len(snap.CreditsFlow) != 0 {
		t.Errorf("credits_flow = %v, want empty", snap.CreditsFlow)
	}
	if snap.Modules.AgentSkill != 0 || snap.Modules.SystemWide != 0 {
		t.Errorf("modules = %+v, want zeros", snap.Modules)
	}
}

func TestStatsSnapshotCountsSeededData(t *testing.T) {
	src, pool := newStatsSource(t)
	ctx := t.Context()

	org := seedStatsAccount(t, pool, membership.RoleFarmerOrganization)
	consumer := seedStatsAccount(t, pool, membership.RoleDataConsumer)

	// Two requests today, one yesterday.
	seedStatsRequest(t, pool, "req-stats-1", consumer, "clarifying", time.Now().UTC())
	seedStatsRequest(t, pool, "req-stats-2", consumer, "clarified", time.Now().UTC())
	seedStatsRequest(t, pool, "req-stats-3", consumer, "clarified", time.Now().UTC().Add(-24*time.Hour))

	// One roster member and two conversations (whatsapp + telegram).
	member := seedStatsMember(t, pool, org, "Siobhán Ní Uaithne", "whatsapp:+353860000001")
	seedStatsConversation(t, pool, "conv-stats-1", org, member, "whatsapp:+353860000001", 4)
	seedStatsConversation(t, pool, "conv-stats-2", org, member, "telegram:12345", 2)

	// One completed collection.
	seedStatsCollection(t, pool, "col-stats-1", org, consumer, "req-stats-1", "completed")

	// Ledger: one top-up in 100 cr, one inference charge out 4 cr.
	credStore := postgres.NewCreditsStore(pool)
	if _, err := credStore.PostMovement(ctx, credits.Movement{
		Kind: credits.KindTopUp, Memo: "checkout",
		Entries: []credits.Entry{
			{Scope: credits.AccountScope(consumer), AmountMicros: 100 * credits.MicrosPerCredit},
			{Scope: credits.Scope(credits.ScopeTreasury), AmountMicros: -100 * credits.MicrosPerCredit},
		},
	}); err != nil {
		t.Fatalf("post top-up: %v", err)
	}
	if _, err := credStore.PostMovement(ctx, credits.Movement{
		Kind: credits.KindInferenceCharge, Memo: "turn",
		Entries: []credits.Entry{
			{Scope: credits.AccountScope(consumer), AmountMicros: -4 * credits.MicrosPerCredit},
			{Scope: credits.Scope(credits.ScopeTreasury), AmountMicros: 4 * credits.MicrosPerCredit},
		},
	}); err != nil {
		t.Fatalf("post charge: %v", err)
	}

	// Modules: mod-skill-1 has two versions (latest system-wide); plus a
	// private skill and a private template. Latest-version counting means
	// mod-skill-1 counts once, not once per version.
	seedStatsModule(t, pool, "mod-skill-1/v1", "mod-skill-1", "Plow advice", "agent_skill", org, false)
	seedStatsModule(t, pool, "mod-skill-1/v2", "mod-skill-1", "Plow advice", "agent_skill", org, true)
	seedStatsModule(t, pool, "mod-skill-2/v1", "mod-skill-2", "Soil guide", "agent_skill", org, false)
	seedStatsModule(t, pool, "mod-tmpl-1/v1", "mod-tmpl-1", "Yield survey", "process_template", org, false)

	snap, err := src.Snapshot(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Snapshot(): %v", err)
	}

	// Requests over time: yesterday 1, today 2 (oldest day first).
	if len(snap.RequestsOverTime) != 2 {
		t.Fatalf("requests_over_time = %v, want 2 days", snap.RequestsOverTime)
	}
	if snap.RequestsOverTime[0].Count != 1 || snap.RequestsOverTime[1].Count != 2 {
		t.Errorf("requests_over_time = %v, want [1, 2]", snap.RequestsOverTime)
	}

	// Statuses.
	gotReq := statusMap(snap.RequestStatuses)
	if gotReq["clarifying"] != 1 || gotReq["clarified"] != 2 {
		t.Errorf("request statuses = %v, want clarifying 1, clarified 2", snap.RequestStatuses)
	}
	gotCol := statusMap(snap.CollectionStatuses)
	if gotCol["completed"] != 1 {
		t.Errorf("collection statuses = %v, want completed 1", snap.CollectionStatuses)
	}

	// Credits flow.
	gotFlow := map[string]stats.KindFlow{}
	for _, f := range snap.CreditsFlow {
		gotFlow[f.Kind] = f
	}
	if tu := gotFlow["top_up"]; tu.InMicros != 100*credits.MicrosPerCredit || tu.Movements != 1 {
		t.Errorf("top_up flow = %+v, want in 100 cr over 1 movement", tu)
	}
	if ic := gotFlow["inference_charge"]; ic.OutMicros != 4*credits.MicrosPerCredit || ic.Movements != 1 {
		t.Errorf("inference_charge flow = %+v, want out 4 cr over 1 movement", ic)
	}

	// Channels: turns = thread length (agent + member messages).
	gotChan := map[string]stats.ChannelActivity{}
	for _, c := range snap.Channels {
		gotChan[c.Channel] = c
	}
	if wa := gotChan["whatsapp"]; wa.Conversations != 1 || wa.Turns != 4 {
		t.Errorf("whatsapp = %+v, want 1 conversation, 4 turns", wa)
	}
	if tg := gotChan["telegram"]; tg.Conversations != 1 || tg.Turns != 2 {
		t.Errorf("telegram = %+v, want 1 conversation, 2 turns", tg)
	}

	// Modules: latest version per module — 2 agent skills (one system-wide),
	// 1 process template; private = 2, system-wide = 1.
	if snap.Modules.AgentSkill != 2 || snap.Modules.ProcessTemplate != 1 || snap.Modules.Connector != 0 {
		t.Errorf("module kinds = %+v, want skills 2, templates 1, connectors 0", snap.Modules)
	}
	if snap.Modules.SystemWide != 1 || snap.Modules.Private != 2 {
		t.Errorf("module visibility = %+v, want 1 system-wide, 2 private", snap.Modules)
	}
}

func statusMap(scs []stats.StatusCount) map[string]int64 {
	m := map[string]int64{}
	for _, sc := range scs {
		m[sc.Status] = sc.Count
	}
	return m
}

var statsSeq int

// --- seed helpers through the real stores ---

func seedStatsAccount(t *testing.T, pool *pgxpool.Pool, role membership.Role) string {
	t.Helper()
	store := postgres.NewStore(pool)
	acct := membership.Account{
		Email:       fmt.Sprintf("stats-%d-%s@example.org", statsSeq, string(role)),
		DisplayName: "Stats Seed",
		Role:        role,
		Status:      membership.StatusActive,
	}
	statsSeq++
	if err := store.CreateAccount(t.Context(), &acct); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return acct.ID
}

func seedStatsRequest(t *testing.T, pool *pgxpool.Pool, id, consumerID, status string, createdAt time.Time) {
	t.Helper()
	r := requests.Request{
		ID: id, ConsumerID: consumerID,
		Description: "Spring barley yields", Format: "csv",
		BudgetMicros: 1_000_000, Status: requests.Status(status),
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := postgres.NewRequestsStore(pool).CreateRequest(t.Context(), &r); err != nil {
		t.Fatalf("seed request %s: %v", id, err)
	}
}

func seedStatsMember(t *testing.T, pool *pgxpool.Pool, orgID, name, contact string) string {
	t.Helper()
	m := roster.Member{
		ID:    fmt.Sprintf("member-stats-%d", statsSeq),
		OrgID: orgID, DisplayName: name, Contact: contact,
	}
	statsSeq++
	if err := postgres.NewRosterStore(pool).CreateMember(t.Context(), &m); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	return m.ID
}

func seedStatsConversation(t *testing.T, pool *pgxpool.Pool, id, orgID, memberID, contact string, turns int) {
	t.Helper()
	c := conversations.Conversation{
		ID: id, OrgID: orgID, MemberID: memberID, MemberName: "Siobhán",
		Contact: contact, Topic: "gathering", Questions: []string{"Q1"},
		ResumeToken: "tok-" + id, Status: conversations.StatusAwaitingMember,
	}
	for i := 0; i < turns; i++ {
		role := "agent"
		if i%2 == 1 {
			role = "member"
		}
		c.Thread = append(c.Thread, conversations.Turn{Role: role, Body: fmt.Sprintf("turn %d", i)})
	}
	if err := postgres.NewConversationsStore(pool).CreateConversation(t.Context(), &c); err != nil {
		t.Fatalf("seed conversation %s: %v", id, err)
	}
}

func seedStatsCollection(t *testing.T, pool *pgxpool.Pool, id, orgID, consumerID, requestID, status string) {
	t.Helper()
	c := collections.Collection{
		ID: id, OrgID: orgID, ConsumerID: consumerID, RequestID: requestID,
		Status: collections.Status(status),
		Items: []collections.Item{{
			MemberID: memberID(), MemberName: "Siobhán", Question: "Q1",
			Status: collections.ItemAccepted, Accepted: "42 ha",
		}},
	}
	if err := postgres.NewCollectionsStore(pool).CreateCollection(t.Context(), &c); err != nil {
		t.Fatalf("seed collection %s: %v", id, err)
	}
}

func memberID() string {
	statsSeq++
	return fmt.Sprintf("member-stats-%d", statsSeq)
}

func seedStatsModule(t *testing.T, pool *pgxpool.Pool, id, moduleID, name, kind, authorID string, systemWide bool) {
	t.Helper()
	version := id[strings.LastIndex(id, "/")+1:]
	m := modules.Module{
		ID: id, ModuleID: moduleID, Name: name, Kind: modules.Kind(kind),
		AuthorID: authorID, Version: version, Capability: "demo capability",
		SystemWide: systemWide,
	}
	if err := postgres.NewModulesStore(pool).CreateModule(t.Context(), &m); err != nil {
		t.Fatalf("seed module %s: %v", id, err)
	}
}
