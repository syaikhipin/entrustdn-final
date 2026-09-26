package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/modules"
	"github.com/syaikhipin/entrustdn-final/backend/internal/postgres"
	"github.com/syaikhipin/entrustdn-final/backend/internal/requests"
)

// The Postgres-backed requests.Store (ticket 07; ADR 0007: the system of
// record). The conversation, matches, and spend survive restarts; the
// in-memory double from the service tests stands in for these same paths
// when no database is reachable.

func requestsFixture(t *testing.T) (*postgres.RequestsStore, *pgxpool.Pool, context.Context, string) {
	t.Helper()
	pool := openCleanTestDB(t, skipIfNoDatabase(t))
	// One Data Consumer account, so request rows have their foreign key.
	var consumerID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO accounts (email, display_name, role, status, password_hash, verified_at)
		VALUES ($1, 'PG Test Consumer', 'data_consumer', 'active', 'x', now())
		RETURNING id`, "requests-pg@example.org").Scan(&consumerID)
	if err != nil {
		t.Fatalf("insert test consumer: %v", err)
	}
	return postgres.NewRequestsStore(pool), pool, context.Background(), consumerID
}

func storedRequest(consumerID string) requests.Request {
	return requests.Request{
		ID:           "req-" + time.Now().UTC().Format("150405.000000000"),
		ConsumerID:   consumerID,
		Description:  "Spring barley yields across Leinster for the 2026 season",
		Format:       "csv",
		QualityBar:   "Farm-level records with provenance",
		BudgetMicros: 10_000_000,
		Status:       requests.StatusClarifying,
	}
}

func TestRequestsStoreRoundTripsARequest(t *testing.T) {
	store, _, ctx, consumerID := requestsFixture(t)
	r := storedRequest(consumerID)

	if err := store.CreateRequest(ctx, &r); err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		t.Fatalf("CreateRequest left timestamps unset: %+v", r)
	}

	// One turn happened elsewhere; the whole record is rewritten.
	r.Messages = []requests.Message{
		{Role: "consumer", Body: "I need spring barley yields", CreatedAt: time.Now().UTC()},
		{Role: "agent", Body: "Which season exactly?", CreatedAt: time.Now().UTC()},
	}
	r.Matches = []requests.Match{{
		AssetID: "asset-01", Name: "Leinster barley yields 2025",
		Reason: "catalog asset tagged spring barley", ReportedAt: time.Now().UTC(),
	}}
	r.SpentMicros = 550
	if err := store.UpdateRequest(ctx, r); err != nil {
		t.Fatalf("UpdateRequest: %v", err)
	}

	got, err := store.RequestByID(ctx, r.ID)
	if err != nil {
		t.Fatalf("RequestByID: %v", err)
	}
	if got.Description != r.Description || got.Format != "csv" || got.QualityBar != r.QualityBar {
		t.Errorf("commission = %+v, want what was stored", got)
	}
	if got.BudgetMicros != 10_000_000 || got.SpentMicros != 550 {
		t.Errorf("budget/spent = (%d, %d), want (10000000, 550)", got.BudgetMicros, got.SpentMicros)
	}
	if got.Status != requests.StatusClarifying {
		t.Errorf("status = %q, want clarifying", got.Status)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "consumer" || got.Messages[1].Body != "Which season exactly?" {
		t.Errorf("messages = %+v, want the two turns in order", got.Messages)
	}
	if len(got.Matches) != 1 || got.Matches[0].AssetID != "asset-01" || got.Matches[0].Reason != "catalog asset tagged spring barley" {
		t.Errorf("matches = %+v, want asset-01 with its reason", got.Matches)
	}

	// A status flip (budget guard or clarification) survives the rewrite.
	got.Status = requests.StatusBudgetExhausted
	if err := store.UpdateRequest(ctx, got); err != nil {
		t.Fatalf("UpdateRequest status: %v", err)
	}
	if again, err := store.RequestByID(ctx, r.ID); err != nil || again.Status != requests.StatusBudgetExhausted {
		t.Errorf("reloaded status = (%q, %v), want budget_exhausted", again.Status, err)
	}
}

func TestRequestsStoreNotFoundAndList(t *testing.T) {
	store, pool, ctx, consumerID := requestsFixture(t)

	if _, err := store.RequestByID(ctx, "no-such-request"); !errors.Is(err, requests.ErrNotFound) {
		t.Errorf("missing lookup = %v, want ErrNotFound", err)
	}

	first := storedRequest(consumerID)
	second := storedRequest(consumerID)
	second.ID += "-b"
	second.CreatedAt = first.CreatedAt.Add(time.Second)
	for _, r := range []*requests.Request{&first, &second} {
		if err := store.CreateRequest(ctx, r); err != nil {
			t.Fatalf("CreateRequest: %v", err)
		}
		time.Sleep(2 * time.Millisecond) // distinct created_at for the ordering
	}
	// A second consumer's request must not leak into the first's list.
	var otherID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO accounts (email, display_name, role, status, password_hash, verified_at)
		VALUES ($1, 'PG Test Consumer 2', 'data_consumer', 'active', 'x', now())
		RETURNING id`, "requests-pg-2@example.org").Scan(&otherID)
	if err != nil {
		t.Fatalf("insert second consumer: %v", err)
	}
	other := storedRequest(otherID)
	if err := store.CreateRequest(ctx, &other); err != nil {
		t.Fatalf("CreateRequest (other consumer): %v", err)
	}

	list, err := store.RequestsByConsumer(ctx, consumerID)
	if err != nil || len(list) != 2 {
		t.Fatalf("RequestsByConsumer = (%d records, %v), want the consumer's two", len(list), err)
	}
	if !list[0].CreatedAt.After(list[1].CreatedAt) {
		t.Errorf("list not newest first: %v then %v", list[0].CreatedAt, list[1].CreatedAt)
	}
	if err := store.UpdateRequest(ctx, requests.Request{ID: "ghost"}); !errors.Is(err, requests.ErrNotFound) {
		t.Errorf("update of a ghost = %v, want ErrNotFound", err)
	}
}

