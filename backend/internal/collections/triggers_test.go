package collections_test

import (
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/collections"
)

// The default quality triggers (ticket 12): completeness and anomaly.
// Template-sourced triggers are ticket 13's work — these are the built-in
// defaults that hold until a Process Template replaces them. Precision
// wins over recall: a trigger that eats honest answers is worse than one
// that lets a weak answer through, so the anomaly patterns are refusal
// phrases, stop words, and two-short-to-be-data answers — never a length
// ceiling or a topic check.

func TestCompletenessAcceptsARealAnswer(t *testing.T) {
	v := collections.Evaluate("What is your farm size?", "42 hectares, mostly spring barley")
	if !v.OK {
		t.Fatalf("real answer refused: %+v", v)
	}
}

func TestCompletenessRefusesBlankAnswers(t *testing.T) {
	for _, a := range []string{"", "   ", "\n"} {
		if v := collections.Evaluate("What is your farm size?", a); v.OK {
			t.Errorf("blank answer %q passed completeness", a)
		}
	}
}

func TestAnomalyRefusesRefusalPhrases(t *testing.T) {
	for _, a := range []string{
		"i don't know", "I dont know", "DON'T KNOW", "n/a", "N/A",
		"no answer", "skip", "idk", "not sure", "no comment",
	} {
		if v := collections.Evaluate("What is your farm size?", a); v.OK {
			t.Errorf("refusal phrase %q passed anomaly", a)
		}
	}
}

func TestAnomalyKeepsAnswersThatMerelyMentionRefusals(t *testing.T) {
	// Precision over recall: an answer about not knowing *something* is
	// data. Only a whole-answer refusal is a refusal.
	for _, a := range []string{
		"I don't know the exact acreage but roughly 40 hectares",
		"There is no answer the paperwork would give you",
	} {
		if v := collections.Evaluate("What is your farm size?", a); !v.OK {
			t.Errorf("honest answer %q was eaten by anomaly: %+v", a, v)
		}
	}
}

func TestAnomalyRefusesTooShortAnswers(t *testing.T) {
	for _, a := range []string{"?", "-", "ok"} {
		if v := collections.Evaluate("What is your farm size?", a); v.OK {
			t.Errorf("two-short answer %q passed anomaly", a)
		}
	}
}

func TestAnomalyRefusesStopWordsLeftInAnswers(t *testing.T) {
	// The conversation layer stops on stop words before they become
	// answers; the trigger is defense in depth.
	for _, a := range []string{"stop", "STOP", "unsubscribe"} {
		if v := collections.Evaluate("What is your farm size?", a); v.OK {
			t.Errorf("stop word %q passed anomaly", a)
		}
	}
}

func TestVerdictsCarryAReason(t *testing.T) {
	v := collections.Evaluate("What is your farm size?", "n/a")
	if v.OK || v.Reason == "" {
		t.Fatalf("refusal verdict = %+v, want a reason", v)
	}
}
