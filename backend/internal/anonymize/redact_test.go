package anonymize_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/anonymize"
)

// The redactor at its unit seam: one line of bytes in, a cleaned line out.
// Identifiers are pseudonymized through a Resolver (the pseudonym map, faked
// here) or stripped (coordinates are coarsened, not mapped). The defining
// assertion style is adversarial: feed it identifiers, assert none survive.

// fakeResolver hands out one stable fake token per distinct value, and
// records every (kind, value) it was asked about.
type fakeResolver struct {
	tokens map[string]string
	calls  []string
}

func newFakeResolver() *fakeResolver {
	return &fakeResolver{tokens: map[string]string{}}
}

func (f *fakeResolver) Pseudonym(_ context.Context, _, kind, value string) (string, error) {
	call := fmt.Sprintf("%s:%s", kind, value)
	f.calls = append(f.calls, call)
	tok, ok := f.tokens[call]
	if !ok {
		tok = fmt.Sprintf("member-%04x", len(f.tokens)+1)
		f.tokens[call] = tok
	}
	return tok, nil
}

func TestRedactEmails(t *testing.T) {
	r := newFakeResolver()
	red := anonymize.NewRedactor(r)

	got, err := red.FreeText(t.Context(), "org-1", []byte("Contact mary.byrne@farm.ie about the herd"))
	if err != nil {
		t.Fatalf("FreeText: %v", err)
	}
	want := "Contact member-0001 about the herd"
	if string(got) != want {
		t.Errorf("FreeText = %q, want %q", got, want)
	}
	// The map learns the identifier so the org can trace it later.
	if len(r.calls) != 1 || r.calls[0] != "email:mary.byrne@farm.ie" {
		t.Errorf("resolver calls = %v, want one email lookup", r.calls)
	}
}

func TestRedactRepeatedEmailStaysStable(t *testing.T) {
	r := newFakeResolver()
	red := anonymize.NewRedactor(r)

	line := []byte("mary.byrne@farm.ie and mary.byrne@farm.ie again")
	got, err := red.FreeText(t.Context(), "org-1", line)
	if err != nil {
		t.Fatalf("FreeText: %v", err)
	}
	want := "member-0001 and member-0001 again"
	if string(got) != want {
		t.Errorf("FreeText = %q, want %q (same value, same pseudonym)", got, want)
	}
}

