package requests_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/requests"
)

// The Requests & clarification service (ticket 07) at the domain seam: a
// Data Consumer creates a Request, the Agent clarifies it one chat turn at
// a time (the agent is a contract fake — the Seam 2 fake the backend-side
// tests drive), every turn is metered and charged to the Ledger against the
// Request's budget, and the budget cannot be silently exceeded. Expected
// amounts are worked examples, not recomputations.

func testRules() credits.PricingRules {
	return credits.PricingRules{
		Inference: []credits.InferenceRule{{
			Model: "test-model", InputMicrosPer1K: 2_500,
			CachedInputMicrosPer1K: 1_250, OutputMicrosPer1K: 10_000,
		}},
		Data: credits.DataRules{CachedAssetMicrosPerUnit: 5_000_000, UniqueMicrosPerUnit: 50_000_000},
	}
}

// clarifierReply is the fake agent's standard answer: usage {120 in, 40
// cached, 30 out} at test-model prices to 550 micros
// (80 fresh @2500/1k = 200, 40 cached @1250/1k = 50, 30 out @10000/1k = 300).
func clarifierReply(requestID string) contract.ClarifyResponse {
	return contract.ClarifyResponse{
		RequestID: requestID,
		Reply:     "Which season exactly?",
		Clarified: false,
		Matches: []contract.CatalogMatch{{
			AssetID: "asset-01", Name: "Leinster barley yields 2025",
			Reason: "catalog asset tagged spring barley",
		}},
		Usage: contract.MeteredUsage{Model: "test-model", InputTokens: 120, CachedInputTokens: 40, OutputTokens: 30},
	}
}

// fakeClarifier records the clarify requests it receives and answers with a
// canned response — the Seam 2 double.
type fakeClarifier struct {
	got    []contract.ClarifyRequest
	reply  contract.ClarifyResponse
	err    error
	called int
}

func (f *fakeClarifier) Clarify(_ context.Context, req contract.ClarifyRequest) (contract.ClarifyResponse, error) {
	f.called++
	f.got = append(f.got, req)
	// A real agent echoes the request ID it was asked about.
	reply := f.reply
	if reply.RequestID == "" {
		reply.RequestID = req.RequestID
	}
	return reply, f.err
}

