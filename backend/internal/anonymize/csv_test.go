package anonymize_test

import (
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/anonymize"
)

// Structured CSV at the same unit seam: recognized identifier columns are
// pseudonymized value-by-value, coordinate columns coarsened, everything
// else untouched.

func TestRedactCSVNameColumn(t *testing.T) {
	r := newFakeResolver()
	red := anonymize.NewRedactor(r)

	in := "row,name,breed\n1,Mary Byrne,friesian\n2,Pat Smith,angus\n3,Mary Byrne,friesian\n"
	got, err := red.CSV(t.Context(), "org-1", []byte(in))
	if err != nil {
		t.Fatalf("CSV: %v", err)
	}
	// Same farmer → same pseudonym on every row; different farmers differ.
	want := "row,name,breed\n1,member-0001,friesian\n2,member-0002,angus\n3,member-0001,friesian\n"
	if string(got) != want {
		t.Errorf("CSV =\n%s\nwant\n%s", got, want)
	}
}

func TestRedactCSVRecognizedIdentifierColumns(t *testing.T) {
	r := newFakeResolver()
	red := anonymize.NewRedactor(r)

	in := "farmer,contact_email,phone,latitude,longitude,tag\n" +
		"Mary Byrne,mary@farm.ie,087 123 4567,52.123456,-8.654321,1\n"
	got, err := red.CSV(t.Context(), "org-1", []byte(in))
	if err != nil {
		t.Fatalf("CSV: %v", err)
	}
	want := "farmer,contact_email,phone,latitude,longitude,tag\n" +
		"member-0001,member-0002,member-0003,52.1,-8.7,1\n"
	if string(got) != want {
		t.Errorf("CSV =\n%s\nwant\n%s", got, want)
	}
}

func TestRedactCSVLeavesCleanColumnsUntouched(t *testing.T) {
	r := newFakeResolver()
	red := anonymize.NewRedactor(r)

	in := "tag,breed,yield\n1,friesian,9.2\n2,angus,8.1\n"
	got, err := red.CSV(t.Context(), "org-1", []byte(in))
	if err != nil {
		t.Fatalf("CSV: %v", err)
	}
	if string(got) != in {
		t.Errorf("CSV =\n%s\nwant\n%s unchanged", got, in)
	}
	if len(r.calls) != 0 {
		t.Errorf("resolver called for clean columns: %v", r.calls)
	}
}

func TestRedactCSVFreeTextColumnStillSwept(t *testing.T) {
	r := newFakeResolver()
	red := anonymize.NewRedactor(r)

	// A notes column is not a recognized identifier column, but an email
	// typed into a note is still an identifier — the sweep runs there too.
	in := "tag,notes\n1,ask for mary.byrne@farm.ie\n"
	got, err := red.CSV(t.Context(), "org-1", []byte(in))
	if err != nil {
		t.Fatalf("CSV: %v", err)
	}
	want := "tag,notes\n1,ask for member-0001\n"
	if string(got) != want {
		t.Errorf("CSV =\n%s\nwant\n%s", got, want)
	}
}
