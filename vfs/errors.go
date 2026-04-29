package vfs

import "errors"

var (
	ErrPathInvalid  = errors.New("aikido/vfs: invalid path")
	ErrFileNotFound = errors.New("aikido/vfs: file not found")
	ErrFileTooLarge = errors.New("aikido/vfs: file too large")
	// ErrReadOnly is returned by Storage backends that do not support mutation
	// (e.g. vfs/embedfs). Callers can errors.Is-check it to fall back to a
	// writable backend or to surface a friendly message.
	ErrReadOnly = errors.New("aikido/vfs: storage is read-only")
)
