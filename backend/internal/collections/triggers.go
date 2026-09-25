// Package collections implements ticket 12: a Request becomes a completed,
// delivered Data Collection. The Agent gathers Farmer Member answers over
// Channels (ticket 11's conversations); quality triggers — completeness and
// anomaly checks — evaluate every answer and trigger re-asks on bad data; a
// Collection completes when every (member, question) slot holds an accepted
// answer, or flags incomplete through the deadline path. Delivery to the
// Data Consumer is anonymized (ADR 0005) and charged to the Ledger.
//
// The triggers here are the defaults, standing in until ticket 13 lets a
// Process Template define its own. The collection record itself is the
// single-writer whole-JSONB pattern the conversations and requests stores
// already use.
package collections

import (
	"fmt"
	"strings"
)

// Verdict is one trigger evaluation: whether the answer stands, and why not
// when it doesn't.
type Verdict struct {
	OK     bool
	Reason string
}

// refusalPhrases are the whole-answer non-answers a Member gives when they
// have nothing to offer. Matching is exact on the trimmed, lowercased,
// punctuation-stripped answer — an answer that merely contains a refusal
// phrase ("I don't know the exact acreage") is data about the farm, and
// precision wins over recall: eating honest answers is worse than letting
// a weak one through.
var refusalPhrases = map[string]bool{
	"i don't know": true, "i dont know": true, "dont know": true,
	"don't know": true, "idk": true, "not sure": true, "no idea": true,
	"n/a": true, "na": true, "none": true, "no answer": true,
	"no comment": true, "skip": true, "skipped": true, "pass": true,
	"unknown": true, "unsure": true, "not applicable": true,
}

// stopWords are the conversation opt-outs — defense in depth: the
// conversation layer stops on them before they become answers, but a
// replayed thread must not carry one through as data.
var stopWords = map[string]bool{
	"stop": true, "unsubscribe": true, "quit": true, "cancel": true,
}

// minAnswerRunes is the too-short floor: an answer under this is noise, not
// data ("?", "-", "ok"). Real answers almost never sit at one or two runes.
const minAnswerRunes = 3

// stripAnswerPunctuation removes characters that carry no data so refusal
// matching sees the words, not their packaging ("n/a." refuses, "42 ha"
// passes).
func stripAnswerPunctuation(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '.', ',', '!', '?', ';', ':', '"', '\'', '(', ')', '-', '*':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Evaluate runs the default quality triggers over one answer: completeness
// (non-blank) and anomaly (whole-answer refusals, stop words, too-short
// noise). The question rides along so template-sourced triggers (ticket 13)
// can hang per-question rules off the same shape.
func Evaluate(question, answer string) Verdict {
	_ = question // the defaults judge the answer alone; per-question rules arrive with templates
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		return Verdict{OK: false, Reason: "completeness: the answer is empty"}
	}
	if len([]rune(trimmed)) < minAnswerRunes {
		return Verdict{OK: false, Reason: fmt.Sprintf("anomaly: the answer is too short to be data (%q)", trimmed)}
	}
	normalized := strings.ToLower(stripAnswerPunctuation(trimmed))
	if stopWords[normalized] {
		return Verdict{OK: false, Reason: fmt.Sprintf("anomaly: the answer is a stop word (%q)", trimmed)}
	}
	if refusalPhrases[normalized] {
		return Verdict{OK: false, Reason: fmt.Sprintf("anomaly: the answer is a refusal (%q)", trimmed)}
	}
	return Verdict{OK: true}
}
