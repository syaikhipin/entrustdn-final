// Package objectstore is the blob-storage seam (ADR 0006: server-side S3
// only — no presigned URLs, no client bucket access). Uploads and downloads
// are io.Reader/io.Writer streams so the backend never buffers whole
// objects; entitlement stays in the caller (the API handler), authorization
// in one place.
package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// ErrNotFound is returned when the named object does not exist.
var ErrNotFound = errors.New("objectstore: not found")

// Blob is one stored object's read side.
type Blob struct {
	Body io.ReadCloser
	Size int64
	// ContentType is what was declared at upload, echoed on download so a
	// browser renders rather than mangles.
	ContentType string
}

// Store is the object-storage seam. Keys are server-minted; content type is
// recorded so download can set it honestly.
type Store interface {
	// Put streams r into the bucket under key, returning the byte count.
	Put(ctx context.Context, key, contentType string, r io.Reader) (int64, error)
	// Get streams the object out. ErrNotFound when missing.
	Get(ctx context.Context, key string) (Blob, error)
	// Delete removes the object. Deleting a missing key is not an error —
	// delete is idempotent so asset deletion never leaves a zombie blob
	// behind a record that is already gone.
	Delete(ctx context.Context, key string) error
	// EnsureBucket creates the bucket when missing (dev convenience; in
	// production the bucket exists and the call is a cheap no-op).
	EnsureBucket(ctx context.Context) error
}

// Compile-time checks: both implementations satisfy the seam.
var (
	_ Store = (*Memory)(nil)
	_ Store = (*S3)(nil)
)

// Memory is the in-memory Store double used by Seam 1 tests. It buffers
// because a test's bytes are small; production streams (S3 below).
type Memory struct {
	mu    sync.Mutex
	parts map[string][]byte
	kinds map[string]string
	onGet func(key string) // optional test hook: fail a download
	onPut func(key string) // optional test hook: fail an upload mid-stream
	onDel func(key string) // optional test hook: fail a delete
}

// NewMemory returns an empty object store.
func NewMemory() *Memory {
	return &Memory{parts: map[string][]byte{}, kinds: map[string]string{}}
}

// SetHooks installs failure-injection hooks (nil entries ignored).
func (m *Memory) SetHooks(onGet, onPut, onDel func(key string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onGet, m.onPut, m.onDel = onGet, onPut, onDel
}

// Put buffers r and stores it under key.
func (m *Memory) Put(_ context.Context, key, contentType string, r io.Reader) (int64, error) {
	m.mu.Lock()
	onPut := m.onPut
	m.mu.Unlock()
	if onPut != nil {
		onPut(key)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, fmt.Errorf("objectstore: read upload: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parts[key] = data
	m.kinds[key] = contentType
	return int64(len(data)), nil
}

// Get returns a re-readable snapshot of the object.
func (m *Memory) Get(_ context.Context, key string) (Blob, error) {
	m.mu.Lock()
	onGet := m.onGet
	m.mu.Unlock()
	if onGet != nil {
		onGet(key)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.parts[key]
	if !ok {
		return Blob{}, ErrNotFound
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return Blob{
		Body:        io.NopCloser(strings.NewReader(string(cp))),
		Size:        int64(len(data)),
		ContentType: m.kinds[key],
	}, nil
}

// Delete removes the object; a missing key is not an error.
func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	onDel := m.onDel
	m.mu.Unlock()
	if onDel != nil {
		onDel(key)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.parts, key)
	delete(m.kinds, key)
	return nil
}

// List returns every stored key, sorted — a test-observation helper, not
// part of the Store seam.
func (m *Memory) List() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.parts))
	for k := range m.parts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// EnsureBucket is a no-op for the in-memory store.
func (m *Memory) EnsureBucket(context.Context) error { return nil }

// S3 is the production Store against any S3-compatible service (MinIO in
// dev, the self-hosted bucket in production — ADR 0006). Put and Get stream;
// nothing reads a whole object into memory.
type S3 struct {
	client *s3.Client
	bucket string
}

// S3Config carries the connection facts config.Load reads from env.
type S3Config struct {
	EndpointURL string // e.g. http://localhost:9000 (MinIO)
	Region      string // S3-compatible services accept any non-empty region
	Bucket      string
	AccessKeyID string
	SecretKey   string
}

// NewS3 returns a Store against the given S3-compatible endpoint. Path-style
// addressing is forced: MinIO and self-hosted services need
// bucket-in-path, not virtual-host style.
func NewS3(ctx context.Context, cfg S3Config) (*S3, error) {
	awsCfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(cfg.Region),
		config.WithCredentialsProvider(awscreds.NewStaticCredentialsProvider(
			cfg.AccessKeyID, cfg.SecretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("objectstore: load aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = &cfg.EndpointURL
		o.UsePathStyle = true
	})
	return &S3{client: client, bucket: cfg.Bucket}, nil
}

// Put streams r into the bucket via a managed uploader. The uploader sends
// fixed-size parts as they fill, so memory stays bounded for large files;
// never a whole-object buffer, never a presigned URL (ADR 0006). The byte
// count comes from counting what was read — the S3 upload result carries
// only an ETag, never a size.
func (s *S3) Put(ctx context.Context, key, contentType string, r io.Reader) (int64, error) {
	cn := &countingReader{r: r}
	uploader := manager.NewUploader(s.client)
	_, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
		Body:        cn,
	})
	if err != nil {
		return 0, fmt.Errorf("objectstore: s3 put %s: %w", key, err)
	}
	return cn.n, nil
}

// countingReader totals the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Get streams the object out through a ranged GET — the body arrives as the
// SDK's response stream and is piped to the caller, never buffered whole.
func (s *S3) Get(ctx context.Context, key string) (Blob, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return Blob{}, ErrNotFound
		}
		return Blob{}, fmt.Errorf("objectstore: s3 get %s: %w", key, err)
	}
	return Blob{
		Body:        out.Body,
		Size:        derefInt64(out.ContentLength),
		ContentType: derefString(out.ContentType),
	}, nil
}

// Delete removes the object; a missing key is not an error (idempotent
// delete, matching the seam's contract).
func (s *S3) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("objectstore: s3 delete %s: %w", key, err)
	}
	return nil
}

func isNotFound(err error) bool {
	if errors.Is(err, ErrNotFound) {
		return true
	}
	var apiErr interface{ ErrorCode() string }
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		}
	}
	return false
}

// EnsureBucket creates the bucket when missing. In dev (MinIO) this saves
// manual setup; against real S3 the bucket normally pre-exists and the
// call short-circuits on BucketAlreadyOwnedByYou.
func (s *S3) EnsureBucket(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	if err == nil {
		return nil
	}
	_, err = s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(s.bucket)})
	if err != nil {
		// Two backends racing to create: owned-by-you is success.
		var berr *types.BucketAlreadyOwnedByYou
		if errors.As(err, &berr) {
			return nil
		}
		return fmt.Errorf("objectstore: create bucket %s: %w", s.bucket, err)
	}
	return nil
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
