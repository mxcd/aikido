package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/tools"
	"github.com/mxcd/aikido/vfs"
)

func readFileDef() llm.ToolDef {
	return llm.ToolDef{
		Name:        "read_file",
		Description: fmt.Sprintf("Read a file by path. Returns up to %d lines starting at offset (1-based line, default 1), plus the content type, the byte size, total_lines and truncated (true when lines follow the returned range). Content is raw, without line numbers.", defaultReadLimit),
		Parameters: tools.Object(map[string]any{
			"path":   tools.String("Relative file path within the workspace."),
			"offset": tools.Integer("Optional 1-based line to start reading at."),
			"limit":  tools.Integer(fmt.Sprintf("Optional maximum number of lines to return (default %d).", defaultReadLimit)),
		}, "path"),
	}
}

func writeFileDef() llm.ToolDef {
	return llm.ToolDef{
		Name:        "write_file",
		Description: "Create or overwrite a file at the given path. Use markdown by default; binary writes are not supported.",
		Parameters: tools.Object(map[string]any{
			"path":         tools.String("Relative file path within the workspace."),
			"content":      tools.String("UTF-8 file contents."),
			"content_type": tools.String("Optional MIME type (e.g., text/markdown). Defaults are inferred from the extension."),
		}, "path", "content"),
	}
}

func listFilesDef() llm.ToolDef {
	return llm.ToolDef{
		Name:        "list_files",
		Description: "List every file in the workspace, sorted by path. Hidden paths are filtered when configured.",
		Parameters:  tools.Object(map[string]any{}),
	}
}

func deleteFileDef() llm.ToolDef {
	return llm.ToolDef{
		Name:        "delete_file",
		Description: "Delete a file by path. Errors if the file does not exist.",
		Parameters: tools.Object(map[string]any{
			"path": tools.String("Relative file path within the workspace."),
		}, "path"),
	}
}

func editFileDef() llm.ToolDef {
	return llm.ToolDef{
		Name:        "edit_file",
		Description: "Replace old_string with new_string in an existing file. old_string must match exactly (whitespace included) and be unique in the file unless replace_all is true. Use write_file to create files.",
		Parameters: tools.Object(map[string]any{
			"path":        tools.String("Relative file path within the workspace."),
			"old_string":  tools.String("Exact text to replace."),
			"new_string":  tools.String("Replacement text; must differ from old_string."),
			"replace_all": tools.Boolean("Replace every occurrence instead of requiring a unique match."),
		}, "path", "old_string", "new_string"),
	}
}

func searchDef(s vfs.Searchable) llm.ToolDef {
	desc := "Search the workspace for files matching a query. " + s.SearchSyntax()
	return llm.ToolDef{
		Name:        "search",
		Description: desc,
		Parameters: tools.Object(map[string]any{
			"query": tools.String("Search query in the syntax described above."),
		}, "query"),
	}
}

func readFileHandler(opts VFSToolOptions) tools.Handler {
	return func(ctx context.Context, args json.RawMessage, env tools.Env) (tools.Result, error) {
		var v struct {
			Path   string `json:"path"`
			Offset int    `json:"offset"`
			Limit  int    `json:"limit"`
		}
		if err := json.Unmarshal(args, &v); err != nil {
			return tools.Result{}, fmt.Errorf("aikido/agent: read_file args: %w", err)
		}
		if err := validatePolicyPath(opts, v.Path); err != nil {
			return tools.Result{}, err
		}
		content, meta, err := opts.Storage.ReadFile(ctx, v.Path)
		if err != nil {
			return tools.Result{}, err
		}
		if v.Offset < 1 {
			v.Offset = 1
		}
		if v.Limit < 1 {
			v.Limit = defaultReadLimit
		}
		lines := splitLines(string(content))
		if v.Offset > 1 && v.Offset > len(lines) {
			return tools.Result{}, fmt.Errorf("aikido/agent: read_file: offset %d is past the end of %s (%d lines)", v.Offset, v.Path, len(lines))
		}
		start := v.Offset - 1
		end := min(start+v.Limit, len(lines))
		return tools.Result{
			Content: map[string]any{
				"content":      strings.Join(lines[start:end], ""),
				"content_type": meta.ContentType,
				"size":         meta.Size,
				"offset":       v.Offset,
				"total_lines":  len(lines),
				"truncated":    end < len(lines),
			},
			Display: fmt.Sprintf("read %s (%d bytes, lines %d-%d of %d)", v.Path, meta.Size, start+1, end, len(lines)),
		}, nil
	}
}