// fixture wires a service with an in-memory store, an in-memory ledger at
// test pricing, a catalog snapshot, and the fake clarifier. The consumer's
// account is funded with 100 credits.
func fixture(t *testing.T, catalog []contract.CatalogAsset) (*requests.Service, *credits.MemoryStore, *fakeClarifier) {
	t.Helper()
	ledger := credits.NewMemoryStore()
	credSvc := credits.NewService(ledger, func(context.Context) (credits.PricingRules, error) { return testRules(), nil })
	if _, err := credSvc.Grant(t.Context(), credits.AccountScope("consumer-1"), 100*credits.MicrosPerCredit, "admin", "pilot seed"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	clari := &fakeClarifier{reply: clarifierReply("")}
	svc := requests.NewService(requests.NewMemoryStore(), clari,
		func(context.Context) ([]contract.CatalogAsset, error) { return catalog, nil },
		credSvc)
	return svc, ledger, clari
}

// newRequest creates the consumer's Request: 1_000 micros of budget.
func newRequest(t *testing.T, svc *requests.Service) requests.Request {
	t.Helper()
	r, err := svc.Create(t.Context(), "consumer-1", requests.NewRequest{
		Description:  "Spring barley yields across Leinster for the 2026 season",
		Format:       "csv",
		QualityBar:   "Farm-level records with provenance",
		BudgetMicros: 1_000,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return r
}

var testCatalog = []contract.CatalogAsset{{
	ID:                "asset-01",
	Name:              "Leinster barley yields 2025",
	Description:       "Farm-level spring barley yields, county tagged",
	Categories:        []contract.CategoryStamp{{Category: "crop", Value: "spring-barley", Label: "Spring barley"}},
	CachedPriceMicros: 5_000_000,
}}

func TestCreateStoresTheConsumersRequest(t *testing.T) {
	svc, _, _ := fixture(t, testCatalog)

	r := newRequest(t, svc)

	if r.ID == "" {
		t.Fatal("Create left ID empty")
	}
	if r.ConsumerID != "consumer-1" || r.Status != requests.StatusClarifying {
		t.Errorf("request = %+v, want consumer-1 in status clarifying", r)
	}
	if r.BudgetMicros != 1_000 || r.SpentMicros != 0 {
		t.Errorf("budget/spent = (%d, %d), want (1000, 0)", r.BudgetMicros, r.SpentMicros)
	}

	// The consumer can list their own requests.
	list, err := svc.ByConsumer(t.Context(), "consumer-1")
	if err != nil || len(list) != 1 || list[0].ID != r.ID {
		t.Fatalf("ByConsumer = (%+v, %v), want the one request", list, err)
	}
}

func TestCreateRefusesRequestsThatCannotRun(t *testing.T) {
	svc, _, _ := fixture(t, testCatalog)

	tests := []struct {
		name string
		req  requests.NewRequest
	}{
		{name: "no description", req: requests.NewRequest{Format: "csv", BudgetMicros: 1_000}},
		{name: "no format", req: requests.NewRequest{Description: "yields", BudgetMicros: 1_000}},
		{name: "zero budget", req: requests.NewRequest{Description: "yields", Format: "csv"}},
		{name: "negative budget", req: requests.NewRequest{Description: "yields", Format: "csv", BudgetMicros: -5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := svc.Create(t.Context(), "consumer-1", tt.req); err == nil {
				t.Errorf("Create(%+v) = nil error, want refusal", tt.req)
			}
		})
	}
}

func TestFirstChatTurnRecordsReplyChargesLedgerAndPassesCatalog(t *testing.T) {
	svc, ledger, clari := fixture(t, testCatalog)
	r := newRequest(t, svc)

	updated, err := svc.Chat(t.Context(), r.ID, "consumer-1", "I need spring barley yield data for Leinster")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// The agent saw the full Request so far: description, format, quality
	// bar, budget and spend to date, the consumer's message as history, and
	// the catalog snapshot to check first.
	if clari.called != 1 {
		t.Fatalf("clarifier called %d times, want 1", clari.called)
	}
	sent := clari.got[0]
	if sent.RequestID != r.ID || sent.Description != r.Description || sent.Format != "csv" {
		t.Errorf("clarify request = %+v, want the request's own fields", sent)
	}
	if sent.BudgetMicros != 1_000 || sent.SpentMicros != 0 {
		t.Errorf("clarify budget/spent = (%d, %d), want (1000, 0)", sent.BudgetMicros, sent.SpentMicros)
	}
	// The new message rides in message; history carries the prior turns —
	// none yet on the first turn.
	if sent.Message != "I need spring barley yield data for Leinster" {
		t.Errorf("clarify message = %q, want the consumer's message", sent.Message)
	}
	if len(sent.History) != 0 {
		t.Errorf("clarify history = %+v, want empty on the first turn", sent.History)
	}
	if len(sent.Catalog) != 1 || sent.Catalog[0].ID != "asset-01" {
		t.Errorf("clarify catalog = %+v, want the snapshot with asset-01", sent.Catalog)
	}

	// The agent's reply and its catalog matches are recorded on the request.
	if len(updated.Request.Messages) != 2 {
		t.Fatalf("messages = %+v, want consumer + agent turns", updated.Request.Messages)
	}
	if updated.Request.Messages[1].Role != "agent" || updated.Request.Messages[1].Body != "Which season exactly?" {
		t.Errorf("agent turn = %+v, want the fake's reply", updated.Request.Messages[1])
	}
	if len(updated.Request.Matches) != 1 || updated.Request.Matches[0].AssetID != "asset-01" {
		t.Errorf("matches = %+v, want asset-01 reported by the agent", updated.Request.Matches)
	}
	if updated.Request.Status != requests.StatusClarifying {
		t.Errorf("status = %q, want still clarifying", updated.Request.Status)
	}

	// Metering: the reported usage is charged to the consumer's ledger
	// scope, with the movement tagged to this request.
	// Worked example: 80 fresh @2500/1k + 40 cached @1250/1k + 30 out
	// @10000/1k = 200 + 50 + 300 = 550 micros.
	bal, err := ledger.Balance(t.Context(), credits.AccountScope("consumer-1"))
	if err != nil || bal != 100*credits.MicrosPerCredit-550 {
		t.Errorf("balance after turn = (%d, %v), want 100 credits − 550 micros", bal, err)
	}
	movs, err := ledger.MovementsByScope(t.Context(), credits.AccountScope("consumer-1"), 10)
	if err != nil || len(movs) != 2 { // grant + inference charge
		t.Fatalf("movements = (%d, %v), want grant + one charge", len(movs), err)
	}
	charge := movs[0]
	if charge.Kind != credits.KindInferenceCharge || charge.RequestID != r.ID {
		t.Errorf("charge = %+v, want an inference charge tagged %s", charge, r.ID)
	}
	if charge.Inference == nil || charge.Inference.InputTokens != 120 || charge.Inference.CachedInputTokens != 40 || charge.Inference.OutputTokens != 30 {
		t.Errorf("charge detail = %+v, want the metered usage", charge.Inference)
	}
	if updated.Request.SpentMicros != 550 {
		t.Errorf("spent = %d, want 550", updated.Request.SpentMicros)
	}
}

// The clarify envelope on the wire must carry [] for empty history/catalog
// arrays — a nil slice marshals to null, which the contract schema and the
// Python side both refuse. Pinned here via the fake's view of the request;
// the round-trip through JSON is pinned at the agentclient seam.
func TestClarifyWireCarriesEmptyArraysNotNull(t *testing.T) {
	svc, _, clari := fixture(t, testCatalog)
	r := newRequest(t, svc)

	if _, err := svc.Chat(t.Context(), r.ID, "consumer-1", "first"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	raw, err := json.Marshal(clari.got[0])
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"history", "catalog"} {
		arr, ok := doc[field].([]any)
		if !ok {
			t.Errorf("clarify %s = %v, want a JSON array (null breaks the contract)", field, doc[field])
		}
		_ = arr
	}
}

func TestChatRefusesStrangers(t *testing.T) {
	svc, _, clari := fixture(t, testCatalog)
	r := newRequest(t, svc)

	if _, err := svc.Chat(t.Context(), r.ID, "someone-else", "hi"); !errors.Is(err, requests.ErrForbidden) {
		t.Errorf("stranger Chat error = %v, want ErrForbidden", err)
	}
	if clari.called != 0 {
		t.Errorf("clarifier called %d times, want 0", clari.called)
	}
}

func TestSecondTurnCarriesHistoryAndSpendSoFar(t *testing.T) {
	svc, _, clari := fixture(t, testCatalog)
	// Two turns at 550 µcr each need more than the 1_000 µcr default budget.
	r, err := svc.Create(t.Context(), "consumer-1", requests.NewRequest{
		Description: "Spring barley yields across Leinster for the 2026 season",
		Format:      "csv", BudgetMicros: 2_000,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Chat(t.Context(), r.ID, "consumer-1", "I need spring barley yields"); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	updated, err := svc.Chat(t.Context(), r.ID, "consumer-1", "For the 2026 season, please")
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}

	sent := clari.got[1]
	// History: the first turn's two messages, oldest first, before the new
	// message.
	if len(sent.History) != 2 {
		t.Fatalf("history = %+v, want the first turn's two messages", sent.History)
	}
	if sent.History[0].Role != "consumer" || sent.History[0].Body != "I need spring barley yields" {
		t.Errorf("history[0] = %+v, want the consumer's first message", sent.History[0])
	}
	if sent.History[1].Role != "agent" || sent.History[1].Body != "Which season exactly?" {
		t.Errorf("history[1] = %+v, want the agent's first reply", sent.History[1])
	}
	// Spend so far rides with every turn so the agent can budget its reply.
	if sent.SpentMicros != 550 {
		t.Errorf("spent_micros = %d, want 550", sent.SpentMicros)
	}
	// Four recorded messages after two turns; spend doubled.
	if len(updated.Request.Messages) != 4 {
		t.Errorf("messages = %d, want 4", len(updated.Request.Messages))
	}
	if updated.Request.SpentMicros != 1_100 {
		t.Errorf("spent = %d, want 1_100", updated.Request.SpentMicros)
	}
}

func TestClarifiedReplyClosesTheConversation(t *testing.T) {
	// One turn where the agent declares the need fully specified.
	clariReply := clarifierReply("")
	clariReply.Clarified = true

	// Run the turn through a one-shot service whose fake answers clarified.
	ledger2 := credits.NewMemoryStore()
	credSvc := credits.NewService(ledger2, func(context.Context) (credits.PricingRules, error) { return testRules(), nil })
	if _, err := credSvc.Grant(t.Context(), credits.AccountScope("consumer-1"), 100*credits.MicrosPerCredit, "admin", "seed"); err != nil {
		t.Fatal(err)
	}
	clari := &fakeClarifier{reply: clariReply}
	svc2 := requests.NewService(requests.NewMemoryStore(), clari,
		func(context.Context) ([]contract.CatalogAsset, error) { return testCatalog, nil }, credSvc)
	r2, err := svc2.Create(t.Context(), "consumer-1", requests.NewRequest{
		Description: "yields", Format: "csv", BudgetMicros: 1_000,
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := svc2.Chat(t.Context(), r2.ID, "consumer-1", "all barley, 2026, Leinster")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if updated.Request.Status != requests.StatusClarified {
		t.Errorf("status = %q, want clarified", updated.Request.Status)
	}

	// A clarified request takes no further turns.
	if _, err := svc2.Chat(t.Context(), r2.ID, "consumer-1", "one more thing"); !errors.Is(err, requests.ErrClosed) {
		t.Errorf("chat on clarified request = %v, want ErrClosed", err)
	}
}

func TestBudgetCannotBeSilentlyExceeded(t *testing.T) {
	svc, ledger, _ := fixture(t, testCatalog)
	// Budget 1_000 micros; each turn prices 550. The second turn would
	// take the total to 1_100 — past the budget — and must be refused
	// before anything posts.
	r := newRequest(t, svc)

	if _, err := svc.Chat(t.Context(), r.ID, "consumer-1", "first"); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	turn, err := svc.Chat(t.Context(), r.ID, "consumer-1", "second")
	if !errors.Is(err, requests.ErrBudgetExceeded) {
		t.Fatalf("second turn error = %v, want ErrBudgetExceeded", err)
	}
	if turn.Reply.Reply != "" {
		t.Errorf("refused turn still returned a reply: %+v", turn.Reply)
	}

	// The refusal is surfaced on the status view, never silent.
	got, err := svc.ByID(t.Context(), r.ID, "consumer-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != requests.StatusBudgetExhausted {
		t.Errorf("status = %q, want budget_exhausted", got.Status)
	}
	// Spend stayed at the first turn's 550; the refused charge posted
	// nothing.
	if got.SpentMicros != 550 {
		t.Errorf("spent = %d, want still 550", got.SpentMicros)
	}
	if bal, _ := ledger.Balance(t.Context(), credits.AccountScope("consumer-1")); bal != 100*credits.MicrosPerCredit-550 {
		t.Errorf("balance = %d, want 100 credits − 550 (refused turn posted nothing)", bal)
	}
	// And an exhausted request takes no further turns.
	if _, err := svc.Chat(t.Context(), r.ID, "consumer-1", "third"); !errors.Is(err, requests.ErrClosed) {
		t.Errorf("chat on exhausted request = %v, want ErrClosed", err)
	}
}

func TestUnpricedModelFailsTheTurnLoudly(t *testing.T) {
	// The fake reports a model no rule covers: the turn fails, nothing is
	// charged, and the request stays open.
	unpriced := clarifierReply("")
	unpriced.Usage.Model = "mystery-model"
	ledger2 := credits.NewMemoryStore()
	credSvc := credits.NewService(ledger2, func(context.Context) (credits.PricingRules, error) { return testRules(), nil })
	if _, err := credSvc.Grant(t.Context(), credits.AccountScope("consumer-1"), 100*credits.MicrosPerCredit, "admin", "seed"); err != nil {
		t.Fatal(err)
	}
	svc2 := requests.NewService(requests.NewMemoryStore(), &fakeClarifier{reply: unpriced},
		func(context.Context) ([]contract.CatalogAsset, error) { return testCatalog, nil }, credSvc)
	r2, err := svc2.Create(t.Context(), "consumer-1", requests.NewRequest{
		Description: "yields", Format: "csv", BudgetMicros: 1_000,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc2.Chat(t.Context(), r2.ID, "consumer-1", "hi"); !errors.Is(err, credits.ErrNoPricingRule) {
		t.Fatalf("unpriced turn error = %v, want ErrNoPricingRule", err)
	}
	got, err := svc2.ByID(t.Context(), r2.ID, "consumer-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 0 {
		t.Errorf("failed turn left messages behind: %+v", got.Messages)
	}
	if bal, _ := ledger2.Balance(t.Context(), credits.AccountScope("consumer-1")); bal != 100*credits.MicrosPerCredit {
		t.Errorf("balance = %d, want untouched", bal)
	}
	if got.Status != requests.StatusClarifying {
		t.Errorf("status = %q, want still clarifying", got.Status)
	}
}

func TestCatalogMissIsNotAnError(t *testing.T) {
	// An empty catalog is a normal state: the agent just fielding the need
	// with no existing assets to report.
	_, _, _ = fixture(t, nil)
	// The fake answers with no matches, as it would against an empty
	// catalog.
	reply := clarifierReply("")
	reply.Matches = nil
	ledger := credits.NewMemoryStore()
	credSvc := credits.NewService(ledger, func(context.Context) (credits.PricingRules, error) { return testRules(), nil })
	if _, err := credSvc.Grant(t.Context(), credits.AccountScope("consumer-1"), 100*credits.MicrosPerCredit, "admin", "seed"); err != nil {
		t.Fatal(err)
	}
	svc2 := requests.NewService(requests.NewMemoryStore(), &fakeClarifier{reply: reply},
		func(context.Context) ([]contract.CatalogAsset, error) { return nil, nil }, credSvc)
	r, err := svc2.Create(t.Context(), "consumer-1", requests.NewRequest{
		Description: "yields", Format: "csv", BudgetMicros: 1_000,
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := svc2.Chat(t.Context(), r.ID, "consumer-1", "hello")
	if err != nil {
		t.Fatalf("Chat against an empty catalog: %v", err)
	}
	if len(updated.Request.Matches) != 0 {
		t.Errorf("matches = %+v, want none", updated.Request.Matches)
	}
}
