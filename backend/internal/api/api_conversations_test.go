package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/agentclient"
	"github.com/syaikhipin/entrustdn-final/backend/internal/api"
	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
	"github.com/syaikhipin/entrustdn-final/backend/internal/conversations"
	"github.com/syaikhipin/entrustdn-final/backend/internal/mailsink"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
	"github.com/syaikhipin/entrustdn-final/backend/internal/roster"
)

// Member conversations & roster (ticket 11) at Seam 1: the org maintains
// its Member roster and opens conversations; the Member replies through
// the resumable link's token — no account, no login. The agent is a
// contract fake pacing the question list the way the real sidecar does
// (thread and cumulative answers ride every response); the stores are
// in-memory doubles.

// conversationAgent fakes the agent's conversation surface: it asks the
// questions in order — the n-th agent turn asks questions[n-1] — and
// reports the member turns as cumulative answers, exactly the shapes the
// real sidecar sends.
type conversationAgent struct {
	server    *httptest.Server
	gotStart  *contract.ConversationStartRequest
	gotReply  *contract.ConversationReplyRequest
	deliverOK bool
}

func newConversationAgent(t *testing.T) *conversationAgent {
	t.Helper()
	ca := &conversationAgent{deliverOK: true}
	ca.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env contract.Envelope
		_ = json.NewDecoder(r.Body).Decode(&env)
		switch env.Type {
		case contract.TypePingRequest:
			var req contract.PingRequest
			_ = env.PayloadInto(&req)
			_ = json.NewEncoder(w).Encode(contract.Envelope{
				Version: contract.Version, Type: contract.TypePingResponse,
				SentAt:  time.Now().UTC(),
				Payload: mustJSON(contract.PingResponse{Nonce: req.Nonce, Pong: true, AgentVersion: "fake-1.0.0"}),
			})
		case contract.TypeConversationStartRequest:
			var req contract.ConversationStartRequest
			if err := env.PayloadInto(&req); err != nil {
				http.Error(w, "bad payload", http.StatusBadRequest)
				return
			}
			ca.gotStart = &req
			_ = json.NewEncoder(w).Encode(contract.Envelope{
				Version: contract.Version, Type: contract.TypeConversationResponse,
				SentAt: time.Now().UTC(),
				Payload: mustJSON(contract.ConversationResponse{
					ConversationID: req.ConversationID,
					Delivered:      true,
					Status:         contract.ConversationAwaitingMember,
					Thread:         []contract.ThreadMessage{{Role: "agent", Body: req.Questions[0]}},
					Answers:        []string{},
				}),
			})
		case contract.TypeConversationReplyRequest:
			var req contract.ConversationReplyRequest
			if err := env.PayloadInto(&req); err != nil {
				http.Error(w, "bad payload", http.StatusBadRequest)
				return
			}
			ca.gotReply = &req
			thread := append(req.Thread, contract.ThreadMessage{Role: "member", Body: req.Message})
			answers := make([]string, 0, len(thread))
			for _, m := range thread {
				if m.Role == "member" {
					answers = append(answers, m.Body)
				}
			}
			status := contract.ConversationCompleted
			if len(thread)/2 < len(req.Questions) {
				// Agent turns so far == member turns; one more question to ask.
				thread = append(thread, contract.ThreadMessage{Role: "agent", Body: req.Questions[len(answers)-1]})
				status = contract.ConversationAwaitingMember
			}
			_ = json.NewEncoder(w).Encode(contract.Envelope{
				Version: contract.Version, Type: contract.TypeConversationResponse,
				SentAt: time.Now().UTC(),
				Payload: mustJSON(contract.ConversationResponse{
					ConversationID: req.ConversationID,
					Delivered:      ca.deliverOK,
					Status:         status,
					Thread:         thread,
					Answers:        answers,
				}),
			})
		default:
			http.Error(w, "unsupported type", http.StatusBadRequest)
		}
	}))
	t.Cleanup(ca.server.Close)
	return ca
}