func writeFileHandler(opts VFSToolOptions) tools.Handler {
	return func(ctx context.Context, args json.RawMessage, env tools.Env) (tools.Result, error) {
		var v struct {
			Path        string `json:"path"`
			Content     string `json:"content"`
			ContentType string `json:"content_type"`
		}
		if err := json.Unmarshal(args, &v); err != nil {
			return tools.Result{}, fmt.Errorf("aikido/agent: write_file args: %w", err)
		}
		if err := validatePolicyPath(opts, v.Path); err != nil {
			return tools.Result{}, err
		}
		if int64(len(v.Content)) > opts.MaxFileBytes {
			return tools.Result{}, fmt.Errorf("%w: %d > %d (caller-policy MaxFileBytes)", vfs.ErrFileTooLarge, len(v.Content), opts.MaxFileBytes)
		}
		ct := v.ContentType
		if ct == "" {
			ct = inferContentType(v.Path)
		}
		if err := opts.Storage.WriteFile(ctx, v.Path, []byte(v.Content), ct); err != nil {
			return tools.Result{}, err
		}
		return tools.Result{
			Content: map[string]any{"path": v.Path, "size": int64(len(v.Content))},
			Display: fmt.Sprintf("wrote %s (%d bytes)", v.Path, len(v.Content)),
		}, nil
	}
}

func listFilesHandler(opts VFSToolOptions) tools.Handler {
	return func(ctx context.Context, _ json.RawMessage, _ tools.Env) (tools.Result, error) {
		files, err := opts.Storage.ListFiles(ctx)
		if err != nil {
			return tools.Result{}, err
		}
		out := make([]map[string]any, 0, len(files))
		for _, f := range files {
			if !visible(opts, f.Path) {
				continue
			}
			out = append(out, map[string]any{
				"path":       f.Path,
				"size":       f.Size,
				"updated_at": f.UpdatedAt,
			})
		}
		return tools.Result{
			Content: map[string]any{"files": out},
			Display: fmt.Sprintf("listed %d files", len(out)),
		}, nil
	}
}

func deleteFileHandler(opts VFSToolOptions) tools.Handler {
	return func(ctx context.Context, args json.RawMessage, _ tools.Env) (tools.Result, error) {
		var v struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(args, &v); err != nil {
			return tools.Result{}, fmt.Errorf("aikido/agent: delete_file args: %w", err)
		}
		if err := validatePolicyPath(opts, v.Path); err != nil {
			return tools.Result{}, err
		}
		if err := opts.Storage.DeleteFile(ctx, v.Path); err != nil {
			return tools.Result{}, err
		}
		return tools.Result{
			Content: map[string]any{"path": v.Path},
			Display: fmt.Sprintf("deleted %s", v.Path),
		}, nil
	}
}

