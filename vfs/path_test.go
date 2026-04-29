package vfs

import (
	"errors"
	"strings"
	"testing"
)

func TestValidatePath_Accepts(t *testing.T) {
	good := []string{
		"a.md",
		"notes/today.md",
		"deep/nested/file.txt",
		"_internal/x.md", // hidden filtering is a caller-policy concern, not structural
		"a/b/c.bin",
	}
	for _, p := range good {
		if err := ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%q) = %v; want nil", p, err)
		}
	}
}

func TestValidatePath_Rejects(t *testing.T) {
	bad := []struct {
		name string
		path string
		want string
	}{
		{"empty", "", "empty"},
		{"absolute", "/abs/path.md", "absolute"},
		{"trailing slash", "foo/", "trailing"},
		{"double slash", "foo//bar", "double"},
		{"parent traversal", "../etc/passwd", "parent"},
		{"parent traversal mid", "a/../b", "parent"},
		{"null byte", "a\x00b", "null"},
		{"oversized", strings.Repeat("a", MaxPathBytes+1), "exceeds"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePath(tt.path)
			if !errors.Is(err, ErrPathInvalid) {
				t.Errorf("ValidatePath(%q) = %v; want ErrPathInvalid", tt.path, err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %q; want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestValidatePath_BoundaryLength(t *testing.T) {
	exact := strings.Repeat("a", MaxPathBytes)
	if err := ValidatePath(exact); err != nil {
		t.Errorf("path of exactly %d bytes should be accepted, got %v", MaxPathBytes, err)
	}
}
