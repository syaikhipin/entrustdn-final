// TemplateSpec (ticket 13): the Process Template's body as the platform
// understands it. A template's config is a JSON document — questions,
// follow-up rules, quality triggers — that replaces the default Collection
// flow when the template is attached to a Request. Parsing is strict:
// unknown fields and unknown rule types are refused, because a template is
// data, never code, and the collection service must be able to trust every
// rule it runs.
package modules

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrUnusableTemplate marks a template config (or module kind) that cannot
// drive a Request — the caller's fault, so the API renders it 422 with the
// reason, distinct from store failures (5xx) and access refusals (404).
var ErrUnusableTemplate = errors.New("modules: unusable template")

// maxTemplateQuestions caps a template's question list — it must fit one
// conversation (the member side's over-survey cap is the same number).
const maxTemplateQuestions = 10

// maxTemplateTriggers caps the rules one template may carry. Triggers are
// cheap checks, but an unbounded list is an accident waiting to run.
const maxTemplateTriggers = 10

// maxReasksCeiling caps a template's follow-up budget. Above the ceiling a
// member would be re-asked into harassment; the platform refuses the
// template outright rather than policce it per collection.
const maxReasksCeiling = 5

// SkillContent is an Agent Skill's loadable body: the module's name and
// the latest version's markdown. The request service hands these to the
// agent on every clarify turn — instructions the agent reads, never runs.
type SkillContent struct {
	ModuleID string
	Name     string
	Content  string
}

// FollowUpRules is a template's follow-up policy: how many re-asks one
// item may get and what the follow-up conversation's topic reads as.
// MaxReasks nil means the platform default (collections.MaxReasksPerItem);
// zero is meaningful — a template may forbid re-asks outright.
type FollowUpRules struct {
	MaxReasks *int   `json:"max_reasks,omitempty"`
	Topic     string `json:"topic,omitempty"`
}

// TriggerRule is one declarative quality check. Type picks the check:
//
//	min_length / max_length — the answer's rune count against Runes
//	require_any             — at least one of Values appears (case-insensitive)
//	forbid_any              — none of Values may appear (case-insensitive)
//	not_one_of              — the whole trimmed answer may not equal any Value
//
// Rules are data: there is no rule type that runs anything.
type TriggerRule struct {
	Type   string   `json:"type"`
	Runes  int      `json:"runes,omitempty"`
	Values []string `json:"values,omitempty"`
}

// Trigger rule type constants — the closed set ParseTemplateSpec accepts.
const (
	RuleMinLength  = "min_length"
	RuleMaxLength  = "max_length"
	RuleRequireAny = "require_any"
	RuleForbidAny  = "forbid_any"
	RuleNotOneOf   = "not_one_of"
)

// validRuleType reports whether t is one of the declarative checks.
func validRuleType(t string) bool {
	switch t {
	case RuleMinLength, RuleMaxLength, RuleRequireAny, RuleForbidAny, RuleNotOneOf:
		return true
	}
	return false
}

// RuleVerdict is one template-rule evaluation: whether the answer stands,
// and why not when it doesn't. The collections package (the default
// triggers' home) maps its own verdicts onto the same shape — a template's
// rules and the defaults speak the same language.
type RuleVerdict struct {
	OK     bool
	Reason string
}

// TemplateSpec is the parsed body of a Process Template Module.
type TemplateSpec struct {
	Questions []string      `json:"questions"`
	FollowUp  FollowUpRules `json:"follow_up,omitempty"`
	Triggers  []TriggerRule `json:"triggers,omitempty"`
}