func editFileHandler(opts VFSToolOptions) tools.Handler {
	return func(ctx context.Context, args json.RawMessage, _ tools.Env) (tools.Result, error) {
		var v struct {
			Path       string `json:"path"`
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		}
		if err := json.Unmarshal(args, &v); err != nil {
			return tools.Result{}, fmt.Errorf("aikido/agent: edit_file args: %w", err)
		}
		if err := validatePolicyPath(opts, v.Path); err != nil {
			return tools.Result{}, err
		}
		if v.OldString == "" {
			return tools.Result{}, errors.New("aikido/agent: edit_file: old_string is empty; use write_file to create a file")
		}
		if v.OldString == v.NewString {
			return tools.Result{}, errors.New("aikido/agent: edit_file: old_string and new_string are identical")
		}
		content, meta, err := opts.Storage.ReadFile(ctx, v.Path)
		if err != nil {
			return tools.Result{}, err
		}
		if isBinary(content) {
			return tools.Result{}, fmt.Errorf("aikido/agent: edit_file: %s is not a UTF-8 text file", v.Path)
		}
		n := strings.Count(string(content), v.OldString)
		switch {
		case n == 0:
			return tools.Result{}, fmt.Errorf("aikido/agent: edit_file: old_string not found in %s", v.Path)
		case n > 1 && !v.ReplaceAll:
			return tools.Result{}, fmt.Errorf("aikido/agent: edit_file: old_string matches %d times in %s; add context to make it unique or set replace_all", n, v.Path)
		}
		updated := strings.ReplaceAll(string(content), v.OldString, v.NewString)
		if int64(len(updated)) > opts.MaxFileBytes {
			return tools.Result{}, fmt.Errorf("%w: %d > %d (caller-policy MaxFileBytes)", vfs.ErrFileTooLarge, len(updated), opts.MaxFileBytes)
		}
		ct := meta.ContentType
		if ct == "" {
			ct = inferContentType(v.Path)
		}
		if err := opts.Storage.WriteFile(ctx, v.Path, []byte(updated), ct); err != nil {
			return tools.Result{}, err
		}
		return tools.Result{
			Content: map[string]any{"path": v.Path, "replacements": n, "size": int64(len(updated))},
			Display: fmt.Sprintf("edited %s (%d replacements)", v.Path, n),
		}, nil
	}
}

func searchHandler(opts VFSToolOptions, s vfs.Searchable) tools.Handler {
	return func(ctx context.Context, args json.RawMessage, _ tools.Env) (tools.Result, error) {
		var v struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(args, &v); err != nil {
			return tools.Result{}, fmt.Errorf("aikido/agent: search args: %w", err)
		}
		paths, err := s.Search(ctx, v.Query)
		if err != nil {
			return tools.Result{}, err
		}
		filtered := paths[:0:0]
		for _, p := range paths {
			if !visible(opts, p) {
				continue
			}
			filtered = append(filtered, p)
		}
		return tools.Result{
			Content: map[string]any{"paths": filtered},
			Display: fmt.Sprintf("search hit %d files", len(filtered)),
		}, nil
	}
}

// validatePolicyPath enforces structural and caller-policy invariants.
func validatePolicyPath(opts VFSToolOptions, p string) error {
	if err := vfs.ValidatePath(p); err != nil {
		return err
	}
	if !extensionAllowed(opts, p) {
		return fmt.Errorf("%w: extension %q not in AllowedExtensions", vfs.ErrPathInvalid, strings.ToLower(path.Ext(p)))
	}
	if opts.HideHiddenPaths && isHidden(p) {
		return fmt.Errorf("%w: hidden path %q is not addressable when HideHiddenPaths is set", vfs.ErrPathInvalid, p)
	}
	return nil
}

// visible reports whether a path listed by the storage may be shown to the
// model under the caller policy (hidden paths, allowed extensions).
func visible(opts VFSToolOptions, p string) bool {
	return (!opts.HideHiddenPaths || !isHidden(p)) && extensionAllowed(opts, p)
}

func extensionAllowed(opts VFSToolOptions, p string) bool {
	if len(opts.AllowedExtensions) == 0 {
		return true
	}
	ext := path.Ext(p)
	for _, e := range opts.AllowedExtensions {
		if strings.EqualFold(e, ext) {
			return true
		}
	}
	return false
}

// splitLines splits s after each newline; joining the result gives s back.
// A trailing newline does not start an extra empty line.
func splitLines(s string) []string {
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// isBinary treats content as binary when its first 8 KiB hold a NUL byte or
// it is not valid UTF-8.
func isBinary(b []byte) bool {
	return bytes.IndexByte(b[:min(len(b), 8<<10)], 0) >= 0 || !utf8.Valid(b)
}

func isHidden(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if len(seg) > 0 && (seg[0] == '_' || seg[0] == '.') {
			return true
		}
	}
	return false
}

func inferContentType(p string) string {
	if ct := vfs.ContentTypeFor(p); ct != "" {
		return ct
	}
	return "text/markdown" // per Q26: text/markdown for unknown
}
