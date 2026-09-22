package assets_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/anonymize"
	"github.com/syaikhipin/entrustdn-final/backend/internal/assets"
	"github.com/syaikhipin/entrustdn-final/backend/internal/objectstore"
)

// The ingest service at its seam: pipeline, blob store, and metadata store
// are all in-memory doubles. Tests assert observable behavior — what lands
// in storage, what the record says, and who may open or delete.

func newService(t *testing.T) (*assets.Service, *assets.MemoryStore, *objectstore.Memory) {
	t.Helper()
	meta := assets.NewMemoryStore()
	blobs := objectstore.NewMemory()
	svc := assets.NewService(meta, blobs, assets.NewPipeline(anonymize.NewStage(anonymize.NewMemoryMap())))
	return svc, meta, blobs
}

func ingestAsset(t *testing.T, svc *assets.Service, orgID, name, body string) assets.Asset {
	t.Helper()
	res, err := svc.Ingest(t.Context(), &assets.Asset{
		OrgID:  orgID,
		Name:   name,
		Format: assets.DetectFormat(name, ""),
	}, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Ingest(%s): %v", name, err)
	}
	return res.Asset
}

func TestIngestRunsPipelineAndRecordsMetadata(t *testing.T) {
	svc, meta, blobs := newService(t)

	prov := assets.Provenance{Source: "Teagasc Moorepark trial", Notes: "spring 2026"}
	collected := time.Date(2026, 4, 15, 9, 0, 0, 0, time.UTC)
	prov.CollectedAt = &collected
	res, err := svc.Ingest(t.Context(), &assets.Asset{
		OrgID:      "org-1",
		Name:       "herd-registry.csv",
		Format:     assets.DetectFormat("herd-registry.csv", ""),
		Provenance: prov,
	}, strings.NewReader("tag,breed\n1,friesian\n2,angus\n"))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	a := res.Asset

	if a.ID == "" {
		t.Fatal("ingest minted no asset ID")
	}
	if a.SizeBytes != int64(len("tag,breed\n1,friesian\n2,angus\n")) {
		t.Errorf("SizeBytes = %d, want the uploaded byte count", a.SizeBytes)
	}
	if got := a.Pipeline; len(got) != 1 || got[0] != "anonymize" {
		t.Errorf("Pipeline = %v, want [anonymize] (ADR 0005 stage recorded at ingest)", got)
	}
	if a.Format != "csv" {
		t.Errorf("Format = %q, want csv", a.Format)
	}

	// The record round-trips through the metadata store.
	stored, err := meta.AssetByID(t.Context(), a.ID)
	if err != nil {
		t.Fatalf("AssetByID: %v", err)
	}
	if stored.Name != "herd-registry.csv" || stored.OrgID != "org-1" {
		t.Errorf("stored = %+v, want the ingested record", stored)
	}
	if stored.Provenance.Source != "Teagasc Moorepark trial" {
		t.Errorf("Provenance.Source = %q, want the trial site", stored.Provenance.Source)
	}
	if stored.Provenance.CollectedAt == nil || !stored.Provenance.CollectedAt.Equal(collected) {
		t.Errorf("Provenance.CollectedAt = %v, want %v", stored.Provenance.CollectedAt, collected)
	}

	// The bytes streamed into storage unchanged (pass-through stage).
	blob, err := blobs.Get(t.Context(), a.ObjectKey)
	if err != nil {
		t.Fatalf("blobs.Get: %v", err)
	}
	defer blob.Body.Close()
	body, _ := io.ReadAll(blob.Body)
	if !bytes.Equal(body, []byte("tag,breed\n1,friesian\n2,angus\n")) {
		t.Errorf("stored bytes = %q, want the upload unchanged", body)
	}
	if blob.ContentType != "text/csv" {
		t.Errorf("ContentType = %q, want text/csv", blob.ContentType)
	}
}

func TestIngestRejectsBadRecords(t *testing.T) {
	tests := []struct {
		name    string
		asset   assets.Asset
		body    string
		wantErr string
	}{
		{
			name:    "no name",
			asset:   assets.Asset{OrgID: "org-1"},
			body:    "x",
			wantErr: "name",
		},
		{
			name:    "no org",
			asset:   assets.Asset{Name: "file.csv"},
			body:    "x",
			wantErr: "organization",
		},
		{
			name:    "empty body",
			asset:   assets.Asset{OrgID: "org-1", Name: "file.csv"},
			body:    "",
			wantErr: "empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, blobs := newService(t)
			_, err := svc.Ingest(t.Context(), &tt.asset, strings.NewReader(tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Ingest error = %v, want something about %q", err, tt.wantErr)
			}
			// A refused upload leaves nothing behind.
			if len(blobs.List()) != 0 {
				t.Errorf("refused upload left blobs: %v", blobs.List())
			}
		})
	}
}

