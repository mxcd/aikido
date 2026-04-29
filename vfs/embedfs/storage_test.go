package embedfs_test

import (
	"context"
	"errors"
	"io/fs"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/mxcd/aikido/vfs"
	"github.com/mxcd/aikido/vfs/embedfs"
)

func newTestFS() fstest.MapFS {
	return fstest.MapFS{
		"a.md":      &fstest.MapFile{Data: []byte("# A\n\nApple Banana")},
		"b.md":      &fstest.MapFile{Data: []byte("# B\n\nCarrot Date")},
		"sub/c.md":  &fstest.MapFile{Data: []byte("# C\n\napple again")},
		"data.json": &fstest.MapFile{Data: []byte(`{"k":"v"}`)},
	}
}

func TestStorage_ListFiles(t *testing.T) {
	s := embedfs.NewStorage(newTestFS())
	files, err := s.ListFiles(context.Background())
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	got := make([]string, 0, len(files))
	for _, f := range files {
		got = append(got, f.Path)
	}
	want := []string{"a.md", "b.md", "data.json", "sub/c.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("paths = %v; want %v", got, want)
	}
}

func TestStorage_ListFiles_Meta(t *testing.T) {
	s := embedfs.NewStorage(newTestFS())
	files, _ := s.ListFiles(context.Background())
	var aMeta vfs.FileMeta
	for _, f := range files {
		if f.Path == "a.md" {
			aMeta = f
		}
	}
	if aMeta.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q; want text/markdown", aMeta.ContentType)
	}
	if aMeta.Size == 0 {
		t.Error("Size = 0")
	}
}

func TestStorage_ReadFile(t *testing.T) {
	s := embedfs.NewStorage(newTestFS())
	data, meta, err := s.ReadFile(context.Background(), "a.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "Apple Banana") {
		t.Errorf("content = %q", data)
	}
	if meta.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q", meta.ContentType)
	}
	if meta.Size != int64(len(data)) {
		t.Errorf("Size = %d; want %d", meta.Size, len(data))
	}
}

func TestStorage_ReadFile_Missing(t *testing.T) {
	s := embedfs.NewStorage(newTestFS())
	_, _, err := s.ReadFile(context.Background(), "nope.md")
	if !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("err = %v; want ErrFileNotFound", err)
	}
}

func TestStorage_ReadFile_InvalidPath(t *testing.T) {
	s := embedfs.NewStorage(newTestFS())
	_, _, err := s.ReadFile(context.Background(), "../escape.md")
	if !errors.Is(err, vfs.ErrPathInvalid) {
		t.Errorf("err = %v; want ErrPathInvalid", err)
	}
}

func TestStorage_WriteFile_ReadOnly(t *testing.T) {
	s := embedfs.NewStorage(newTestFS())
	err := s.WriteFile(context.Background(), "x.md", []byte("hi"), "text/markdown")
	if !errors.Is(err, vfs.ErrReadOnly) {
		t.Errorf("err = %v; want ErrReadOnly", err)
	}
}

func TestStorage_DeleteFile_ReadOnly(t *testing.T) {
	s := embedfs.NewStorage(newTestFS())
	err := s.DeleteFile(context.Background(), "a.md")
	if !errors.Is(err, vfs.ErrReadOnly) {
		t.Errorf("err = %v; want ErrReadOnly", err)
	}
}

func TestStorage_Search(t *testing.T) {
	s := embedfs.NewStorage(newTestFS())
	hits, err := s.Search(context.Background(), "apple")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	got := append([]string(nil), hits...)
	sort.Strings(got)
	want := []string{"a.md", "sub/c.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("hits = %v; want %v", got, want)
	}
}

func TestStorage_SearchSyntax_NonEmpty(t *testing.T) {
	s := embedfs.NewStorage(newTestFS())
	if syn := s.SearchSyntax(); syn == "" {
		t.Error("SearchSyntax should be non-empty")
	}
}

func TestStorage_AsSearchable(t *testing.T) {
	var s vfs.Storage = embedfs.NewStorage(newTestFS())
	if _, ok := s.(vfs.Searchable); !ok {
		t.Error("Storage should satisfy vfs.Searchable")
	}
}

func TestStorage_FsSubScoped(t *testing.T) {
	// fs.Sub-style usage: scope to "sub/" so callers see paths without prefix.
	parent := fstest.MapFS{
		"data/a.md": &fstest.MapFile{Data: []byte("inside data")},
		"data/b.md": &fstest.MapFile{Data: []byte("also inside")},
		"outside":   &fstest.MapFile{Data: []byte("filtered out")},
	}
	sub, err := fs.Sub(parent, "data")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}
	s := embedfs.NewStorage(sub)
	files, _ := s.ListFiles(context.Background())
	if len(files) != 2 {
		t.Fatalf("len(files) = %d; want 2 (filtered)", len(files))
	}
	if files[0].Path != "a.md" || files[1].Path != "b.md" {
		t.Errorf("paths = %+v; want [a.md, b.md]", files)
	}
}
