package usecase

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path"
	"time"

	"github.com/amurru/lesac/internal/domain"
	"github.com/amurru/lesac/internal/ports"
)

// IDGenerator creates valid file identifiers.
type IDGenerator func() (domain.FileID, error)

// Service coordinates file storage and metadata operations.
type Service struct {
	metadata    ports.MetadataStore
	blobs       ports.BlobStore
	idGenerator IDGenerator
	now         func() time.Time
}

// PutBatchItem represents one file payload in a batch upload.
type PutBatchItem struct {
	Body []byte
}

// PutBatchResult contains the result for one batch item.
type PutBatchResult struct {
	Index int
	ID    domain.FileID
	Err   error
}

// NewService creates a Service with optional ID generator and clock overrides.
func NewService(
	metadata ports.MetadataStore,
	blobs ports.BlobStore,
	idGenerator IDGenerator,
	now func() time.Time,
) *Service {
	if idGenerator == nil {
		idGenerator = randomID
	}
	if now == nil {
		now = time.Now
	}
	return &Service{
		metadata:    metadata,
		blobs:       blobs,
		idGenerator: idGenerator,
		now:         now,
	}
}

// Put stores a single file and returns its generated file ID.
func (s *Service) Put(
	ctx context.Context,
	body io.Reader,
	lifetimeSeconds *int64,
) (domain.FileID, error) {
	if lifetimeSeconds != nil && *lifetimeSeconds <= 0 {
		return "", domain.ErrInvalidLifetime
	}

	id, err := s.idGenerator()
	if err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}

	now := s.now().UTC()
	storageKey := keyFromID(id)

	var expiresAt *time.Time
	if lifetimeSeconds != nil {
		exp := now.Add(time.Duration(*lifetimeSeconds) * time.Second)
		expiresAt = &exp
	}

	if err := s.blobs.Put(ctx, storageKey, body); err != nil {
		return "", err
	}

	meta := domain.FileMeta{
		ID:         id,
		StorageKey: storageKey,
		CreatedAt:  now,
		ExpiresAt:  expiresAt,
	}
	if err := s.metadata.Save(ctx, meta); err != nil {
		cleanupErr := s.blobs.Delete(ctx, storageKey)
		if cleanupErr != nil && !errors.Is(cleanupErr, domain.ErrNotFound) {
			return "", errors.Join(err, cleanupErr)
		}
		return "", err
	}

	return id, nil
}

// PutBatch stores a batch of files and returns a result per item.
func (s *Service) PutBatch(
	ctx context.Context,
	items []PutBatchItem,
	lifetimeSeconds *int64,
) ([]PutBatchResult, error) {
	if lifetimeSeconds != nil && *lifetimeSeconds <= 0 {
		return nil, domain.ErrInvalidLifetime
	}
	if len(items) == 0 {
		return nil, domain.ErrEmptyBatch
	}

	results := make([]PutBatchResult, 0, len(items))
	for i, item := range items {
		id, err := s.Put(ctx, bytes.NewReader(item.Body), lifetimeSeconds)
		results = append(results, PutBatchResult{
			Index: i,
			ID:    id,
			Err:   err,
		})
	}
	return results, nil
}

// Get returns the stored file body for the given file ID.
func (s *Service) Get(ctx context.Context, id domain.FileID) (io.ReadCloser, error) {
	meta, err := s.metadata.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if meta.IsExpired(s.now().UTC()) {
		return nil, domain.ErrNotFound
	}

	reader, err := s.blobs.Get(ctx, meta.StorageKey)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			_ = s.metadata.Delete(ctx, id)
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return reader, nil
}

// Delete removes a stored file and its metadata for the given file ID.
func (s *Service) Delete(ctx context.Context, id domain.FileID) error {
	meta, err := s.metadata.Get(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return err
	}

	if err := s.blobs.Delete(
		ctx,
		meta.StorageKey,
	); err != nil &&
		!errors.Is(err, domain.ErrNotFound) {
		return err
	}

	return s.metadata.Delete(ctx, id)
}

// SweepExpired removes expired files and metadata, up to the provided limit.
func (s *Service) SweepExpired(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}

	expired, err := s.metadata.ListExpired(ctx, s.now().UTC(), limit)
	if err != nil {
		return 0, err
	}

	var combined error
	processed := 0
	for _, meta := range expired {
		if err := s.blobs.Delete(
			ctx,
			meta.StorageKey,
		); err != nil &&
			!errors.Is(err, domain.ErrNotFound) {
			combined = errors.Join(combined, fmt.Errorf("delete blob %s: %w", meta.ID, err))
			continue
		}

		if err := s.metadata.Delete(
			ctx,
			meta.ID,
		); err != nil &&
			!errors.Is(err, domain.ErrNotFound) {
			combined = errors.Join(combined, fmt.Errorf("delete metadata %s: %w", meta.ID, err))
			continue
		}
		processed++
	}

	return processed, combined
}

func keyFromID(id domain.FileID) string {
	value := id.String()
	if len(value) < 4 {
		return value
	}
	return path.Join(value[:2], value[2:4], value)
}

func randomID() (domain.FileID, error) {
	data := make([]byte, 18)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	return domain.ParseFileID(encoded)
}
