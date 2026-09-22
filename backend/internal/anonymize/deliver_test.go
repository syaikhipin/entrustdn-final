package anonymize_test

import (
	"bytes"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/anonymize"
)

// Delivery at its seam (ADR 0005): Collection outputs are anonymized
// before a Data Consumer may retrieve them. The defining assertions: no
// identifier survives delivery, and the pseudonym map itself never rides
// along — delivered bytes carry pseudonyms only.

func TestCleanDeliveryCSVStripsIdentifiers(t *testing.T) {
	m := anonymize.NewMemoryMap()
	d := anonymize.NewDelivery(m)

	payload := []byte("name,phone,plot_lat,plot_lng,notes\n" +
		"Mary Byrne,087 123 4567,52.123456,-8.654321,write mary.byrne@farm.ie\n" +
		"Pat Smith,0869876543,51.987654,-9.123456,call Dr John Smith\n")
	cleaned, err := d.Collection(t.Context(), "org-1", "csv", payload)
	if err != nil {
		t.Fatalf("Collection: %v", err)
	}
	got := cleaned.Bytes()
	for _, needle := range []string{
		"Mary Byrne", "Pat Smith", "John Smith",
		"087 123 4567", "0869876543",
		"52.123456", "-8.654321", "51.987654", "-9.123456",
		"mary.byrne@farm.ie",
	} {
		if bytes.Contains(got, []byte(needle)) {
			t.Errorf("IDENTITY LEAK: %q survives delivery:\n%s", needle, got)
		}
	}
}

func TestCleanedCollectionIsOpaqueUntilDelivered(t *testing.T) {
	// The choke point: a CleanedCollection exists only through
	// Collection(), so delivered bytes are cleaned bytes by construction.
	var c anonymize.CleanedCollection
	if c.Bytes() != nil {
		t.Errorf("zero CleanedCollection carries bytes: %q", c.Bytes())
	}
}

func TestCleanDeliveryCarriesNoPseudonymMap(t *testing.T) {
	m := anonymize.NewMemoryMap()
	d := anonymize.NewDelivery(m)

	// Prime the map, then deliver data that uses it.
	if _, err := m.Pseudonym(t.Context(), "org-1", "name", "Mary Byrne"); err != nil {
		t.Fatalf("Pseudonym: %v", err)
	}
	cleaned, err := d.Collection(t.Context(), "org-1", "csv",
		[]byte("name\nMary Byrne\n"))
	if err != nil {
		t.Fatalf("Collection: %v", err)
	}
	got := cleaned.Bytes()

	// No map entry (kind + token pairs, the whole map, row listings) may
	// appear in the delivered bytes — only the in-data pseudonym does.
	entries, _, err := m.Entries(t.Context(), "org-1", 100, 0)
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	for _, e := range entries {
		// A token may legitimately appear as a replacement inside the data,
		// but never as a "kind,pseudonym" map row.
		row := e.Kind + "," + e.Pseudonym
		if bytes.Contains(got, []byte(row)) {
			t.Errorf("pseudonym map row %q rode along in delivery", row)
		}
	}
	if !bytes.Contains(got, []byte("name\n")) {
		t.Errorf("delivered payload lost its structure:\n%s", got)
	}
}

func TestCleanDeliveryFreeText(t *testing.T) {
	m := anonymize.NewMemoryMap()
	d := anonymize.NewDelivery(m)

	cleaned, err := d.Collection(t.Context(), "org-1", "txt",
		[]byte("Farmer Mrs Mary Byrne can be reached at 0871234567\n"))
	if err != nil {
		t.Fatalf("Collection: %v", err)
	}
	got := cleaned.Bytes()
	for _, needle := range []string{"Mary Byrne", "0871234567"} {
		if bytes.Contains(got, []byte(needle)) {
			t.Errorf("IDENTITY LEAK: %q survives delivery:\n%s", needle, got)
		}
	}
}
