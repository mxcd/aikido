package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/tools"
)

const (
	defaultReadLimit = 2000     // read_file lines per call
	grepMaxLines     = 200      // grep output lines (or files / counts)
	grepMaxLineChars = 500      // runes kept per grep output line
	grepMaxContext   = 5        // context lines around a match
	grepScanBudget   = 32 << 20 // bytes grep reads per call before stopping
	globMaxPaths     = 500      // glob result paths
)

const globSyntax = "Glob syntax: `*` matches within one path segment, `**` across segments (`**/*.md` = every markdown file), `?` one character, `{a,b}` alternatives. Paths are relative, without a leading slash."

func grepDef() llm.ToolDef {
	return llm.ToolDef{
		Name: "grep",
		Description: fmt.Sprintf("Search file contents with a regular expression (Go RE2 syntax, no backreferences or lookaround). "+
			"output_mode content (default) returns `path:line:text` for matches and `path-line-text` for context lines; "+
			"files_with_matches returns matching paths; count returns matches per file. "+
			"Returns at most %d entries, lines cut at %d characters; truncated is true when results were cut or the scan stopped early. "+
			"Binary files are skipped. %s", grepMaxLines, grepMaxLineChars, globSyntax),
		Parameters: tools.Object(map[string]any{
			"pattern":          tools.String("Regular expression to search for."),
			"path_glob":        tools.String("Optional glob limiting which files are searched, e.g. `docs/**/*.md`."),
			"case_insensitive": tools.Boolean("Match case-insensitively."),
			"context":          tools.Integer(fmt.Sprintf("Lines of context before and after each match in content mode (0-%d).", grepMaxContext)),
			"output_mode":      tools.Enum("Result shape.", "content", "files_with_matches", "count"),
		}, "pattern"),
	}
}

func globDef() llm.ToolDef {
	return llm.ToolDef{
		Name:        "glob",
		Description: fmt.Sprintf("List file paths matching a glob pattern, sorted by path, at most %d. %s", globMaxPaths, globSyntax),
		Parameters: tools.Object(map[string]any{
			"pattern": tools.String("Glob pattern, e.g. `**/*.md`."),
		}, "pattern"),
	}
}

func globHandler(opts VFSToolOptions) tools.Handler {
	return func(ctx context.Context, args json.RawMessage, _ tools.Env) (tools.Result, error) {
		var v struct {
			Pattern string `json:"pattern"`
		}
		if err := json.Unmarshal(args, &v); err != nil {
			return tools.Result{}, fmt.Errorf("aikido/agent: glob args: %w", err)
		}
		re, err := globToRegexp(v.Pattern)
		if err != nil {
			return tools.Result{}, err
		}
		files, err := opts.Storage.ListFiles(ctx)
		if err != nil {
			return tools.Result{}, err
		}
		paths := []string{}
		truncated := false
		for _, f := range files {
			if !visible(opts, f.Path) || !re.MatchString(f.Path) {
				continue
			}
			if len(paths) == globMaxPaths {
				truncated = true
				break
			}
			paths = append(paths, f.Path)
		}
		return tools.Result{
			Content: map[string]any{"paths": paths, "truncated": truncated},
			Display: fmt.Sprintf("glob %s: %d paths", v.Pattern, len(paths)),
		}, nil
	}
}

