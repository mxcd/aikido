package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/llm/llmtest"
	"github.com/mxcd/aikido/tools"
	"github.com/mxcd/aikido/vfs"
	vfsmem "github.com/mxcd/aikido/vfs/memory"
)

func newRegistryWithVFS(t *testing.T, opts *VFSToolOptions) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry()
	if err := RegisterVFSTools(reg, opts); err != nil {
		t.Fatalf("RegisterVFSTools: %v", err)
	}
	return reg
}

func dispatch(t *testing.T, reg *tools.Registry, name, args string) (tools.Result, error) {
	t.Helper()
	return reg.Dispatch(context.Background(), llm.ToolCall{
		ID: "c", Name: name, Arguments: args,
	}, tools.Env{SessionID: "s", TurnID: uuid.New()})
}

func TestRegisterVFSTools_Defaults(t *testing.T) {
	storage := vfsmem.NewStorage()
	reg := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})

	for _, name := range []string{"read_file", "write_file", "list_files", "delete_file", "search"} {
		if !reg.Has(name) {
			t.Errorf("%s should be registered", name)
		}
	}
}

func TestRegisterVFSTools_ReadOnly(t *testing.T) {
	storage := vfsmem.NewStorage()
	reg := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage, ReadOnly: true})

	for _, name := range []string{"read_file", "list_files", "search"} {
		if !reg.Has(name) {
			t.Errorf("%s should be registered in read-only mode", name)
		}
	}
	for _, name := range []string{"write_file", "delete_file"} {
		if reg.Has(name) {
			t.Errorf("%s must NOT be registered in read-only mode", name)
		}
	}
}

func TestRegisterVFSTools_NoSearchWhenNotSearchable(t *testing.T) {
	reg := tools.NewRegistry()
	if err := RegisterVFSTools(reg, &VFSToolOptions{Storage: nonSearchableStorage{}}); err != nil {
		t.Fatalf("RegisterVFSTools: %v", err)
	}
	if reg.Has("search") {
		t.Error("search should not be registered when storage is not Searchable")
	}
	if !reg.Has("read_file") {
		t.Error("read_file should still be registered")
	}
}

func TestRegisterVFSTools_NilArgsRejected(t *testing.T) {
	if err := RegisterVFSTools(nil, &VFSToolOptions{Storage: vfsmem.NewStorage()}); err == nil {
		t.Error("expected error for nil registry")
	}
	if err := RegisterVFSTools(tools.NewRegistry(), nil); err == nil {
		t.Error("expected error for nil opts")
	}
	if err := RegisterVFSTools(tools.NewRegistry(), &VFSToolOptions{}); err == nil {
		t.Error("expected error for nil Storage")
	}
}

func TestWriteFile_HappyPath(t *testing.T) {
	storage := vfsmem.NewStorage()
	reg := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})

	res, err := dispatch(t, reg, "write_file", `{"path":"notes.md","content":"# hi"}`)
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	m, _ := res.Content.(map[string]any)
	if m["path"] != "notes.md" {
		t.Errorf("res = %v", m)
	}
}

func TestReadFile_HappyPath(t *testing.T) {
	storage := vfsmem.NewStorage()
	_ = storage.WriteFile(context.Background(), "x.md", []byte("# hello"), "text/markdown")
	reg := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})
	res, err := dispatch(t, reg, "read_file", `{"path":"x.md"}`)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	m, _ := res.Content.(map[string]any)
	if m["content"] != "# hello" {
		t.Errorf("content = %v", m["content"])
	}
	if m["content_type"] != "text/markdown" {
		t.Errorf("content_type = %v", m["content_type"])
	}
}

func TestReadFile_NotFound(t *testing.T) {
	storage := vfsmem.NewStorage()
	reg := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})
	_, err := dispatch(t, reg, "read_file", `{"path":"missing.md"}`)
	if !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("err = %v; want ErrFileNotFound", err)
	}
}

func TestReadFile_InvalidPath(t *testing.T) {
	storage := vfsmem.NewStorage()
	reg := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})
	_, err := dispatch(t, reg, "read_file", `{"path":"../escape.md"}`)
	if !errors.Is(err, vfs.ErrPathInvalid) {
		t.Errorf("err = %v; want ErrPathInvalid", err)
	}
}

func TestListFiles_HappyPath(t *testing.T) {
	storage := vfsmem.NewStorage()
	_ = storage.WriteFile(context.Background(), "a.md", []byte("x"), "text/markdown")
	_ = storage.WriteFile(context.Background(), "b.md", []byte("y"), "text/markdown")
	reg := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})
	res, err := dispatch(t, reg, "list_files", `{}`)
	if err != nil {
		t.Fatalf("list_files: %v", err)
	}
	m, _ := res.Content.(map[string]any)
	files, _ := m["files"].([]map[string]any)
	if len(files) != 2 {
		t.Errorf("len(files) = %d; want 2", len(files))
	}
}

func TestListFiles_HideHiddenPaths(t *testing.T) {
	storage := vfsmem.NewStorage()
	_ = storage.WriteFile(context.Background(), "_internal/secret.md", []byte("x"), "text/markdown")
	_ = storage.WriteFile(context.Background(), "notes.md", []byte("y"), "text/markdown")
	reg := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage, HideHiddenPaths: true})
	res, _ := dispatch(t, reg, "list_files", `{}`)
	m, _ := res.Content.(map[string]any)
	files, _ := m["files"].([]map[string]any)
	if len(files) != 1 || files[0]["path"] != "notes.md" {
		t.Errorf("hidden path leaked: %v", files)
	}
}