func TestRequestsStoreRoundTripsTemplateAndSkills(t *testing.T) {
	// Ticket 13: the module attachments ride the record — the template's
	// parsed spec as a snapshot, the skills as module ids. NULL template is
	// the default flow and must come back as nil, not an empty spec.
	store, _, ctx, consumerID := requestsFixture(t)

	r := storedRequest(consumerID)
	maxReasks := 1
	r.Template = &requests.TemplateAttachment{
		ModuleID: "mod-tpl-1",
		Spec: modules.TemplateSpec{
			Questions: []string{"Which county is your farm in?"},
			FollowUp:  modules.FollowUpRules{MaxReasks: &maxReasks, Topic: "A quick follow-up"},
			Triggers: []modules.TriggerRule{
				{Type: modules.RuleRequireAny, Values: []string{"carlow", "kilkenny"}},
			},
		},
	}
	r.Skills = []string{"mod-skill-1", "mod-skill-2"}
	if err := store.CreateRequest(ctx, &r); err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}

	got, err := store.RequestByID(ctx, r.ID)
	if err != nil {
		t.Fatalf("RequestByID: %v", err)
	}
	if got.Template == nil || got.Template.ModuleID != "mod-tpl-1" {
		t.Fatalf("template = %+v, want the mod-tpl-1 snapshot", got.Template)
	}
	if len(got.Template.Spec.Questions) != 1 || got.Template.Spec.Questions[0] != "Which county is your farm in?" {
		t.Errorf("spec questions = %+v, want the snapshot's question", got.Template.Spec.Questions)
	}
	if got.Template.Spec.FollowUp.MaxReasks == nil || *got.Template.Spec.FollowUp.MaxReasks != 1 {
		t.Errorf("follow_up = %+v, want max_reasks 1", got.Template.Spec.FollowUp)
	}
	if len(got.Skills) != 2 || got.Skills[0] != "mod-skill-1" {
		t.Errorf("skills = %v, want both module ids in order", got.Skills)
	}

	// Detach (nil + empty) survives the whole-record rewrite.
	got.Template = nil
	got.Skills = nil
	if err := store.UpdateRequest(ctx, got); err != nil {
		t.Fatalf("UpdateRequest detach: %v", err)
	}
	again, err := store.RequestByID(ctx, r.ID)
	if err != nil {
		t.Fatalf("RequestByID after detach: %v", err)
	}
	if again.Template != nil || len(again.Skills) != 0 {
		t.Errorf("after detach template/skills = (%+v, %v), want nil/empty", again.Template, again.Skills)
	}
}
