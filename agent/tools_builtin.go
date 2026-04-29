package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/tools"
	"github.com/mxcd/aikido/vfs"
)

func readFileDef() llm.ToolDef {
	return llm.ToolDef{
		Name:        "read_file",
		Description: "Read the full content of a file by path. Returns the bytes, the inferred content type, and the size.",
		Parameters: tools.Object(map[string]any{
			"path": tools.String("Relative file path within the workspace."),
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
			Path string `json:"path"`
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
		return tools.Result{
			Content: map[string]any{
				"content":      string(content),
				"content_type": meta.ContentType,
				"size":         meta.Size,
			},
			Display: fmt.Sprintf("read %s (%d bytes)", v.Path, meta.Size),
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
			if opts.HideHiddenPaths && isHidden(f.Path) {
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
			if opts.HideHiddenPaths && isHidden(p) {
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
	if len(opts.AllowedExtensions) > 0 {
		ext := strings.ToLower(path.Ext(p))
		ok := false
		for _, e := range opts.AllowedExtensions {
			if strings.EqualFold(e, ext) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("%w: extension %q not in AllowedExtensions", vfs.ErrPathInvalid, ext)
		}
	}
	if opts.HideHiddenPaths && isHidden(p) {
		return fmt.Errorf("%w: hidden path %q is not addressable when HideHiddenPaths is set", vfs.ErrPathInvalid, p)
	}
	return nil
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
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".html", ".htm":
		return "text/html"
	case ".csv":
		return "text/csv"
	default:
		return "text/markdown" // per Q26: text/markdown for unknown
	}
}
