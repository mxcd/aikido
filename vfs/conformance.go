package vfs

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
)

// RunConformance executes the standard conformance suite against a Storage
// factory. Each sub-test calls the factory to obtain a fresh Storage. Tests
// are serial — implementations that want parallel execution can opt in with
// their own t.Parallel() inside the factory if applicable.
//
// Searchable-related sub-tests run only when the factory's Storage satisfies
// vfs.Searchable.
func RunConformance(t *testing.T, factory func() Storage) {
	t.Helper()

	t.Run("ListEmpty", func(t *testing.T) {
		s := factory()
		files, err := s.ListFiles(context.Background())
		if err != nil {
			t.Fatalf("ListFiles: %v", err)
		}
		if len(files) != 0 {
			t.Errorf("ListFiles on empty = %v; want []", files)
		}
	})

	t.Run("WriteReadRoundTrip", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		want := []byte("# Hello\n\nWorld.\n")
		if err := s.WriteFile(ctx, "notes.md", want, "text/markdown"); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		got, meta, err := s.ReadFile(ctx, "notes.md")
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != string(want) {
			t.Errorf("content = %q; want %q", got, want)
		}
		if meta.Path != "notes.md" {
			t.Errorf("meta.Path = %q", meta.Path)
		}
		if meta.ContentType != "text/markdown" {
			t.Errorf("meta.ContentType = %q", meta.ContentType)
		}
		if meta.Size != int64(len(want)) {
			t.Errorf("meta.Size = %d; want %d", meta.Size, len(want))
		}
		if meta.UpdatedAt.IsZero() {
			t.Error("meta.UpdatedAt is zero")
		}
	})

	t.Run("WriteOverwrites", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		_ = s.WriteFile(ctx, "x.md", []byte("v1"), "text/markdown")
		if err := s.WriteFile(ctx, "x.md", []byte("v2"), "text/markdown"); err != nil {
			t.Fatalf("WriteFile second: %v", err)
		}
		got, _, err := s.ReadFile(ctx, "x.md")
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != "v2" {
			t.Errorf("content = %q; want v2", got)
		}
	})

	t.Run("ReadMissing", func(t *testing.T) {
		s := factory()
		_, _, err := s.ReadFile(context.Background(), "missing.md")
		if !errors.Is(err, ErrFileNotFound) {
			t.Errorf("err = %v; want ErrFileNotFound", err)
		}
	})

	t.Run("DeleteHappyPath", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		_ = s.WriteFile(ctx, "x.md", []byte("hi"), "text/markdown")
		if err := s.DeleteFile(ctx, "x.md"); err != nil {
			t.Fatalf("DeleteFile: %v", err)
		}
		_, _, err := s.ReadFile(ctx, "x.md")
		if !errors.Is(err, ErrFileNotFound) {
			t.Errorf("after delete, ReadFile err = %v; want ErrFileNotFound", err)
		}
	})

	t.Run("DeleteMissing", func(t *testing.T) {
		s := factory()
		err := s.DeleteFile(context.Background(), "missing.md")
		if !errors.Is(err, ErrFileNotFound) {
			t.Errorf("err = %v; want ErrFileNotFound", err)
		}
	})

	t.Run("ListIsDeterministic", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		paths := []string{"c.md", "a.md", "b/x.md", "b/a.md"}
		for _, p := range paths {
			if err := s.WriteFile(ctx, p, []byte("x"), "text/markdown"); err != nil {
				t.Fatalf("WriteFile %q: %v", p, err)
			}
		}
		files1, err := s.ListFiles(ctx)
		if err != nil {
			t.Fatalf("ListFiles: %v", err)
		}
		files2, err := s.ListFiles(ctx)
		if err != nil {
			t.Fatalf("ListFiles 2: %v", err)
		}
		if len(files1) != len(paths) {
			t.Fatalf("len(files1) = %d; want %d", len(files1), len(paths))
		}
		for i := range files1 {
			if files1[i].Path != files2[i].Path {
				t.Errorf("non-deterministic order: files1[%d]=%s files2[%d]=%s", i, files1[i].Path, i, files2[i].Path)
			}
		}
		gotPaths := make([]string, len(files1))
		for i, f := range files1 {
			gotPaths[i] = f.Path
		}
		want := append([]string(nil), paths...)
		sort.Strings(want)
		got := append([]string(nil), gotPaths...)
		sort.Strings(got)
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("missing path: want %v, got %v", want, got)
				break
			}
		}
	})

	t.Run("WriteRejectsInvalidPath", func(t *testing.T) {
		s := factory()
		ctx := context.Background()
		bad := []string{"", "/abs", "../parent.md", "trailing/", "double//slash", "with\x00null"}
		for _, p := range bad {
			err := s.WriteFile(ctx, p, []byte("x"), "text/plain")
			if !errors.Is(err, ErrPathInvalid) {
				t.Errorf("WriteFile(%q) err = %v; want ErrPathInvalid", p, err)
			}
		}
	})

	t.Run("DeleteRejectsInvalidPath", func(t *testing.T) {
		s := factory()
		err := s.DeleteFile(context.Background(), "../etc/passwd")
		if !errors.Is(err, ErrPathInvalid) {
			t.Errorf("DeleteFile invalid path err = %v; want ErrPathInvalid", err)
		}
	})

	t.Run("ReadRejectsInvalidPath", func(t *testing.T) {
		s := factory()
		_, _, err := s.ReadFile(context.Background(), "../etc/passwd")
		if !errors.Is(err, ErrPathInvalid) {
			t.Errorf("ReadFile invalid path err = %v; want ErrPathInvalid", err)
		}
	})

	if _, ok := factory().(Searchable); ok {
		runSearchableConformance(t, factory)
	}
}

func runSearchableConformance(t *testing.T, factory func() Storage) {
	t.Helper()

	t.Run("Searchable/EmptyCorpus", func(t *testing.T) {
		s, _ := factory().(Searchable)
		paths, err := s.Search(context.Background(), "anything")
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(paths) != 0 {
			t.Errorf("paths = %v; want empty", paths)
		}
	})

	t.Run("Searchable/SubstringMatch", func(t *testing.T) {
		st := factory()
		ctx := context.Background()
		_ = st.WriteFile(ctx, "a.md", []byte("apple banana"), "text/markdown")
		_ = st.WriteFile(ctx, "b.md", []byte("carrot date"), "text/markdown")
		_ = st.WriteFile(ctx, "c.md", []byte("avocado banana"), "text/markdown")
		s, _ := st.(Searchable)
		paths, err := s.Search(ctx, "banana")
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		got := append([]string(nil), paths...)
		sort.Strings(got)
		want := []string{"a.md", "c.md"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("paths = %v; want %v", got, want)
		}
	})

	t.Run("Searchable/SyntaxNonEmpty", func(t *testing.T) {
		s, _ := factory().(Searchable)
		if syn := s.SearchSyntax(); syn == "" {
			t.Error("SearchSyntax() returned empty")
		}
	})
}
