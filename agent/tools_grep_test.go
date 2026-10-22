package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	vfsmem "github.com/mxcd/aikido/vfs/memory"
)

func seeded(t *testing.T, opts VFSToolOptions, files map[string]string) *VFSToolOptions {
	t.Helper()
	storage := vfsmem.NewStorage()
	for p, c := range files {
		if err := storage.WriteFile(context.Background(), p, []byte(c), ""); err != nil {
			t.Fatal(err)
		}
	}
	opts.Storage = storage
	return &opts
}

func TestGlobToRegexp(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"*.md", "a.md", true},
		{"*.md", "dir/a.md", false},
		{"**/*.md", "a.md", true},
		{"**/*.md", "x/y/a.md", true},
		{"docs/**", "docs/a/b.txt", true},
		{"docs/**", "other/a.txt", false},
		{"a?.md", "ab.md", true},
		{"a?.md", "a/.md", false},
		{"*.{md,txt}", "x.txt", true},
		{"*.{md,txt}", "x.json", false},
		{"a.md", "aXmd", false},
	}
	for _, c := range cases {
		re, err := globToRegexp(c.glob)
		if err != nil {
			t.Fatalf("%q: %v", c.glob, err)
		}
		if got := re.MatchString(c.path); got != c.want {
			t.Errorf("glob %q on %q = %v; want %v", c.glob, c.path, got, c.want)
		}
	}
	if _, err := globToRegexp("{a,b"); err == nil {
		t.Error("unclosed brace should error")
	}
}

func TestRegisterVFSTools_Extended(t *testing.T) {
	reg := newRegistryWithVFS(t, &VFSToolOptions{Storage: vfsmem.NewStorage()})
	for _, n := range []string{"grep", "glob", "edit_file"} {
		if !reg.Has(n) {
			t.Errorf("%s should be registered by default", n)
		}
	}
	reg = newRegistryWithVFS(t, &VFSToolOptions{Storage: vfsmem.NewStorage(), ReadOnly: true})
	if !reg.Has("grep") || !reg.Has("glob") || reg.Has("edit_file") {
		t.Error("read-only: want grep and glob, no edit_file")
	}
	reg = newRegistryWithVFS(t, &VFSToolOptions{Storage: vfsmem.NewStorage(), DisableExtended: true})
	for _, n := range []string{"grep", "glob", "edit_file"} {
		if reg.Has(n) {
			t.Errorf("%s must not be registered with DisableExtended", n)
		}
	}
}

func TestGrep_ContentContextAndPolicy(t *testing.T) {
	opts := seeded(t, VFSToolOptions{HideHiddenPaths: true, AllowedExtensions: []string{".md"}}, map[string]string{
		"a.md":       "one\ntwo needle\nthree\nfour\nfive needle\n",
		"_hidden.md": "needle\n",
		"b.txt":      "needle\n",
		"bin.md":     "needle\x00\n",
	})
	reg := newRegistryWithVFS(t, opts)
	res, err := dispatch(t, reg, "grep", `{"pattern":"NEEDLE","case_insensitive":true,"context":1}`)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(res.Content.(map[string]any)["matches"].([]string), "|")
	want := "a.md-1-one|a.md:2:two needle|a.md-3-three|a.md-4-four|a.md:5:five needle"
	if got != want {
		t.Errorf("matches = %q; want %q", got, want)
	}
}

func TestGrep_ModesAndCaps(t *testing.T) {
	var many strings.Builder
	for i := 0; i < grepMaxLines+10; i++ {
		fmt.Fprintf(&many, "hit %d %s\n", i, strings.Repeat("x", grepMaxLineChars))
	}
	opts := seeded(t, VFSToolOptions{}, map[string]string{
		"big.md":   many.String(),
		"small.md": "hit\nhit\n",
		"none.md":  "miss\n",
	})
	reg := newRegistryWithVFS(t, opts)

	res, err := dispatch(t, reg, "grep", `{"pattern":"hit"}`)
	if err != nil {
		t.Fatal(err)
	}
	c := res.Content.(map[string]any)
	matches := c["matches"].([]string)
	if len(matches) != grepMaxLines || c["truncated"] != true {
		t.Errorf("len = %d truncated = %v; want %d true", len(matches), c["truncated"], grepMaxLines)
	}
	if !strings.HasSuffix(matches[0], "…") || len([]rune(matches[0])) > grepMaxLineChars+20 {
		t.Errorf("long line not clipped: %d runes", len([]rune(matches[0])))
	}

	res, _ = dispatch(t, reg, "grep", `{"pattern":"hit","output_mode":"files_with_matches"}`)
	if got := strings.Join(res.Content.(map[string]any)["files"].([]string), ","); got != "big.md,small.md" {
		t.Errorf("files = %q", got)
	}
	res, _ = dispatch(t, reg, "grep", `{"pattern":"hit","output_mode":"count","path_glob":"small.*"}`)
	counts := res.Content.(map[string]any)["counts"].([]map[string]any)
	if len(counts) != 1 || counts[0]["count"] != 2 {
		t.Errorf("counts = %v", counts)
	}
	if _, err := dispatch(t, reg, "grep", `{"pattern":"("}`); err == nil {
		t.Error("invalid regexp should error")
	}
}

