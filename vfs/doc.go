// Package vfs is the pluggable storage abstraction for AI-managed projects.
//
// The base Storage contract is intentionally minimal (4 methods).
// Optional capabilities — scoping (ScopedStorage) and search (Searchable) —
// extend it via interface assertion or registration-time binding, mirroring
// the capability pattern used in mxcd/go-basicauth.
//
// v1 ships memory-only (vfs/memory). Other backends are caller-implemented
// or come in v1.x / v2.
package vfs
