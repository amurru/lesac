package domain

import (
	"errors"
	"regexp"
	"time"
)

var (
	ErrNotFound        = errors.New("file not found")
	ErrInvalidLifetime = errors.New("invalid lifetime")
	ErrInvalidFileID   = errors.New("invalid file id")
	ErrEmptyBatch      = errors.New("empty batch")
)

var fileIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// FileID is the public identifier for a stored file.
type FileID string

// ParseFileID validates and converts a raw string to a FileID.
func ParseFileID(value string) (FileID, error) {
	if !fileIDPattern.MatchString(value) {
		return "", ErrInvalidFileID
	}
	return FileID(value), nil
}

// String returns the string representation of the file ID.
func (id FileID) String() string {
	return string(id)
}

// FileMeta describes where a file is stored and when it expires.
type FileMeta struct {
	ID         FileID
	StorageKey string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
}

// IsExpired reports whether the file is expired at the given time.
func (m FileMeta) IsExpired(now time.Time) bool {
	return m.ExpiresAt != nil && !m.ExpiresAt.After(now)
}
