package ports

import (
	"context"
	"io"
	"time"

	"github.com/amurru/lesac/internal/domain"
)

// MetadataStore persists and queries file metadata records.
type MetadataStore interface {
	Save(ctx context.Context, meta domain.FileMeta) error
	Get(ctx context.Context, id domain.FileID) (domain.FileMeta, error)
	Delete(ctx context.Context, id domain.FileID) error
	ListExpired(ctx context.Context, before time.Time, limit int) ([]domain.FileMeta, error)
}

// BlobStore stores and retrieves binary file content by key.
type BlobStore interface {
	Put(ctx context.Context, key string, body io.Reader) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}
