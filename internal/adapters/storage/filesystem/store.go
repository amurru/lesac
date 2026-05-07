package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/amurru/lesac/internal/domain"
)

// Store is a filesystem-backed blob store implementation.
type Store struct {
	root string
}

// NewStore creates a filesystem-backed blob store rooted at root.
func NewStore(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("filesystem root is empty")
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create root directory: %w", err)
	}

	return &Store{root: absRoot}, nil
}

// Put writes a blob to the filesystem under key.
func (s *Store) Put(_ context.Context, key string, body io.Reader) error {
	filePath, err := s.resolvePath(key)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return fmt.Errorf("create parent directories: %w", err)
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(filePath), ".lesac-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	tmpPath := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if _, err := io.Copy(tmpFile, body); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

// Get opens a blob by key for reading.
func (s *Store) Get(_ context.Context, key string) (io.ReadCloser, error) {
	filePath, err := s.resolvePath(key)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("open file: %w", err)
	}
	return file, nil
}

// Delete removes a blob by key.
func (s *Store) Delete(_ context.Context, key string) error {
	filePath, err := s.resolvePath(key)
	if err != nil {
		return err
	}

	if err := os.Remove(filePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("remove file: %w", err)
	}
	return nil
}

func (s *Store) resolvePath(key string) (string, error) {
	if key == "" || strings.ContainsRune(key, '\x00') {
		return "", fmt.Errorf("invalid key")
	}

	clean := path.Clean("/" + key)
	rel := strings.TrimPrefix(clean, "/")
	if rel == "." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("invalid key path")
	}

	fullPath := filepath.Join(s.root, filepath.FromSlash(rel))
	relativeToRoot, err := filepath.Rel(s.root, fullPath)
	if err != nil {
		return "", fmt.Errorf("resolve relative path: %w", err)
	}
	if strings.HasPrefix(relativeToRoot, "..") {
		return "", fmt.Errorf("key escapes root")
	}

	return fullPath, nil
}
