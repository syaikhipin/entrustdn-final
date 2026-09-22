package assets_test

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/anonymize"
	"github.com/syaikhipin/entrustdn-final/backend/internal/assets"
	"github.com/syaikhipin/entrustdn-final/backend/internal/objectstore"
)

// The real anonymization stage at the service seam (ADR 0005): adversarial
// fixtures with names, phones, emails, and field coordinates go in; the
// defining assertion is that none of them survive into stored bytes.

// mapResolver is the smallest real Resolver: stable fake tokens per
// (org, kind, value), no persistence — the pseudonym map's storage is its
// own seam, tested separately.
type mapResolver struct {
	tokens map[string]string
}

func (m *mapResolver) Pseudonym(_ context.Context, org, kind, value string) (string, error) {
	key := org + "\x00" + kind + "\x00" + value
	tok, ok := m.tokens[key]
	if !ok {
		tok = "psn-" + strconv.Itoa(len(m.tokens)+1)
		m.tokens[key] = tok
	}
	return tok, nil
}

func newAnonService(t *testing.T) (*assets.Service, *mapResolver, *objectstore.Memory) {
	t.Helper()
	resolver := &mapResolver{tokens: map[string]string{}}
	meta := assets.NewMemoryStore()
	blobs := objectstore.NewMemory()
	svc := assets.NewService(meta, blobs, assets.NewPipeline(anonymize.NewStage(resolver)))
	return svc, resolver, blobs
}

// storedBytes reads one blob back whole.
func storedBytes(t *testing.T, blobs *objectstore.Memory, key string) []byte {
	t.Helper()
	blob, err := blobs.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("blobs.Get(%s): %v", key, err)
	}
	defer blob.Body.Close()
	out, err := io.ReadAll(blob.Body)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	return out
}

// leakMustNotSurvive fails if any needle appears in the stored bytes.
func leakMustNotSurvive(t *testing.T, stored []byte, needles ...string) {
	t.Helper()
	for _, needle := range needles {
		if bytes.Contains(stored, []byte(needle)) {
			t.Errorf("IDENTITY LEAK: %q survives in stored bytes:\n%s", needle, stored)
		}
	}
}

func TestIngestAnonymizesFreeTextIdentifiers(t *testing.T) {
	svc, _, blobs := newAnonService(t)

	body := "farm log for Mrs Mary Byrne (mary.byrne@farm.ie, 087 123 4567)\n" +
		"plot at 52.123456,-8.654321; yield 9.2 t/ha\n"
	res, err := svc.Ingest(t.Context(), &assets.Asset{
		OrgID:  "org-1",
		Name:   "log.txt",
		Format: "txt",
	}, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	a := res.Asset

	if len(a.Pipeline) != 1 || a.Pipeline[0] != "anonymize" {
		t.Errorf("Pipeline = %v, want [anonymize]", a.Pipeline)
	}
	leakMustNotSurvive(t, storedBytes(t, blobs, a.ObjectKey),
		"Mary Byrne", "mary.byrne@farm.ie", "087 123 4567", "52.123456", "-8.654321")
}

func TestIngestAnonymizesCSVIdentifierColumns(t *testing.T) {
	svc, _, blobs := newAnonService(t)

	in := "name,phone,latitude,tag\nMary Byrne,0871234567,52.123456,1\nPat Smith,0869876543,51.987654,2\n"
	res, err := svc.Ingest(t.Context(), &assets.Asset{OrgID: "org-1", Name: "herd.csv", Format: "csv"},
		strings.NewReader(in))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	a := res.Asset
	leakMustNotSurvive(t, storedBytes(t, blobs, a.ObjectKey),
		"Mary Byrne", "Pat Smith", "0871234567", "0869876543", "52.123456", "51.987654")
}

func TestIngestCleanDataPassesUnchanged(t *testing.T) {
	svc, _, blobs := newAnonService(t)

	body := "tag,breed\n1,friesian\n2,angus\n"
	res, err := svc.Ingest(t.Context(), &assets.Asset{OrgID: "org-1", Name: "clean.csv", Format: "csv"},
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	a := res.Asset
	if got := storedBytes(t, blobs, a.ObjectKey); string(got) != body {
		t.Errorf("clean data mangled: got %q, want %q", got, body)
	}
}

func TestIngestPseudonymsAreScopedPerOrg(t *testing.T) {
	svc, resolver, blobs := newAnonService(t)

	upload := func(org, name string) assets.Asset {
		res, err := svc.Ingest(t.Context(), &assets.Asset{OrgID: org, Name: name, Format: "csv"},
			strings.NewReader("name\nMary Byrne\n"))
		if err != nil {
			t.Fatalf("Ingest(%s): %v", name, err)
		}
		return res.Asset
	}
	a1 := upload("org-1", "one.csv")
	a2 := upload("org-2", "two.csv")

	// The two orgs' maps are independent: the same farmer pseudonymizes to
	// different tokens per org, and neither org's bytes carry the other's.
	if resolver.tokens["org-1\x00name\x00Mary Byrne"] == "" || resolver.tokens["org-2\x00name\x00Mary Byrne"] == "" {
		t.Fatalf("resolver never learned the farmer: %v", resolver.tokens)
	}
	if resolver.tokens["org-1\x00name\x00Mary Byrne"] == resolver.tokens["org-2\x00name\x00Mary Byrne"] {
		t.Errorf("pseudonyms leaked across orgs: both are %q", resolver.tokens["org-1\x00name\x00Mary Byrne"])
	}
	leakMustNotSurvive(t, storedBytes(t, blobs, a1.ObjectKey), resolver.tokens["org-2\x00name\x00Mary Byrne"])
	leakMustNotSurvive(t, storedBytes(t, blobs, a2.ObjectKey), resolver.tokens["org-1\x00name\x00Mary Byrne"])
}