func grepHandler(opts VFSToolOptions) tools.Handler {
	return func(ctx context.Context, args json.RawMessage, _ tools.Env) (tools.Result, error) {
		var v struct {
			Pattern         string `json:"pattern"`
			PathGlob        string `json:"path_glob"`
			CaseInsensitive bool   `json:"case_insensitive"`
			Context         int    `json:"context"`
			OutputMode      string `json:"output_mode"`
		}
		if err := json.Unmarshal(args, &v); err != nil {
			return tools.Result{}, fmt.Errorf("aikido/agent: grep args: %w", err)
		}
		expr := v.Pattern
		if v.CaseInsensitive {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return tools.Result{}, fmt.Errorf("aikido/agent: grep: invalid pattern: %w", err)
		}
		var pathRe *regexp.Regexp
		if v.PathGlob != "" {
			if pathRe, err = globToRegexp(v.PathGlob); err != nil {
				return tools.Result{}, err
			}
		}
		if v.OutputMode == "" {
			v.OutputMode = "content"
		}
		switch v.OutputMode {
		case "content", "files_with_matches", "count":
		default:
			return tools.Result{}, fmt.Errorf("aikido/agent: grep: unknown output_mode %q", v.OutputMode)
		}
		ctxLines := min(max(v.Context, 0), grepMaxContext)

		files, err := opts.Storage.ListFiles(ctx)
		if err != nil {
			return tools.Result{}, err
		}
		// ponytail: brute force over ListFiles + ReadFile (one storage round
		// trip per file), bounded by grepScanBudget. Add an optional
		// vfs.Greppable capability once a network backend needs native grep.
		var (
			out       = []string{}
			counts    = []map[string]any{}
			entries   int
			scanned   int64
			truncated bool
		)
	files:
		for _, f := range files {
			if !visible(opts, f.Path) || (pathRe != nil && !pathRe.MatchString(f.Path)) {
				continue
			}
			if scanned >= grepScanBudget {
				truncated = true
				break
			}
			content, _, err := opts.Storage.ReadFile(ctx, f.Path)
			if err != nil {
				return tools.Result{}, err
			}
			scanned += int64(len(content))
			if isBinary(content) {
				continue
			}
			lines := splitLines(string(content))
			var hits []int
			isHit := map[int]bool{}
			for i, l := range lines {
				if re.MatchString(l) {
					hits = append(hits, i)
					isHit[i] = true
				}
			}
			if len(hits) == 0 {
				continue
			}
			if entries == grepMaxLines {
				truncated = true
				break
			}
			switch v.OutputMode {
			case "files_with_matches":
				out = append(out, f.Path)
				entries++
			case "count":
				counts = append(counts, map[string]any{"path": f.Path, "count": len(hits)})
				entries++
			default:
				next := 0 // first line not yet emitted for this file
				for _, i := range hits {
					from := max(i-ctxLines, next)
					to := min(i+ctxLines, len(lines)-1)
					for j := from; j <= to; j++ {
						if entries == grepMaxLines {
							truncated = true
							break files
						}
						sep := "-"
						if isHit[j] {
							sep = ":"
						}
						out = append(out, fmt.Sprintf("%s%s%d%s%s", f.Path, sep, j+1, sep, clip(strings.TrimRight(lines[j], "\r\n"))))
						entries++
					}
					next = to + 1
				}
			}
		}
		content := map[string]any{"truncated": truncated}
		switch v.OutputMode {
		case "files_with_matches":
			content["files"] = out
		case "count":
			content["counts"] = counts
		default:
			content["matches"] = out
		}
		return tools.Result{
			Content: content,
			Display: fmt.Sprintf("grep %s: %d results", v.Pattern, entries),
		}, nil
	}
}

// clip cuts a grep output line to grepMaxLineChars runes.
func clip(line string) string {
	r := []rune(line)
	if len(r) <= grepMaxLineChars {
		return line
	}
	return string(r[:grepMaxLineChars]) + "…"
}

// globToRegexp compiles a glob (`*`, `**`, `?`, `{a,b}`) into an anchored
// regexp over slash-separated paths. `**/` also matches zero segments.
func globToRegexp(glob string) (*regexp.Regexp, error) {
	if glob == "" {
		return nil, fmt.Errorf("aikido/agent: empty glob pattern")
	}
	var b strings.Builder
	b.WriteString("^")
	inBraces := false
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case c == '*' && strings.HasPrefix(glob[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case c == '*' && strings.HasPrefix(glob[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '{' && !inBraces:
			b.WriteString("(?:")
			inBraces = true
		case c == '}' && inBraces:
			b.WriteString(")")
			inBraces = false
		case c == ',' && inBraces:
			b.WriteString("|")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	if inBraces {
		return nil, fmt.Errorf("aikido/agent: glob %q: unclosed {", glob)
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