// newConversationServer boots the API with in-memory membership, roster,
// and conversation stores plus the contract fake agent.
func newConversationServer(t *testing.T) (*httptest.Server, *bytes.Buffer, *conversationAgent) {
	t.Helper()
	agent := newConversationAgent(t)
	memStore := membership.NewMemoryStore()
	if err := memStore.PublishTOS(t.Context(), membership.TOSVersion{Version: "1.0", Body: "Be good."}); err != nil {
		t.Fatalf("seed TOS: %v", err)
	}
	provisionAdmin(t, memStore, "admin@thresh.dev")
	mail := &bytes.Buffer{}
	srv := httptest.NewServer(api.NewHandler(api.Deps{
		Agent:   agentclient.New(agent.server.URL),
		Version: "test-backend",
		Store:   memStore,
		Mail:    mailsink.NewLogSink(mail),
		Roster:  &api.RosterDeps{Store: roster.NewMemoryStore()},
		Conversations: &api.ConversationsDeps{
			Store:         conversations.NewMemoryStore(),
			PublicBaseURL: "https://thresh.dev",
		},
	}))
	t.Cleanup(srv.Close)
	return srv, mail, agent
}

// addRosterMember adds one member and returns its ID.
func addRosterMember(t *testing.T, srv *httptest.Server, token, name, contact string) string {
	t.Helper()
	code, doc := postWithToken(t, srv, "/api/v1/members", token, map[string]any{
		"display_name": name, "contact": contact,
	})
	if code != http.StatusCreated {
		t.Fatalf("add member = %d (doc: %v)", code, doc)
	}
	return doc["member"].(map[string]any)["id"].(string)
}

func TestOrgMaintainsRosterOverAPI(t *testing.T) {
	srv, mail, _ := newConversationServer(t)
	_, orgToken := newApprovedOrg(t, srv, mail, "roster-org@example.org")

	id := addRosterMember(t, srv, orgToken, "Siobhán", "whatsapp:+353860000001")

	// The roster lists it.
	code, doc := getWithToken(t, srv, "/api/v1/members", orgToken)
	if code != http.StatusOK || len(doc["members"].([]any)) != 1 {
		t.Fatalf("list members = %d (doc: %v)", code, doc)
	}
	if doc["members"].([]any)[0].(map[string]any)["contact"] != "whatsapp:+353860000001" {
		t.Errorf("listed contact = %v", doc["members"].([]any)[0])
	}

	// Update the contact point (the Member switched channels).
	code, doc = doWithToken(t, http.MethodPatch, srv, "/api/v1/members/"+id, orgToken, map[string]any{
		"display_name": "Siobhán", "contact": "telegram:99112233",
	})
	if code != http.StatusOK {
		t.Fatalf("update member = %d (doc: %v)", code, doc)
	}
	if doc["member"].(map[string]any)["contact"] != "telegram:99112233" {
		t.Errorf("updated contact = %v", doc["member"].(map[string]any)["contact"])
	}

	// Remove.
	code, _ = doWithToken(t, http.MethodDelete, srv, "/api/v1/members/"+id, orgToken, nil)
	if code != http.StatusOK {
		t.Fatalf("delete member = %d", code)
	}
	code, doc = getWithToken(t, srv, "/api/v1/members", orgToken)
	if len(doc["members"].([]any)) != 0 {
		t.Errorf("roster not empty after delete: %v", doc)
	}
}

func TestRosterRefusesBadContactPoints(t *testing.T) {
	srv, mail, _ := newConversationServer(t)
	_, orgToken := newApprovedOrg(t, srv, mail, "roster-org2@example.org")

	// The format rule is the channel prefix (whatsapp/telegram/email/web
	// + non-empty address) — address syntax belongs to the channel that
	// delivers, not the roster that stores it.
	for _, contact := range []string{"+353860000001", "smoke:signals", "telegram:"} {
		if code, doc := postWithToken(t, srv, "/api/v1/members", orgToken, map[string]any{
			"display_name": "S", "contact": contact,
		}); code != http.StatusBadRequest {
			t.Errorf("contact %q: add = %d, want 400 (doc: %v)", contact, code, doc)
		}
	}
}

