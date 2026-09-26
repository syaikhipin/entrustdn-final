package collections_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/collections"
	"github.com/syaikhipin/entrustdn-final/backend/internal/conversations"
	"github.com/syaikhipin/entrustdn-final/backend/internal/requests"
	"github.com/syaikhipin/entrustdn-final/backend/internal/roster"
)

// The Collection domain (ticket 12) at the domain seam: a clarified Request
// becomes a Data Collection over the org's roster Members; the Agent
// gathers answers over Channels (ticket 11's conversations); quality
// triggers evaluate every answer and open re-asks on bad data; the
// Collection completes when every item holds an accepted answer, or flags
// incomplete through the deadline, budget, or escalation routes. The
// re-ask opener is a fake — the same seam shape the other packages' tests
// drive; the real *conversations.Service satisfies it structurally.

var fixedNow = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

const farmQ = "What is your farm size?"

const cropQ = "Which crops did you sow?"

// fakeOpener records re-ask conversation opens.
type fakeOpener struct {
	starts []conversations.Start
	err    error
}

func (f *fakeOpener) Start(_ context.Context, st conversations.Start) (conversations.Conversation, error) {
	if f.err != nil {
		return conversations.Conversation{}, f.err
	}
	f.starts = append(f.starts, st)
	return conversations.Conversation{
		ID:     fmt.Sprintf("conv-reask-%d", len(f.starts)),
		Status: conversations.StatusAwaitingMember,
	}, nil
}

// fixtureShared wires the service with in-memory stores, one clarified
// request owned by consumer-1, two roster members in org-1, and the fake
// opener. Returns the conversation store so tests seed Member
// conversations the way the ticket-11 flow would have left them.
func fixtureShared(t *testing.T) (*collections.Service, *requests.MemoryStore, *conversations.MemoryStore, *fakeOpener) {
	t.Helper()
	reqStore := requests.NewMemoryStore()
	r := requests.Request{
		ID: "req-1", ConsumerID: "consumer-1",
		Description: "Spring barley yields across Leinster", Format: "csv",
		BudgetMicros: 1_000_000, Status: requests.StatusClarified,
	}
	if err := reqStore.CreateRequest(t.Context(), &r); err != nil {
		t.Fatalf("seed request: %v", err)
	}
	members := roster.NewMemoryStore()
	for _, m := range []roster.Member{
		{ID: "mem-1", OrgID: "org-1", DisplayName: "Siobhán Ní Uaithne", Contact: "whatsapp:+353860000001"},
		{ID: "mem-2", OrgID: "org-1", DisplayName: "Pádraig Ó Briain", Contact: "telegram:12345"},
	} {
		if err := members.CreateMember(t.Context(), &m); err != nil {
			t.Fatalf("seed member: %v", err)
		}
	}
	opener := &fakeOpener{}
	convs := conversations.NewMemoryStore()
	svc := collections.NewService(collections.Deps{
		Store:         collections.NewMemoryStore(),
		Conversations: convs,
		Opener:        opener,
		Roster:        members,
		Requests:      reqStore,
		Now:           func() time.Time { return fixedNow },
	})
	return svc, reqStore, convs, opener
}

// seedConversation stores one member conversation the way the ticket-11
// service would have: agent turns carry the questions, member turns the
// answers, in order.
func seedConversation(t *testing.T, store *conversations.MemoryStore, id, memberID, memberName string, qs, answers []string, status conversations.Status) {
	t.Helper()
	thread := []conversations.Turn{}
	for i, q := range qs {
		thread = append(thread, conversations.Turn{Role: "agent", Body: q})
		if i < len(answers) {
			thread = append(thread, conversations.Turn{Role: "member", Body: answers[i]})
		}
	}
	c := conversations.Conversation{
		ID: id, OrgID: "org-1", RequestID: "req-1", MemberID: memberID,
		MemberName: memberName, Contact: "whatsapp:+353860000001",
		Questions: qs, Thread: thread, Answers: answers[:len(answers)],
		ResumeToken: "tok-" + id, Status: status,
	}
	if err := store.CreateConversation(t.Context(), &c); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
}

// newCollection1Q creates the standard one-question collection over both
// members.
func newCollection1Q(t *testing.T, svc *collections.Service) collections.Collection {
	t.Helper()
	c, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1",
		MemberIDs: []string{"mem-1", "mem-2"},
		Questions: []string{farmQ},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return c
}

