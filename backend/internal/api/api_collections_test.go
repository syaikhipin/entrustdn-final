package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/agentclient"
	"github.com/syaikhipin/entrustdn-final/backend/internal/anonymize"
	"github.com/syaikhipin/entrustdn-final/backend/internal/api"
	"github.com/syaikhipin/entrustdn-final/backend/internal/collections"
	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
	"github.com/syaikhipin/entrustdn-final/backend/internal/conversations"
	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/mailsink"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
	"github.com/syaikhipin/entrustdn-final/backend/internal/requests"
	"github.com/syaikhipin/entrustdn-final/backend/internal/roster"
)

// Collection endpoints (ticket 12) at Seam 1: the org creates a collection
// over its roster, opens conversations that carry the request ID, syncs to
// ingest answers through the quality triggers, and the consumer downloads
// the anonymized delivery — charged to the Ledger at the unique rate. The
// agent is the contract fake pacing the question list; the stores are
// in-memory doubles.

// agentForConversations fits the agent client to conversations.Agent —
// the same contract-only shape the conversation endpoints use.
type agentForConversations struct{ client *agentclient.Client }

func (a agentForConversations) Start(ctx context.Context, req contract.ConversationStartRequest) (contract.ConversationResponse, error) {
	return a.client.StartConversation(ctx, req)
}

func (a agentForConversations) Reply(ctx context.Context, req contract.ConversationReplyRequest) (contract.ConversationResponse, error) {
	return a.client.Converse(ctx, req)
}

// deliveryRules prices the test's delivery charge: unique data at 50
// credits per unit.
func deliveryRules() credits.PricingRules {
	return credits.PricingRules{
		Inference: []credits.InferenceRule{{
			Model: "test-model", InputMicrosPer1K: 2_500,
			CachedInputMicrosPer1K: 1_250, OutputMicrosPer1K: 10_000,
		}},
		Data: credits.DataRules{CachedAssetMicrosPerUnit: 5_000_000, UniqueMicrosPerUnit: 50_000_000},
	}
}

// collectionsFixture is everything the collection tests reach into.
type collectionsFixture struct {
	srv      *httptest.Server
	mail     *bytes.Buffer
	agent    *conversationAgent
	colSvc   *collections.Service
	convs    *conversations.MemoryStore
	reqStore *requests.MemoryStore
	credSvc  *credits.Service
	ledger   *credits.MemoryStore
}

// newCollectionsServer boots the API with in-memory stores, the contract
// fake agent, credits, and the collection endpoints wired end to end.
func newCollectionsServer(t *testing.T) *collectionsFixture {
	t.Helper()
	agent := newConversationAgent(t)
	memStore := membership.NewMemoryStore()
	if err := memStore.PublishTOS(t.Context(), membership.TOSVersion{Version: "1.0", Body: "Be good."}); err != nil {
		t.Fatalf("seed TOS: %v", err)
	}
	provisionAdmin(t, memStore, "admin@thresh.dev")
	mail := &bytes.Buffer{}

	ledger := credits.NewMemoryStore()
	credSvc := credits.NewService(ledger, func(context.Context) (credits.PricingRules, error) { return deliveryRules(), nil })
	convs := conversations.NewMemoryStore()
	reqStore := requests.NewMemoryStore()
	rosterStore := roster.NewMemoryStore()
	pmap := anonymize.NewMemoryMap()
	agentClient := agentclient.New(agent.server.URL)
	colSvc := collections.NewService(collections.Deps{
		Store:         collections.NewMemoryStore(),
		Conversations: convs,
		Opener:        conversations.NewService(convs, agentForConversations{client: agentClient}, "https://thresh.dev"),
		Roster:        rosterStore,
		Requests:      reqStore,
	}).WithCredits(credSvc).WithCleaner(anonymize.NewDelivery(pmap)).WithPseudonyms(pmap)

	srv := httptest.NewServer(api.NewHandler(api.Deps{
		Agent:   agentClient,
		Version: "test-backend",
		Store:   memStore,
		Mail:    mailsink.NewLogSink(mail),
		Roster:  &api.RosterDeps{Store: rosterStore},
		Conversations: &api.ConversationsDeps{
			Store:         convs,
			PublicBaseURL: "https://thresh.dev",
		},
		Requests: &api.RequestsDeps{
			Store:   reqStore,
			Catalog: func(context.Context) ([]contract.CatalogAsset, error) { return nil, nil },
		},
		Credits: &api.CreditsDeps{
			Store: ledger,
			Rules: func(context.Context) (credits.PricingRules, error) { return deliveryRules(), nil },
		},
		Collections: &api.CollectionsDeps{Service: colSvc},
	}))
	t.Cleanup(srv.Close)
	return &collectionsFixture{
		srv: srv, mail: mail, agent: agent, colSvc: colSvc,
		convs: convs, reqStore: reqStore, credSvc: credSvc, ledger: ledger,
	}
}

