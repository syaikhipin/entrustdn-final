package collections_test

import (
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/collections"
	"github.com/syaikhipin/entrustdn-final/backend/internal/conversations"
	"github.com/syaikhipin/entrustdn-final/backend/internal/modules"
	"github.com/syaikhipin/entrustdn-final/backend/internal/requests"
)

// Template-driven Collections (ticket 13): when the Request carries an
// attached Process Template, Create refuses caller-supplied questions and
// materializes items from the template's own list; Sync evaluates answers
// through the template's trigger rules instead of the defaults, and opens
// re-asks with the template's topic and re-ask budget. The snapshot rides
// the Request, so a collection is immune to later template changes.

func templateSpec() modules.TemplateSpec {
	spec, err := modules.ParseTemplateSpec([]byte(`{
		"questions": ["Which county is your farm in?", "How many hectares of spring barley did you harvest?"],
		"follow_up": {"max_reasks": 1, "topic": "A quick follow-up on your survey"},
		"triggers": [{"type": "require_any", "values": ["carlow", "kilkenny", "hectares", "tonnes"]}]
	}`))
	if err != nil {
		panic(err)
	}
	return spec
}

// templateFixture wires the shared fixture and attaches a template to
// req-1 — the state a clarified Request carries after the consumer's
// attach call.
func templateFixture(t *testing.T) (*collections.Service, *requests.MemoryStore, *conversations.MemoryStore, *fakeOpener) {
	t.Helper()
	svc, reqStore, convs, opener := fixtureShared(t)
	spec := templateSpec()
	r, err := reqStore.RequestByID(t.Context(), "req-1")
	if err != nil {
		t.Fatalf("load request: %v", err)
	}
	r.Template = &requests.TemplateAttachment{ModuleID: "mod-tpl", Spec: spec}
	if err := reqStore.UpdateRequest(t.Context(), r); err != nil {
		t.Fatalf("attach template: %v", err)
	}
	return svc, reqStore, convs, opener
}

func TestCreateSourcesQuestionsFromTheTemplate(t *testing.T) {
	svc, _, _, _ := templateFixture(t)

	c, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1",
		MemberIDs: []string{"mem-1"},
		Questions: []string{"Anything the caller makes up"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The items carry the template's questions, not the caller's.
	if len(c.Items) != 2 {
		t.Fatalf("items = %d, want 1 member × 2 template questions", len(c.Items))
	}
	if c.Items[0].Question != "Which county is your farm in?" ||
		c.Items[1].Question != "How many hectares of spring barley did you harvest?" {
		t.Errorf("questions = %q, %q; want the template's own", c.Items[0].Question, c.Items[1].Question)
	}
}

func TestCreateWithoutTemplateKeepsCallerQuestions(t *testing.T) {
	// The default flow is untouched: no template on the request, the
	// caller's questions drive the items.
	svc, _, _, _ := fixtureShared(t)
	c, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1",
		MemberIDs: []string{"mem-1"},
		Questions: []string{farmQ},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(c.Items) != 1 || c.Items[0].Question != farmQ {
		t.Errorf("items = %+v, want the caller's one question", c.Items)
	}
}

func TestCreateRefusesEmptyQuestionsWithoutTemplate(t *testing.T) {
	// The template supplies questions when attached; without one, the
	// caller's non-empty list is required.
	svc, _, _, _ := fixtureShared(t)
	if _, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1",
		MemberIDs: []string{"mem-1"},
	}); err == nil {
		t.Errorf("Create without any questions succeeded, want a refusal")
	}
}