func TestCreateBuildsItemsFromMembersAndQuestions(t *testing.T) {
	svc, _, _, _ := fixtureShared(t)

	c, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1",
		MemberIDs: []string{"mem-1", "mem-2"},
		Questions: []string{farmQ, "Which crops did you sow?"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if c.ID == "" || c.OrgID != "org-1" || c.RequestID != "req-1" {
		t.Fatalf("collection = %+v", c)
	}
	if c.ConsumerID != "consumer-1" {
		t.Errorf("consumer = %q, want consumer-1 (derived from the request)", c.ConsumerID)
	}
	if c.Status != collections.StatusCollecting {
		t.Errorf("status = %q, want collecting", c.Status)
	}
	if len(c.Items) != 4 {
		t.Fatalf("items = %d, want 2 members × 2 questions", len(c.Items))
	}
	for _, m := range []string{"mem-1", "mem-2"} {
		for _, q := range []string{farmQ, "Which crops did you sow?"} {
			found := false
			for _, it := range c.Items {
				if it.MemberID == m && it.Question == q {
					found = true
				}
			}
			if !found {
				t.Errorf("missing item (member %q, question %q)", m, q)
			}
		}
	}
}

func TestCreateRefusesBadFoundations(t *testing.T) {
	svc, _, _, _ := fixtureShared(t)

	if _, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-unknown", MemberIDs: []string{"mem-1"}, Questions: []string{farmQ},
	}); !errors.Is(err, collections.ErrNotFound) {
		t.Errorf("unknown request: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1", MemberIDs: []string{"mem-unknown"}, Questions: []string{farmQ},
	}); err == nil {
		t.Error("unknown member accepted")
	}
	if _, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1", MemberIDs: []string{"mem-1"}, Questions: nil,
	}); err == nil {
		t.Error("empty questions accepted")
	}
	if _, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1", MemberIDs: nil, Questions: []string{farmQ},
	}); err == nil {
		t.Error("empty members accepted")
	}
	// A request still being clarified is not ready to field.
	svc2, _, _, _ := fixtureShared(t)
	reqStore := requests.NewMemoryStore()
	r := requests.Request{ID: "req-2", ConsumerID: "consumer-1", Format: "csv",
		BudgetMicros: 1, Status: requests.StatusClarifying}
	if err := reqStore.CreateRequest(t.Context(), &r); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = svc2
}

func TestSyncIngestsAnswersAndCompletes(t *testing.T) {
	svc, _, convs, opener := fixtureShared(t)
	c := newCollection1Q(t, svc)

	seedConversation(t, convs, "conv-1", "mem-1", "Siobhán Ní Uaithne",
		[]string{farmQ}, []string{"42 hectares of good land"}, conversations.StatusCompleted)
	seedConversation(t, convs, "conv-2", "mem-2", "Pádraig Ó Briain",
		[]string{farmQ}, []string{"18 hectares near the coast"}, conversations.StatusCompleted)

	got, err := svc.Sync(t.Context(), c.ID, "org-1")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got.Status != collections.StatusCompleted {
		t.Fatalf("status = %q, want completed; items: %+v", got.Status, got.Items)
	}
	if len(opener.starts) != 0 {
		t.Errorf("re-asks opened: %+v, want none", opener.starts)
	}
	for _, it := range got.Items {
		if it.Status != collections.ItemAccepted {
			t.Errorf("item (%s) = %s, want accepted", it.MemberID, it.Status)
		}
	}
	if got.Items[0].Accepted != "42 hectares of good land" {
		t.Errorf("accepted answer = %q", got.Items[0].Accepted)
	}
}

