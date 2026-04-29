package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	contents := []byte("# comment\nFOO=bar\nQUOTED=\"hello world\"\n\nSINGLE='abc'\nALREADY=fromfile\n")
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ALREADY", "fromenv")
	_ = os.Unsetenv("FOO")
	_ = os.Unsetenv("QUOTED")
	_ = os.Unsetenv("SINGLE")

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	if got := os.Getenv("FOO"); got != "bar" {
		t.Errorf("FOO = %q; want bar", got)
	}
	if got := os.Getenv("QUOTED"); got != "hello world" {
		t.Errorf("QUOTED = %q; want 'hello world'", got)
	}
	if got := os.Getenv("SINGLE"); got != "abc" {
		t.Errorf("SINGLE = %q; want abc", got)
	}
	if got := os.Getenv("ALREADY"); got != "fromenv" {
		t.Errorf("ALREADY clobbered: got %q; want existing fromenv", got)
	}
}

func TestLoadDotEnv_Missing(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Errorf("missing file should be a no-op, got %v", err)
	}
}
