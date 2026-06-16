package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSkillInstall_WritesSkillFile(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := runSkillInstall(skillInstallOpts{dir: root}, &out); err != nil {
		t.Fatalf("runSkillInstall: %v", err)
	}

	target := filepath.Join(root, skillName, "SKILL.md")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read installed skill: %v", err)
	}
	body := string(data)
	if !strings.HasPrefix(body, "---\nname: "+skillName) {
		t.Errorf("installed skill missing frontmatter name: %.40q", body)
	}
	if !strings.Contains(body, "aikido image") {
		t.Errorf("installed skill missing image usage")
	}
	if !strings.Contains(out.String(), target) {
		t.Errorf("output should report the install path: %q", out.String())
	}
}

func TestRunSkillInstall_RefusesOverwriteWithoutForce(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := runSkillInstall(skillInstallOpts{dir: root}, &out); err != nil {
		t.Fatalf("first install: %v", err)
	}

	err := runSkillInstall(skillInstallOpts{dir: root}, &out)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected 'already exists' error, got %v", err)
	}
}

func TestRunSkillInstall_ForceOverwrites(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, skillName, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runSkillInstall(skillInstallOpts{dir: root, force: true}, &out); err != nil {
		t.Fatalf("force install: %v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) == "stale" {
		t.Errorf("force did not overwrite the existing file")
	}
}

func TestRunSkillInstall_PrintDoesNotWriteFiles(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := runSkillInstall(skillInstallOpts{dir: root, print: true}, &out); err != nil {
		t.Fatalf("print: %v", err)
	}
	if !strings.Contains(out.String(), "name: "+skillName) {
		t.Errorf("print should emit the skill markdown, got %.60q", out.String())
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("print must not write files, found %+v", entries)
	}
}

func TestSkillsRoot(t *testing.T) {
	if got, err := skillsRoot("user", "/explicit/override"); err != nil || got != "/explicit/override" {
		t.Errorf("dir override = (%q, %v), want /explicit/override", got, err)
	}
	if got, err := skillsRoot("project", ""); err != nil || got != filepath.Join(".claude", "skills") {
		t.Errorf("project scope = (%q, %v)", got, err)
	}
	if _, err := skillsRoot("bogus", ""); err == nil {
		t.Errorf("expected error for invalid scope")
	}
}

func TestSkillInstallCommand_WiredIntoApp(t *testing.T) {
	root := t.TempDir()
	// The ClientFactory is never invoked: skill install needs no LLM client.
	app := NewApp(nil)
	var buf bytes.Buffer
	app.Writer = &buf
	args := []string{"aikido", "skill", "install", "--dir", root}
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatalf("app.Run skill install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, skillName, "SKILL.md")); err != nil {
		t.Fatalf("skill not installed via app: %v", err)
	}
}
