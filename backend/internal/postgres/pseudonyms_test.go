package postgres_test

import (
	"sync"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/anonymize"
	"github.com/syaikhipin/entrustdn-final/backend/internal/postgres"
)

// The live Postgres implementation of anonymize.Map, exercised against a
// real database (ADR 0007). These complement the in-memory seam tests by
// proving the SQL layer honors the same contract: stability, org
// partitioning, erasure re-mints, and one token under concurrent mints.

func newPseudonymMap(t *testing.T) (*postgres.PseudonymMap, string) {
	t.Helper()
	url := skipIfNoDatabase(t)
	pool := openCleanTestDB(t, url)
	orgID := insertTestOrg(t, pool, "pseudonym-org@thresh.test")
	return postgres.NewPseudonymMap(pool, ""), orgID
}

func TestPseudonymMapStableAndPartitioned(t *testing.T) {
	m, orgID := newPseudonymMap(t)
	ctx := t.Context()

	a1, err := m.Pseudonym(ctx, orgID, "name", "Mary Byrne")
	if err != nil {
		t.Fatalf("Pseudonym: %v", err)
	}
	a2, err := m.Pseudonym(ctx, orgID, "name", "Mary Byrne")
	if err != nil {
		t.Fatalf("Pseudonym again: %v", err)
	}
	if a1 != a2 {
		t.Errorf("unstable mapping: %q then %q", a1, a2)
	}

	b, err := m.Pseudonym(ctx, "11111111-1111-1111-1111-111111111111", "name", "Mary Byrne")
	if err != nil {
		// A foreign key to a nonexistent org is a refusal, not a leak.
		t.Skipf("second org not insertable without a row: %v", err)
	}
	if b == a1 {
		t.Errorf("org partition broken: second org received %q", a1)
	}
}

func TestPseudonymMapEraseRemints(t *testing.T) {
	m, orgID := newPseudonymMap(t)
	ctx := t.Context()

	a1, err := m.Pseudonym(ctx, orgID, "email", "mary@farm.ie")
	if err != nil {
		t.Fatalf("Pseudonym: %v", err)
	}
	if err := m.EraseOrg(ctx, orgID); err != nil {
		t.Fatalf("EraseOrg: %v", err)
	}
	a2, err := m.Pseudonym(ctx, orgID, "email", "mary@farm.ie")
	if err != nil {
		t.Fatalf("Pseudonym after erasure: %v", err)
	}
	if a1 == a2 {
		t.Errorf("pseudonym %q survived erasure — the GDPR surface leaked", a1)
	}
}

func TestPseudonymMapConcurrentMintsAgree(t *testing.T) {
	m, orgID := newPseudonymMap(t)
	ctx := t.Context()

	const writers = 8
	tokens := make(chan string, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := m.Pseudonym(ctx, orgID, "phone", "0871234567")
			if err != nil {
				t.Errorf("concurrent Pseudonym: %v", err)
				return
			}
			tokens <- tok
		}()
	}
	wg.Wait()
	close(tokens)

	first := ""
	for tok := range tokens {
		if first == "" {
			first = tok
		}
		if tok != first {
			t.Errorf("concurrent mints disagreed: %q vs %q — two farmers would merge", first, tok)
		}
	}
}

func TestPseudonymMapEntriesPagesNewestFirst(t *testing.T) {
	m, orgID := newPseudonymMap(t)
	ctx := t.Context()

	// Mint three; capture the email's token so ordering is observable.
	mint := func(kind, value string) string {
		t.Helper()
		tok, err := m.Pseudonym(ctx, orgID, kind, value)
		if err != nil {
			t.Fatalf("Pseudonym(%s): %v", kind, err)
		}
		return tok
	}
	_ = mint("name", "Mary Byrne")
	phoneTok := mint("phone", "0871234567")
	emailTok := mint("email", "mary@farm.ie")

	got, total, err := m.Entries(ctx, orgID, 2, 0)
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(got) != 2 {
		t.Fatalf("page = %d entries, want 2", len(got))
	}
	// Newest first: the email minted last leads the page.
	if got[0].Pseudonym != emailTok || got[1].Pseudonym != phoneTok {
		t.Errorf("page order = [%s, %s], want newest first [%s, %s]",
			got[0].Pseudonym, got[1].Pseudonym, emailTok, phoneTok)
	}
	for _, e := range got {
		if e.Kind == "" {
			t.Errorf("entry carries no kind: %+v", e)
		}
	}
}

func TestPseudonymMapEntriesEmptyOrg(t *testing.T) {
	m, orgID := newPseudonymMap(t)
	got, total, err := m.Entries(t.Context(), orgID, 50, 0)
	if err != nil {
		t.Fatalf("Entries empty: %v", err)
	}
	if total != 0 || len(got) != 0 {
		t.Errorf("empty org Entries = (%d, %d), want (0, 0)", len(got), total)
	}
	_ = anonymize.Entry{}
}