func TestRedactPhones(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "spaced mobile",
			in:   "ring 087 123 4567 after 6pm",
			want: "ring member-0001 after 6pm",
		},
		{
			name: "international landline",
			in:   "call +353 1 234 5678 today",
			want: "call member-0001 today",
		},
		{
			name: "packed digits",
			in:   "phone 0871234567 now",
			want: "phone member-0001 now",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newFakeResolver()
			red := anonymize.NewRedactor(r)
			got, err := red.FreeText(t.Context(), "org-1", []byte(tt.in))
			if err != nil {
				t.Fatalf("FreeText: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("FreeText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRedactLeavesNumbersThatAreNotPhones(t *testing.T) {
	r := newFakeResolver()
	red := anonymize.NewRedactor(r)

	// Dates, prices, and sizes must survive untouched: precision over
	// recall — the redactor refuses to mangle ordinary numbers.
	for _, keep := range []string{
		"collected 2026-04-15 at Moorepark",
		"yield 0.92 t/ha, price 0.85/kg",
		"batch 123456 shipped",
	} {
		got, err := red.FreeText(t.Context(), "org-1", []byte(keep))
		if err != nil {
			t.Fatalf("FreeText(%q): %v", keep, err)
		}
		if string(got) != keep {
			t.Errorf("FreeText = %q, want %q unchanged", got, keep)
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("resolver called for non-identifiers: %v", r.calls)
	}
}

func TestRedactCoordinatesAreCoarsenedNotMapped(t *testing.T) {
	r := newFakeResolver()
	red := anonymize.NewRedactor(r)

	t.Run("pair is rounded to one decimal", func(t *testing.T) {
		got, err := red.FreeText(t.Context(), "org-1", []byte("plot at 52.123456,-8.654321 by the river"))
		if err != nil {
			t.Fatalf("FreeText: %v", err)
		}
		want := "plot at 52.1, -8.7 by the river"
		if string(got) != want {
			t.Errorf("FreeText = %q, want %q", got, want)
		}
	})

	t.Run("labeled singles are rounded too", func(t *testing.T) {
		got, err := red.FreeText(t.Context(), "org-1", []byte("lat: 52.1234 lon: -8.654321"))
		if err != nil {
			t.Fatalf("FreeText: %v", err)
		}
		want := "lat: 52.1 lon: -8.7"
		if string(got) != want {
			t.Errorf("FreeText = %q, want %q", got, want)
		}
	})

	// Coordinates are stripped of precision, not pseudonymized: the map
	// must not fill with per-plot values.
	if len(r.calls) != 0 {
		t.Errorf("resolver called for coordinates: %v", r.calls)
	}
}

func TestRedactTitledNames(t *testing.T) {
	r := newFakeResolver()
	red := anonymize.NewRedactor(r)

	got, err := red.FreeText(t.Context(), "org-1", []byte("Mrs Mary Byrne signed the form; Dr John Smith witnessed"))
	if err != nil {
		t.Fatalf("FreeText: %v", err)
	}
	want := "Mrs member-0001 signed the form; Dr member-0002 witnessed"
	if string(got) != want {
		t.Errorf("FreeText = %q, want %q", got, want)
	}
	// The title stays; the name is what the map learns.
	if len(r.calls) != 2 || r.calls[0] != "name:Mary Byrne" || r.calls[1] != "name:John Smith" {
		t.Errorf("resolver calls = %v, want the bare names", r.calls)
	}
}

func TestRedactContextCuedNames(t *testing.T) {
	// Untitled names — the common case in farm logs — are caught when a
	// role word cues them: "farmer Mary Byrne", "signed by Pat Smith".
	// Bare capitalized pairs without a cue stay untouched (precision over
	// recall: "Spring Barley" is a crop, not a farmer).
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "farmer cue",
			in:   "farmer Mary Byrne has 40 hectares",
			want: "farmer member-0001 has 40 hectares",
		},
		{
			name: "signed-by cue",
			in:   "form signed by Pat Smith on 2026-04-15",
			want: "form signed by member-0001 on 2026-04-15",
		},
		{
			name: "member cue with Irish surname",
			in:   "member Sean O'Brien reported the yield",
			want: "member member-0001 reported the yield",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newFakeResolver()
			red := anonymize.NewRedactor(r)
			got, err := red.FreeText(t.Context(), "org-1", []byte(tt.in))
			if err != nil {
				t.Fatalf("FreeText: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("FreeText = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("no cue, no touch", func(t *testing.T) {
		r := newFakeResolver()
		red := anonymize.NewRedactor(r)
		line := "Spring Barley yielded well at Moorepark"
		got, err := red.FreeText(t.Context(), "org-1", []byte(line))
		if err != nil {
			t.Fatalf("FreeText: %v", err)
		}
		if string(got) != line {
			t.Errorf("FreeText = %q, want %q unchanged (precision over recall)", got, line)
		}
		if len(r.calls) != 0 {
			t.Errorf("resolver called without a name cue: %v", r.calls)
		}
	})
}

func TestRedactCleanTextUntouched(t *testing.T) {
	r := newFakeResolver()
	red := anonymize.NewRedactor(r)

	line := "tag,breed,yield\n1,friesian,9.2\n"
	got, err := red.FreeText(t.Context(), "org-1", []byte(line))
	if err != nil {
		t.Fatalf("FreeText: %v", err)
	}
	if string(got) != line {
		t.Errorf("FreeText = %q, want %q unchanged", got, line)
	}
	if len(r.calls) != 0 {
		t.Errorf("resolver called for clean data: %v", r.calls)
	}
}