func TestOnlyOrganizationsTouchTheRoster(t *testing.T) {
	srv, mail, _ := newConversationServer(t)
	_, consumerToken := newConsumer(t, srv, mail, "no-roster@example.org")

	if code, _ := postWithToken(t, srv, "/api/v1/members", consumerToken, map[string]any{
		"display_name": "S", "contact": "telegram:1",
	}); code != http.StatusForbidden {
		t.Errorf("consumer add member = %d, want 403", code)
	}
	if code, _ := getWithToken(t, srv, "/api/v1/members", consumerToken); code != http.StatusForbidden {
		t.Errorf("consumer list members = %d, want 403", code)
	}
}

func TestStrangersRosterReadsAsNotFound(t *testing.T) {
	srv, mail, _ := newConversationServer(t)
	_, orgToken := newApprovedOrg(t, srv, mail, "roster-owner@example.org")
	_, strangerToken := newApprovedOrg(t, srv, mail, "roster-stranger@example.org")

	id := addRosterMember(t, srv, orgToken, "S", "email:s@farm.ie")

	if code, _ := getWithToken(t, srv, "/api/v1/members/"+id, strangerToken); code != http.StatusNotFound {
		t.Errorf("stranger get member = %d, want 404", code)
	}
	if code, _ := doWithToken(t, http.MethodDelete, srv, "/api/v1/members/"+id, strangerToken, nil); code != http.StatusNotFound {
		t.Errorf("stranger delete member = %d, want 404", code)
	}
}

func TestOrgOpensConversationAndMemberResumesByToken(t *testing.T) {
	srv, mail, agent := newConversationServer(t)
	_, orgToken := newApprovedOrg(t, srv, mail, "conv-org@example.org")
	memberID := addRosterMember(t, srv, orgToken, "Siobhán", "whatsapp:+353860000001")

	// Open the conversation: the agent asks question one over WhatsApp.
	code, doc := postWithToken(t, srv, "/api/v1/conversations", orgToken, map[string]any{
		"member_id": memberID,
		"topic":     "this season's cropping",
		"questions": []string{"What crop did you plant this season?", "How many hectares are under it?"},
	})
	if code != http.StatusCreated {
		t.Fatalf("open conversation = %d (doc: %v)", code, doc)
	}
	conv := doc["conversation"].(map[string]any)
	token := conv["resume_token"].(string)
	if token == "" {
		t.Fatal("resume_token is empty, want the Member's capability")
	}
	if conv["status"] != "awaiting_member" {
		t.Errorf("status = %v, want awaiting_member", conv["status"])
	}
	if agent.gotStart == nil || agent.gotStart.ResumeURL == "" || agent.gotStart.Contact != "whatsapp:+353860000001" {
		t.Errorf("agent saw %+v, want the start with contact and resumable URL", agent.gotStart)
	}

	// The org's list carries the conversation.
	code, doc = getWithToken(t, srv, "/api/v1/conversations", orgToken)
	if code != http.StatusOK || len(doc["conversations"].([]any)) != 1 {
		t.Fatalf("list conversations = %d (doc: %v)", code, doc)
	}

	// The Member replies — days later, by token, no login.
	code, doc = postJSON(t, srv, "/api/v1/member/reply", map[string]any{
		"token": token, "message": "Spring barley",
	})
	if code != http.StatusOK {
		t.Fatalf("member reply = %d (doc: %v)", code, doc)
	}
	conv = doc["conversation"].(map[string]any)
	if conv["status"] != "awaiting_member" {
		t.Errorf("status after first reply = %v, want awaiting_member (one question left)", conv["status"])
	}
	if agent.gotReply == nil || agent.gotReply.Message != "Spring barley" {
		t.Errorf("agent saw %+v, want the reply turn", agent.gotReply)
	}

	// Second answer exhausts the question list: completed, both answers kept.
	code, doc = postJSON(t, srv, "/api/v1/member/reply", map[string]any{
		"token": token, "message": "Twelve hectares",
	})
	if code != http.StatusOK {
		t.Fatalf("member reply 2 = %d (doc: %v)", code, doc)
	}
	conv = doc["conversation"].(map[string]any)
	if conv["status"] != "completed" {
		t.Errorf("status after second reply = %v, want completed", conv["status"])
	}
	if answers := conv["answers"].([]any); len(answers) != 2 {
		t.Errorf("answers = %v, want both gathered answers", answers)
	}

	// The closed conversation takes nothing more.
	if code, _ := postJSON(t, srv, "/api/v1/member/reply", map[string]any{
		"token": token, "message": "hello again",
	}); code != http.StatusConflict {
		t.Errorf("reply after completion = %d, want 409", code)
	}
}

