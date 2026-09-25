package agentclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/agentclient"
	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
)

// The conversation round trips (ticket 11): the backend opens a Member
// conversation (conversation.start) and delivers Member replies
// (conversation.reply); the agent answers conversation.response naming the
// conversation and acknowledging delivery. A response the agent did not
// actually deliver on the medium must be refused here, before the backend
// ever records the turn.

// conversationAgent answers both conversation envelopes, recording what it
// received; respond builds the reply envelope.
func conversationAgent(t *testing.T, seen *contract.Envelope, respond func(w http.ResponseWriter, in contract.Envelope)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env contract.Envelope
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			http.Error(w, "bad envelope", http.StatusBadRequest)
			return
		}
		if env.Type != contract.TypeConversationStartRequest && env.Type != contract.TypeConversationReplyRequest {
			http.Error(w, "want conversation request", http.StatusBadRequest)
			return
		}
		*seen = env
		respond(w, env)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeConversationResponse(w http.ResponseWriter, resp contract.ConversationResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(contract.Envelope{
		Version: contract.Version,
		Type:    contract.TypeConversationResponse,
		SentAt:  time.Now().UTC(),
		Payload: mustJSON(resp),
	})
}

func TestClientStartsConversation(t *testing.T) {
	var seen contract.Envelope
	srv := conversationAgent(t, &seen, func(w http.ResponseWriter, in contract.Envelope) {
		var req contract.ConversationStartRequest
		if err := in.PayloadInto(&req); err != nil {
			t.Errorf("bad start payload: %v", err)
		}
		writeConversationResponse(w, contract.ConversationResponse{
			ConversationID: req.ConversationID,
			Delivered:      true,
			Status:         contract.ConversationAwaitingMember,
			Thread: []contract.ThreadMessage{{
				Role: "agent", Body: "What crop did you plant this season?",
			}},
			Answers: []string{},
		})
	})

	resp, err := agentclient.New(srv.URL).StartConversation(context.Background(), contract.ConversationStartRequest{
		ConversationID: "conv-77",
		MemberName:     "Siobhán",
		Contact:        "whatsapp:+353860000001",
		Topic:          "this season's cropping",
		Questions:      []string{"What crop did you plant this season?"},
		ResumeURL:      "https://thresh.dev/member/resume/tok",
	})
	if err != nil {
		t.Fatalf("StartConversation() error = %v", err)
	}
	if seen.Type != contract.TypeConversationStartRequest {
		t.Errorf("agent saw %q, want a conversation.start.request", seen.Type)
	}
	if resp.ConversationID != "conv-77" || resp.Status != contract.ConversationAwaitingMember || !resp.Delivered {
		t.Errorf("response = %+v, want conv-77 awaiting_member delivered", resp)
	}
}

func TestClientDeliversMemberReply(t *testing.T) {
	var seen contract.Envelope
	srv := conversationAgent(t, &seen, func(w http.ResponseWriter, in contract.Envelope) {
		var req contract.ConversationReplyRequest
		if err := in.PayloadInto(&req); err != nil {
			t.Errorf("bad reply payload: %v", err)
		}
		writeConversationResponse(w, contract.ConversationResponse{
			ConversationID: req.ConversationID,
			Delivered:      true,
			Status:         contract.ConversationCompleted,
			Thread: append(req.Thread, contract.ThreadMessage{
				Role: "member", Body: req.Message,
			}),
			Answers: []string{req.Message},
		})
	})

	resp, err := agentclient.New(srv.URL).Converse(context.Background(), contract.ConversationReplyRequest{
		ConversationID: "conv-77",
		Message:        "spring barley, 12 ha",
		Thread: []contract.ThreadMessage{
			{Role: "agent", Body: "What crop did you plant this season?"},
		},
	})
	if err != nil {
		t.Fatalf("Converse() error = %v", err)
	}
	if seen.Type != contract.TypeConversationReplyRequest {
		t.Errorf("agent saw %q, want a conversation.reply.request", seen.Type)
	}
	if resp.Status != contract.ConversationCompleted || len(resp.Answers) != 1 {
		t.Errorf("response = %+v, want completed with one answer", resp)
	}
}

func TestClientRefusesUndeliveredConversationResponse(t *testing.T) {
	// The agent answered but admits the medium does not have the message:
	// that turn must not pass as delivered.
	var seen contract.Envelope
	srv := conversationAgent(t, &seen, func(w http.ResponseWriter, in contract.Envelope) {
		var req contract.ConversationStartRequest
		_ = in.PayloadInto(&req)
		writeConversationResponse(w, contract.ConversationResponse{
			ConversationID: req.ConversationID,
			Delivered:      false,
			Status:         contract.ConversationAwaitingMember,
		})
	})

	_, err := agentclient.New(srv.URL).StartConversation(context.Background(), contract.ConversationStartRequest{
		ConversationID: "conv-78",
		MemberName:     "Member",
		Contact:        "email:member@farm.ie",
		Topic:          "crops",
		Questions:      []string{"q1"},
	})
	if err == nil {
		t.Fatal("StartConversation() accepted an undelivered response = nil error, want error")
	}
}

func TestClientRefusesConversationResponseNamingAnotherConversation(t *testing.T) {
	var seen contract.Envelope
	srv := conversationAgent(t, &seen, func(w http.ResponseWriter, in contract.Envelope) {
		writeConversationResponse(w, contract.ConversationResponse{
			ConversationID: "some-other-conv",
			Delivered:      true,
			Status:         contract.ConversationAwaitingMember,
		})
	})

	_, err := agentclient.New(srv.URL).StartConversation(context.Background(), contract.ConversationStartRequest{
		ConversationID: "conv-79",
		MemberName:     "Member",
		Contact:        "telegram:42",
		Topic:          "crops",
		Questions:      []string{"q1"},
	})
	if err == nil {
		t.Fatal("StartConversation() accepted a response for another conversation = nil error, want error")
	}
}

func TestClientTimesOutAgainstADeadAgent(t *testing.T) {
	// The pause-for-days loop must treat an unreachable agent as a failed
	// turn, not hang the backend.
	var seen contract.Envelope
	srv := conversationAgent(t, &seen, func(w http.ResponseWriter, in contract.Envelope) {
		time.Sleep(50 * time.Millisecond)
		writeConversationResponse(w, contract.ConversationResponse{})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := agentclient.New(srv.URL).StartConversation(ctx, contract.ConversationStartRequest{
		ConversationID: "conv-80", MemberName: "m", Contact: "telegram:1",
		Topic: "t", Questions: []string{"q"},
	}); err == nil {
		t.Fatal("StartConversation() returned nil error against a too-slow agent")
	}
}
