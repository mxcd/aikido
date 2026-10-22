package osfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mxcd/aikido/vfs"
)

func newStorage(t *testing.T, dir string) *Storage {
	t.Helper()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestConformance(t *testing.T) {
	vfs.RunConformance(t, func() vfs.Storage { return newStorage(t, t.TempDir()) })
}

func TestSymlinkEscapeRejected(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("s3cret"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(dir, "link.md")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	s := newStorage(t, dir)
	ctx := context.Background()

	if _, _, err := s.ReadFile(ctx, "link.md"); !errors.Is(err, vfs.ErrPathInvalid) {
		t.Errorf("ReadFile via escaping symlink err = %v; want ErrPathInvalid", err)
	}
	if err := s.WriteFile(ctx, "link.md", []byte("x"), ""); err != nil {
		t.Fatalf("WriteFile over symlink: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(outside, "secret.md")); string(got) != "s3cret" {
		t.Errorf("write followed the symlink out of the root: outside file = %q", got)
	}
}

func TestListSkipsGitAndCreatesDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newStorage(t, dir)
	ctx := context.Background()
	if err := s.WriteFile(ctx, "a/b/c.md", []byte("deep"), ""); err != nil {
		t.Fatalf("WriteFile nested: %v", err)
	}
	files, err := s.ListFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "a/b/c.md" || files[0].ContentType != "text/markdown" {
		t.Errorf("ListFiles = %+v; want only a/b/c.md as text/markdown", files)
	}
}
