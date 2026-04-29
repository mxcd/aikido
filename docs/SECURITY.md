# Security

aikido runs untrusted model output. The library defends the host application by sandboxing what tools can do and enforcing strict bounds on resource use. This file documents the principles; implementation lives in `agent/`, `vfs/`, and `tools/`.

## Threat model

The model is **not trusted**. Tool inputs are model output. The library assumes:

- The model may emit malformed JSON arguments.
- The model may attempt path traversal (`../etc/passwd`, `/abs/path`, null bytes).
- The model may attempt to call tools the user did not register (the registry rejects unknown names).
- The model may loop indefinitely if not bounded (max-turns, timeout).
- The model may emit arbitrary text that the host application surfaces to end users (caller's responsibility to escape on output).

The library does **not** assume:

- The user's `vfs.Storage` implementation is safe against SQL injection or directory traversal at its own layer. Path validation in aikido protects the model→agent boundary; storage implementations remain responsible for their own internal safety.

## Sandbox principles

The agent's tool surface in v1 is intentionally narrow.

### What aikido tools can do

- Read, write, delete, list, search files via the supplied `vfs.Storage` (which the caller may have pre-bound to a tenant scope via `vfs.ScopedStorage.Scope`).
- Call user-supplied custom tools registered with `tools.Registry`.

### What aikido tools cannot do

- Execute shell commands (no `bash`, no `exec`).
- Open network sockets, fetch URLs, or hit DNS.
- Read or write the host filesystem outside the VFS.
- Evaluate arbitrary code (no `eval`, no template execution of model-supplied content).
- Cross scope boundaries — built-in VFS tool handlers capture the registration-time `Storage` (typically already scope-bound by the caller; ADR-013) via closure, so there is no runtime path for the model to address a different scope's storage.
- Read or modify other agents' state, message history, or API keys.

These limits are not configurable in v1. Custom tools the user registers can do anything Go can do; the library has no opinion on their safety. Document and review user-registered tools accordingly.

## Path validation

Every path that crosses into `vfs.Storage` goes through `vfs.ValidatePath`. The rules:

- **No `..` segments.** Reject `../foo`, `foo/../bar`, `foo/../../etc`.
- **No absolute paths.** Reject any path starting with `/`.
- **No null bytes.** `\x00` anywhere in the path is rejected.
- **Length cap.** 512 bytes maximum.
- **No empty path.** Empty string is rejected.
- **Optional extension whitelist.** When `agent.VFSToolOptions.AllowedExtensions` is non-nil, only paths whose extension is in the list are allowed for write/delete (read and list are unaffected).

The validation runs in two places:

1. Inside the built-in VFS tools (`read_file`, `write_file`, etc.) before any storage call.
2. Inside `vfs.WriteFile` / `vfs.DeleteFile` as a defense-in-depth check.

User-implemented `Storage` backends should call `vfs.ValidatePath` themselves on entry to protect against bugs in custom tools.

## Hidden-path convention

Paths starting with `_` or `.` are considered library-managed or hidden. The default `agent.VFSToolOptions.HideHiddenPaths = true` causes the built-in `list_files` and `search` tools to filter them out of model-visible results. The model can still write to these paths if it constructs them explicitly — hiding is a cosmetic default, not a security boundary.

The note-then-consolidate recipe (PATTERNS.md) writes to `_notes/{turn-uuid}.md` and relies on this convention so its bookkeeping does not pollute the model's view of the workspace.

## API key handling

- Keys are passed in via `Options` structs at construction time (`openrouter.Options.APIKey`). The library never reads environment variables on its own.
- Keys are never logged. The OpenRouter client uses `Authorization: Bearer <key>` and the request body never includes the key.
- The library does not persist keys. Callers manage key lifetime.

When implementing custom providers, follow the same rules: take the key in the constructor, store it on the unexported struct, never log it, never serialize it.

## Tool error containment

Tool errors do not abort the agent loop. They are returned to the model as `tool_result` messages with `OK = false` and the error text. This lets the model recover ("the path was wrong, try `notes.md` instead"). The library never lets a tool error propagate to the agent's caller as a fatal error; the only fatal errors are provider errors, context cancellation, or `MaxTurns` exhaustion.

This is by design and not configurable in v1. If a custom tool needs to halt the agent, it should return a `tool_result` with content telling the model to stop, rather than returning an error.

## Resource bounds

The agent enforces these unconditionally; defaults are caller-tunable but the guards are always active.

| Bound | Default | Why |
|-------|---------|-----|
| `MaxTurns` | 20 | Prevent runaway tool loops. |
| `RunTimeout` | 10m (`0` = no cap) | Total wall-clock cap for one `Run` call; cancels the whole goroutine via `context.WithTimeout`. |
| `LLMCallTimeout` | 180s (`0` = no cap) | Per-provider-call cap; protects `RunTimeout` from a single stuck LLM call. |
| `MaxTokens` | 16384 | Per-LLM-call output cap. |
| Per-session lock (via `agent.Locker`) | one `Run` at a time per session ID | Prevents partial-state corruption on shared History. |
| Path length | 512 bytes | Prevent storage abuse. |
| Default file size cap (built-in tools) | 1 MiB | Configurable via `VFSToolOptions.MaxFileBytes`. |

User code can impose stricter limits via `SessionOptions` (lower `MaxTurns`, shorter timeouts) but cannot loosen the path-length cap or path-validation rules without modifying the library.

## Multi-replica deployments

v1 ships an in-process `agent.LocalLocker` that is production-grade for **single-replica** deployments: two `Run` calls on the same session ID serialize within one process even when callers build a fresh `*Session` per HTTP request, as long as they share one `LocalLocker`.

For **multi-replica** deployments, the implementing application provides the lock backend by implementing the `agent.Locker` interface (typically Redis-backed; etcd, Postgres advisory locks, or anything with mutual-exclusion-by-key works). v1.1 ships `agent/locker/redis` with an abstract `Client` interface so aikido does not import a Redis client library directly.

Without a distributed `Locker`, two processes running aikido against the same backend `History` and `Storage` for the same session ID can produce interleaved transcripts and concurrent writes — this is **not safe**. The library does not warn at runtime; deployment correctness is the caller's responsibility.

## Reporting issues

aikido is pre-v0.1. Until a SECURITY.md security policy with a contact address is added, report security-relevant issues by opening a private GitHub issue or contacting MaPa directly via the email on the GitHub profile.
