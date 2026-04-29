package memory

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/mxcd/aikido/vfs"
)

func TestConformance(t *testing.T) {
	vfs.RunConformance(t, func() vfs.Storage { return NewStorage() })
}

func TestSearch_GlobalEmpty(t *testing.T) {
	s := NewStorage()
	ctx := context.Background()
	_ = s.WriteFile(ctx, "a.md", []byte("hello"), "text/markdown")
	_ = s.WriteFile(ctx, "b.md", []byte("world"), "text/markdown")

	// Empty query returns all paths (acts as "list everything").
	paths, err := s.Search(ctx, "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	got := append([]string(nil), paths...)
	sort.Strings(got)
	want := []string{"a.md", "b.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("paths = %v; want %v", got, want)
	}
}

func TestSearch_CaseInsensitive(t *testing.T) {
	s := NewStorage()
	ctx := context.Background()
	_ = s.WriteFile(ctx, "a.md", []byte("Hello WORLD"), "text/markdown")
	paths, err := s.Search(ctx, "world")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(paths) != 1 || paths[0] != "a.md" {
		t.Errorf("paths = %v", paths)
	}
}

func TestWrite_DefenseInDepthValidation(t *testing.T) {
	s := NewStorage()
	// Bypass tool layer; storage itself must reject.
	err := s.WriteFile(context.Background(), "../escape", []byte("x"), "text/plain")
	if err == nil {
		t.Fatal("expected ErrPathInvalid; got nil")
	}
}

func TestStorage_Concurrency(t *testing.T) {
	s := NewStorage()
	ctx := context.Background()
	const n = 50
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(i int) {
			path := "f-" + string(rune('a'+i%26)) + ".md"
			_ = s.WriteFile(ctx, path, []byte("payload"), "text/markdown")
			_, _, _ = s.ReadFile(ctx, path)
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
}