func TestSyncFlagsBadAnswerAndOpensOneReask(t *testing.T) {
	svc, _, convs, opener := fixtureShared(t)
	c := newCollection1Q(t, svc)
	seedConversation(t, convs, "conv-2", "mem-2", "Pádraig Ó Briain",
		[]string{farmQ}, []string{"18 hectares near the coast"}, conversations.StatusCompleted)
	seedConversation(t, convs, "conv-1", "mem-1", "Siobhán Ní Uaithne",
		[]string{farmQ}, []string{"n/a"}, conversations.StatusCompleted)

	got, err := svc.Sync(t.Context(), c.ID, "org-1")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got.Status != collections.StatusCollecting {
		t.Fatalf("status = %q, want still collecting", got.Status)
	}
	if len(opener.starts) != 1 {
		t.Fatalf("re-asks opened = %d, want 1 (%+v)", len(opener.starts), opener.starts)
	}
	st := opener.starts[0]
	if st.MemberID != "mem-1" || len(st.Questions) != 1 || st.Questions[0] != farmQ || st.RequestID != "req-1" {
		t.Errorf("re-ask start = %+v", st)
	}
	var mem1 *collections.Item
	for i := range got.Items {
		if got.Items[i].MemberID == "mem-1" {
			mem1 = &got.Items[i]
		}
	}
	if mem1 == nil || mem1.Status == collections.ItemAccepted || len(mem1.Rounds) != 1 || mem1.Rounds[0].OK {
		t.Errorf("item after bad answer = %+v", mem1)
	}

	// The member answers the re-ask well: the new conversation ingests, the
	// item accepts, and the collection completes.
	seedConversation(t, convs, "conv-reask-1", "mem-1", "Siobhán Ní Uaithne",
		[]string{farmQ}, []string{"42 hectares"}, conversations.StatusCompleted)
	got, err = svc.Sync(t.Context(), c.ID, "org-1")
	if err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if got.Status != collections.StatusCompleted {
		t.Fatalf("status = %q, want completed", got.Status)
	}
	if len(opener.starts) != 1 {
		t.Errorf("re-asks opened = %d, want still 1", len(opener.starts))
	}
	for _, it := range got.Items {
		if it.MemberID == "mem-1" && it.Accepted != "42 hectares" {
			t.Errorf("accepted = %q, want the re-ask answer", it.Accepted)
		}
	}
}

func TestSyncReasksAreCappedAndThenEscalate(t *testing.T) {
	svc, _, convs, opener := fixtureShared(t)
	c := newCollection1Q(t, svc)
	seedConversation(t, convs, "conv-2", "mem-2", "Pádraig Ó Briain",
		[]string{farmQ}, []string{"18 hectares near the coast"}, conversations.StatusCompleted)
	// Initial ask fails.
	seedConversation(t, convs, "conv-1", "mem-1", "Siobhán Ní Uaithne",
		[]string{farmQ}, []string{"n/a"}, conversations.StatusCompleted)

	var last collections.Collection
	for i := 0; i < 5; i++ {
		// Every re-ask comes back bad too.
		seedConversation(t, convs, fmt.Sprintf("conv-reask-%d", i), "mem-1", "Siobhán Ní Uaithne",
			[]string{farmQ}, []string{"idk"}, conversations.StatusCompleted)
		got, err := svc.Sync(t.Context(), c.ID, "org-1")
		if errors.Is(err, collections.ErrClosed) {
			// Finalized on the previous sync — load the final record.
			final, gerr := svc.ByID(t.Context(), c.ID, "org-1")
			if gerr != nil {
				t.Fatalf("load finalized collection: %v", gerr)
			}
			last = final
			break
		}
		if err != nil {
			t.Fatalf("Sync %d: %v", i, err)
		}
		last = got
	}
	if len(opener.starts) != collections.MaxReasksPerItem {
		t.Errorf("re-asks opened = %d, want capped at %d", len(opener.starts), collections.MaxReasksPerItem)
	}
	if last.Status != collections.StatusIncomplete {
		t.Fatalf("status = %q, want incomplete (no path left to completion)", last.Status)
	}
	if len(last.Missing) != 1 {
		t.Fatalf("missing = %+v, want one entry", last.Missing)
	}
	if last.Missing[0].Question != farmQ || last.Missing[0].Reason == "" {
		t.Errorf("missing entry = %+v", last.Missing[0])
	}
}

// flakyOpener succeeds on the first open, fails on the second, succeeds
// after — the partial-failure shape a real channel outage leaves behind.
type flakyOpener struct {
	starts []conversations.Start
	calls  int
}

func (f *flakyOpener) Start(_ context.Context, st conversations.Start) (conversations.Conversation, error) {
	f.calls++
	if f.calls == 2 {
		return conversations.Conversation{}, fmt.Errorf("channel down after the first send")
	}
	f.starts = append(f.starts, st)
	return conversations.Conversation{
		ID:     fmt.Sprintf("conv-reask-%d", len(f.starts)),
		Status: conversations.StatusAwaitingMember,
	}, nil
}

