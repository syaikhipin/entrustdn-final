package requests_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/modules"
	"github.com/syaikhipin/entrustdn-final/backend/internal/requests"
)

// Attaching Modules to a Request (ticket 13): a Data Consumer may attach
// their own private Process Template — the questions, follow-up rules, and
// quality triggers it defines replace the default Collection flow — and
// Agent Skills whose instructions the agent loads for this Request. The
// registry's authorization is the gate: author and grantees may attach a
// private Module; a stranger's private Module is refused. The attachment
// snapshots the spec at attach time; the Conversation's questions can then
// never be quietly redefined mid-flight.

func validSpec() modules.TemplateSpec {
	spec, err := modules.ParseTemplateSpec([]byte(`{
		"questions": ["What county is your farm in?", "How many hectares of spring barley?"],
		"follow_up": {"max_reasks": 3, "topic": "A quick follow-up"},
		"triggers": [{"type": "require_any", "values": ["hectares", "acres"]}]
	}`))
	if err != nil {
		panic(err)
	}
	return spec
}

func attachFixture(t *testing.T) (*requests.Service, *fakeModules) {
	t.Helper()
	svc, _, _ := fixture(t, testCatalog)
	// By default the registry answers "consumer-1 may use any module";
	// refusal tests build their own deny-all fake. skill-1 resolves as a
	// real agent skill so happy-path attaches pass the kind check.
	fm := &fakeModules{
		accessible: map[string]bool{"consumer-1": true},
		skills: map[string]modules.SkillContent{
			"skill-1": {ModuleID: "skill-1", Name: "barley-notes", Content: "# notes"},
		},
	}
	requests.SetModuleSource(svc, fm)
	return svc, fm
}

// denyFixture wires the registry to refuse everything — no grants.
func denyFixture(t *testing.T) (*requests.Service, *fakeModules) {
	t.Helper()
	svc, _, _ := fixture(t, testCatalog)
	fm := &fakeModules{accessible: map[string]bool{}}
	requests.SetModuleSource(svc, fm)
	return svc, fm
}

// fakeModules stands in for the modules registry at the attach seam: it
// answers Accessible per the scripted grants and records what was asked.
type fakeModules struct {
	accessible map[string]bool // callerID → allowed
	asks       []string        // every Accessible ask, "caller|module"
	skills     map[string]modules.SkillContent
}

func (f *fakeModules) Accessible(_ context.Context, callerID, moduleID string) error {
	f.asks = append(f.asks, callerID+"|"+moduleID)
	if !f.accessible[callerID] {
		return modules.ErrForbidden
	}
	return nil
}

func (f *fakeModules) Skill(_ context.Context, callerID, moduleID string) (modules.SkillContent, error) {
	sc, ok := f.skills[moduleID]
	if !ok {
		return modules.SkillContent{}, modules.ErrNotFound
	}
	return sc, nil
}

func TestAttachTemplateStoresSnapshotOnTheRequest(t *testing.T) {
	svc, _ := attachFixture(t)
	r := newRequest(t, svc)
	spec := validSpec()

	updated, err := svc.AttachTemplate(t.Context(), r.ID, "consumer-1", "mod-1", spec)
	if err != nil {
		t.Fatalf("AttachTemplate: %v", err)
	}
	if updated.Template == nil || updated.Template.ModuleID != "mod-1" {
		t.Fatalf("template = %+v, want a snapshot of mod-1", updated.Template)
	}
	if len(updated.Template.Spec.Questions) != 2 {
		t.Errorf("snapshotted questions = %d, want 2", len(updated.Template.Spec.Questions))
	}

	// The attachment is durable: a fresh read carries it.
	stored, err := svc.ByID(t.Context(), r.ID, "consumer-1")
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if stored.Template == nil || stored.Template.ModuleID != "mod-1" {
		t.Errorf("stored template = %+v, want the mod-1 snapshot", stored.Template)
	}
}

func TestAttachTemplateRefusesStrangers(t *testing.T) {
	svc, _ := attachFixture(t)
	r := newRequest(t, svc)

	if _, err := svc.AttachTemplate(t.Context(), r.ID, "consumer-2", "mod-1", validSpec()); !errors.Is(err, requests.ErrForbidden) {
		t.Errorf("stranger attach = %v, want ErrForbidden", err)
	}
}

func TestAttachTemplateRefusesModulesTheCallerCannotUse(t *testing.T) {
	svc, _ := denyFixture(t)
	r := newRequest(t, svc)

	// consumer-1 holds no grant on the private module: the registry refuses,
	// and nothing is attached.
	if _, err := svc.AttachTemplate(t.Context(), r.ID, "consumer-1", "private-mod", validSpec()); !errors.Is(err, requests.ErrForbidden) {
		t.Errorf("unauthorized attach = %v, want ErrForbidden", err)
	}
	stored, _ := svc.ByID(t.Context(), r.ID, "consumer-1")
	if stored.Template != nil {
		t.Errorf("refused attach left a template attached: %+v", stored.Template)
	}
}

