package assets

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/syaikhipin/entrustdn-final/backend/internal/objectstore"
)

// Service is the ingest front door (ticket 04): the only path bytes take
// from an upload request to a stored, cataloged Asset. Pipeline, blob
// storage, and metadata records are collaborators behind interfaces, so
// tests drive the service with in-memory doubles.
type Service struct {
	store    Store
	blobs    objectstore.Store
	pipeline *Pipeline
}

// NewService wires the ingest service: metadata store, blob store, and the
// ordered pipeline stages every upload passes through.
func NewService(store Store, blobs objectstore.Store, pipeline *Pipeline) *Service {
	return &Service{store: store, blobs: blobs, pipeline: pipeline}
}

// IngestResult is what one upload produces.
type IngestResult struct {
	Asset Asset
}

// Ingest streams an upload through the pipeline into storage, then records
// the metadata. Order matters: bytes land first, record second — a failed
// record write leaves an orphan blob (cleaned by best-effort delete), while
// the reverse would point a record at bytes that never landed.
func (s *Service) Ingest(ctx context.Context, a *Asset, r io.Reader) (IngestResult, error) {
	if err := a.Validate(); err != nil {
		return IngestResult{}, err
	}
	if a.ID == "" {
		id, err := NewID()
		if err != nil {
			return IngestResult{}, err
		}
		a.ID = id
	}
	if a.ObjectKey == "" {
		a.ObjectKey = objectKey(a.OrgID, a.ID)
	}

	piped, stages, err := s.pipeline.Run(r)
	if err != nil {
		return IngestResult{}, err
	}
	// Bound what the server will read: one byte past the cap tells us the
	// file is oversized without draining the rest of the request body.
	n, err := s.blobs.Put(ctx, a.ObjectKey, contentTypeOf(a.Format), io.LimitReader(piped, MaxAssetBytes+1))
	if err != nil {
		return IngestResult{}, fmt.Errorf("assets: store blob: %w", err)
	}
	switch {
	case n == 0:
		// An empty upload is a mistake, not an Asset — nothing stored.
		if delErr := s.blobs.Delete(ctx, a.ObjectKey); delErr != nil {
			err = errors.Join(err, fmt.Errorf("assets: also failed to clean the empty blob: %w", delErr))
		}
		return IngestResult{}, fmt.Errorf("assets: file is empty")
	case n > MaxAssetBytes:
		// Oversized: storage wrote the capped bytes — remove them.
		if delErr := s.blobs.Delete(ctx, a.ObjectKey); delErr != nil {
			err = errors.Join(err, fmt.Errorf("assets: also failed to clean the oversized blob: %w", delErr))
		}
		return IngestResult{}, fmt.Errorf("assets: file exceeds the %d byte limit", MaxAssetBytes)
	}
	a.SizeBytes = n
	a.Pipeline = stages

	if err := s.store.CreateAsset(ctx, a); err != nil {
		// The record failed; don't strand the blob behind nothing.
		if delErr := s.blobs.Delete(ctx, a.ObjectKey); delErr != nil {
			err = errors.Join(err, fmt.Errorf("assets: also failed to clean the orphan blob: %w", delErr))
		}
		return IngestResult{}, err
	}
	return IngestResult{Asset: *a}, nil
}

// Get returns one Asset record — the entitlement check reads this before
// any byte moves.
func (s *Service) Get(ctx context.Context, id string) (Asset, error) {
	return s.store.AssetByID(ctx, id)
}

// ByOrg lists the org's Assets, newest first.
func (s *Service) ByOrg(ctx context.Context, orgID string) ([]Asset, error) {
	return s.store.AssetsByOrg(ctx, orgID)
}

// ErrForbidden is returned when the caller may not touch the Asset. The API
// layer maps it to 403; the caller's identity decides, never the object.
var ErrForbidden = errors.New("assets: not your asset")

// Open streams the Asset's bytes out of storage after re-checking
// entitlement against the current record — the record could have changed
// hands since the caller last saw the dashboard.
func (s *Service) Open(ctx context.Context, id string, orgID string) (Asset, Blob, error) {
	a, err := s.store.AssetByID(ctx, id)
	if err != nil {
		return Asset{}, Blob{}, err
	}
	if a.OrgID != orgID {
		return Asset{}, Blob{}, ErrForbidden
	}
	blob, err := s.blobs.Get(ctx, a.ObjectKey)
	if err != nil {
		return Asset{}, Blob{}, err
	}
	return a, blob, nil
}

// UpdateMeta rewrites an Asset's editable fields after the entitlement
// check. Size, format, pipeline, and the blob itself are immutable.
func (s *Service) UpdateMeta(ctx context.Context, id, orgID string, name, description string, prov Provenance) (Asset, error) {
	a, err := s.store.AssetByID(ctx, id)
	if err != nil {
		return Asset{}, err
	}
	if a.OrgID != orgID {
		return Asset{}, ErrForbidden
	}
	a.Name = name
	a.Description = description
	a.Provenance = prov
	if err := a.Validate(); err != nil {
		return Asset{}, err
	}
	if err := s.store.UpdateAssetMeta(ctx, a); err != nil {
		return Asset{}, err
	}
	return a, nil
}

// Delete removes the record and the blob after the entitlement check.
// Record first, blob second: a blob that outlives its record is invisible
// to the API (the record is the only door), while a record without its
// blob would 500 on download. DeleteAsset returns the record — the caller
// path needs its ObjectKey for the blob cleanup.
func (s *Service) Delete(ctx context.Context, id, orgID string) error {
	a, err := s.store.AssetByID(ctx, id)
	if err != nil {
		return err
	}
	if a.OrgID != orgID {
		return ErrForbidden
	}
	if _, err := s.store.DeleteAsset(ctx, id); err != nil {
		return err
	}
	if err := s.blobs.Delete(ctx, a.ObjectKey); err != nil {
		return fmt.Errorf("assets: record deleted but the blob delete failed (orphan blob %s): %w", a.ObjectKey, err)
	}
	return nil
}

// Blob is the read side of a stored object (objectstore.Blob, aliased so
// callers of this package need not import objectstore).
type Blob = objectstore.Blob

func objectKey(orgID, assetID string) string {
	return "assets/" + orgID + "/" + assetID
}

// contentTypeOf maps a format tag to a MIME type for storage and download.
func contentTypeOf(format string) string {
	switch format {
	case "csv":
		return "text/csv"
	case "json":
		return "application/json"
	case "pdf":
		return "application/pdf"
	case "txt", "md":
		return "text/plain"
	case "xls":
		return "application/vnd.ms-excel"
	case "xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "zip":
		return "application/zip"
	case "parquet":
		return "application/vnd.apache.parquet"
	default:
		return "application/octet-stream"
	}
}