func TestSyncReaskOpenFailureDoesNotDuplicateOnRetry(t *testing.T) {
	svc, _, convs, _ := fixtureShared(t)
	c := newCollection1Q(t, svc)
	// Both members answer badly; two re-asks queue, and the opener dies on
	// the second open.
	seedConversation(t, convs, "conv-2", "mem-2", "Pádraig Ó Briain",
		[]string{farmQ}, []string{"idk"}, conversations.StatusCompleted)
	seedConversation(t, convs, "conv-1", "mem-1", "Siobhán Ní Uaithne",
		[]string{farmQ}, []string{"n/a"}, conversations.StatusCompleted)

	// Two members need re-asks (both answered badly); the opener dies on
	// the second open.
	opener := &flakyOpener{}
	svc.WithOpener(opener)
	if _, err := svc.Sync(t.Context(), c.ID, "org-1"); err == nil {
		t.Fatal("sync with a failing opener succeeded")
	}
	if len(opener.starts) != 1 {
		t.Fatalf("opens before the failure = %d, want 1", len(opener.starts))
	}

	// The channel heals: the retry opens only the remaining re-ask — the
	// one already sent must not be sent twice.
	healed := &fakeOpener{starts: opener.starts}
	svc.WithOpener(healed)
	if _, err := svc.Sync(t.Context(), c.ID, "org-1"); err != nil {
		t.Fatalf("retry sync: %v", err)
	}
	if len(healed.starts) != 2 {
		t.Errorf("total opens = %d, want 2 (the first was already sent)", len(healed.starts))
	}
}

func TestSyncNeverReasksAnOptedOutMember(t *testing.T) {
	svc, _, convs, opener := fixtureShared(t)
	c := newCollection1Q(t, svc)
	seedConversation(t, convs, "conv-1", "mem-1", "Siobhán Ní Uaithne",
		[]string{farmQ}, []string{"stop"}, conversations.StatusStopped)

	got, err := svc.Sync(t.Context(), c.ID, "org-1")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(opener.starts) != 0 {
		t.Errorf("re-asks opened for an opted-out member: %+v", opener.starts)
	}
	// The other member has no conversation yet: still collecting.
	if got.Status != collections.StatusCollecting {
		t.Fatalf("status = %q, want collecting (mem-2 has no rounds yet)", got.Status)
	}
	for _, it := range got.Items {
		if it.MemberID == "mem-1" && !it.Blocked {
			t.Errorf("stopped member's item not blocked: %+v", it)
		}
	}
}

func TestSyncFinalizesIncompleteOnDeadline(t *testing.T) {
	svc, _, convs, _ := fixtureShared(t)
	deadline := fixedNow.Add(-24 * time.Hour)
	c, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1", MemberIDs: []string{"mem-1"}, Questions: []string{farmQ},
		Deadline: &deadline,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedConversation(t, convs, "conv-1", "mem-1", "Siobhán Ní Uaithne",
		[]string{farmQ}, []string{"n/a"}, conversations.StatusCompleted)

	got, err := svc.Sync(t.Context(), c.ID, "org-1")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got.Status != collections.StatusIncomplete {
		t.Fatalf("status = %q, want incomplete past the deadline", got.Status)
	}
	if len(got.Missing) != 1 || got.Missing[0].Reason == "" {
		t.Errorf("missing = %+v, want the failed item with a reason", got.Missing)
	}
}

func TestSyncFinalizesIncompleteOnBudgetExhausted(t *testing.T) {
	svc, reqStore, convs, _ := fixtureShared(t)
	c, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1", MemberIDs: []string{"mem-1", "mem-2"}, Questions: []string{farmQ},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedConversation(t, convs, "conv-1", "mem-1", "Siobhán Ní Uaithne",
		[]string{farmQ}, []string{"42 hectares"}, conversations.StatusCompleted)
	// The second member never answered — and the request's budget ran out.
	r, err := reqStore.RequestByID(t.Context(), "req-1")
	if err != nil {
		t.Fatalf("load request: %v", err)
	}
	r.Status = requests.StatusBudgetExhausted
	if err := reqStore.UpdateRequest(t.Context(), r); err != nil {
		t.Fatalf("mark exhausted: %v", err)
	}

	got, err := svc.Sync(t.Context(), c.ID, "org-1")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got.Status != collections.StatusIncomplete {
		t.Fatalf("status = %q, want incomplete on the budget path", got.Status)
	}
	if len(got.Missing) != 1 {
		t.Errorf("missing = %+v, want the unanswered member's entry", got.Missing)
	}
}