func TestAttachTemplateRefusedWhenClarified(t *testing.T) {
	// A clarified request has left the shaping phase: its conversation is
	// about to become a Collection, so the question set is frozen.
	svc, ledger, clari := fixture(t, testCatalog)
	fm := &fakeModules{accessible: map[string]bool{"consumer-1": true}}
	requests.SetModuleSource(svc, fm)
	clari.reply.Clarified = true

	r := newRequest(t, svc)
	if _, err := svc.Chat(t.Context(), r.ID, "consumer-1", "That's everything"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if _, err := svc.AttachTemplate(t.Context(), r.ID, "consumer-1", "mod-1", validSpec()); !errors.Is(err, requests.ErrClosed) {
		t.Errorf("attach after clarified = %v, want ErrClosed", err)
	}
	_ = ledger
}

func TestDetachTemplateRemovesTheSnapshot(t *testing.T) {
	svc, fm := attachFixture(t)
	r := newRequest(t, svc)
	fm.accessible["consumer-1"] = true

	if _, err := svc.AttachTemplate(t.Context(), r.ID, "consumer-1", "mod-1", validSpec()); err != nil {
		t.Fatalf("AttachTemplate: %v", err)
	}
	updated, err := svc.DetachTemplate(t.Context(), r.ID, "consumer-1")
	if err != nil {
		t.Fatalf("DetachTemplate: %v", err)
	}
	if updated.Template != nil {
		t.Errorf("template = %+v, want nil after detach", updated.Template)
	}
}

func TestAttachSkillRecordsTheModuleRef(t *testing.T) {
	svc, fm := attachFixture(t)
	r := newRequest(t, svc)
	fm.accessible["consumer-1"] = true

	updated, err := svc.AttachSkill(t.Context(), r.ID, "consumer-1", "skill-1")
	if err != nil {
		t.Fatalf("AttachSkill: %v", err)
	}
	if len(updated.Skills) != 1 || updated.Skills[0] != "skill-1" {
		t.Fatalf("skills = %+v, want [skill-1]", updated.Skills)
	}
	// Durable.
	stored, _ := svc.ByID(t.Context(), r.ID, "consumer-1")
	if len(stored.Skills) != 1 || stored.Skills[0] != "skill-1" {
		t.Errorf("stored skills = %+v, want [skill-1]", stored.Skills)
	}
}

func TestAttachSkillIsIdempotent(t *testing.T) {
	svc, fm := attachFixture(t)
	r := newRequest(t, svc)
	fm.accessible["consumer-1"] = true

	if _, err := svc.AttachSkill(t.Context(), r.ID, "consumer-1", "skill-1"); err != nil {
		t.Fatalf("AttachSkill: %v", err)
	}
	updated, err := svc.AttachSkill(t.Context(), r.ID, "consumer-1", "skill-1")
	if err != nil {
		t.Fatalf("second AttachSkill: %v", err)
	}
	if len(updated.Skills) != 1 {
		t.Errorf("skills = %+v, want the single ref", updated.Skills)
	}
}

func TestAttachSkillRefusesUnauthorizedModules(t *testing.T) {
	svc, _ := denyFixture(t)
	r := newRequest(t, svc)

	if _, err := svc.AttachSkill(t.Context(), r.ID, "consumer-1", "private-skill"); !errors.Is(err, requests.ErrForbidden) {
		t.Errorf("unauthorized skill attach = %v, want ErrForbidden", err)
	}
	if _, err := svc.AttachSkill(t.Context(), r.ID, "consumer-2", "skill-1"); !errors.Is(err, requests.ErrForbidden) {
		t.Errorf("stranger skill attach = %v, want ErrForbidden", err)
	}
}

func TestDetachSkillRemovesTheRef(t *testing.T) {
	svc, fm := attachFixture(t)
	r := newRequest(t, svc)
	fm.accessible["consumer-1"] = true

	if _, err := svc.AttachSkill(t.Context(), r.ID, "consumer-1", "skill-1"); err != nil {
		t.Fatalf("AttachSkill: %v", err)
	}
	updated, err := svc.DetachSkill(t.Context(), r.ID, "consumer-1", "skill-1")
	if err != nil {
		t.Fatalf("DetachSkill: %v", err)
	}
	if len(updated.Skills) != 0 {
		t.Errorf("skills = %+v, want none after detach", updated.Skills)
	}
}

func TestClarifyTurnCarriesSkillContentToTheAgent(t *testing.T) {
	svc, ledger, clari := fixture(t, testCatalog)
	fm := &fakeModules{
		accessible: map[string]bool{"consumer-1": true},
		skills:     map[string]modules.SkillContent{"skill-1": {ModuleID: "skill-1", Name: "organic-certification", Content: "# Ask about certification first"}},
	}
	requests.SetModuleSource(svc, fm)

	r := newRequest(t, svc)
	// The consumer attaches a skill the registry says they may use.
	if _, err := svc.AttachSkill(t.Context(), r.ID, "consumer-1", "skill-1"); err != nil {
		t.Fatalf("AttachSkill: %v", err)
	}
	// The loaded skill's instructions: the registry supplies them at
	// turn time so the latest version always rides the wire.
	fm.skills = map[string]modules.SkillContent{
		"skill-1": {ModuleID: "skill-1", Name: "organic-certification", Content: "# Ask about certification first"},
	}

	if _, err := svc.Chat(t.Context(), r.ID, "consumer-1", "I need yield data"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	sent := clari.got[0]
	if len(sent.Skills) != 1 {
		t.Fatalf("clarify carried %d skills, want 1", len(sent.Skills))
	}
	if sent.Skills[0].Name != "organic-certification" || sent.Skills[0].Content != "# Ask about certification first" {
		t.Errorf("skill on the wire = %+v", sent.Skills[0])
	}
	_ = ledger
}

func TestClarifyTurnCarriesNoSkillsWhenNoneAttached(t *testing.T) {
	svc, _, clari := fixture(t, testCatalog)
	requests.SetModuleSource(svc, &fakeModules{})

	r := newRequest(t, svc)
	if _, err := svc.Chat(t.Context(), r.ID, "consumer-1", "I need yield data"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(clari.got[0].Skills) != 0 {
		t.Errorf("skills = %+v, want none", clari.got[0].Skills)
	}
}

func TestClarifyTurnSurfacesAnUnreadableSkill(t *testing.T) {
	svc, _, _ := fixture(t, testCatalog)
	fm := &fakeModules{
		accessible: map[string]bool{"consumer-1": true},
		skills:     map[string]modules.SkillContent{"skill-1": {ModuleID: "skill-1", Name: "organic-certification", Content: "# notes"}},
	}
	requests.SetModuleSource(svc, fm)

	r := newRequest(t, svc)
	if _, err := svc.AttachSkill(t.Context(), r.ID, "consumer-1", "skill-1"); err != nil {
		t.Fatalf("AttachSkill: %v", err)
	}
	// The module vanished between attach and turn: the turn is refused —
	// surfaced, never silent.
	fm.skills = map[string]modules.SkillContent{}
	if _, err := svc.Chat(t.Context(), r.ID, "consumer-1", "I need yield data"); err == nil {
		t.Errorf("Chat succeeded with an unloadable skill, want an error")
	}
}

func TestAttachSkillRefusesAModuleThatIsNotASkill(t *testing.T) {
	// A template (or connector) on the skills list would fail every future
	// clarify turn — the loader refuses non-skill kinds. The refusal is
	// attach-time, not a bricked request later.
	svc, fm := attachFixture(t)
	r := newRequest(t, svc)
	fm.accessible["consumer-1"] = true
	// No skills entry for "tpl-1": the registry's Skill read refuses it
	// (kind is not agent_skill), exactly like the real registry.

	_, err := svc.AttachSkill(t.Context(), r.ID, "consumer-1", "tpl-1")
	if err == nil {
		t.Fatalf("attaching a non-skill module succeeded, want a refusal")
	}
	if !errors.Is(err, requests.ErrForbidden) {
		t.Errorf("error = %v, want ErrForbidden wrapped", err)
	}
	// Nothing rode onto the request.
	stored, _ := svc.ByID(t.Context(), r.ID, "consumer-1")
	if len(stored.Skills) != 0 {
		t.Errorf("skills = %+v, want none attached", stored.Skills)
	}
}

func TestAttachSkillPastTheCapIsACallerFault(t *testing.T) {
	// The 5-skill cap refusal carries ErrTooManySkills so the API renders
	// 422, not a server fault.
	svc, fm := attachFixture(t)
	r := newRequest(t, svc)
	fm.accessible["consumer-1"] = true
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("skill-%d", i+2)
		fm.skills[id] = modules.SkillContent{ModuleID: id, Name: id, Content: "# c"}
		if _, err := svc.AttachSkill(t.Context(), r.ID, "consumer-1", id); err != nil {
			t.Fatalf("attach %d: %v", i+2, err)
		}
	}
	fm.skills["skill-99"] = modules.SkillContent{ModuleID: "skill-99", Name: "skill-99", Content: "# c"}
	_, err := svc.AttachSkill(t.Context(), r.ID, "consumer-1", "skill-99")
	if !errors.Is(err, requests.ErrTooManySkills) {
		t.Errorf("error = %v, want ErrTooManySkills", err)
	}
}