func TestIngestBlobFailureLeavesNoRecord(t *testing.T) {
	svc, meta, _ := newService(t)

	_, err := svc.Ingest(t.Context(), &assets.Asset{OrgID: "org-1", Name: "x.csv"},
		iotestErrReader{})
	if err == nil {
		t.Fatal("Ingest with a failing reader = nil error, want failure")
	}
	got, _ := meta.AssetsByOrg(t.Context(), "org-1")
	if len(got) != 0 {
		t.Errorf("failed ingest left records: %v", got)
	}
}

func TestOpenStreamsAfterEntitlementCheck(t *testing.T) {
	svc, _, _ := newService(t)
	a := ingestAsset(t, svc, "org-1", "yields.csv", "plot,yield\nA,9.2\n")

	t.Run("owner org streams its bytes", func(t *testing.T) {
		got, blob, err := svc.Open(t.Context(), a.ID, "org-1")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer blob.Body.Close()
		if got.ID != a.ID {
			t.Errorf("Open returned asset %s, want %s", got.ID, a.ID)
		}
		if blob.ContentType != "text/csv" {
			t.Errorf("ContentType = %q", blob.ContentType)
		}
		body, _ := io.ReadAll(blob.Body)
		if string(body) != "plot,yield\nA,9.2\n" {
			t.Errorf("streamed = %q, want the stored bytes", body)
		}
	})

	t.Run("another org is denied", func(t *testing.T) {
		_, _, err := svc.Open(t.Context(), a.ID, "org-2")
		if !errors.Is(err, assets.ErrForbidden) {
			t.Fatalf("Open by stranger = %v, want ErrForbidden", err)
		}
	})

	t.Run("unknown asset is not found", func(t *testing.T) {
		_, _, err := svc.Open(t.Context(), "no-such-id", "org-1")
		if !errors.Is(err, assets.ErrNotFound) {
			t.Fatalf("Open unknown = %v, want ErrNotFound", err)
		}
	})
}

func TestUpdateMetaRewritesEditableFieldsOnly(t *testing.T) {
	svc, _, _ := newService(t)
	a := ingestAsset(t, svc, "org-1", "raw.csv", "a\n1\n")

	newProv := assets.Provenance{Source: "re-checked with the co-op"}
	got, err := svc.UpdateMeta(t.Context(), a.ID, "org-1", "cleaned.csv", "curated", newProv)
	if err != nil {
		t.Fatalf("UpdateMeta: %v", err)
	}
	if got.Name != "cleaned.csv" || got.Description != "curated" {
		t.Errorf("meta not updated: %+v", got)
	}
	if got.SizeBytes != a.SizeBytes || got.Format != a.Format || got.ObjectKey != a.ObjectKey {
		t.Errorf("immutable fields changed: was %+v, now %+v", a, got)
	}
	if len(got.Pipeline) != 1 || got.Pipeline[0] != "anonymize" {
		t.Errorf("Pipeline changed: %v", got.Pipeline)
	}

	if _, err := svc.UpdateMeta(t.Context(), a.ID, "org-2", "stolen.csv", "", assets.Provenance{}); !errors.Is(err, assets.ErrForbidden) {
		t.Errorf("stranger UpdateMeta = %v, want ErrForbidden", err)
	}
	if _, err := svc.UpdateMeta(t.Context(), a.ID, "org-1", "", "", assets.Provenance{}); err == nil {
		t.Error("UpdateMeta to an empty name = nil error, want refusal")
	}
}

func TestDeleteRemovesRecordAndBlob(t *testing.T) {
	svc, meta, blobs := newService(t)
	a := ingestAsset(t, svc, "org-1", "temp.csv", "bye\n")

	if err := svc.Delete(t.Context(), a.ID, "org-2"); !errors.Is(err, assets.ErrForbidden) {
		t.Fatalf("stranger delete = %v, want ErrForbidden", err)
	}
	// The denied delete left the Asset intact.
	if _, err := meta.AssetByID(t.Context(), a.ID); err != nil {
		t.Fatalf("asset vanished after denied delete: %v", err)
	}

	if err := svc.Delete(t.Context(), a.ID, "org-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := meta.AssetByID(t.Context(), a.ID); !errors.Is(err, assets.ErrNotFound) {
		t.Errorf("record still present after delete: %v", err)
	}
	if _, err := blobs.Get(t.Context(), a.ObjectKey); !errors.Is(err, objectstore.ErrNotFound) {
		t.Errorf("blob still present after delete: %v", err)
	}
}

// iotestErrReader is an io.Reader that always fails — the upload's source
// dies mid-stream, which must surface as an Ingest error with no record.
type iotestErrReader struct{}

func (iotestErrReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
