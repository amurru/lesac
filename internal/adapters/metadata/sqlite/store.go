package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/amurru/lesac/internal/domain"
)

// Store is a SQLite-backed metadata store implementation.
type Store struct {
	db *sql.DB
}

// NewStore opens a SQLite metadata store and initializes the schema.
func NewStore(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("sqlite path is empty")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create sqlite directory: %w", err)
	}

	dsn := fmt.Sprintf("file:%s?_busy_timeout=5000", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}
	db.SetMaxOpenConns(1)

	store := &Store{db: db}
	if err := store.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close closes the underlying SQLite connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Save inserts file metadata into SQLite.
func (s *Store) Save(ctx context.Context, meta domain.FileMeta) error {
	var expires any
	if meta.ExpiresAt != nil {
		expires = meta.ExpiresAt.UTC().Unix()
	}

	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO files (id, storage_key, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		meta.ID.String(),
		meta.StorageKey,
		meta.CreatedAt.UTC().Unix(),
		expires,
	)
	if err != nil {
		return fmt.Errorf("save metadata: %w", err)
	}
	return nil
}

// Get reads file metadata by file ID.
func (s *Store) Get(ctx context.Context, id domain.FileID) (domain.FileMeta, error) {
	var (
		meta       domain.FileMeta
		expiresRaw sql.NullInt64
		createdRaw int64
	)

	err := s.db.QueryRowContext(
		ctx,
		`SELECT id, storage_key, created_at, expires_at FROM files WHERE id = ?`,
		id.String(),
	).Scan(&meta.ID, &meta.StorageKey, &createdRaw, &expiresRaw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.FileMeta{}, domain.ErrNotFound
		}
		return domain.FileMeta{}, fmt.Errorf("get metadata: %w", err)
	}

	meta.CreatedAt = time.Unix(createdRaw, 0).UTC()
	if expiresRaw.Valid {
		exp := time.Unix(expiresRaw.Int64, 0).UTC()
		meta.ExpiresAt = &exp
	}
	return meta, nil
}

// Delete removes metadata by file ID.
func (s *Store) Delete(ctx context.Context, id domain.FileID) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM files WHERE id = ?`, id.String())
	if err != nil {
		return fmt.Errorf("delete metadata: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read rows affected: %w", err)
	}
	if rows == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ListExpired returns metadata entries expired at or before the given time.
func (s *Store) ListExpired(
	ctx context.Context,
	before time.Time,
	limit int,
) ([]domain.FileMeta, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, storage_key, created_at, expires_at
		 FROM files
		 WHERE expires_at IS NOT NULL AND expires_at <= ?
		 ORDER BY expires_at ASC
		 LIMIT ?`,
		before.UTC().Unix(),
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list expired metadata: %w", err)
	}
	defer rows.Close()

	items := make([]domain.FileMeta, 0)
	for rows.Next() {
		var (
			meta       domain.FileMeta
			createdRaw int64
			expiresRaw sql.NullInt64
		)
		if err := rows.Scan(&meta.ID, &meta.StorageKey, &createdRaw, &expiresRaw); err != nil {
			return nil, fmt.Errorf("scan expired metadata: %w", err)
		}
		meta.CreatedAt = time.Unix(createdRaw, 0).UTC()
		if expiresRaw.Valid {
			exp := time.Unix(expiresRaw.Int64, 0).UTC()
			meta.ExpiresAt = &exp
		}
		items = append(items, meta)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired metadata: %w", err)
	}
	return items, nil
}

func (s *Store) initSchema() error {
	const schema = `
CREATE TABLE IF NOT EXISTS files (
  id TEXT PRIMARY KEY,
  storage_key TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_files_expires_at ON files(expires_at);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("init sqlite schema: %w", err)
	}
	return nil
}
