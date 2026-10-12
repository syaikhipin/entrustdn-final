package taxonomy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// SourceJev marks assignments made by the Jev decision model via MindsHub.
const SourceJev = "jev"

// JevClassifier auto-categorizes Data Assets at ingest using the Jev decision
// model (TypeSafe, via MindsHub) instead of keyword matching. It implements
// the same Categorizer seam as Classifier: given the asset's classification
// text, it asks Jev one `choice` question per taxonomy category and maps the
// chosen terms to Assignments.
//
// Jev understands semantics the keyword classifier misses — "winter cereal
// yield per hectare by province" maps to tillage/yield without any of those
// exact keywords appearing. The keyword Classifier remains the fallback when
// the API is unreachable or unconfigured.
type JevClassifier struct {
	apiKey   string
	baseURL  string
	model    string
	client   *http.Client
	terms    []Term
	fallback *Classifier
}

// NewJevClassifier builds a Jev-backed classifier over the given taxonomy
// terms. An empty apiKey disables Jev (Classify falls back to keywords).
func NewJevClassifier(terms []Term, apiKey string) *JevClassifier {
	return &JevClassifier{
		apiKey:  strings.TrimSpace(apiKey),
		baseURL: strings.TrimRight(os.Getenv("THRESH_JEV_BASE_URL"), "/"),
		model:   os.Getenv("THRESH_JEV_MODEL"),
		client:  &http.Client{Timeout: 45 * time.Second},
		terms:   terms,
		fallback: NewClassifier(terms),
	}
}

// jevBaseURL returns the configured endpoint or the MindsHub default.
func (j *JevClassifier) jevBaseURL() string {
	if j.baseURL != "" {
		return j.baseURL
	}
	return "https://api.mindshub.ai"
}

// jevModel returns the configured model alias or the default.
func (j *JevClassifier) jevModel() string {
	if j.model != "" {
		return j.model
	}
	return "jev"
}

// jevQuestion is one named decision question in the MindsHub wire format.
type jevQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

// jevRequest is the MindsHub decisions request body.
type jevRequest struct {
	Model     string                 `json:"model"`
	State     map[string]string      `json:"state"`
	Questions map[string]jevQuestion `json:"questions"`
}

// jevChoiceAnswer is a `choice` answer in the MindsHub response.
type jevChoiceAnswer struct {
	Type        string             `json:"type"`
	Choice      string             `json:"choice"`
	Confidence  float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

// jevResponse is the MindsHub decisions response body.
type jevResponse struct {
	Model   string                     `json:"model"`
	Answers map[string]jevChoiceAnswer `json:"answers"`
}

// Classify sends the text to Jev as one choice question per category and
// maps the answers to Assignments. Terms are keyed by their Value slug so
// the model's choice maps back unambiguously. A low-confidence or "other"
// choice yields no assignment for that category — like the keyword
// classifier, Jev never guesses without evidence.
//
// Any API failure falls back to the keyword classifier so ingest never
// blocks on the model.
func (j *JevClassifier) Classify(ctx context.Context, text string) []Assignment {
	if j == nil || j.apiKey == "" || len(j.terms) == 0 {
		if j != nil && j.fallback != nil {
			return j.fallback.Classify(ctx, text)
		}
		return nil
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}

	// One choice question per category that has terms.
	questions := map[string]jevQuestion{}
	termsByCategory := map[Category][]Term{}
	for _, t := range j.terms {
		termsByCategory[t.Category] = append(termsByCategory[t.Category], t)
	}
	for _, cat := range Categories {
		terms := termsByCategory[cat]
		if len(terms) == 0 {
			continue
		}
		criteria := map[string]string{}
		for _, t := range terms {
			desc := t.Label
			if len(t.Keywords) > 0 {
				desc = fmt.Sprintf("%s (e.g. %s)", t.Label, strings.Join(t.Keywords, ", "))
			}
			criteria[t.Value] = desc
		}
		criteria["other"] = "None of the above fit, or not enough information."
		questions[string(cat)] = jevQuestion{
			Type:         "choice",
			Instructions: fmt.Sprintf("Which %s best describes the agricultural dataset below? Choose the single best fit.", categoryLabel(cat)),
			Criteria:     criteria,
		}
	}
	if len(questions) == 0 {
		return j.fallback.Classify(ctx, text)
	}

	body, err := json.Marshal(jevRequest{
		Model:     j.jevModel(),
		State:     map[string]string{"dataset": trimmed},
		Questions: questions,
	})
	if err != nil {
		return j.fallback.Classify(ctx, text)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.jevBaseURL()+"/v1/decisions", bytes.NewReader(body))
	if err != nil {
		return j.fallback.Classify(ctx, text)
	}
	req.Header.Set("Authorization", "Bearer "+j.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "curl/8.0")
	req.Header.Set("Accept", "*/*")

	resp, err := j.client.Do(req)
	if err != nil {
		log.Printf("taxonomy: jev request failed: %v", err)
		return j.fallback.Classify(ctx, text)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		log.Printf("taxonomy: jev status %d: %s", resp.StatusCode, buf.String()[:200])
		return j.fallback.Classify(ctx, text)
	}

	var out jevResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return j.fallback.Classify(ctx, text)
	}

	now := time.Now().UTC()
	assignments := make([]Assignment, 0, len(questions))
	for _, cat := range Categories {
		ans, ok := out.Answers[string(cat)]
		if !ok || ans.Choice == "" || ans.Choice == "other" {
			continue
		}
		// Map the chosen slug back to its term.
		var term *Term
		for i, t := range termsByCategory[cat] {
			if t.Value == ans.Choice {
				term = &termsByCategory[cat][i]
				break
			}
		}
		if term == nil {
			continue
		}
		// Require reasonable confidence — Jev's confidence is its own
		// certainty statistic, not the top probability.
		if ans.Confidence < 0.5 {
			continue
		}
		assignments = append(assignments, Assignment{
			Category:   cat,
			TermID:     term.ID,
			Value:      term.Value,
			Label:      term.Label,
			Confidence: round2(ans.Confidence),
			Source:     SourceJev,
			AssignedAt: now,
		})
	}
	return assignments
}

// categoryLabel renders a category for the model's instructions.
func categoryLabel(c Category) string {
	switch c {
	case CategoryCrop:
		return "crop or livestock system"
	case CategoryRegion:
		return "region"
	case CategoryGrowthStage:
		return "growth stage"
	case CategoryIntervention:
		return "intervention or input applied"
	case CategoryOutcome:
		return "measured outcome"
	case CategoryDataType:
		return "data type"
	default:
		return string(c)
	}
}