// seedClarifiedRequest stores one clarified request in the fixture's
// request store, owned by the given consumer.
func (f *collectionsFixture) seedClarifiedRequest(t *testing.T, id, consumerID string) {
	t.Helper()
	r := requests.Request{
		ID: id, ConsumerID: consumerID,
		Description: "Spring barley yields across Leinster", Format: "csv",
		BudgetMicros: 1_000_000, Status: requests.StatusClarified,
	}
	if err := f.reqStore.CreateRequest(t.Context(), &r); err != nil {
		t.Fatalf("seed request: %v", err)
	}
}

// seedCollection walks one collection through create → conversation →
// full answer → sync, leaving it completed with every item accepted.
// Returns the collection ID.
func (f *collectionsFixture) seedCompletedCollection(t *testing.T, orgToken, memberID, requestID string) string {
	t.Helper()
	code, doc := postWithToken(t, f.srv, "/api/v1/collections", orgToken, map[string]any{
		"request_id": requestID, "member_ids": []string{memberID},
		"questions": []string{"What is your farm size?"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create collection = %d (doc: %v)", code, doc)
	}
	colID := doc["collection"].(map[string]any)["id"].(string)

	// The org opens the gathering conversation; it carries the request ID
	// so the collection's sync matches it.
	code, doc = postWithToken(t, f.srv, "/api/v1/conversations", orgToken, map[string]any{
		"member_id": memberID, "topic": "Spring barley yields",
		"questions": []string{"What is your farm size?"}, "request_id": requestID,
	})
	if code != http.StatusCreated {
		t.Fatalf("open conversation = %d (doc: %v)", code, doc)
	}
	conv := doc["conversation"].(map[string]any)
	token := conv["resume_token"].(string)

	// The member answers fully through their resumable link.
	code, doc = postJSON(t, f.srv, "/api/v1/member/reply", map[string]any{
		"token": token, "message": "42 hectares of good land",
	})
	if code != http.StatusOK {
		t.Fatalf("member reply = %d (doc: %v)", code, doc)
	}

	// Sync ingests the answer; one member, all accepted → completed.
	code, doc = postWithToken(t, f.srv, "/api/v1/collections/"+colID+"/sync", orgToken, nil)
	if code != http.StatusOK {
		t.Fatalf("sync = %d (doc: %v)", code, doc)
	}
	if got := doc["collection"].(map[string]any)["status"]; got != "completed" {
		t.Fatalf("synced collection status = %v, want completed", got)
	}
	return colID
}

func TestOrgCreatesAndSyncsCollectionOverAPI(t *testing.T) {
	f := newCollectionsServer(t)
	orgID, orgToken := newApprovedOrg(t, f.srv, f.mail, "col-org@example.org")
	_, consumerToken := newConsumerWithRequest(t, f, "col-consumer@example.org")
	f.seedClarifiedRequest(t, "req-1", consumerAccountID(t, f.srv, consumerToken))
	_ = orgID
	memberID := addRosterMember(t, f.srv, orgToken, "Siobhán", "whatsapp:+353860000001")

	code, doc := postWithToken(t, f.srv, "/api/v1/collections", orgToken, map[string]any{
		"request_id": "req-1", "member_ids": []string{memberID},
		"questions": []string{"What is your farm size?"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create collection = %d (doc: %v)", code, doc)
	}
	col := doc["collection"].(map[string]any)
	colID := col["id"].(string)
	if col["status"] != "collecting" || len(col["items"].([]any)) != 1 {
		t.Fatalf("created collection = %v", col)
	}

	// Sync with no conversations yet: still collecting, no re-asks.
	code, doc = postWithToken(t, f.srv, "/api/v1/collections/"+colID+"/sync", orgToken, nil)
	if code != http.StatusOK {
		t.Fatalf("sync = %d (doc: %v)", code, doc)
	}
	if got := doc["collection"].(map[string]any)["status"]; got != "collecting" {
		t.Fatalf("sync status = %v, want collecting (no answers yet)", got)
	}

	// A stranger's sync and view are 404s.
	_, otherToken := newApprovedOrg(t, f.srv, f.mail, "col-other-org@example.org")
	if code, _ := getWithToken(t, f.srv, "/api/v1/collections/"+colID, otherToken); code != http.StatusNotFound {
		t.Errorf("stranger view = %d, want 404", code)
	}
	if code, _ := postWithToken(t, f.srv, "/api/v1/collections/"+colID+"/sync", otherToken, nil); code != http.StatusNotFound {
		t.Errorf("stranger sync = %d, want 404", code)
	}
}

func TestCollectionSyncOpensReaskOnBadAnswer(t *testing.T) {
	f := newCollectionsServer(t)
	_, orgToken := newApprovedOrg(t, f.srv, f.mail, "col-org@example.org")
	_, consumerToken := newConsumerWithRequest(t, f, "col-consumer@example.org")
	f.seedClarifiedRequest(t, "req-1", consumerAccountID(t, f.srv, consumerToken))
	memberID := addRosterMember(t, f.srv, orgToken, "Siobhán", "whatsapp:+353860000001")

	code, doc := postWithToken(t, f.srv, "/api/v1/collections", orgToken, map[string]any{
		"request_id": "req-1", "member_ids": []string{memberID},
		"questions": []string{"What is your farm size?"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create collection = %d", code)
	}
	colID := doc["collection"].(map[string]any)["id"].(string)

	// Open the conversation, member answers "n/a" — the anomaly trigger
	// refuses it.
	code, doc = postWithToken(t, f.srv, "/api/v1/conversations", orgToken, map[string]any{
		"member_id": memberID, "topic": "Spring barley yields",
		"questions": []string{"What is your farm size?"}, "request_id": "req-1",
	})
	if code != http.StatusCreated {
		t.Fatalf("open conversation = %d", code)
	}
	token := doc["conversation"].(map[string]any)["resume_token"].(string)
	if code, _ := postJSON(t, f.srv, "/api/v1/member/reply", map[string]any{"token": token, "message": "n/a"}); code != http.StatusOK {
		t.Fatal("member reply failed")
	}

	// Sync: the trigger fires, the re-ask conversation opens.
	code, doc = postWithToken(t, f.srv, "/api/v1/collections/"+colID+"/sync", orgToken, nil)
	if code != http.StatusOK {
		t.Fatalf("sync = %d (doc: %v)", code, doc)
	}
	items := doc["collection"].(map[string]any)["items"].([]any)
	item := items[0].(map[string]any)
	if item["status"] == "accepted" {
		t.Fatalf("bad answer accepted: %v", item)
	}
	if len(item["rounds"].([]any)) != 1 {
		t.Fatalf("rounds = %v, want the one refused attempt", item["rounds"])
	}

	// The re-ask conversation exists in the org's list; the member answers
	// it well through its token.
	code, doc = getWithToken(t, f.srv, "/api/v1/conversations", orgToken)
	if code != http.StatusOK {
		t.Fatalf("list conversations = %d", code)
	}
	var reaskToken string
	for _, c := range doc["conversations"].([]any) {
		cv := c.(map[string]any)
		if cv["topic"] == "A follow-up on your earlier answer" {
			reaskToken = cv["resume_token"].(string)
		}
	}
	if reaskToken == "" {
		t.Fatal("no re-ask conversation appeared in the org's list")
	}
	if code, _ := postJSON(t, f.srv, "/api/v1/member/reply", map[string]any{"token": reaskToken, "message": "42 hectares"}); code != http.StatusOK {
		t.Fatal("re-ask reply failed")
	}

	// Second sync: the re-ask answer ingests, the item accepts, the
	// collection completes.
	code, doc = postWithToken(t, f.srv, "/api/v1/collections/"+colID+"/sync", orgToken, nil)
	if code != http.StatusOK {
		t.Fatalf("sync 2 = %d (doc: %v)", code, doc)
	}
	if got := doc["collection"].(map[string]any)["status"]; got != "completed" {
		t.Fatalf("status = %v, want completed after the re-ask", got)
	}
}

func TestConsumerDownloadsAnonymizedDelivery(t *testing.T) {
	f := newCollectionsServer(t)
	_, orgToken := newApprovedOrg(t, f.srv, f.mail, "col-org@example.org")
	consumerID, consumerToken := newConsumerWithRequest(t, f, "col-consumer@example.org")
	f.seedClarifiedRequest(t, "req-1", consumerID)
	// Fund the consumer for the delivery charge.
	if _, err := f.credSvc.Grant(t.Context(), credits.AccountScope(consumerID), 100*credits.MicrosPerCredit, "admin", "pilot seed"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	memberID := addRosterMember(t, f.srv, orgToken, "Siobhán Ní Uaithne", "whatsapp:+353860000001")

	colID := f.seedCompletedCollection(t, orgToken, memberID, "req-1")

	// The consumer sees the collection in their list.
	code, doc := getWithToken(t, f.srv, "/api/v1/consumer/collections", consumerToken)
	if code != http.StatusOK {
		t.Fatalf("list consumer collections = %d (doc: %v)", code, doc)
	}
	if got := len(doc["collections"].([]any)); got != 1 {
		t.Fatalf("consumer collections = %d, want 1", got)
	}

	// The consumer's detail view shows completeness, never identities or
	// raw answers — those ride only the cleaned delivery (ADR 0005).
	raw, err := json.Marshal(doc["collections"].([]any)[0])
	if err != nil {
		t.Fatal(err)
	}
	view := string(raw)
	for _, leak := range []string{"Siobhán", "member_name", "rounds", "42 hectares"} {
		if strings.Contains(view, leak) {
			t.Errorf("consumer collection view leaks %q:\n%s", leak, view)
		}
	}
	code, doc = getWithToken(t, f.srv, "/api/v1/consumer/collections/"+colID, consumerToken)
	if code != http.StatusOK {
		t.Fatalf("consumer collection detail = %d", code)
	}
	if doc["collection"].(map[string]any)["status"] != "completed" {
		t.Error("consumer detail lost the status")
	}

	// Download: cleaned CSV bytes, no identities, charged.
	before := ledgerBalance(t, f.ledger, consumerID)
	resp, err := http.NewRequest(http.MethodGet, f.srv.URL+"/api/v1/consumer/collections/"+colID+"/download", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Header.Set("Authorization", "Bearer "+consumerToken)
	httpResp, err := http.DefaultClient.Do(resp)
	if err != nil {
		t.Fatal(err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("download = %d", httpResp.StatusCode)
	}
	var body bytes.Buffer
	if _, err := body.ReadFrom(httpResp.Body); err != nil {
		t.Fatal(err)
	}
	csvText := body.String()
	if strings.Contains(csvText, "Siobhán") || strings.Contains(csvText, "+353860000001") || strings.Contains(csvText, memberID) {
		t.Fatalf("delivery leaks an identity:\n%s", csvText)
	}
	if !strings.Contains(csvText, "42 hectares of good land") || !strings.Contains(csvText, "participant") {
		t.Fatalf("delivery lost the data:\n%s", csvText)
	}
	after := ledgerBalance(t, f.ledger, consumerID)
	if before-after != deliveryRules().Data.UniqueMicrosPerUnit {
		t.Errorf("charged %d µcr, want %d", before-after, deliveryRules().Data.UniqueMicrosPerUnit)
	}

	// A stranger's download is a 404.
	_, strangerToken := newConsumerWithRequest(t, f, "col-stranger@example.org")
	if code, _ := getWithToken(t, f.srv, "/api/v1/consumer/collections/"+colID+"/download", strangerToken); code != http.StatusNotFound {
		t.Errorf("stranger download = %d, want 404", code)
	}
}

// newConsumerWithRequest registers, verifies, and logs in one Data
// Consumer (consumers are active on arrival), returning the account ID and
// session token.
func newConsumerWithRequest(t *testing.T, f *collectionsFixture, email string) (string, string) {
	t.Helper()
	code, doc := postJSON(t, f.srv, "/api/v1/register", map[string]any{
		"email": email, "password": "analyze-2026", "display_name": "Analyst",
		"role": "data_consumer", "tos_version": "1.0",
	})
	if code != http.StatusCreated {
		t.Fatalf("register consumer: %d (doc: %v)", code, doc)
	}
	if code, _ := postJSON(t, f.srv, "/api/v1/verify", map[string]any{"token": verificationTokenFor(t, f.mail, email)}); code != http.StatusOK {
		t.Fatal("verify consumer failed")
	}
	return doc["account"].(map[string]any)["id"].(string), loginWith(t, f.srv, email, "analyze-2026")
}

// consumerAccountID resolves the account ID behind a consumer session —
// GET /api/v1/me.
func consumerAccountID(t *testing.T, srv *httptest.Server, token string) string {
	t.Helper()
	code, doc := getWithToken(t, srv, "/api/v1/me", token)
	if code != http.StatusOK {
		t.Fatalf("me = %d (doc: %v)", code, doc)
	}
	return doc["account"].(map[string]any)["id"].(string)
}

// ledgerBalance derives the consumer's balance from the Ledger.
func ledgerBalance(t *testing.T, ledger *credits.MemoryStore, consumerID string) int64 {
	t.Helper()
	b, err := ledger.Balance(t.Context(), credits.AccountScope(consumerID))
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	return b
}
