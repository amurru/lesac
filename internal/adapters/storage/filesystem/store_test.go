package filesystem

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/amurru/lesac/internal/domain"
)

func TestPutGetDelete(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("new store failed: %v", err)
	}

	key := filepath.ToSlash("ab/cd/abcdefghijklmnop")
	if err := store.Put(context.Background(), key, bytes.NewBufferString("hello")); err != nil {
		t.Fatalf("put failed: %v", err)
	}

	reader, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected data: %q", string(data))
	}

	if err := store.Delete(context.Background(), key); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	if err := store.Delete(context.Background(), key); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected not found, got: %v", err)
	}
}
