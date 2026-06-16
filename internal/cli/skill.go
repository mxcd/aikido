package cli

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	urfavecli "github.com/urfave/cli/v3"
)

// skillName is both the installed skill's directory name and its /slash
// invocation in Claude Code.
const skillName = "aikido"

// skillMarkdown is the SKILL.md shipped with the binary. It teaches a Claude
// Code agent to drive this CLI — chiefly for high-quality image generation.
//
//go:embed skills/SKILL.md
var skillMarkdown string

func skillCommand() *urfavecli.Command {
	return &urfavecli.Command{
		Name:  "skill",
		Usage: "Install the aikido Claude Code skill",
		Description: "Writes the bundled SKILL.md into a Claude Code skills directory so\n" +
			"Claude learns to use this CLI — especially for generating high-quality images.",
		Commands: []*urfavecli.Command{
			skillInstallCommand(),
		},
	}
}

func skillInstallCommand() *urfavecli.Command {
	return &urfavecli.Command{
		Name:  "install",
		Usage: "Install the aikido skill into a Claude Code skills directory",
		Flags: []urfavecli.Flag{
			&urfavecli.StringFlag{
				Name:  "scope",
				Value: "user",
				Usage: "Install scope: 'user' (~/.claude/skills) or 'project' (./.claude/skills)",
			},
			&urfavecli.StringFlag{
				Name:  "dir",
				Usage: "Skills root directory (overrides --scope)",
			},
			&urfavecli.BoolFlag{
				Name:    "force",
				Aliases: []string{"f"},
				Usage:   "Overwrite an existing SKILL.md",
			},
			&urfavecli.BoolFlag{
				Name:  "print",
				Usage: "Print the skill markdown to stdout instead of installing",
			},
		},
		Action: func(ctx context.Context, cmd *urfavecli.Command) error {
			return runSkillInstall(skillInstallOpts{
				scope: cmd.String("scope"),
				dir:   cmd.String("dir"),
				force: cmd.Bool("force"),
				print: cmd.Bool("print"),
			}, resolveWriter(cmd))
		},
	}
}

type skillInstallOpts struct {
	scope string
	dir   string
	force bool
	print bool
}

func runSkillInstall(o skillInstallOpts, out io.Writer) error {
	if o.print {
		_, err := io.WriteString(out, skillMarkdown)
		return err
	}

	root, err := skillsRoot(o.scope, o.dir)
	if err != nil {
		return err
	}

	skillDir := filepath.Join(root, skillName)
	target := filepath.Join(skillDir, "SKILL.md")

	if !o.force {
		switch _, statErr := os.Stat(target); {
		case statErr == nil:
			return fmt.Errorf("%s already exists; pass --force to overwrite", target)
		case !errors.Is(statErr, os.ErrNotExist):
			return fmt.Errorf("stat %s: %w", target, statErr)
		}
	}

	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", skillDir, err)
	}
	if err := os.WriteFile(target, []byte(skillMarkdown), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}

	fmt.Fprintf(out, "installed aikido skill → %s\n", target)
	fmt.Fprintf(out, "invoke it in Claude Code with /%s, or it triggers automatically on image-generation requests\n", skillName)
	return nil
}

// skillsRoot resolves the Claude Code skills directory. An explicit dir wins;
// otherwise scope selects the user-global or project-local convention.
func skillsRoot(scope, dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	switch scope {
	case "", "user":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		return filepath.Join(home, ".claude", "skills"), nil
	case "project":
		return filepath.Join(".claude", "skills"), nil
	default:
		return "", fmt.Errorf("invalid --scope %q (want 'user' or 'project')", scope)
	}
}
