package conversations_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
	"github.com/syaikhipin/entrustdn-final/backend/internal/conversations"
)

// The Member conversation record (ticket 11): the backend owns the durable
// thread and its status; the agent holds the LangGraph checkpoint. Every
// turn round-trips the contract (conversation.start / conversation.reply);
// every turn the agent completes is appended to the thread. The Member has
// no account — the resumable token is their capability.

// fakeAgent is the Seam 2 double: it appends the member's message as an
// answer, asks the next scripted question, and reports the scripted status.
type fakeAgent struct {
	gotStart    *contract.ConversationStartRequest
	gotReply    *contract.ConversationReplyRequest
	nextMessage string
	status      string
	answers     []string
}

func (f *fakeAgent) Start(_ context.Context, req contract.ConversationStartRequest) (contract.ConversationResponse, error) {
	f.gotStart = &req
	return contract.ConversationResponse{
		ConversationID: req.ConversationID,
		Delivered:      true,
		Status:         f.status,
		Thread: []contract.ThreadMessage{
			{Role: "agent", Body: f.nextMessage},
		},
		Answers: f.answers,
	}, nil
}

func (f *fakeAgent) Reply(_ context.Context, req contract.ConversationReplyRequest) (contract.ConversationResponse, error) {
	f.gotReply = &req
	thread := append(req.Thread, contract.ThreadMessage{Role: "member", Body: req.Message})
	return contract.ConversationResponse{
		ConversationID: req.ConversationID,
		Delivered:      true,
		Status:         f.status,
		Thread:         append(thread, contract.ThreadMessage{Role: "agent", Body: f.nextMessage}),
		Answers:        f.answers,
	}, nil
}

