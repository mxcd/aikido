// Package embedfs adapts any fs.FS (including embed.FS) to vfs.Storage.
//
// The backend is read-only: WriteFile and DeleteFile return vfs.ErrReadOnly.
// Suitable for chatbot/knowledge-base use cases where the corpus is fixed at
// compile time and the model only reads.
//
//	import _ "embed"
//
//	//go:embed knowledge/*.md
//	var knowledgeFS embed.FS
//
//	knowledge, _ := fs.Sub(knowledgeFS, "knowledge")
//	storage := embedfs.NewStorage(knowledge)
//
// Storage satisfies vfs.Searchable with case-insensitive substring matching
// against file contents — same syntax as vfs/memory.
package embedfs

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/mxcd/aikido/vfs"
)

// Storage wraps an fs.FS as a read-only vfs.Storage.
type Storage struct {
	fsys fs.FS
}

var (
	_ vfs.Storage    = (*Storage)(nil)
	_ vfs.Searchable = (*Storage)(nil)
)

// NewStorage wraps fsys. The fsys is consulted on every call; nothing is cached.
//
// Caller-bound subdirectories: pass fs.Sub(fsys, "data") if your embed includes
// a parent prefix you want to hide from the model.
func NewStorage(fsys fs.FS) *Storage {
	return &Storage{fsys: fsys}
}

// ListFiles walks the entire fs and returns every file (no directories).
// Paths are relative; deterministic alphabetical order.
func (s *Storage) ListFiles(ctx context.Context) ([]vfs.FileMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []vfs.FileMeta
	err := fs.WalkDir(s.fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, vfs.FileMeta{
			Path:        p,
			ContentType: contentTypeFor(p),
			Size:        info.Size(),
			UpdatedAt:   info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("aikido/vfs/embedfs: walk: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ReadFile returns the bytes for the requested path.
func (s *Storage) ReadFile(ctx context.Context, p string) ([]byte, vfs.FileMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, vfs.FileMeta{}, err
	}
	if err := vfs.ValidatePath(p); err != nil {
		return nil, vfs.FileMeta{}, err
	}
	data, err := fs.ReadFile(s.fsys, p)
	if err != nil {
		if isNotExist(err) {
			return nil, vfs.FileMeta{}, fmt.Errorf("%w: %s", vfs.ErrFileNotFound, p)
		}
		return nil, vfs.FileMeta{}, fmt.Errorf("aikido/vfs/embedfs: ReadFile: %w", err)
	}
	info, statErr := fs.Stat(s.fsys, p)
	meta := vfs.FileMeta{
		Path:        p,
		ContentType: contentTypeFor(p),
		Size:        int64(len(data)),
	}
	if statErr == nil {
		meta.UpdatedAt = info.ModTime()
	}
	return data, meta, nil
}

// WriteFile always returns vfs.ErrReadOnly.
func (s *Storage) WriteFile(_ context.Context, p string, _ []byte, _ string) error {
	return fmt.Errorf("%w: %s", vfs.ErrReadOnly, p)
}

// DeleteFile always returns vfs.ErrReadOnly.
func (s *Storage) DeleteFile(_ context.Context, p string) error {
	return fmt.Errorf("%w: %s", vfs.ErrReadOnly, p)
}

// Search performs a case-insensitive substring search against file contents.
func (s *Storage) Search(ctx context.Context, query string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q := strings.ToLower(query)
	var hits []string
	err := fs.WalkDir(s.fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(s.fsys, p)
		if err != nil {
			return err
		}
		if q == "" || strings.Contains(strings.ToLower(string(data)), q) {
			hits = append(hits, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("aikido/vfs/embedfs: search: %w", err)
	}
	sort.Strings(hits)
	return hits, nil
}

// SearchSyntax describes the query syntax this backend accepts.
func (s *Storage) SearchSyntax() string {
	return "Case-insensitive substring search against file contents. " +
		"Pass a single substring; the backend returns every file whose contents contain it."
}

func contentTypeFor(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".html", ".htm":
		return "text/html"
	case ".csv":
		return "text/csv"
	default:
		if ct := http.DetectContentType([]byte(p)); ct != "" {
			return ct
		}
		return "application/octet-stream"
	}
}

func isNotExist(err error) bool {
	return err != nil && (err == fs.ErrNotExist ||
		strings.Contains(err.Error(), "file does not exist") ||
		strings.Contains(err.Error(), "no such file"))
}