func TestSyncEvaluatesAnswersThroughTemplateTriggers(t *testing.T) {
	svc, _, convs, _ := templateFixture(t)
	c, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1",
		MemberIDs: []string{"mem-1"},
		Questions: []string{"Which county is your farm in?"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// An answer the defaults would pass ("a proper sentence about farms")
	// but the template's require_any refuses: no named county in it.
	seedConversation(t, convs, "conv-tpl-1", "mem-1", "Siobhán Ní Uaithne",
		[]string{"Which county is your farm in?"},
		[]string{"I have always farmed the same place my father did"},
		conversations.StatusCompleted)

	synced, err := svc.Sync(t.Context(), c.ID, "org-1")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	it := synced.Items[0]
	if it.Status != collections.ItemCollecting {
		t.Fatalf("item = %+v, want still collecting (template rule refused the answer)", it)
	}
	if len(it.Rounds) != 1 || it.Rounds[0].OK {
		t.Fatalf("rounds = %+v, want one refused answer", it.Rounds)
	}
	if it.Reasks != 1 {
		t.Errorf("reasks = %d, want 1 (the refusal opens a re-ask)", it.Reasks)
	}
}

func TestSyncAcceptsAnswersTheTemplateTriggersPass(t *testing.T) {
	svc, _, convs, _ := templateFixture(t)
	c, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1",
		MemberIDs: []string{"mem-1"},
		Questions: []string{"How many hectares of spring barley did you harvest?"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	seedConversation(t, convs, "conv-tpl-2", "mem-1", "Siobhán Ní Uaithne",
		[]string{"How many hectares of spring barley did you harvest?"},
		[]string{"About 40 hectares, mostly feed barley"},
		conversations.StatusCompleted)

	synced, err := svc.Sync(t.Context(), c.ID, "org-1")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	var it collections.Item
	for _, item := range synced.Items {
		if item.Question == "How many hectares of spring barley did you harvest?" {
			it = item
			break
		}
	}
	if it.Status != collections.ItemAccepted || it.Accepted == "" {
		t.Fatalf("hectares item = %+v, want accepted through the template's triggers", it)
	}
}

func TestSyncHonorsTheTemplateReaskBudget(t *testing.T) {
	// The template caps re-asks at 1: the second bad answer blocks the item
	// instead of opening another re-ask (the platform default is 2).
	svc, _, convs, opener := templateFixture(t)
	c, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1",
		MemberIDs: []string{"mem-1"},
		Questions: []string{"Which county is your farm in?"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// First bad answer → one re-ask (the template's whole budget).
	seedConversation(t, convs, "conv-tpl-3", "mem-1", "Siobhán Ní Uaithne",
		[]string{"Which county is your farm in?"},
		[]string{"the usual place"},
		conversations.StatusAwaitingMember)
	if _, err := svc.Sync(t.Context(), c.ID, "org-1"); err != nil {
		t.Fatalf("Sync 1: %v", err)
	}
	if len(opener.starts) != 1 {
		t.Fatalf("re-asks opened = %d, want 1", len(opener.starts))
	}
	// The re-ask's topic comes from the template.
	if opener.starts[0].Topic != "A quick follow-up on your survey" {
		t.Errorf("re-ask topic = %q, want the template's", opener.starts[0].Topic)
	}

	// Second bad answer (arrived on the re-ask thread) → blocked, no third ask.
	seedConversation(t, convs, "conv-tpl-4", "mem-1", "Siobhán Ní Uaithne",
		[]string{"Which county is your farm in?"},
		[]string{"down south somewhere"},
		conversations.StatusAwaitingMember)
	synced, err := svc.Sync(t.Context(), c.ID, "org-1")
	if err != nil {
		t.Fatalf("Sync 2: %v", err)
	}
	if len(opener.starts) != 1 {
		t.Errorf("re-asks opened = %d, want still 1 (template budget spent)", len(opener.starts))
	}
	if synced.Items[0].Status != collections.ItemBlocked {
		t.Errorf("item = %+v, want blocked past the template's re-ask budget", synced.Items[0])
	}
}

func TestSyncUsesDefaultTriggersWithoutTemplate(t *testing.T) {
	// Regression guard: the default flow's triggers are untouched.
	svc, _, convs, _ := fixtureShared(t)
	c, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1",
		MemberIDs: []string{"mem-1"},
		Questions: []string{farmQ},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedConversation(t, convs, "conv-def-1", "mem-1", "Siobhán Ní Uaithne",
		[]string{farmQ}, []string{"42 hectares, mostly spring barley"},
		conversations.StatusCompleted)
	synced, err := svc.Sync(t.Context(), c.ID, "org-1")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if synced.Items[0].Status != collections.ItemAccepted {
		t.Errorf("default triggers refused an honest answer: %+v", synced.Items[0])
	}
}

func TestSyncReaskBudgetFallsBackToDefault(t *testing.T) {
	// A template with refusable triggers but no follow_up rules gets the
	// platform default re-ask budget.
	svc, reqStore, convs, opener := fixtureShared(t)
	spec, err := modules.ParseTemplateSpec([]byte(`{
		"questions": ["Which county?"],
		"triggers": [{"type": "require_any", "values": ["carlow", "kilkenny"]}]
	}`))
	if err != nil {
		t.Fatalf("ParseTemplateSpec: %v", err)
	}
	r, _ := reqStore.RequestByID(t.Context(), "req-1")
	r.Template = &requests.TemplateAttachment{ModuleID: "mod-tpl", Spec: spec}
	if err := reqStore.UpdateRequest(t.Context(), r); err != nil {
		t.Fatalf("attach: %v", err)
	}

	c, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1", MemberIDs: []string{"mem-1"},
		Questions: []string{"Which county?"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Two bad answers burn the default budget of 2: the second re-ask's
	// sync leaves no path (reasks = cap) and the collection finalizes
	// incomplete — the same finalization semantics as the default flow.
	answers := []string{"somewhere flat", "out west"}
	var last collections.Collection
	for i, a := range answers {
		seedConversation(t, convs, "conv-cap-"+string(rune('a'+i)), "mem-1", "Siobhán Ní Uaithne",
			[]string{"Which county?"}, []string{a}, conversations.StatusAwaitingMember)
		var err error
		last, err = svc.Sync(t.Context(), c.ID, "org-1")
		if err != nil {
			t.Fatalf("Sync %d: %v", i+1, err)
		}
	}
	if len(opener.starts) != collections.MaxReasksPerItem {
		t.Errorf("re-asks = %d, want the default %d", len(opener.starts), collections.MaxReasksPerItem)
	}
	if last.Status != collections.StatusIncomplete {
		t.Errorf("status = %s, want incomplete once the default budget is spent", last.Status)
	}
	// A template that caps re-asks at 1 finalizes a sync sooner.
	if last.Missing == nil || len(last.Missing) == 0 {
		t.Errorf("missing summary = %+v, want the unanswered question", last.Missing)
	}
}
