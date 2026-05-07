package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/amurru/lesac/internal/domain"
)

func TestSaveGetListExpiredDelete(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lesac.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}
	defer store.Close()

	id, err := domain.ParseFileID("abcdefghijklmnop")
	if err != nil {
		t.Fatalf("parse file id failed: %v", err)
	}
	now := time.Unix(1_000, 0).UTC()
	exp := now.Add(10 * time.Second)

	meta := domain.FileMeta{
		ID:         id,
		StorageKey: "ab/cd/abcdefghijklmnop",
		CreatedAt:  now,
		ExpiresAt:  &exp,
	}
	if err := store.Save(context.Background(), meta); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if loaded.StorageKey != meta.StorageKey {
		t.Fatalf("unexpected storage key: %q", loaded.StorageKey)
	}

	expired, err := store.ListExpired(context.Background(), now.Add(20*time.Second), 100)
	if err != nil {
		t.Fatalf("list expired failed: %v", err)
	}
	if len(expired) != 1 {
		t.Fatalf("expected one expired item, got %d", len(expired))
	}

	if err := store.Delete(context.Background(), id); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if err := store.Delete(context.Background(), id); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected not found after delete, got: %v", err)
	}
}
