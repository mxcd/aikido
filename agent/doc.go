// Package agent provides the session-based streaming agent loop that ties llm,
// tools, History, and Locker together.
//
// The library never sees a scope parameter. Multi-tenant callers bind scope at
// VFS-tool registration time via vfs.ScopedStorage.Scope (ADR-013).
//
// Concurrency is bounded at the session granularity by a pluggable Locker
// (ADR-024). v1 ships LocalLocker for in-process use; v1.1 ships a Redis impl;
// callers can implement Locker themselves for any other backend.
package agent