// ParseTemplateSpec decodes and validates a template's config JSON. Every
// refusable shape is refused here so an unusable template can never ride
// onto a Request: the collection service may trust what this returns.
func ParseTemplateSpec(config []byte) (TemplateSpec, error) {
	dec := json.NewDecoder(bytes.NewReader(config))
	dec.DisallowUnknownFields()
	var spec TemplateSpec
	if err := dec.Decode(&spec); err != nil {
		return TemplateSpec{}, fmt.Errorf("%w: config is not a valid template: %w", ErrUnusableTemplate, err)
	}
	for _, q := range spec.Questions {
		if strings.TrimSpace(q) == "" {
			return TemplateSpec{}, fmt.Errorf("%w: questions must not be blank", ErrUnusableTemplate)
		}
	}
	if len(spec.Questions) == 0 {
		return TemplateSpec{}, fmt.Errorf("%w: needs at least one question", ErrUnusableTemplate)
	}
	if len(spec.Questions) > maxTemplateQuestions {
		return TemplateSpec{}, fmt.Errorf("%w: carries %d questions, at most %d fit one conversation", ErrUnusableTemplate, len(spec.Questions), maxTemplateQuestions)
	}
	if len(spec.Triggers) > maxTemplateTriggers {
		return TemplateSpec{}, fmt.Errorf("%w: carries %d trigger rules, at most %d are allowed", ErrUnusableTemplate, len(spec.Triggers), maxTemplateTriggers)
	}
	// Duplicate questions would cross-wire the collection: answers are
	// matched back to items by question text, so two identical questions
	// would both ingest the first answer and drop the second.
	seen := make(map[string]bool, len(spec.Questions))
	for _, q := range spec.Questions {
		if seen[q] {
			return TemplateSpec{}, fmt.Errorf("%w: duplicate question %q", ErrUnusableTemplate, q)
		}
		seen[q] = true
	}
	for _, r := range spec.Triggers {
		if !validRuleType(r.Type) {
			return TemplateSpec{}, fmt.Errorf("%w: trigger type %q is not one of min_length, max_length, require_any, forbid_any, not_one_of", ErrUnusableTemplate, r.Type)
		}
		switch r.Type {
		case RuleMinLength, RuleMaxLength:
			if r.Runes <= 0 {
				return TemplateSpec{}, fmt.Errorf("%w: %s rule needs a positive runes count", ErrUnusableTemplate, r.Type)
			}
		case RuleRequireAny, RuleForbidAny, RuleNotOneOf:
			if len(r.Values) == 0 {
				return TemplateSpec{}, fmt.Errorf("%w: %s rule needs a non-empty values list", ErrUnusableTemplate, r.Type)
			}
			for _, v := range r.Values {
				if strings.TrimSpace(v) == "" {
					return TemplateSpec{}, fmt.Errorf("%w: %s rule values must not be blank", ErrUnusableTemplate, r.Type)
				}
			}
		}
	}
	if spec.FollowUp.MaxReasks != nil && (*spec.FollowUp.MaxReasks < 0 || *spec.FollowUp.MaxReasks > maxReasksCeiling) {
		return TemplateSpec{}, fmt.Errorf("%w: max_reasks must sit between 0 and %d", ErrUnusableTemplate, maxReasksCeiling)
	}
	return spec, nil
}

// Evaluate runs the template's trigger rules over one answer. The
// completeness floor (a blank answer is not data) always applies — a
// template may tighten the checks but never disable the floor. With no
// rules of its own the template's verdicts are the floor alone.
func (s TemplateSpec) Evaluate(question, answer string) RuleVerdict {
	_ = question // rules judge the answer; the question rides for future per-question rules
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		return RuleVerdict{OK: false, Reason: "completeness: the answer is empty"}
	}
	lowered := strings.ToLower(trimmed)
	for _, r := range s.Triggers {
		if v := runRule(r, trimmed, lowered); !v.OK {
			return v
		}
	}
	return RuleVerdict{OK: true}
}

// runRule evaluates one rule against the trimmed answer (and its lowercase
// twin, so value matching is case-insensitive).
func runRule(r TriggerRule, trimmed, lowered string) RuleVerdict {
	switch r.Type {
	case RuleMinLength:
		if len([]rune(trimmed)) < r.Runes {
			return RuleVerdict{OK: false, Reason: fmt.Sprintf("trigger %s: the answer is shorter than %d runes", r.Type, r.Runes)}
		}
	case RuleMaxLength:
		if len([]rune(trimmed)) > r.Runes {
			return RuleVerdict{OK: false, Reason: fmt.Sprintf("trigger %s: the answer is longer than %d runes", r.Type, r.Runes)}
		}
	case RuleRequireAny:
		for _, v := range r.Values {
			if strings.Contains(lowered, strings.ToLower(v)) {
				return RuleVerdict{OK: true}
			}
		}
		return RuleVerdict{OK: false, Reason: fmt.Sprintf("trigger %s: none of %q appear in the answer", r.Type, r.Values)}
	case RuleForbidAny:
		for _, v := range r.Values {
			if strings.Contains(lowered, strings.ToLower(v)) {
				return RuleVerdict{OK: false, Reason: fmt.Sprintf("trigger %s: %q may not appear in the answer", r.Type, v)}
			}
		}
	case RuleNotOneOf:
		for _, v := range r.Values {
			if lowered == strings.ToLower(strings.TrimSpace(v)) {
				return RuleVerdict{OK: false, Reason: fmt.Sprintf("trigger %s: %q alone is not an answer", r.Type, trimmed)}
			}
		}
	}
	return RuleVerdict{OK: true}
}