func TestGlob(t *testing.T) {
	opts := seeded(t, VFSToolOptions{HideHiddenPaths: true}, map[string]string{
		"a.md": "", "d/b.md": "", "d/c.txt": "", ".x/y.md": "",
	})
	reg := newRegistryWithVFS(t, opts)
	res, err := dispatch(t, reg, "glob", `{"pattern":"**/*.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.Content.(map[string]any)["paths"].([]string), ","); got != "a.md,d/b.md" {
		t.Errorf("paths = %q", got)
	}
}

func TestEditFile(t *testing.T) {
	opts := seeded(t, VFSToolOptions{}, map[string]string{"a.md": "foo bar foo\n", "bin.md": "x\x00"})
	reg := newRegistryWithVFS(t, opts)
	ctx := context.Background()

	if _, err := dispatch(t, reg, "edit_file", `{"path":"a.md","old_string":"foo","new_string":"baz"}`); err == nil || !strings.Contains(err.Error(), "2 times") {
		t.Errorf("ambiguous match err = %v", err)
	}
	if _, err := dispatch(t, reg, "edit_file", `{"path":"a.md","old_string":"nope","new_string":"x"}`); err == nil {
		t.Error("missing old_string should error")
	}
	if _, err := dispatch(t, reg, "edit_file", `{"path":"a.md","old_string":"","new_string":"x"}`); err == nil {
		t.Error("empty old_string should error")
	}
	if _, err := dispatch(t, reg, "edit_file", `{"path":"bin.md","old_string":"x","new_string":"y"}`); err == nil {
		t.Error("binary file should error")
	}
	if _, err := dispatch(t, reg, "edit_file", `{"path":"a.md","old_string":"bar","new_string":"qux"}`); err != nil {
		t.Fatal(err)
	}
	res, err := dispatch(t, reg, "edit_file", `{"path":"a.md","old_string":"foo","new_string":"baz","replace_all":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content.(map[string]any)["replacements"] != 2 {
		t.Errorf("replacements = %v", res.Content)
	}
	got, _, _ := opts.Storage.ReadFile(ctx, "a.md")
	if string(got) != "baz qux baz\n" {
		t.Errorf("content = %q", got)
	}
}

func TestReadFile_Ranges(t *testing.T) {
	opts := seeded(t, VFSToolOptions{}, map[string]string{"a.md": "1\n2\n3\n4\n5\n"})
	reg := newRegistryWithVFS(t, opts)
	res, err := dispatch(t, reg, "read_file", `{"path":"a.md","offset":2,"limit":2}`)
	if err != nil {
		t.Fatal(err)
	}
	c := res.Content.(map[string]any)
	if c["content"] != "2\n3\n" || c["total_lines"] != 5 || c["truncated"] != true {
		t.Errorf("content = %v", c)
	}
	res, _ = dispatch(t, reg, "read_file", `{"path":"a.md"}`)
	if c := res.Content.(map[string]any); c["content"] != "1\n2\n3\n4\n5\n" || c["truncated"] != false {
		t.Errorf("full read = %v", c)
	}
	if _, err := dispatch(t, reg, "read_file", `{"path":"a.md","offset":9}`); err == nil {
		t.Error("offset past end should error")
	}
}

func TestListAndSearch_RespectAllowedExtensions(t *testing.T) {
	opts := seeded(t, VFSToolOptions{AllowedExtensions: []string{".md"}}, map[string]string{"a.md": "x", "b.txt": "x"})
	reg := newRegistryWithVFS(t, opts)
	res, _ := dispatch(t, reg, "list_files", `{}`)
	if files := res.Content.(map[string]any)["files"].([]map[string]any); len(files) != 1 || files[0]["path"] != "a.md" {
		t.Errorf("list_files = %v", files)
	}
	res, _ = dispatch(t, reg, "search", `{"query":"x"}`)
	if paths := res.Content.(map[string]any)["paths"].([]string); len(paths) != 1 || paths[0] != "a.md" {
		t.Errorf("search = %v", paths)
	}
}