func TestMemberReplyWithUnknownTokenIs404(t *testing.T) {
	srv, _, _ := newConversationServer(t)
	if code, _ := postJSON(t, srv, "/api/v1/member/reply", map[string]any{
		"token": "no-such-token", "message": "hello",
	}); code != http.StatusNotFound {
		t.Errorf("unknown token = %d, want 404", code)
	}
}

// TestResumeLinkOpensTheConversationForMember: GET on the resumable link
// is the Member's redemption surface — the token alone (no account, no
// login) shows the conversation so far, questions asked and answers given.
func TestResumeLinkOpensTheConversationForMember(t *testing.T) {
	srv, mail, _ := newConversationServer(t)
	_, orgToken := newApprovedOrg(t, srv, mail, "resume-org@example.org")
	memberID := addRosterMember(t, srv, orgToken, "Siobhán", "whatsapp:+353860000001")
	_, doc := postWithToken(t, srv, "/api/v1/conversations", orgToken, map[string]any{
		"member_id": memberID, "topic": "cropping", "questions": []string{"q1", "q2"},
	})
	conv := doc["conversation"].(map[string]any)
	token := conv["resume_token"].(string)

	// One answer in — the partial state the resumable link exists for.
	code, doc := postJSON(t, srv, "/api/v1/member/reply", map[string]any{
		"token": token, "message": "Spring barley",
	})
	if code != http.StatusOK {
		t.Fatalf("member reply = %d (doc: %v)", code, doc)
	}

	// The link redeems: GET /member/resume/{token} — no auth header.
	code, doc = getJSON(t, srv, "/api/v1/member/resume/"+token, "")
	if code != http.StatusOK {
		t.Fatalf("resume link = %d (doc: %v)", code, doc)
	}
	resumed := doc["conversation"].(map[string]any)
	if resumed["id"] != conv["id"] {
		t.Errorf("resumed conversation id = %v, want %v", resumed["id"], conv["id"])
	}
	if answers := resumed["answers"].([]any); len(answers) != 1 || answers[0] != "Spring barley" {
		t.Errorf("resumed answers = %v, want the partial answer", answers)
	}

	// An unknown token reads as 404: existence is not disclosed.
	if code, _ := getJSON(t, srv, "/api/v1/member/resume/no-such-token", ""); code != http.StatusNotFound {
		t.Errorf("unknown resume token = %d, want 404", code)
	}
}

func TestStopWordStopsTheConversation(t *testing.T) {
	srv, mail, agent := newConversationServer(t)
	_, orgToken := newApprovedOrg(t, srv, mail, "stop-org@example.org")
	memberID := addRosterMember(t, srv, orgToken, "S", "email:s@farm.ie")
	_, doc := postWithToken(t, srv, "/api/v1/conversations", orgToken, map[string]any{
		"member_id": memberID, "topic": "t", "questions": []string{"q1", "q2"},
	})
	token := doc["conversation"].(map[string]any)["resume_token"].(string)

	// The Member opts out: the conversation stops without another turn.
	code, doc := postJSON(t, srv, "/api/v1/member/reply", map[string]any{
		"token": token, "message": "STOP",
	})
	if code != http.StatusOK {
		t.Fatalf("stop reply = %d (doc: %v)", code, doc)
	}
	if doc["conversation"].(map[string]any)["status"] != "stopped" {
		t.Errorf("status = %v, want stopped", doc["conversation"].(map[string]any)["status"])
	}
	if agent.gotReply != nil {
		t.Error("the agent was consulted after the Member opted out")
	}
}

func TestConversationWithUnknownMemberIs404(t *testing.T) {
	srv, mail, _ := newConversationServer(t)
	_, orgToken := newApprovedOrg(t, srv, mail, "no-member@example.org")

	if code, doc := postWithToken(t, srv, "/api/v1/conversations", orgToken, map[string]any{
		"member_id": "ghost", "topic": "t", "questions": []string{"q1"},
	}); code != http.StatusNotFound {
		t.Errorf("unknown member = %d, want 404 (doc: %v)", code, doc)
	}
}