func TestDeleteFile_HappyPath(t *testing.T) {
	storage := vfsmem.NewStorage()
	_ = storage.WriteFile(context.Background(), "a.md", []byte("x"), "text/markdown")
	reg := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})
	if _, err := dispatch(t, reg, "delete_file", `{"path":"a.md"}`); err != nil {
		t.Errorf("delete: %v", err)
	}
	if _, _, err := storage.ReadFile(context.Background(), "a.md"); !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("file should be gone: %v", err)
	}
}

func TestDeleteFile_NotFound(t *testing.T) {
	storage := vfsmem.NewStorage()
	reg := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})
	_, err := dispatch(t, reg, "delete_file", `{"path":"missing.md"}`)
	if !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("err = %v; want ErrFileNotFound", err)
	}
}

func TestWriteFile_AllowedExtensions(t *testing.T) {
	storage := vfsmem.NewStorage()
	reg := newRegistryWithVFS(t, &VFSToolOptions{
		Storage:           storage,
		AllowedExtensions: []string{".md"},
	})
	if _, err := dispatch(t, reg, "write_file", `{"path":"x.md","content":"ok"}`); err != nil {
		t.Errorf("md write: %v", err)
	}
	_, err := dispatch(t, reg, "write_file", `{"path":"x.bin","content":"ok"}`)
	if !errors.Is(err, vfs.ErrPathInvalid) {
		t.Errorf("bin write err = %v; want ErrPathInvalid", err)
	}
}

func TestWriteFile_MaxFileBytes(t *testing.T) {
	storage := vfsmem.NewStorage()
	reg := newRegistryWithVFS(t, &VFSToolOptions{
		Storage: storage, MaxFileBytes: 8,
	})
	args, _ := json.Marshal(map[string]any{"path": "x.md", "content": "this is too big"})
	_, err := dispatch(t, reg, "write_file", string(args))
	if !errors.Is(err, vfs.ErrFileTooLarge) {
		t.Errorf("err = %v; want ErrFileTooLarge", err)
	}
}

func TestSearch_HappyPath(t *testing.T) {
	storage := vfsmem.NewStorage()
	_ = storage.WriteFile(context.Background(), "a.md", []byte("hello world"), "text/markdown")
	_ = storage.WriteFile(context.Background(), "b.md", []byte("hello there"), "text/markdown")
	_ = storage.WriteFile(context.Background(), "c.md", []byte("nothing"), "text/markdown")
	reg := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})
	res, err := dispatch(t, reg, "search", `{"query":"hello"}`)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	m, _ := res.Content.(map[string]any)
	paths, _ := m["paths"].([]string)
	if len(paths) != 2 {
		t.Errorf("paths = %v; want 2 hits", paths)
	}
}

func TestSearch_HidesHidden(t *testing.T) {
	storage := vfsmem.NewStorage()
	_ = storage.WriteFile(context.Background(), "_internal/x.md", []byte("hello"), "text/markdown")
	_ = storage.WriteFile(context.Background(), "notes.md", []byte("hello"), "text/markdown")
	reg := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage, HideHiddenPaths: true})
	res, _ := dispatch(t, reg, "search", `{"query":"hello"}`)
	m, _ := res.Content.(map[string]any)
	paths, _ := m["paths"].([]string)
	if len(paths) != 1 || paths[0] != "notes.md" {
		t.Errorf("hidden path leaked: %v", paths)
	}
}

func TestSearch_DescriptionEmbedsSyntax(t *testing.T) {
	storage := vfsmem.NewStorage()
	reg := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})
	defs := reg.Defs()
	var searchDef llm.ToolDef
	for _, d := range defs {
		if d.Name == "search" {
			searchDef = d
		}
	}
	if !strings.Contains(searchDef.Description, storage.SearchSyntax()) {
		t.Errorf("search description does not embed SearchSyntax: %q", searchDef.Description)
	}
}

func TestVFSToolsViaSession_RoundTrip(t *testing.T) {
	storage := vfsmem.NewStorage()
	reg := tools.NewRegistry()
	if err := RegisterVFSTools(reg, &VFSToolOptions{Storage: storage}); err != nil {
		t.Fatalf("register: %v", err)
	}

	stub := llmtest.NewStubClient(
		// turn 1: write_file
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{
				ID: "c1", Name: "write_file",
				Arguments: `{"path":"hello.md","content":"hello world"}`,
			}},
			{Kind: llm.EventEnd},
		}},
		// turn 2: read_file
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{
				ID: "c2", Name: "read_file",
				Arguments: `{"path":"hello.md"}`,
			}},
			{Kind: llm.EventEnd},
		}},
		// turn 3: stop
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventTextDelta, Text: "all done"},
			{Kind: llm.EventEnd},
		}},
	)

	s, err := NewLocalSession(&SessionOptions{
		ID: "sx", Client: stub, Model: "m", Tools: reg,
	})
	if err != nil {
		t.Fatalf("NewLocalSession: %v", err)
	}
	ch, _ := s.Run(context.Background(), "write hello world")
	for range ch {
	}

	got, _, err := storage.ReadFile(context.Background(), "hello.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("content = %q", got)
	}
}

// nonSearchableStorage implements only vfs.Storage.
type nonSearchableStorage struct{}

func (nonSearchableStorage) ListFiles(_ context.Context) ([]vfs.FileMeta, error) { return nil, nil }
func (nonSearchableStorage) ReadFile(_ context.Context, _ string) ([]byte, vfs.FileMeta, error) {
	return nil, vfs.FileMeta{}, vfs.ErrFileNotFound
}
func (nonSearchableStorage) WriteFile(_ context.Context, _ string, _ []byte, _ string) error {
	return nil
}
func (nonSearchableStorage) DeleteFile(_ context.Context, _ string) error { return nil }
