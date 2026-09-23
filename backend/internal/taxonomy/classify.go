package taxonomy

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Classifier auto-categorizes Data Assets at ingest (ticket 06): it scans
// the asset's name, description, and provenance — metadata the uploader
// already wrote — against the taxonomy's keywords, and assigns every term
// whose keywords appear. Deterministic, explainable, and fast: the right
// tool for a pilot where the owning org corrects anything the classifier
// misses. A learned or LLM classifier slots in behind the same seam later.
type Classifier struct {
	// byKeyword maps one lowercase keyword to the terms it votes for.
	byKeyword map[string][]Term
}

// NewClassifier indexes the given terms for keyword matching. Multi-word
// keywords are split into their parts and each part (three characters or
// longer — the tokenizer's floor) indexes the term, so "spring barley"
// scores two hits for tillage when both words appear, which the
// longest-keyword tiebreak already favors over single-word terms.
func NewClassifier(terms []Term) *Classifier {
	c := &Classifier{byKeyword: map[string][]Term{}}
	for _, t := range terms {
		for _, kw := range t.DefaultKeywords() {
			for _, part := range strings.Fields(kw) {
				if len(part) >= 3 {
					c.byKeyword[part] = append(c.byKeyword[part], t)
				}
			}
		}
	}
	return c
}

// Classify returns one assignment per category the text gives evidence for.
// Within a category, the term with the most keyword hits wins; ties go to
// the longest matched keyword (more specific), then alphabetically, so the
// same text always classifies the same way. No evidence means no assignment
// — the classifier never guesses.
func (c *Classifier) Classify(_ context.Context, text string) []Assignment {
	if c == nil || len(c.byKeyword) == 0 {
		return nil
	}
	words := tokenize(text)
	if len(words) == 0 {
		return nil
	}
	// Count, per term, its matched keywords and the longest one.
	hits := map[string]int{}    // term ID → keyword hit count
	longest := map[string]int{} // term ID → longest matched keyword
	for _, w := range words {
		for _, t := range c.byKeyword[w] {
			hits[t.ID]++
			if len(w) > longest[t.ID] {
				longest[t.ID] = len(w)
			}
		}
	}
	if len(hits) == 0 {
		return nil
	}

	// The best-supported term per category.
	byCategory := map[Category]Term{}
	bestScore := map[Category][2]int{} // [hits, longestKeyword]
	termsByID := map[string]Term{}
	for _, kws := range c.byKeyword {
		for _, t := range kws {
			termsByID[t.ID] = t
		}
	}
	for id, n := range hits {
		t := termsByID[id]
		score := [2]int{n, longest[id]}
		cur, seen := bestScore[t.Category]
		if !seen || score[0] > cur[0] || (score[0] == cur[0] &&
			(score[1] > cur[1] || (score[1] == cur[1] && t.Value < byCategory[t.Category].Value))) {
			byCategory[t.Category] = t
			bestScore[t.Category] = score
		}
	}

	now := time.Now().UTC()
	out := make([]Assignment, 0, len(byCategory))
	for _, cat := range Categories {
		t, ok := byCategory[cat]
		if !ok {
			continue
		}
		score := bestScore[cat]
		out = append(out, Assignment{
			Category:   cat,
			TermID:     t.ID,
			Value:      t.Value,
			Label:      t.Label,
			Confidence: confidence(score[0], score[1], len(words)),
			Source:     SourceClassifier,
			AssignedAt: now,
		})
	}
	return out
}

// confidence maps evidence to 0..1: one short keyword in a long document is
// a hunch; many long keywords are near-certain. The curve is deliberately
// conservative — the classifier's labels are suggestions the org corrects,
// so overstating certainty would be the worse failure.
func confidence(hits, longestKeyword, totalWords int) float64 {
	if totalWords <= 0 {
		return 0
	}
	density := float64(hits) / float64(totalWords)
	scaled := density * 10 // 10% keyword density saturates
	if scaled > 1 {
		scaled = 1
	}
	specificity := float64(longestKeyword) / 12 // a 12-char keyword saturates
	if specificity > 1 {
		specificity = 1
	}
	return round2(0.4*scaled + 0.6*specificity)
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

// tokenize lowercases and splits text into words, keeping letters and
// numbers. Multi-word keywords match through their parts, which
// NewClassifier indexed individually.
func tokenize(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	out := fields[:0:0]
	for _, f := range fields {
		if len(f) >= 3 && !isStopword(f) {
			out = append(out, f)
		}
	}
	return out
}

// isStopword reports whether w is too generic to classify on.
func isStopword(w string) bool { return stopwords[w] }

// stopwords are words no keyword should ride on.
var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true,
	"data": true, "dataset": true, "records": true, "file": true,
}

// SortAssignments orders assignments by category for stable storage and
// comparison.
func SortAssignments(a []Assignment) {
	sort.Slice(a, func(i, j int) bool {
		if a[i].Category != a[j].Category {
			return a[i].Category < a[j].Category
		}
		return a[i].TermID < a[j].TermID
	})
}
