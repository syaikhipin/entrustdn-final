package taxonomy

import (
	"context"
	"os"
	"testing"
)

// TestJevClassifyLive hits the real MindsHub API. Skipped without a key.
func TestJevClassifyLive(t *testing.T) {
	key := os.Getenv("THRESH_JEV_API_KEY")
	if key == "" {
		t.Skip("THRESH_JEV_API_KEY not set")
	}
	jc := NewJevClassifier(SeedData, key)
	text := "Winter cereal yield data for Italy 2025, by province, with sowing and harvest dates. Winter wheat yield per hectare."
	assignments := jc.Classify(context.Background(), text)
	t.Logf("Jev assignments: %d", len(assignments))
	for _, a := range assignments {
		t.Logf("  %s: %s (%s) conf=%.2f src=%s", a.Category, a.Value, a.Label, a.Confidence, a.Source)
	}
	if len(assignments) == 0 {
		t.Error("expected at least one assignment from Jev")
	}
	// The wheat text should map to tillage, not veterinary.
	for _, a := range assignments {
		if a.Category == CategoryCrop && a.Value != "tillage" {
			t.Errorf("crop: got %q, want tillage", a.Value)
		}
		if a.Category == CategoryOutcome && a.Value != "yield" {
			t.Errorf("outcome: got %q, want yield", a.Value)
		}
	}
}
