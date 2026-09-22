package anonymize_test

import (
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/anonymize"
)

// The pseudonym map at its seam: stable per (org, kind, value), partitioned
// by org and by kind, and erasable — erasure is the GDPR surface (ADR 0005),
// so a deleted entry re-mints on next use.

func TestPseudonymMapStablePerOrg(t *testing.T) {
	m := anonymize.NewMemoryMap()
	ctx := t.Context()

	a1, err := m.Pseudonym(ctx, "org-1", "name", "Mary Byrne")
	if err != nil {
		t.Fatalf("Pseudonym: %v", err)
	}
	a2, err := m.Pseudonym(ctx, "org-1", "name", "Mary Byrne")
	if err != nil {
		t.Fatalf("Pseudonym again: %v", err)
	}
	if a1 != a2 {
		t.Errorf("same value mapped to %q then %q — the map must be stable", a1, a2)
	}

	b, err := m.Pseudonym(ctx, "org-2", "name", "Mary Byrne")
	if err != nil {
		t.Fatalf("Pseudonym org-2: %v", err)
	}
	if b == a1 {
		t.Errorf("org-2 received org-1's pseudonym %q — maps must be org-partitioned", a1)
	}

	// Kinds partition too: a name and an email of the same person are
	// different identifiers with different maps.
	e, err := m.Pseudonym(ctx, "org-1", "email", "Mary Byrne")
	if err != nil {
		t.Fatalf("Pseudonym email: %v", err)
	}
	if e == a1 {
		t.Errorf("email kind shares the name map (%q) — kinds must partition", e)
	}
}

func TestPseudonymMapErasureRemints(t *testing.T) {
	m := anonymize.NewMemoryMap()
	ctx := t.Context()

	a1, err := m.Pseudonym(ctx, "org-1", "name", "Mary Byrne")
	if err != nil {
		t.Fatalf("Pseudonym: %v", err)
	}
	if err := m.EraseOrg(ctx, "org-1"); err != nil {
		t.Fatalf("EraseOrg: %v", err)
	}
	a2, err := m.Pseudonym(ctx, "org-1", "name", "Mary Byrne")
	if err != nil {
		t.Fatalf("Pseudonym after erasure: %v", err)
	}
	if a1 == a2 {
		t.Errorf("pseudonym %q survived erasure — erasure must re-mint", a1)
	}
}

func TestPseudonymMapEraseUnknownOrgIsNoop(t *testing.T) {
	m := anonymize.NewMemoryMap()
	if err := m.EraseOrg(t.Context(), "org-never-seen"); err != nil {
		t.Fatalf("EraseOrg unknown: %v", err)
	}
}