func newStartedConversation(t *testing.T) (*conversations.Service, *fakeAgent, conversations.Conversation) {
	t.Helper()
	agent := &fakeAgent{
		nextMessage: "What crop did you plant this season?",
		status:      contract.ConversationAwaitingMember,
	}
	svc := conversations.NewService(conversations.NewMemoryStore(), agent, "https://thresh.dev")
	conv, err := svc.Start(t.Context(), conversations.Start{
		OrgID:      "org-1",
		MemberID:   "member-1",
		MemberName: "Siobhán",
		Contact:    "whatsapp:+353860000001",
		Topic:      "this season's cropping",
		Questions:  []string{"What crop did you plant this season?", "How many hectares?"},
		RequestID:  "req-1",
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	return svc, agent, conv
}

func TestStartOpensTheThreadOverTheChannel(t *testing.T) {
	_, agent, conv := newStartedConversation(t)

	if conv.Status != conversations.StatusAwaitingMember {
		t.Errorf("Status = %q, want awaiting_member — the agent asked and is now paused", conv.Status)
	}
	if len(conv.Thread) != 1 || conv.Thread[0].Role != "agent" {
		t.Errorf("Thread = %+v, want the agent's opening question", conv.Thread)
	}
	if agent.gotStart == nil || agent.gotStart.MemberName != "Siobhán" {
		t.Errorf("agent saw %+v, want the start request", agent.gotStart)
	}
	// The resumable link rode with the start: the Member has no account,
	// the token is the capability.
	if agent.gotStart.ResumeURL == "" {
		t.Error("ResumeURL is empty, want the Member's resumable link")
	}
	if !strings.Contains(agent.gotStart.ResumeURL, conv.ResumeToken) {
		t.Errorf("ResumeURL %q does not carry the conversation's token %q", agent.gotStart.ResumeURL, conv.ResumeToken)
	}
}

func TestMemberReplyResumesMidThread(t *testing.T) {
	svc, agent, conv := newStartedConversation(t)
	agent.nextMessage = "How many hectares are under it?"
	agent.answers = []string{"spring barley"}

	got, err := svc.MemberReply(t.Context(), conv.ResumeToken, "Spring barley, twelve hectares")
	if err != nil {
		t.Fatalf("MemberReply() error = %v", err)
	}

	if agent.gotReply == nil {
		t.Fatal("agent never received the reply turn")
	}
	if agent.gotReply.Message != "Spring barley, twelve hectares" {
		t.Errorf("agent saw message %q", agent.gotReply.Message)
	}
	// The thread rides along: the agent can rebuild from a cold checkpoint.
	if len(agent.gotReply.Thread) != 1 || agent.gotReply.Thread[0].Role != "agent" {
		t.Errorf("agent saw thread %+v, want the prior thread", agent.gotReply.Thread)
	}
	if len(got.Thread) != 3 {
		t.Errorf("Thread has %d turns, want 3 (ask → reply → next ask)", len(got.Thread))
	}
	if len(got.Answers) != 1 || got.Answers[0] != "spring barley" {
		t.Errorf("Answers = %+v, want the agent's reported answer", got.Answers)
	}
}

func TestMemberReplyAdoptsTheAgentsReportedAnswers(t *testing.T) {
	// The contract carries "the answers gathered so far" — the agent's
	// cumulative list, like the thread. The record adopts it wholesale:
	// appending per turn would double-count every answer after the first
	// and trip the over-survey cap early.
	svc, agent, conv := newStartedConversation(t)

	agent.answers = []string{"spring barley"}
	if _, err := svc.MemberReply(t.Context(), conv.ResumeToken, "Spring barley"); err != nil {
		t.Fatalf("first reply: %v", err)
	}
	agent.answers = []string{"spring barley", "twelve hectares"}
	got, err := svc.MemberReply(t.Context(), conv.ResumeToken, "Twelve hectares")
	if err != nil {
		t.Fatalf("second reply: %v", err)
	}
	want := []string{"spring barley", "twelve hectares"}
	if len(got.Answers) != len(want) || got.Answers[0] != want[0] || got.Answers[1] != want[1] {
		t.Errorf("Answers = %+v, want %v adopted wholesale", got.Answers, want)
	}
}

func TestMemberReplyByResumeTokenOnly(t *testing.T) {
	// The Member has no account: the token alone identifies the
	// conversation. An unknown token reads as not-found, never as an
	// account error.
	svc, _, conv := newStartedConversation(t)
	if _, err := svc.MemberReply(t.Context(), "no-such-token", "hello"); !errors.Is(err, conversations.ErrNotFound) {
		t.Errorf("unknown token error = %v, want ErrNotFound", err)
	}
	if _, err := svc.MemberReply(t.Context(), conv.ResumeToken, "hello"); err != nil {
		t.Errorf("good token error = %v, want nil", err)
	}
}

func TestConversationCompletesWhenAgentSaysSo(t *testing.T) {
	svc, agent, conv := newStartedConversation(t)
	agent.status = contract.ConversationCompleted
	agent.nextMessage = "Thanks — that's everything I need."
	agent.answers = []string{"spring barley", "twelve hectares"}

	got, err := svc.MemberReply(t.Context(), conv.ResumeToken, "12 ha, and a good year")
	if err != nil {
		t.Fatalf("MemberReply() error = %v", err)
	}
	if got.Status != conversations.StatusCompleted {
		t.Errorf("Status = %q, want completed", got.Status)
	}

	// A completed conversation takes no further replies: the survey is over.
	if _, err := svc.MemberReply(t.Context(), conv.ResumeToken, "one more thing"); !errors.Is(err, conversations.ErrClosed) {
		t.Errorf("reply after completion = %v, want ErrClosed", err)
	}
}

func TestOverSurveyProtectionStopsWhenMemberDeclines(t *testing.T) {
	// The Member says stop: the conversation must stop — no further
	// questions, regardless of the agent's scripted status.
	svc, agent, conv := newStartedConversation(t)
	agent.status = contract.ConversationAwaitingMember // the agent would keep asking
	agent.nextMessage = "And your fertilizer plan?"

	got, err := svc.MemberReply(t.Context(), conv.ResumeToken, "STOP")
	if err != nil {
		t.Fatalf("MemberReply(STOP) error = %v", err)
	}
	if got.Status != conversations.StatusStopped {
		t.Errorf("Status = %q, want stopped — the Member opted out", got.Status)
	}

	if _, err := svc.MemberReply(t.Context(), conv.ResumeToken, "changed my mind"); !errors.Is(err, conversations.ErrClosed) {
		t.Errorf("reply after stop = %v, want ErrClosed", err)
	}
}

func TestOverSurveyProtectionStopsAfterTheQuestionBudget(t *testing.T) {
	// Quality trigger (ticket 12's defaults land here): a Member answers at
	// most MaxQuestionsPerConversation questions; the agent may not keep a
	// Member on the hook past that.
	svc := conversations.NewService(conversations.NewMemoryStore(), &loopAgent{}, "https://thresh.dev")
	conv, err := svc.Start(t.Context(), conversations.Start{
		OrgID: "org-1", MemberID: "m", MemberName: "M", Contact: "telegram:1",
		Topic: "t", Questions: []string{"q1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < conversations.MaxQuestionsPerConversation; i++ {
		got, err := svc.MemberReply(t.Context(), conv.ResumeToken, "an answer")
		if err != nil {
			t.Fatalf("answer %d: MemberReply() error = %v", i+1, err)
		}
		if got.Status == conversations.StatusStopped {
			t.Fatalf("stopped after only %d answers, want the cap", i+1)
		}
	}
	_, err = svc.MemberReply(t.Context(), conv.ResumeToken, "yet another answer")
	if !errors.Is(err, conversations.ErrOverSurveyed) {
		t.Errorf("answer past the cap = %v, want ErrOverSurveyed", err)
	}
}

// loopAgent always asks another question — the pressure the cap exists for.
type loopAgent struct{}

func (loopAgent) Start(_ context.Context, req contract.ConversationStartRequest) (contract.ConversationResponse, error) {
	return contract.ConversationResponse{
		ConversationID: req.ConversationID, Delivered: true,
		Status: contract.ConversationAwaitingMember,
		Thread: []contract.ThreadMessage{{Role: "agent", Body: "another question"}},
	}, nil
}

func (loopAgent) Reply(_ context.Context, req contract.ConversationReplyRequest) (contract.ConversationResponse, error) {
	// The contract carries every answer gathered so far — count the member
	// turns on the thread, plus the one riding this reply.
	answers := make([]string, 0, len(req.Thread)+1)
	for _, t := range req.Thread {
		if t.Role == "member" {
			answers = append(answers, t.Body)
		}
	}
	answers = append(answers, req.Message)
	return contract.ConversationResponse{
		ConversationID: req.ConversationID, Delivered: true,
		Status: contract.ConversationAwaitingMember,
		Thread: append(req.Thread,
			contract.ThreadMessage{Role: "member", Body: req.Message},
			contract.ThreadMessage{Role: "agent", Body: "another question"}),
		Answers: answers,
	}, nil
}
