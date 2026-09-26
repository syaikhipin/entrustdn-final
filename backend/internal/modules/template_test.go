package modules_test

import (
	"strings"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/modules"
)

// Ticket 13: authorization for module *consumption* tests live in
// service_test.go (TestAccessible*); this file holds the template spec.
// config is a JSON document — questions, follow-up rules, quality triggers
// — parsed and validated before anything may attach it to a Request. A
// template is data, never code: unknown fields and unknown rule types are
// refused outright, and the trigger rules are declarative checks only.

func validSpecJSON() string {
	return `{
		"questions": ["What county is your farm in?", "How many hectares of spring barley did you harvest?"],
		"follow_up": {"max_reasks": 3, "topic": "A quick follow-up on your survey"},
		"triggers": [
			{"type": "min_length", "runes": 10},
			{"type": "require_any", "values": ["hectares", "acres", "ha"]}
		]
	}`
}

func TestParseTemplateSpecAcceptsAWellFormedSpec(t *testing.T) {
	spec, err := modules.ParseTemplateSpec([]byte(validSpecJSON()))
	if err != nil {
		t.Fatalf("ParseTemplateSpec: %v", err)
	}
	if len(spec.Questions) != 2 {
		t.Errorf("questions = %d, want 2", len(spec.Questions))
	}
	if spec.FollowUp.MaxReasks == nil || *spec.FollowUp.MaxReasks != 3 || spec.FollowUp.Topic != "A quick follow-up on your survey" {
		t.Errorf("follow_up = %+v", spec.FollowUp)
	}
	if len(spec.Triggers) != 2 || spec.Triggers[0].Type != "min_length" {
		t.Errorf("triggers = %+v", spec.Triggers)
	}
}

func TestParseTemplateSpecRefusesUnusableConfigs(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name:    "not JSON at all",
			config:  "ask about the county first",
			wantErr: "template",
		},
		{
			name: "no questions",
			config: `{
				"questions": [],
				"triggers": []
			}`,
			wantErr: "question",
		},
		{
			name: "blank question",
			config: `{
				"questions": ["   "],
				"triggers": []
			}`,
			wantErr: "question",
		},
		{
			name: "too many questions for one conversation",
			config: `{
				"questions": ["q1","q2","q3","q4","q5","q6","q7","q8","q9","q10","q11"],
				"triggers": []
			}`,
			wantErr: "question",
		},
		{
			name: "unknown field is a contract violation",
			config: `{
				"questions": ["q"],
				"triggers": [],
				"script": "rm -rf /"
			}`,
			wantErr: "unknown field",
		},
		{
			name: "unknown rule type",
			config: `{
				"questions": ["q"],
				"triggers": [{"type": "runs_sql"}]
			}`,
			wantErr: "type",
		},
		{
			name: "unknown field inside a rule",
			config: `{
				"questions": ["q"],
				"triggers": [{"type": "min_length", "runes": 3, "query": "DROP TABLE answers"}]
			}`,
			wantErr: "unknown field",
		},
		{
			name: "value rule with no values",
			config: `{
				"questions": ["q"],
				"triggers": [{"type": "require_any"}]
			}`,
			wantErr: "values",
		},
		{
			name: "length rule with no runes",
			config: `{
				"questions": ["q"],
				"triggers": [{"type": "min_length"}]
			}`,
			wantErr: "runes",
		},
		{
			name: "negative length",
			config: `{
				"questions": ["q"],
				"triggers": [{"type": "max_length", "runes": -5}]
			}`,
			wantErr: "runes",
		},
		{
			name: "duplicate question would cross-wire answers",
			config: `{
				"questions": ["What county?", "What county?"],
				"triggers": []
			}`,
			wantErr: "duplicate question",
		},
		{
			name: "max_reasks out of range",
			config: `{
				"questions": ["q"],
				"follow_up": {"max_reasks": 99},
				"triggers": []
			}`,
			wantErr: "max_reasks",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := modules.ParseTemplateSpec([]byte(tt.config))
			if err == nil {
				t.Fatalf("config accepted, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestTemplateSpecEvaluateSourcesTriggersFromTheTemplate(t *testing.T) {
	spec, err := modules.ParseTemplateSpec([]byte(`{
		"questions": ["How many hectares of spring barley?"],
		"triggers": [{"type": "require_any", "values": ["hectares", "acres"]}]
	}`))
	if err != nil {
		t.Fatalf("ParseTemplateSpec: %v", err)
	}

	q := "How many hectares of spring barley?"

	// The template's rule refuses what the defaults would accept: a real
	// sentence with no unit in it.
	if v := spec.Evaluate(q, "Around forty fields this year"); v.OK {
		t.Errorf("answer without a unit passed the template's require_any: %+v", v)
	}
	// And accepts what carries the unit.
	if v := spec.Evaluate(q, "About 42 hectares of spring barley"); !v.OK {
		t.Errorf("answer with the unit refused: %+v", v)
	}
	// The verdict names its rule so the org sees why an answer was refused.
	v := spec.Evaluate(q, "Around forty fields this year")
	if !strings.Contains(v.Reason, "require_any") {
		t.Errorf("reason = %q, want it to name the rule", v.Reason)
	}
}

func TestTemplateSpecEvaluateKeepsTheCompletenessFloor(t *testing.T) {
	spec, err := modules.ParseTemplateSpec([]byte(`{"questions": ["q"], "triggers": []}`))
	if err != nil {
		t.Fatalf("ParseTemplateSpec: %v", err)
	}
	if v := spec.Evaluate("q", "   "); v.OK {
		t.Errorf("blank answer passed even with an empty rule list")
	}
}

func TestTemplateSpecEvaluateRunsEveryRuleType(t *testing.T) {
	spec, err := modules.ParseTemplateSpec([]byte(`{
		"questions": ["q"],
		"triggers": [
			{"type": "min_length", "runes": 5},
			{"type": "max_length", "runes": 40},
			{"type": "not_one_of", "values": ["yes", "no"]},
			{"type": "forbid_any", "values": ["maybe"]}
		]
	}`))
	if err != nil {
		t.Fatalf("ParseTemplateSpec: %v", err)
	}
	tests := []struct {
		name   string
		answer string
		wantOK bool
	}{
		{name: "a real answer passes all four", answer: "42 hectares of spring barley", wantOK: true},
				{name: "too short for min_length", answer: "42 h", wantOK: false},
		{name: "too long for max_length", answer: strings.Repeat("x", 41), wantOK: false},
		{name: "a bare yes is refused by not_one_of", answer: "yes", wantOK: false},
		{name: "maybe anywhere is forbidden", answer: "maybe around 40 hectares", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := spec.Evaluate("q", tt.answer)
			if v.OK != tt.wantOK {
				t.Errorf("answer %q verdict = %+v, want OK %v", tt.answer, v, tt.wantOK)
			}
		})
	}
}

func TestTemplateSpecRulesAreCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"questions": ["q"], "triggers": [`)
	for i := 0; i < 25; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"type": "min_length", "runes": 3}`)
	}
	b.WriteString("]}")
	if _, err := modules.ParseTemplateSpec([]byte(b.String())); err == nil {
		t.Errorf("25 rules accepted, want a cap")
	}
}
