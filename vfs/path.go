package vfs

import (
	"fmt"
	"strings"
)

// MaxPathBytes is the structural cap on a path's byte length.
const MaxPathBytes = 512

// ValidatePath enforces aikido-wide structural invariants on a path.
//
// Caller-policy filters (max bytes, allowed extensions, hidden-path filtering)
// live on agent.VFSToolOptions, not here.
func ValidatePath(path string) error {
	if path == "" {
		return fmt.Errorf("%w: empty path", ErrPathInvalid)
	}
	if len(path) > MaxPathBytes {
		return fmt.Errorf("%w: path exceeds %d bytes", ErrPathInvalid, MaxPathBytes)
	}
	if strings.ContainsRune(path, 0) {
		return fmt.Errorf("%w: contains null byte", ErrPathInvalid)
	}
	if path[0] == '/' {
		return fmt.Errorf("%w: absolute path", ErrPathInvalid)
	}
	if path[len(path)-1] == '/' {
		return fmt.Errorf("%w: trailing slash", ErrPathInvalid)
	}
	if strings.Contains(path, "//") {
		return fmt.Errorf("%w: double slash", ErrPathInvalid)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." {
			return fmt.Errorf("%w: parent-directory segment", ErrPathInvalid)
		}
	}
	return nil
}
