package usecase

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/amurru/lesac/internal/domain"
)

type fakeMetadataStore struct {
	items map[domain.FileID]domain.FileMeta
}

func newFakeMetadataStore() *fakeMetadataStore {
	return &fakeMetadataStore{items: map[domain.FileID]domain.FileMeta{}}
}

func (f *fakeMetadataStore) Save(_ context.Context, meta domain.FileMeta) error {
	f.items[meta.ID] = meta
	return nil
}

func (f *fakeMetadataStore) Get(_ context.Context, id domain.FileID) (domain.FileMeta, error) {
	meta, ok := f.items[id]
	if !ok {
		return domain.FileMeta{}, domain.ErrNotFound
	}
	return meta, nil
}

func (f *fakeMetadataStore) Delete(_ context.Context, id domain.FileID) error {
	if _, ok := f.items[id]; !ok {
		return domain.ErrNotFound
	}
	delete(f.items, id)
	return nil
}

func (f *fakeMetadataStore) ListExpired(
	_ context.Context,
	before time.Time,
	_ int,
) ([]domain.FileMeta, error) {
	expired := make([]domain.FileMeta, 0)
	for _, meta := range f.items {
		if meta.ExpiresAt != nil && !meta.ExpiresAt.After(before) {
			expired = append(expired, meta)
		}
	}
	return expired, nil
}

type fakeBlobStore struct {
	blobs map[string][]byte
}

func newFakeBlobStore() *fakeBlobStore {
	return &fakeBlobStore{blobs: map[string][]byte{}}
}

func (f *fakeBlobStore) Put(_ context.Context, key string, body io.Reader) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.blobs[key] = data
	return nil
}

func (f *fakeBlobStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := f.blobs[key]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeBlobStore) Delete(_ context.Context, key string) error {
	if _, ok := f.blobs[key]; !ok {
		return domain.ErrNotFound
	}
	delete(f.blobs, key)
	return nil
}

func TestPutGetDeleteFlow(t *testing.T) {
	metadata := newFakeMetadataStore()
	blobs := newFakeBlobStore()

	service := NewService(
		metadata,
		blobs,
		func() (domain.FileID, error) { return domain.ParseFileID("abcdefghijklmnop") },
		func() time.Time { return time.Unix(1000, 0).UTC() },
	)

	id, err := service.Put(context.Background(), bytes.NewBufferString("hello"), nil, PutFileMeta{
		MIMEType:  "text/plain",
		Extension: "txt",
	})
	if err != nil {
		t.Fatalf("put failed: %v", err)
	}
	if id.String() != "abcdefghijklmnop" {
		t.Fatalf("unexpected id: %s", id)
	}

	reader, meta, err := service.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	defer reader.Close()
	if meta.MIMEType != "text/plain" {
		t.Fatalf("unexpected mime type: %q", meta.MIMEType)
	}
	if meta.Extension != "txt" {
		t.Fatalf("unexpected extension: %q", meta.Extension)
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected data: %q", string(data))
	}

	if err := service.Delete(context.Background(), id); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if err := service.Delete(context.Background(), id); err != nil {
		t.Fatalf("second delete should be idempotent, got: %v", err)
	}
}

func TestPutRejectsInvalidLifetime(t *testing.T) {
	metadata := newFakeMetadataStore()
	blobs := newFakeBlobStore()
	service := NewService(metadata, blobs, nil, nil)

	invalid := int64(0)
	_, err := service.Put(context.Background(), bytes.NewBufferString("x"), &invalid, PutFileMeta{})
	if !errors.Is(err, domain.ErrInvalidLifetime) {
		t.Fatalf("expected invalid lifetime error, got: %v", err)
	}
}

func TestGetExpiredReturnsNotFound(t *testing.T) {
	metadata := newFakeMetadataStore()
	blobs := newFakeBlobStore()

	now := time.Unix(2000, 0).UTC()
	service := NewService(
		metadata,
		blobs,
		func() (domain.FileID, error) { return domain.ParseFileID("abcdefghijklmnop") },
		func() time.Time { return now },
	)

	lifetime := int64(1)
	id, err := service.Put(context.Background(), bytes.NewBufferString("hello"), &lifetime, PutFileMeta{})
	if err != nil {
		t.Fatalf("put failed: %v", err)
	}

	service.now = func() time.Time { return now.Add(2 * time.Second) }
	_, _, err = service.Get(context.Background(), id)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected not found for expired file, got: %v", err)
	}
}

func TestPutBatchReturnsPerItemResults(t *testing.T) {
	metadata := newFakeMetadataStore()
	blobs := newFakeBlobStore()

	service := NewService(metadata, blobs, nil, nil)
	items := []PutBatchItem{
		{Body: []byte("one"), Metadata: PutFileMeta{MIMEType: "text/plain", Extension: "txt"}},
		{Body: []byte("two"), Metadata: PutFileMeta{MIMEType: "application/json", Extension: "json"}},
	}

	results, err := service.PutBatch(context.Background(), items, nil)
	if err != nil {
		t.Fatalf("put batch failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for i, result := range results {
		if result.Index != i {
			t.Fatalf("unexpected result index %d at position %d", result.Index, i)
		}
		if result.Err != nil {
			t.Fatalf("unexpected item error: %v", result.Err)
		}
		if result.ID == "" {
			t.Fatalf("expected id at position %d", i)
		}
		if result.Metadata != items[i].Metadata {
			t.Fatalf("unexpected metadata at position %d: %+v", i, result.Metadata)
		}
	}
}

func TestPutBatchRejectsEmptyItems(t *testing.T) {
	service := NewService(newFakeMetadataStore(), newFakeBlobStore(), nil, nil)

	_, err := service.PutBatch(context.Background(), nil, nil)
	if !errors.Is(err, domain.ErrEmptyBatch) {
		t.Fatalf("expected empty batch error, got: %v", err)
	}
}
