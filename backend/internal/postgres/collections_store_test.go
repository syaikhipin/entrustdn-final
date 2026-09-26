package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/collections"
	"github.com/syaikhipin/entrustdn-final/backend/internal/modules"
	"github.com/syaikhipin/entrustdn-final/backend/internal/postgres"
	"github.com/syaikhipin/entrustdn-final/backend/internal/requests"
)

// The Postgres-backed collections.Store (ticket 12; ADR 0007: the system
// of record). Items with their gathering rounds and the missing-data
// summary survive restarts as JSONB; the single-writer Sync path rewrites
// the whole record. The in-memory double from the service tests stands in
// for these same paths when no database is reachable.

func collectionsFixture(t *testing.T) (*postgres.CollectionsStore, *pgxpool.Pool, context.Context, string, string) {
	t.Helper()
	pool := openCleanTestDB(t, skipIfNoDatabase(t))
	// One Farmer Organization and one Data Consumer, so collection rows
	// have their foreign keys.
	var orgID, consumerID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO accounts (email, display_name, role, status, password_hash, verified_at)
		VALUES ($1, 'PG Test Org', 'farmer_organization', 'active', 'x', now())
		RETURNING id`, "collections-pg-org@example.org").Scan(&orgID)
	if err != nil {
		t.Fatalf("insert test org: %v", err)
	}
	err = pool.QueryRow(context.Background(), `
		INSERT INTO accounts (email, display_name, role, status, password_hash, verified_at)
		VALUES ($1, 'PG Test Consumer', 'data_consumer', 'active', 'x', now())
		RETURNING id`, "collections-pg-consumer@example.org").Scan(&consumerID)
	if err != nil {
		t.Fatalf("insert test consumer: %v", err)
	}
	return postgres.NewCollectionsStore(pool), pool, context.Background(), orgID, consumerID
}

func storedCollection(orgID, consumerID string) collections.Collection {
	deadline := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	return collections.Collection{
		ID:         "col-" + time.Now().UTC().Format("150405.000000000"),
		OrgID:      orgID,
		ConsumerID: consumerID,
		RequestID:  "req-pg-1",
		Deadline:   &deadline,
		Status:     collections.StatusCollecting,
		Items: []collections.Item{{
			MemberID: "mem-1", MemberName: "Siobhán Ní Uaithne",
			Question: "What is your farm size?",
			Status:   collections.ItemAccepted, Accepted: "42 hectares",
			Rounds: []collections.Round{
				{ConversationID: "conv-1", Answer: "n/a", OK: false, Reason: "anomaly: refused placeholder", At: time.Now().UTC()},
				{ConversationID: "conv-reask-1", Answer: "42 hectares", OK: true, At: time.Now().UTC()},
			},
			Reasks: 1,
		}},
	}
}

func TestCollectionsStoreRoundTripsACollection(t *testing.T) {
	store, _, ctx, orgID, consumerID := collectionsFixture(t)
	c := storedCollection(orgID, consumerID)

	if err := store.CreateCollection(ctx, &c); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		t.Fatalf("CreateCollection left timestamps unset: %+v", c)
	}

	// The gathering went further: the item's re-ask came back blocked at
	// the cap, the collection finalized incomplete with its summary — the
	// whole record is rewritten.
	c.Status = collections.StatusIncomplete
	c.Items[0] = collections.Item{
		MemberID: "mem-1", MemberName: "Siobhán Ní Uaithne",
		Question: "What is your farm size?", Status: collections.ItemBlocked,
		Rounds:      c.Items[0].Rounds,
		Reasks:      collections.MaxReasksPerItem,
		Blocked:     true,
		BlockReason: "the re-ask limit (2) was reached without a usable answer",
	}
	c.Missing = []collections.Missing{{
		MemberName: "Siobhán Ní Uaithne", Question: "What is your farm size?",
		Reason: "the re-ask limit (2) was reached without a usable answer",
	}}
	if err := store.UpdateCollection(ctx, c); err != nil {
		t.Fatalf("UpdateCollection: %v", err)
	}

	got, err := store.CollectionByID(ctx, c.ID)
	if err != nil {
		t.Fatalf("CollectionByID: %v", err)
	}
	if got.OrgID != orgID || got.ConsumerID != consumerID || got.RequestID != "req-pg-1" {
		t.Errorf("identity = (%s, %s, %s), want what was stored", got.OrgID, got.ConsumerID, got.RequestID)
	}
	if got.Status != collections.StatusIncomplete {
		t.Errorf("status = %q, want incomplete", got.Status)
	}
	if got.Deadline == nil || !got.Deadline.Equal(*c.Deadline) {
		t.Errorf("deadline = %v, want %v", got.Deadline, c.Deadline)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items = %+v, want one", got.Items)
	}
	item := got.Items[0]
	if item.Status != collections.ItemBlocked || !item.Blocked || item.Reasks != collections.MaxReasksPerItem {
		t.Errorf("item = %+v, want blocked at the cap", item)
	}
	if item.BlockReason == "" {
		t.Errorf("block reason lost")
	}
	if len(item.Rounds) != 2 || item.Rounds[0].OK || !item.Rounds[1].OK || item.Rounds[0].Reason == "" {
		t.Errorf("rounds = %+v, want the refused attempt then the accepted one", item.Rounds)
	}
	if len(got.Missing) != 1 || got.Missing[0].Reason == "" || got.Missing[0].MemberName != "Siobhán Ní Uaithne" {
		t.Errorf("missing = %+v, want the one gap with its reason", got.Missing)
	}
}

func TestCollectionsStoreListsAndNotFound(t *testing.T) {
	store, pool, ctx, orgID, consumerID := collectionsFixture(t)

	if _, err := store.CollectionByID(ctx, "no-such-collection"); !errors.Is(err, collections.ErrNotFound) {
		t.Errorf("missing lookup = %v, want ErrNotFound", err)
	}

	first := storedCollection(orgID, consumerID)
	second := storedCollection(orgID, consumerID)
	second.ID += "-b"
	second.CreatedAt = first.CreatedAt.Add(time.Second)
	for _, c := range []*collections.Collection{&first, &second} {
		if err := store.CreateCollection(ctx, c); err != nil {
			t.Fatalf("CreateCollection: %v", err)
		}
		time.Sleep(2 * time.Millisecond) // distinct created_at for the ordering
	}

	// A second org's collection must not leak into the first org's list.
	var otherOrgID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO accounts (email, display_name, role, status, password_hash, verified_at)
		VALUES ($1, 'PG Test Org 2', 'farmer_organization', 'active', 'x', now())
		RETURNING id`, "collections-pg-org-2@example.org").Scan(&otherOrgID)
	if err != nil {
		t.Fatalf("insert second org: %v", err)
	}
	other := storedCollection(otherOrgID, consumerID)
	if err := store.CreateCollection(ctx, &other); err != nil {
		t.Fatalf("CreateCollection (other org): %v", err)
	}

	byOrg, err := store.CollectionsByOrg(ctx, orgID)
	if err != nil || len(byOrg) != 2 {
		t.Fatalf("CollectionsByOrg = (%d records, %v), want the org's two", len(byOrg), err)
	}
	if !byOrg[0].CreatedAt.After(byOrg[1].CreatedAt) {
		t.Errorf("list not newest first: %v then %v", byOrg[0].CreatedAt, byOrg[1].CreatedAt)
	}

	byConsumer, err := store.CollectionsByConsumer(ctx, consumerID)
	if err != nil || len(byConsumer) != 3 {
		t.Fatalf("CollectionsByConsumer = (%d records, %v), want all three (one payer)", len(byConsumer), err)
	}

	if err := store.UpdateCollection(ctx, collections.Collection{ID: "ghost"}); !errors.Is(err, collections.ErrNotFound) {
		t.Errorf("update of a ghost = %v, want ErrNotFound", err)
	}
}

