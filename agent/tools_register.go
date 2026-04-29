package agent

import (
	"errors"

	"github.com/mxcd/aikido/tools"
	"github.com/mxcd/aikido/vfs"
)

// VFSToolOptions configure the built-in VFS tools.
type VFSToolOptions struct {
	Storage           vfs.Storage // required; pre-bound to scope if multi-tenant
	HideHiddenPaths   bool        // hide paths beginning with "_" or "." (per segment)
	AllowedExtensions []string    // nil = allow all
	MaxFileBytes      int64       // default 1 MiB; 0 means default

	// ReadOnly disables registration of write_file and delete_file. Use this
	// for fixed-corpus deployments (e.g. an embedded knowledge base over
	// vfs/embedfs). Search and list/read remain registered.
	ReadOnly bool
}

// RegisterVFSTools registers read_file, list_files into the supplied registry,
// plus write_file/delete_file when not read-only. If opts.Storage satisfies
// vfs.Searchable, also registers `search`. Tool handlers capture opts.Storage
// via closure (ADR-021).
func RegisterVFSTools(reg *tools.Registry, opts *VFSToolOptions) error {
	if reg == nil {
		return errors.New("aikido/agent: RegisterVFSTools: nil registry")
	}
	if opts == nil || opts.Storage == nil {
		return errors.New("aikido/agent: RegisterVFSTools: Storage is required")
	}
	cp := *opts
	if cp.MaxFileBytes <= 0 {
		cp.MaxFileBytes = 1 << 20 // 1 MiB
	}

	if err := reg.Register(readFileDef(), readFileHandler(cp)); err != nil {
		return err
	}
	if err := reg.Register(listFilesDef(), listFilesHandler(cp)); err != nil {
		return err
	}
	if !cp.ReadOnly {
		if err := reg.Register(writeFileDef(), writeFileHandler(cp)); err != nil {
			return err
		}
		if err := reg.Register(deleteFileDef(), deleteFileHandler(cp)); err != nil {
			return err
		}
	}
	if searchable, ok := cp.Storage.(vfs.Searchable); ok {
		if err := reg.Register(searchDef(searchable), searchHandler(cp, searchable)); err != nil {
			return err
		}
	}
	return nil
}