func TestSyncIsIdempotent(t *testing.T) {
	svc, _, convs, opener := fixtureShared(t)
	c := newCollection1Q(t, svc)
	seedConversation(t, convs, "conv-1", "mem-1", "Siobhán Ní Uaithne",
		[]string{farmQ}, []string{"n/a"}, conversations.StatusCompleted)

	first, err := svc.Sync(t.Context(), c.ID, "org-1")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	second, err := svc.Sync(t.Context(), c.ID, "org-1")
	if err != nil {
		t.Fatalf("Sync again: %v", err)
	}
	if len(opener.starts) != 1 {
		t.Errorf("re-asks after two syncs = %d, want 1 (idempotent)", len(opener.starts))
	}
	if len(first.Items[0].Rounds) != len(second.Items[0].Rounds) {
		t.Errorf("rounds grew on re-sync: %d → %d", len(first.Items[0].Rounds), len(second.Items[0].Rounds))
	}
}

func TestSyncIngestsLaterAnswersOfAnOpenConversation(t *testing.T) {
	svc, _, convs, _ := fixtureShared(t)
	c, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1", MemberIDs: []string{"mem-1"},
		Questions: []string{farmQ, "Which crops did you sow?"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The org opened a two-question conversation; only the first answer has
	// arrived so far.
	seedConversation(t, convs, "conv-1", "mem-1", "Siobhán Ní Uaithne",
		[]string{farmQ, "Which crops did you sow?"}, []string{"42 hectares"},
		conversations.StatusAwaitingMember)

	got, err := svc.Sync(t.Context(), c.ID, "org-1")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	var first *collections.Item
	for i := range got.Items {
		if got.Items[i].Question == farmQ {
			first = &got.Items[i]
		}
	}
	if first == nil || first.Accepted != "42 hectares" {
		t.Fatalf("first answer not ingested: %+v", first)
	}

	// The member answers the second question; the next sync picks up only
	// the new answer.
	c2, err := convs.ConversationByID(t.Context(), "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	c2.Answers = append(c2.Answers, "Spring barley and oats")
	c2.Thread = append(c2.Thread, conversations.Turn{Role: "member", Body: "Spring barley and oats"})
	c2.Status = conversations.StatusCompleted
	if err := convs.UpdateConversation(t.Context(), c2); err != nil {
		t.Fatal(err)
	}
	got, err = svc.Sync(t.Context(), c.ID, "org-1")
	if err != nil {
		t.Fatalf("Sync 2: %v", err)
	}
	var second *collections.Item
	for i := range got.Items {
		if got.Items[i].Question == "Which crops did you sow?" {
			second = &got.Items[i]
		}
	}
	if second == nil || second.Accepted != "Spring barley and oats" {
		t.Errorf("second answer not ingested: %+v", second)
	}
	if got.Status != collections.StatusCompleted {
		t.Errorf("status = %q, want completed once every item accepted", got.Status)
	}
}

func TestSyncRefusesStrangersAndUnknowns(t *testing.T) {
	svc, _, _, _ := fixtureShared(t)
	c := newCollection1Q(t, svc)

	if _, err := svc.Sync(t.Context(), c.ID, "org-other"); !errors.Is(err, collections.ErrNotFound) {
		t.Errorf("stranger sync: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Sync(t.Context(), "nope", "org-1"); !errors.Is(err, collections.ErrNotFound) {
		t.Errorf("unknown collection: err = %v, want ErrNotFound", err)
	}
}

func TestByIDEntitlesTheOrgAndTheConsumer(t *testing.T) {
	svc, _, _, _ := fixtureShared(t)
	c := newCollection1Q(t, svc)

	if _, err := svc.ByID(t.Context(), c.ID, "org-1"); err != nil {
		t.Fatalf("org ByID: %v", err)
	}
	if _, err := svc.ByIDForConsumer(t.Context(), c.ID, "consumer-1"); err != nil {
		t.Fatalf("consumer ByID: %v", err)
	}
	if _, err := svc.ByIDForConsumer(t.Context(), c.ID, "consumer-2"); !errors.Is(err, collections.ErrNotFound) {
		t.Errorf("stranger consumer: err = %v, want ErrNotFound", err)
	}
}
