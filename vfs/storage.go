package vfs

import (
	"context"
	"time"
)

// FileMeta describes one file's metadata.
type FileMeta struct {
	Path        string
	ContentType string
	Size        int64
	UpdatedAt   time.Time
}

// Storage is the base contract every VFS backend implements.
//
// Path semantics: paths are forward-slash separated, relative, no leading
// slash, no `..` segments, no double slashes, no trailing slash, no null
// bytes, length <= 512 bytes. See ValidatePath.
//
// Atomicity: each call is atomic at the backend level. The agent loop is not
// transactional in v1 (ADR-019).
type Storage interface {
	ListFiles(ctx context.Context) ([]FileMeta, error)
	ReadFile(ctx context.Context, path string) ([]byte, FileMeta, error)
	WriteFile(ctx context.Context, path string, content []byte, contentType string) error
	DeleteFile(ctx context.Context, path string) error
}
