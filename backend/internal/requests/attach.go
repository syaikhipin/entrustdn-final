// Module attachment (ticket 13): a Data Consumer attaches a Process
// Template — whose questions, follow-up rules, and quality triggers replace
// the default Collection flow — and Agent Skills, whose instructions the
// agent loads into context for every clarify turn. The registry's
// authorization decides who may attach: a private Module is usable only by
// its author and grantees, system-wide by everyone. A template attach
// snapshots the parsed spec onto the Request, so later edits to the Module
// can never quietly redefine a conversation already being shaped.
package requests

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/syaikhipin/entrustdn-final/backend/internal/contract"
	"github.com/syaikhipin/entrustdn-final/backend/internal/modules"
)

// maxSkillsPerRequest caps the skill list — context is finite, and a
// request that loads every skill on the platform helps no one.
const maxSkillsPerRequest = 5

// AttachTemplate validates, authorizes, and freezes the Process Template
// onto the Request. A clarified request refuses: its shaping is done, and
// the question set must stay what the conversation was built from.
func (s *Service) AttachTemplate(ctx context.Context, id, consumerID, moduleID string, spec modules.TemplateSpec) (Request, error) {
	mu := s.lockRequest(id)
	mu.Lock()
	defer mu.Unlock()

	r, err := s.ByID(ctx, id, consumerID)
	if err != nil {
		return Request{}, err
	}
	if r.Status != StatusClarifying {
		return Request{}, ErrClosed
	}
	if err := s.authorizeModule(ctx, consumerID, moduleID); err != nil {
		return Request{}, err
	}
	r.Template = &TemplateAttachment{ModuleID: moduleID, Spec: spec}
	if err := s.store.UpdateRequest(ctx, r); err != nil {
		return Request{}, err
	}
	return r, nil
}

// DetachTemplate removes the attached Process Template; the Request falls
// back to the default collection flow.
func (s *Service) DetachTemplate(ctx context.Context, id, consumerID string) (Request, error) {
	mu := s.lockRequest(id)
	mu.Lock()
	defer mu.Unlock()

	r, err := s.ByID(ctx, id, consumerID)
	if err != nil {
		return Request{}, err
	}
	if r.Status != StatusClarifying {
		return Request{}, ErrClosed
	}
	r.Template = nil
	if err := s.store.UpdateRequest(ctx, r); err != nil {
		return Request{}, err
	}
	return r, nil
}

// AttachSkill adds an Agent Skill's module ID to the Request after the
// registry's authorization check. The module must actually be an agent
// skill — attaching a template here would fail every future clarify turn
// (the loader refuses non-skill kinds), so the refusal happens now, not
// silently later. Idempotent: a skill already attached stays exactly once.
func (s *Service) AttachSkill(ctx context.Context, id, consumerID, moduleID string) (Request, error) {
	mu := s.lockRequest(id)
	mu.Lock()
	defer mu.Unlock()

	r, err := s.ByID(ctx, id, consumerID)
	if err != nil {
		return Request{}, err
	}
	if r.Status != StatusClarifying {
		return Request{}, ErrClosed
	}
	if err := s.authorizeModule(ctx, consumerID, moduleID); err != nil {
		return Request{}, err
	}
	// Kind check through the loader's read half — a template or connector
	// never rides the skills list.
	if _, err := s.mods.Skill(ctx, consumerID, moduleID); err != nil {
		return Request{}, fmt.Errorf("requests: %w: module %s is not an agent skill", ErrForbidden, moduleID)
	}
	if slices.Contains(r.Skills, moduleID) {
		return r, nil
	}
	if len(r.Skills) >= maxSkillsPerRequest {
		return Request{}, fmt.Errorf("%w: requests: at most %d skills may ride one request", ErrTooManySkills, maxSkillsPerRequest)
	}
	r.Skills = append(r.Skills, moduleID)
	if err := s.store.UpdateRequest(ctx, r); err != nil {
		return Request{}, err
	}
	return r, nil
}

// DetachSkill removes one Agent Skill's module ID. Detaching an unattached
// skill is not an error — the end state is what matters.
func (s *Service) DetachSkill(ctx context.Context, id, consumerID, moduleID string) (Request, error) {
	mu := s.lockRequest(id)
	mu.Lock()
	defer mu.Unlock()

	r, err := s.ByID(ctx, id, consumerID)
	if err != nil {
		return Request{}, err
	}
	if r.Status != StatusClarifying {
		return Request{}, ErrClosed
	}
	r.Skills = slices.DeleteFunc(r.Skills, func(m string) bool { return m == moduleID })
	if err := s.store.UpdateRequest(ctx, r); err != nil {
		return Request{}, err
	}
	return r, nil
}

// authorizeModule asks the registry whether consumerID may use moduleID.
// No registry seam means nothing may be attached — surfaced, never silent.
// A refusal wraps ErrForbidden (the API renders it 404, existence not
// disclosed); a registry/store failure passes through untouched, so an
// outage reads as 5xx, not as "no such module".
func (s *Service) authorizeModule(ctx context.Context, consumerID, moduleID string) error {
	if s.mods == nil {
		return fmt.Errorf("requests: %w: modules are not configured on this deployment", ErrForbidden)
	}
	if err := s.mods.Accessible(ctx, consumerID, moduleID); err != nil {
		if !errors.Is(err, modules.ErrForbidden) && !errors.Is(err, modules.ErrNotFound) {
			return err
		}
		return fmt.Errorf("requests: %w: module %s is not available to you", ErrForbidden, moduleID)
	}
	return nil
}

// loadSkills resolves every attached skill's content for one clarify turn.
// The registry supplies the latest version's content at turn time. A skill
// that cannot be loaded fails the turn — a silently missing instruction
// would shape the agent's answers without anyone knowing.
func (s *Service) loadSkills(ctx context.Context, r Request) ([]contract.SkillModule, error) {
	out := []contract.SkillModule{}
	if len(r.Skills) == 0 {
		return out, nil
	}
	if s.mods == nil {
		return nil, fmt.Errorf("requests: request %s carries skills but modules are not configured", r.ID)
	}
	for _, moduleID := range r.Skills {
		sc, err := s.mods.Skill(ctx, r.ConsumerID, moduleID)
		if err != nil {
			return nil, fmt.Errorf("requests: load skill %s: %w", moduleID, err)
		}
		out = append(out, contract.SkillModule{Name: sc.Name, Content: sc.Content})
	}
	return out, nil
}
