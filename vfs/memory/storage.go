// Package memory implements an in-process vfs.Storage with case-insensitive
// substring search.
//
// Suitable for single-process production use and tests; data is lost on
// process exit.
package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mxcd/aikido/vfs"
)

type fileEntry struct {
	content     []byte
	contentType string
	updatedAt   time.Time
}

// Storage is the in-memory implementation.
type Storage struct {
	mu    sync.RWMutex
	files map[string]fileEntry
	now   func() time.Time
}

var (
	_ vfs.Storage    = (*Storage)(nil)
	_ vfs.Searchable = (*Storage)(nil)
)

// NewStorage returns an empty in-memory Storage.
func NewStorage() *Storage {
	return &Storage{
		files: make(map[string]fileEntry),
		now:   time.Now,
	}
}

func (s *Storage) ListFiles(ctx context.Context) ([]vfs.FileMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]vfs.FileMeta, 0, len(s.files))
	for path, e := range s.files {
		out = append(out, vfs.FileMeta{
			Path:        path,
			ContentType: e.contentType,
			Size:        int64(len(e.content)),
			UpdatedAt:   e.updatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (s *Storage) ReadFile(ctx context.Context, path string) ([]byte, vfs.FileMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, vfs.FileMeta{}, err
	}
	if err := vfs.ValidatePath(path); err != nil {
		return nil, vfs.FileMeta{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.files[path]
	if !ok {
		return nil, vfs.FileMeta{}, fmt.Errorf("%w: %s", vfs.ErrFileNotFound, path)
	}
	out := make([]byte, len(e.content))
	copy(out, e.content)
	return out, vfs.FileMeta{
		Path:        path,
		ContentType: e.contentType,
		Size:        int64(len(e.content)),
		UpdatedAt:   e.updatedAt,
	}, nil
}

func (s *Storage) WriteFile(ctx context.Context, path string, content []byte, contentType string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := vfs.ValidatePath(path); err != nil {
		return err
	}
	buf := make([]byte, len(content))
	copy(buf, content)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = fileEntry{
		content:     buf,
		contentType: contentType,
		updatedAt:   s.now(),
	}
	return nil
}

func (s *Storage) DeleteFile(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := vfs.ValidatePath(path); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.files[path]; !ok {
		return fmt.Errorf("%w: %s", vfs.ErrFileNotFound, path)
	}
	delete(s.files, path)
	return nil
}

// Search performs a case-insensitive substring search against file contents.
//
// Empty query matches every file in the corpus. Returns paths sorted.
func (s *Storage) Search(ctx context.Context, query string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q := strings.ToLower(query)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0)
	for path, e := range s.files {
		if q == "" || strings.Contains(strings.ToLower(string(e.content)), q) {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out, nil
}

// SearchSyntax describes the query language this backend accepts.
func (s *Storage) SearchSyntax() string {
	return "Case-insensitive substring search against file contents. " +
		"Pass a single substring; the backend returns every file whose contents contain it. " +
		"Example query: 'release notes'."
}