func TestCollectionsStoreRoundTripsTheTemplateSnapshot(t *testing.T) {
	// Ticket 13: a collection created from a templated Request carries the
	// frozen template snapshot; NULL comes back as nil (the default flow).
	store, _, ctx, orgID, consumerID := collectionsFixture(t)

	c := storedCollection(orgID, consumerID)
	maxReasks := 2
	c.Template = &requests.TemplateAttachment{
		ModuleID: "mod-tpl-9",
		Spec: modules.TemplateSpec{
			Questions: []string{"How many hectares of spring barley?"},
			FollowUp:  modules.FollowUpRules{MaxReasks: &maxReasks},
		},
	}
	if err := store.CreateCollection(ctx, &c); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	got, err := store.CollectionByID(ctx, c.ID)
	if err != nil {
		t.Fatalf("CollectionByID: %v", err)
	}
	if got.Template == nil || got.Template.ModuleID != "mod-tpl-9" {
		t.Fatalf("template = %+v, want the mod-tpl-9 snapshot", got.Template)
	}
	if len(got.Template.Spec.Questions) != 1 || got.Template.Spec.Questions[0] != "How many hectares of spring barley?" {
		t.Errorf("spec = %+v, want the frozen question", got.Template.Spec)
	}

	// NULL template (the default flow) reads back as nil.
	plain := storedCollection(orgID, consumerID)
	if err := store.CreateCollection(ctx, &plain); err != nil {
		t.Fatalf("CreateCollection plain: %v", err)
	}
	gotPlain, err := store.CollectionByID(ctx, plain.ID)
	if err != nil {
		t.Fatalf("CollectionByID plain: %v", err)
	}
	if gotPlain.Template != nil {
		t.Errorf("plain template = %+v, want nil", gotPlain.Template)
	}
}
