# Waves — Late (W5–W8 + tag)

> ⚠️ **Divergence banner (30.04.2026).** This file reflects the *pre-second-grilling* surface in places. Authoritative shapes live in `../API.md`, `../../ARCHITECTURE.md`, and `../../DECISIONS.md`; authoritative wave structure lives in `../PLAN.md`. Use this file for *implementation depth* but cross-check call shapes against `../API.md`.
>
> **Known divergences:** W2+W3 collapsed (wave numbers shift down by one from W2); W8 (notes) deleted (ADR-015); `agent.NewAgent` → `Session` / `NewLocalSession` (ADR-012); per-Session struct mutex → `agent.Locker` plug-in with `agent.NewLocalLocker` (ADR-024); `History.Append` variadic single-flush; `History` and `Locker` strict-error policy (ADR-023); `Tx` / `Snapshot` / `Restore` / `HashState` dropped (ADR-017, ADR-019); `Catalog` dropped (ADR-025); `RunTimeout` + `LLMCallTimeout` replace `TurnTimeout`; `MaxTokens` default 16384.

Detailed wave specs for the second half of v1: storage substrate, agent core, and built-in tools. (The original W8 — `notes` package — is deferred to v1.x per ADR-015.) The early-waves doc (W0–W4) is owned by another agent; this file picks up where the renamed-`tools`-wave lands.

Reading order: API.md is locked — the signatures here come straight from there. ARCHITECTURE.md (especially "The agent loop" and "Transaction model") is the W6 source of truth. SECURITY.md's threat model is enforced by W6/W7 unconditionally. REFERENCE-CODE.md and OPENROUTER-DETAILS.md are sibling planning docs — cited inline for production-loop deltas and wire-format details.

Date format `dd.MM.yyyy` per CONTRIBUTING.md. Estimates assume one focused implementer with the references open in another window.

---

## W5 — `vfs` interface + memory impl + conformance suite

**Estimated LOC:** ~600 go + ~600 test | **Estimated effort:** 10–12 hours focused | **Status:** not started

### File tree

```
vfs/
  doc.go              — package godoc; describes the Storage contract in prose
  storage.go          — Storage interface, FileMeta, SnapshotID
  txstorage.go        — TxStorage capability interface, Tx interface
  path.go             — ValidatePath
  hash.go             — HashContent helper used by HashState implementations
  errors.go           — ErrPathInvalid, ErrFileNotFound, ErrFileTooLarge, ErrProjectNotFound
  conformance.go      — RunConformance(t, factory) — exported test suite
  conformance_tx.go   — separate file so the tx tests can be cleanly skipped
  path_test.go        — direct unit tests for ValidatePath
  hash_test.go        — direct unit tests for HashContent
vfs/memory/
  doc.go              — one-paragraph "in-memory backend" godoc
  storage.go          — *Storage type, NewStorage, all Storage method implementations
  storage_tx.go       — BeginTx + *memTx implementation (separate file for clarity)
  storage_test.go     — calls vfs.RunConformance(t, func() vfs.Storage { return memvfs.NewStorage() })
```

The conformance suite ships under `vfs/` (not `vfs/memory/`) because it is consumed by every storage backend, including ones in caller code. Per CONTRIBUTING.md it follows the "conformance suites are `<package>_conformance.go` exporting a single `Run<Name>(t *testing.T, factory)` function" rule — but extended to two files so the tx tests can be conditionally compiled or skipped without touching the base suite.

### Public API delivered

Signatures locked by API.md (lines 280–339 / 354–365); reproduced here verbatim.

```go
package vfs

import (
    "context"
    "testing"
    "time"

    "github.com/google/uuid"
)

type FileMeta struct {
    Path        string
    ContentType string
    Size        int64
    UpdatedAt   time.Time
}

type SnapshotID string

type Storage interface {
    CreateProject(ctx context.Context) (uuid.UUID, error)
    ProjectExists(ctx context.Context, projectID uuid.UUID) (bool, error)

    ListFiles(ctx context.Context, projectID uuid.UUID) ([]FileMeta, error)
    ReadFile(ctx context.Context, projectID uuid.UUID, path string) ([]byte, FileMeta, error)
    WriteFile(ctx context.Context, projectID uuid.UUID, path string, content []byte, contentType string) error
    DeleteFile(ctx context.Context, projectID uuid.UUID, path string) error

    Snapshot(ctx context.Context, projectID uuid.UUID, turnID uuid.UUID, changedPaths []string) (SnapshotID, error)
    Restore(ctx context.Context, projectID uuid.UUID, snapshotID SnapshotID) error
    HashState(ctx context.Context, projectID uuid.UUID) (string, error)
}

type TxStorage interface {
    Storage
    BeginTx(ctx context.Context, projectID uuid.UUID) (Tx, error)
}

type Tx interface {
    Storage
    Commit() error
    Rollback() error
}

func ValidatePath(path string) error
func HashContent(metas []FileMeta, contents map[string][]byte) string
func RunConformance(t *testing.T, factory func() Storage)
```

```go
package memory

import "github.com/mxcd/aikido/vfs"

type Storage struct{ /* fields described in "Internal types" below */ }

func NewStorage() *Storage

var (
    _ vfs.Storage   = (*Storage)(nil)
    _ vfs.TxStorage = (*Storage)(nil)
)
```

The compile-time assertion lines belong at the top of `vfs/memory/storage.go` per the convention in API.md and CONTRIBUTING.md.

### Internal types (unexported)

```go
// vfs/memory/storage.go
type Storage struct {
    mu       sync.RWMutex
    projects map[uuid.UUID]*memProject
}

type memProject struct {
    files     map[string]*memFile           // path -> file entry
    snapshots map[vfs.SnapshotID]*memSnapshot
}

type memFile struct {
    content     []byte
    contentType string
    size        int64
    updatedAt   time.Time
}

// memSnapshot stores per-path "previous content" so Restore can undo writes
// and creations that happened after the snapshot. nil prevContent means the
// file did not exist before — Restore deletes it.
type memSnapshot struct {
    turnID    uuid.UUID
    createdAt time.Time
    entries   map[string]*memFile // nil value = file did not exist pre-write
}

// vfs/memory/storage_tx.go
type memTx struct {
    parent    *Storage
    projectID uuid.UUID
    files     map[string]*memFile // copy-on-begin
    committed bool
    rolled    bool
}
```

This shape mirrors `mxcd/go-basicauth/storage_memory.go` (sync.RWMutex over a map of pointers; constructor initialises both the outer map and per-project sub-maps lazily on first `CreateProject`). Differences: aikido has only one primary index per project (`path -> *memFile`); go-basicauth had three (by-id, by-username, by-email). Per REFERENCE-CODE.md §11.

### Errors introduced

```go
// vfs/errors.go
var (
    ErrPathInvalid     = errors.New("vfs: invalid path")
    ErrFileNotFound    = errors.New("vfs: file not found")
    ErrFileTooLarge    = errors.New("vfs: file exceeds size limit")
    ErrProjectNotFound = errors.New("vfs: project not found")
)
```

- `ErrPathInvalid` — returned by `ValidatePath`, by `WriteFile` / `DeleteFile` / `ReadFile` defense-in-depth checks (per Q8 in REFERENCE-CODE.md), and by `BeginTx` if the projectID is the zero UUID. Wrapped with the offending path as context where useful: `fmt.Errorf("vfs: %w (path=%q)", ErrPathInvalid, path)`.
- `ErrFileNotFound` — returned by `ReadFile` and `DeleteFile` when the path is not present. Tools turn this into a graceful `Result{OK: false}` for the model rather than aborting the loop (per SECURITY.md "Tool error containment").
- `ErrFileTooLarge` — returned by `WriteFile` when the storage backend has its own size cap. The bundled memory backend does not enforce one (the agent's `VFSToolOptions.MaxFileBytes` is the v1 enforcement layer); the error sentinel exists so user-supplied backends with hard caps can use it.
- `ErrProjectNotFound` — returned by every method that takes `projectID` when the project does not exist. The `ProjectExists` helper allows callers to test without dispatching a real read.

The error block follows the `mxcd/go-basicauth/types.go` style (REFERENCE-CODE.md §13): top-of-file `var ( … )`, `Err` + descriptive lowercase phrase, no terminal period, lowercase first letter.

### Implementation order within the wave

1. **`vfs/errors.go`** — sentinels first; everything else imports them.
2. **`vfs/path.go` + `vfs/path_test.go`** — pure function, no dependencies. Land green tests for every reject case before moving on. This is the single most security-critical file in the wave (per SECURITY.md "Path validation"); get it right in isolation.
3. **`vfs/hash.go` + `vfs/hash_test.go`** — pure function. Determinism test (same input, same output across N invocations) and order-independence test (same files in different list order produce the same hash).
4. **`vfs/storage.go`** — interface declaration plus godoc per method. Comment style follows `go-basicauth/storage_interface.go` (REFERENCE-CODE.md §10): one-sentence purpose per method, mention error sentinels by name.
5. **`vfs/txstorage.go`** — capability interface. Doc-comment lifts the structure of `AtomicBackupCodeConsumer`'s comment verbatim (purpose / what we do without it / implementation hint), per REFERENCE-CODE.md §10 "Snippet to mirror".
6. **`vfs/memory/storage.go`** — non-tx Storage methods. Construction shape mirrors `storage_memory.go` L11–L24: `sync.RWMutex` + map-of-pointers + lazy project init. Per-method `s.mu.Lock(); defer s.mu.Unlock()` for writes; `RLock` for reads. Snapshot / Restore inline — they are non-trivial but not transactional in the SQL sense.
7. **`vfs/memory/storage_tx.go`** — `BeginTx` returns `*memTx` wrapping a copy-on-begin clone of the project's `files` map. `memTx`'s `Storage` methods operate on the clone. `Commit` re-acquires the parent's write lock, replaces `parent.projects[projectID].files = tx.files`, sets `committed=true`. `Rollback` just sets `rolled=true`.
8. **`vfs/conformance.go`** — base suite. ~25 sub-tests using `t.Run`. Skips tx sub-tests when the factory output does not satisfy `TxStorage`.
9. **`vfs/conformance_tx.go`** — tx-only sub-tests, gated on type assertion at function entry.
10. **`vfs/memory/storage_test.go`** — single test function calling `vfs.RunConformance(t, func() vfs.Storage { return memvfs.NewStorage() })`. That is the entire test file. Per CONTRIBUTING.md, `examples/` build is the smoke check; the conformance suite is the unit-test foundation.

### Conformance suite — full contents

The suite is a flat `RunConformance(t *testing.T, factory func() Storage)` with `t.Run(...)` sub-tests. Every sub-test calls `factory()` to get a fresh `Storage` so they cannot bleed state between cases. The factory is invoked anew per test, never shared.

Layout:

```go
// vfs/conformance.go
func RunConformance(t *testing.T, factory func() Storage) {
    t.Helper()
    t.Run("ValidatePath_RejectsInvalid", func(t *testing.T) { testValidatePathRejects(t) })
    t.Run("ValidatePath_AcceptsValid",   func(t *testing.T) { testValidatePathAccepts(t) })
    t.Run("CreateProject_ReturnsUniqueUUIDs", func(t *testing.T) { testCreateProjectUnique(t, factory) })
    t.Run("ProjectExists_TrueAfterCreate",    func(t *testing.T) { testProjectExistsAfterCreate(t, factory) })
    t.Run("ProjectExists_FalseForUnknown",    func(t *testing.T) { testProjectExistsUnknown(t, factory) })
    t.Run("WriteRead_RoundTripsBytes",        func(t *testing.T) { testWriteReadRoundTrip(t, factory) })
    t.Run("WriteRead_RoundTripsContentType",  func(t *testing.T) { testWriteReadContentType(t, factory) })
    t.Run("Write_RejectsInvalidPath",         func(t *testing.T) { testWriteRejectsBadPath(t, factory) })  // defense-in-depth, per Q8
    t.Run("Read_NotFoundError",               func(t *testing.T) { testReadNotFound(t, factory) })
    t.Run("List_DeterministicOrder",          func(t *testing.T) { testListDeterministicOrder(t, factory) })
    t.Run("List_EmptyProject",                func(t *testing.T) { testListEmpty(t, factory) })
    t.Run("Delete_NotFoundError",             func(t *testing.T) { testDeleteNotFound(t, factory) })
    t.Run("Delete_RemovesFile",               func(t *testing.T) { testDeleteRemoves(t, factory) })
    t.Run("Snapshot_CapturesPriorState",      func(t *testing.T) { testSnapshotPrior(t, factory) })
    t.Run("Restore_RevertsWrites",            func(t *testing.T) { testRestoreRevertsWrites(t, factory) })
    t.Run("Restore_DeletesCreatedFiles",      func(t *testing.T) { testRestoreDeletesCreated(t, factory) })
    t.Run("HashState_StableForSameContent",   func(t *testing.T) { testHashStable(t, factory) })
    t.Run("HashState_DiffersForDiffContent",  func(t *testing.T) { testHashDiffers(t, factory) })
    t.Run("HashState_OrderIndependent",       func(t *testing.T) { testHashOrderIndependent(t, factory) })

    // Tx tests gated by TxStorage assertion (see conformance_tx.go).
    runTxConformance(t, factory)
}
```

`conformance_tx.go`:

```go
// vfs/conformance_tx.go
func runTxConformance(t *testing.T, factory func() Storage) {
    t.Helper()
    s := factory()
    txS, ok := s.(TxStorage)
    if !ok {
        t.Log("storage does not satisfy TxStorage; skipping tx conformance tests")
        return
    }
    _ = txS // silence linter; the inner tests re-assert per fresh factory output
    t.Run("BeginTx_CommitApplies",            func(t *testing.T) { testTxCommit(t, factory) })
    t.Run("BeginTx_RollbackDiscards",         func(t *testing.T) { testTxRollback(t, factory) })
    t.Run("BeginTx_NestedReads_SeeWrites",    func(t *testing.T) { testTxNestedReads(t, factory) })
    t.Run("BeginTx_OutsideReadSeesNothing",   func(t *testing.T) { testTxIsolation(t, factory) })
    t.Run("BeginTx_DoubleCommitErrors",       func(t *testing.T) { testTxDoubleCommit(t, factory) })
    t.Run("BeginTx_RollbackAfterCommitErrs",  func(t *testing.T) { testTxRollbackAfterCommit(t, factory) })
}
```

The type-assertion at the top of `runTxConformance` is the explicit "tx tests are skipped when factory returns a Storage that does not satisfy TxStorage" gate per the user-facing exit criterion.

### Test scenarios (fully enumerated)

| Test name | Setup | Assertion |
|---|---|---|
| `ValidatePath_RejectsInvalid` | direct unit call, no Storage | each of `""`, `"/abs"`, `"../foo"`, `"foo/../bar"`, `"foo/../../etc"`, `"a\x00b"`, 513-byte string returns `ErrPathInvalid` |
| `ValidatePath_AcceptsValid` | direct unit call | each of `"foo.md"`, `"a/b.md"`, `"_notes/x.md"`, `".hidden"`, 512-byte string returns nil |
| `CreateProject_ReturnsUniqueUUIDs` | call `CreateProject` 3× | three returned UUIDs are pairwise distinct, none is the zero UUID |
| `ProjectExists_TrueAfterCreate` | `CreateProject` then `ProjectExists` | returns `(true, nil)` |
| `ProjectExists_FalseForUnknown` | `ProjectExists` with random UUID | returns `(false, nil)` (NOT an error — non-existence is queryable) |
| `WriteRead_RoundTripsBytes` | `WriteFile("notes.md", "hello world", "text/markdown")`; then `ReadFile` | content bytes == "hello world", `meta.Size == 11`, `meta.ContentType == "text/markdown"`, `meta.UpdatedAt` non-zero |
| `WriteRead_RoundTripsContentType` | write with `"image/png"` | meta carries `"image/png"` exactly; second write overwrites both content and content type |
| `Write_RejectsInvalidPath` | `WriteFile(projectID, "../etc/passwd", ...)` | returns `ErrPathInvalid`; **this exercises the defense-in-depth check inside the storage backend itself**, not the tool layer (per Q8 in REFERENCE-CODE.md). Failing this means `vfs.ValidatePath` was not called inside `WriteFile`. |
| `Read_NotFoundError` | empty project, `ReadFile("missing.md")` | returns `(nil, FileMeta{}, err)` where `errors.Is(err, ErrFileNotFound)` |
| `List_DeterministicOrder` | write `"b.md"`, `"a.md"`, `"_x.md"` in that order; `ListFiles` | result is sorted by `Path` ascending — `["_x.md", "a.md", "b.md"]`. **Sorted, NOT insertion order**. The hidden-path filter is a tool-layer concern (`HideHiddenPaths`), not a storage-layer concern. |
| `List_EmptyProject` | new project, `ListFiles` | returns empty slice + nil error (NOT nil slice) |
| `Delete_NotFoundError` | empty project, `DeleteFile("missing.md")` | returns `errors.Is(err, ErrFileNotFound)` |
| `Delete_RemovesFile` | write then delete | post-delete `ReadFile` returns `ErrFileNotFound`; `ListFiles` does not include the path |
| `Snapshot_CapturesPriorState` | write `a.md=v1`; `Snapshot(turnID, ["a.md"])`; write `a.md=v2` | `Restore(snapshot)` brings `a.md` back to `v1` |
| `Restore_RevertsWrites` | write `a.md=v1`; snapshot; write `a.md=v2`; restore | `ReadFile("a.md")` content == `v1` |
| `Restore_DeletesCreatedFiles` | snapshot of empty project (changedPaths=`["new.md"]`); write `new.md`; restore | `ReadFile("new.md")` returns `ErrFileNotFound` (the snapshot's nil-prev-content semantics deletes the file) |
| `HashState_StableForSameContent` | write `"a.md"="x"`, `"b.md"="y"`; call `HashState` 5× | all 5 results are equal |
| `HashState_DiffersForDiffContent` | start with `"a.md"="x"`; hash; mutate to `"a.md"="y"`; hash | the two hashes are not equal |
| `HashState_OrderIndependent` | project A: write `a`, then `b`; project B: write `b`, then `a` (same content); compare hashes | hash(A) == hash(B) — file write order does not matter |
| `BeginTx_CommitApplies` | begin tx; `tx.WriteFile("a.md", "v1")`; `tx.Commit()` | `Storage.ReadFile("a.md")` returns `"v1"` |
| `BeginTx_RollbackDiscards` | begin tx; `tx.WriteFile("a.md", "v1")`; `tx.Rollback()` | `Storage.ReadFile("a.md")` returns `ErrFileNotFound` |
| `BeginTx_NestedReads_SeeWrites` | begin tx; `tx.WriteFile("a.md", "v1")`; `tx.ReadFile("a.md")` | tx-internal read returns `"v1"` (the tx is itself a Storage and observes its own writes) |
| `BeginTx_OutsideReadSeesNothing` | start with `Storage.WriteFile("a.md", "v1")`; begin tx; `tx.WriteFile("a.md", "v2")`; from a *different* call path read via the parent `Storage.ReadFile` | parent read returns `"v1"` until commit (tx isolation) |
| `BeginTx_DoubleCommitErrors` | begin tx; commit; commit again | second commit returns a non-nil error (does not panic, does not silently no-op) |
| `BeginTx_RollbackAfterCommitErrs` | begin tx; commit; rollback | rollback returns a non-nil error |

The matrix above is exhaustive for v1. Anything outside it is open question or v2.

### Memory backend implementation strategy

The strategy lives in two files. Per REFERENCE-CODE.md §11, the shape mirrors `mxcd/go-basicauth/storage_memory.go` 1:1 with two changes: (a) one index instead of three, (b) `BeginTx` instead of `ConsumeBackupCodeHash` as the capability hook.

**`vfs/memory/storage.go`:**

```go
type Storage struct {
    mu       sync.RWMutex
    projects map[uuid.UUID]*memProject
}

func NewStorage() *Storage {
    return &Storage{
        projects: map[uuid.UUID]*memProject{},
    }
}

func (s *Storage) CreateProject(ctx context.Context) (uuid.UUID, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    id := uuid.New()
    s.projects[id] = &memProject{
        files:     map[string]*memFile{},
        snapshots: map[vfs.SnapshotID]*memSnapshot{},
    }
    return id, nil
}

func (s *Storage) WriteFile(ctx context.Context, projectID uuid.UUID, path string, content []byte, contentType string) error {
    if err := vfs.ValidatePath(path); err != nil {
        return err  // defense-in-depth, per Q8
    }
    s.mu.Lock()
    defer s.mu.Unlock()
    p, ok := s.projects[projectID]
    if !ok {
        return vfs.ErrProjectNotFound
    }
    p.files[path] = &memFile{
        content:     append([]byte(nil), content...),  // defensive copy
        contentType: contentType,
        size:        int64(len(content)),
        updatedAt:   time.Now().UTC(),
    }
    return nil
}
```

The defensive copy on `content` matters because callers can mutate the slice they passed in; the in-memory store must not be perturbed by post-hoc mutations.

**`HashState` algorithm.** Per ARCHITECTURE.md and the W5 deliverables list:

1. Read all files in the project under read-lock.
2. Sort the file metas by `Path` ascending.
3. For each file in order, compute SHA-256 of the content (the per-file content hash).
4. Build a triple string: `path + "|" + size + "|" + hex(content_hash) + "\n"` and concat all triples.
5. SHA-256 the concatenated string; return its hex form.

`vfs.HashContent(metas []FileMeta, contents map[string][]byte) string` is the pure-function helper that implements the algorithm. Backends hand it sorted metas and a content map and get the canonical hash back. Memory backend's `HashState` is a thin wrapper that reads under lock and delegates.

The `path|size|sha256(content)` triple is what the algorithm string says: storing size separately makes the hash sensitive to byte-identical content with unequal recorded sizes (catches storage-layer bugs).

**`BeginTx` semantics — copy-on-begin.**

```go
func (s *Storage) BeginTx(ctx context.Context, projectID uuid.UUID) (vfs.Tx, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    p, ok := s.projects[projectID]
    if !ok {
        return nil, vfs.ErrProjectNotFound
    }
    clone := make(map[string]*memFile, len(p.files))
    for k, v := range p.files {
        // memFile is value-shared but content is byte-slice-shared too.
        // Tx writes always replace the whole entry, so that's fine; but
        // be careful never to mutate *memFile.content in place.
        clone[k] = v
    }
    return &memTx{parent: s, projectID: projectID, files: clone}, nil
}

func (tx *memTx) Commit() error {
    if tx.committed { return errors.New("vfs/memory: tx already committed") }
    if tx.rolled    { return errors.New("vfs/memory: tx already rolled back") }
    tx.parent.mu.Lock()
    defer tx.parent.mu.Unlock()
    p, ok := tx.parent.projects[tx.projectID]
    if !ok { return vfs.ErrProjectNotFound }
    p.files = tx.files
    tx.committed = true
    return nil
}
```

Tx storage methods read/write `tx.files` directly without locking the parent — the tx is single-goroutine by contract (the agent holds the per-project mutex outside it; see W6).

### Defense-in-depth note (Q8)

Per REFERENCE-CODE.md Q8, `Storage.WriteFile` and `Storage.DeleteFile` both call `vfs.ValidatePath` *internally* even though the W7 tools always validate first. The conformance suite's `Write_RejectsInvalidPath` test exists specifically to catch a backend that skipped this. Implementers writing their own backend who pass the conformance suite are guaranteed to be safe against malicious paths smuggled in via custom tools that bypassed the W7 validators.

### Wave dependencies

- **Consumes from:** none (W5 has no upstream dependencies inside aikido). The only external dep is `github.com/google/uuid` already pulled in by W1.
- **Provides to:** W6 uses `vfs.Storage` as `agent.Options.Storage`'s type and `vfs.TxStorage` for the capability assertion. W7 uses every `Storage` method from inside built-in tool handlers. W8's `Notebook` reads/writes via `vfs.Storage`.

### Risks (concrete mitigations)

- **Risk:** Conformance suite over-specifies — some user backends (S3, eventually-consistent KV) cannot satisfy all base tests. **Mitigation:** the suite is opinionated for v1; if a real user backend falls foul of, say, `List_DeterministicOrder`, that's the user's problem to handle in the storage layer. The conformance suite is the contract; backends that cannot meet it should not pretend to satisfy `vfs.Storage`. v2 may add a "weak conformance" subset if needed; v1 does not.
- **Risk:** Snapshot/Restore semantics of "nil prev content means delete on restore" surprise users. **Mitigation:** the `Restore_DeletesCreatedFiles` test forces every backend to implement this consistently; the godoc on `Snapshot` and `Restore` says it explicitly.
- **Risk:** Memory backend's tx clone is O(N) in file count — pathological for projects with thousands of files. **Mitigation:** v1 ships memory only for dev/test; production users supply Postgres-or-Ent backends where tx is real. Document the O(N) characteristic in `vfs/memory`'s package doc; out of scope to optimise in v1.
- **Risk:** Path validation regex misses an exotic case (e.g., Windows reserved names like `con`, `nul`). **Mitigation:** v1 validation is platform-agnostic — paths are byte strings, validated for `..`, `/`, null, length, empty. Windows-specific reserved-name handling is the storage backend's concern; the bundled in-memory backend has no filesystem so it cannot care.

### Exit criteria

- [ ] `vfs/memory/storage_test.go` passes — `RunConformance` green across all sub-tests.
- [ ] `go doc github.com/mxcd/aikido/vfs` reads as a complete reference: every method has a one-paragraph godoc; every exported error is documented; `TxStorage` and `Tx` doc-comments explain the capability pattern (not just what they are).
- [ ] `go test ./vfs/...` produces no race warnings (`-race` clean).
- [ ] `Write_RejectsInvalidPath` is green — confirms defense-in-depth wired through.
- [ ] No new third-party dependencies beyond the `uuid` already brought in by W1.
- [ ] `vfs/conformance.go` is importable from outside aikido — i.e., it lives in a non-`_test.go` file and is exported. Per CONTRIBUTING.md: "Conformance suites are `<package>_conformance.go` exporting a single `Run<Name>(t *testing.T, factory)` function."

---

## W6 — `agent` core

**Estimated LOC:** ~500 go + ~700 test | **Estimated effort:** 14–18 hours focused | **Status:** not started

W6 is the highest-risk wave. The `runLoop` function is the single load-bearing piece of code in v1 — it is what every caller exercises, every test asserts on, and every guardrail flows through. Get it wrong and the rest of the library inherits the bug. Reproduce the ARCHITECTURE.md pseudocode literally, with TODO blocks pointing at the algorithm step they implement.

### File tree

```
agent/
  doc.go            — package godoc; explains the loop in two paragraphs
  agent.go          — Options, Agent, NewAgent, validation + defaults
  event.go          — Event, EventKind, ToolResult, EndReason constants
  run.go            — Run, RunWithMessages, runLoop, drain, helpers
  safety.go         — per-project mutex (sync.Map), acquireLock, releaseLock
  agent_test.go     — happy path + tool error + max-turns + timeout + concurrency
  run_test.go       — drain-helper tests; tool dispatch wiring; tx detection
internal/testutil/
  stub_client.go    — StubClient implementing llm.Client
  stub_client_test.go — sanity checks on the stub itself
```

`tools_builtin.go` lives in W7, not here. W6 ships an empty agent that runs against a *no-op* tool registry; W7 fills it with `read_file` etc.

### Public API delivered

Locked by API.md L372–L461; reproduced verbatim.

```go
package agent

import (
    "context"
    "log/slog"
    "time"

    "github.com/google/uuid"
    "github.com/mxcd/aikido/llm"
    "github.com/mxcd/aikido/notes"
    "github.com/mxcd/aikido/tools"
    "github.com/mxcd/aikido/vfs"
)

type Options struct {
    Client       llm.Client
    Storage      vfs.Storage
    Tools        *tools.Registry
    Notes        *notes.Notebook

    Model        string
    SystemPrompt string

    MaxTurns    int
    TurnTimeout time.Duration
    MaxTokens   int
    Temperature float32

    Logger *slog.Logger
}

type Agent struct{ /* opts + locks */ }

func NewAgent(opts *Options) (*Agent, error)

type EventKind int

const (
    EventText EventKind = iota
    EventToolCall
    EventToolResult
    EventUsage
    EventError
    EventEnd
)

type ToolResult struct {
    CallID  string
    Name    string
    OK      bool
    Content any
    Error   string
}

type Event struct {
    Kind       EventKind
    Text       string
    ToolCall   *llm.ToolCall
    ToolResult *ToolResult
    Usage      *llm.Usage
    Err        error
    EndReason  string
}

func (a *Agent) Run(ctx context.Context, projectID uuid.UUID, userText string) (<-chan Event, error)
func (a *Agent) RunWithMessages(ctx context.Context, projectID uuid.UUID, history []llm.Message) (<-chan Event, error)
```

`VFSToolOptions` and `RegisterVFSTools` are in W7 — they sit in `agent/tools_register.go` and `agent/tools_builtin.go`.

### Internal types (unexported)

```go
// agent/agent.go
type Agent struct {
    opts  *Options
    locks *sync.Map // map[uuid.UUID]*sync.Mutex
}

// agent/safety.go — no exported types; just helpers on *Agent.

// agent/run.go
// drainResult bundles what drain() returns to the loop body so callers don't
// need 4 named returns.
type drainResult struct {
    text       string
    calls      []llm.ToolCall
    usage      *llm.Usage
    midError   error // if non-nil, the LLM stream errored mid-flight
}
```

### Errors introduced

W6 does not introduce package-level error sentinels. Setup errors from `NewAgent` and `Run` are plain `fmt.Errorf("agent: …")` strings; runtime errors flow through the Event channel as `EventError` + `EventEnd("error")`, never as a returned error.

The one nuance: `NewAgent(nil)` and `NewAgent(opts with nil Client)` etc. return `error` per the constructor contract (REFERENCE-CODE.md §12). The wording mirrors `go-basicauth/handler.go`'s `errors.New("storage implementation is required")` style: short, lowercase, no period.

### Implementation order within the wave

1. **`agent/event.go`** — types and constants. Trivial; lock the `EndReason` strings as exported constants too: `EndReasonStop`, `EndReasonMaxTurns`, `EndReasonTimeout`, `EndReasonError`. Even though `EndReason` is `string`, exported constants prevent typos at the emit sites.
2. **`agent/agent.go`** — `Options`, `Agent`, `NewAgent`. Validation + defaults follows REFERENCE-CODE.md §12 verbatim:
   ```go
   func NewAgent(opts *Options) (*Agent, error) {
       if opts == nil { return nil, fmt.Errorf("agent: options required") }
       if opts.Client == nil { return nil, fmt.Errorf("agent: Client required") }
       if opts.Storage == nil { return nil, fmt.Errorf("agent: Storage required") }
       if opts.Model == "" { return nil, fmt.Errorf("agent: Model required") }
       if opts.MaxTurns == 0 { opts.MaxTurns = 20 }
       if opts.TurnTimeout == 0 { opts.TurnTimeout = 120 * time.Second }
       if opts.MaxTokens == 0 { opts.MaxTokens = 8192 }
       if opts.Tools == nil { opts.Tools = tools.NewRegistry() }
       if opts.Logger == nil { opts.Logger = slog.Default() }
       return &Agent{opts: opts, locks: &sync.Map{}}, nil
   }
   ```
3. **`agent/safety.go`** — `acquireLock(projectID) func()` returning a release closure. Implementation:
   ```go
   func (a *Agent) acquireLock(projectID uuid.UUID) func() {
       v, _ := a.locks.LoadOrStore(projectID, &sync.Mutex{})
       mu := v.(*sync.Mutex)
       mu.Lock()
       return mu.Unlock
   }
   ```
   The `LoadOrStore` is the race-safe init pattern. If two goroutines call it simultaneously for an unseen `projectID`, exactly one's mutex pointer wins; the loser's freshly-allocated `&sync.Mutex{}` is GC'd unused. The returned closure is the unlock function — caller defers it.
4. **`internal/testutil/stub_client.go`** — `StubClient`. Built before `runLoop` so `runLoop` can be tested as it is written:
   ```go
   package testutil

   import (
       "context"
       "errors"
       "sync/atomic"

       "github.com/mxcd/aikido/llm"
   )

   // StubClient is a scriptable llm.Client. Tests construct one with a
   // sequence of per-turn event scripts. Each Stream() call replays the
   // next script and increments the turn counter.
   type StubClient struct {
       turnIndex atomic.Int32
       scripts   [][]llm.Event
   }

   func NewStubClient(scripts ...[]llm.Event) *StubClient {
       return &StubClient{scripts: scripts}
   }

   func (s *StubClient) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
       i := s.turnIndex.Add(1) - 1
       if int(i) >= len(s.scripts) {
           return nil, errors.New("stubclient: no script for turn")
       }
       script := s.scripts[i]
       out := make(chan llm.Event, len(script)+1)
       go func() {
           defer close(out)
           for _, ev := range script {
               select {
               case <-ctx.Done():
                   return
               case out <- ev:
               }
           }
       }()
       return out, nil
   }

   var _ llm.Client = (*StubClient)(nil)
   ```
   Each script ends with an `EventUsage` (optional) and either an implicit close (no `EventEnd` from the LLM — the agent emits its own end events) or, for error tests, an `EventError`. This is the engine that powers W6 + W7 + W8 tests; getting it right here saves three waves' worth of pain.
5. **`agent/run.go`** — first the `drain` helper, then `runLoop`, then the public `Run`/`RunWithMessages`. The order matters for testability: `drain` is purely event-translating and can be unit-tested without an Agent.
6. **`agent_test.go` and `run_test.go`** — the matrix below.

### The `runLoop` reproduction

This is the critical surface. The pseudocode in ARCHITECTURE.md lines 69–110 maps to actual Go nearly 1:1; every step is annotated with the algorithm's step number for reviewer cross-check. (Mirrors `asolabs/hub/sandbox/agent-test/agent/loop.go` `Run` (L226–L267), generalized to streaming + tx detection per REFERENCE-CODE.md §1.)

```go
// agent/run.go
func (a *Agent) Run(ctx context.Context, projectID uuid.UUID, userText string) (<-chan Event, error) {
    history := []llm.Message{
        {Role: llm.RoleSystem, Content: a.opts.SystemPrompt},
        {Role: llm.RoleUser,   Content: userText},
    }
    return a.RunWithMessages(ctx, projectID, history)
}

func (a *Agent) RunWithMessages(ctx context.Context, projectID uuid.UUID, history []llm.Message) (<-chan Event, error) {
    if projectID == uuid.Nil {
        return nil, fmt.Errorf("agent: projectID required")
    }
    out := make(chan Event, 16)

    // ARCHITECTURE.md step 1: per-project mutex.
    release := a.acquireLock(projectID)

    // ARCHITECTURE.md step 2: per-call context with TurnTimeout.
    runCtx, cancel := context.WithTimeout(ctx, a.opts.TurnTimeout)

    // history may or may not start with a system prompt depending on whether
    // it came from Run or directly from RunWithMessages. RunWithMessages does
    // not auto-prepend; if the caller wants a system prompt they include it.
    msgs := append([]llm.Message{}, history...)

    go func() {
        defer close(out)
        defer cancel()
        defer release()
        a.runLoop(runCtx, projectID, msgs, out)
    }()
    return out, nil
}

func (a *Agent) runLoop(ctx context.Context, projectID uuid.UUID, msgs []llm.Message, out chan<- Event) {
    // ARCHITECTURE.md step 3: TxStorage capability detection.
    txStorage, hasTx := a.opts.Storage.(vfs.TxStorage)

    // ARCHITECTURE.md step 4: turn loop.
    for turn := 0; turn < a.opts.MaxTurns; turn++ {
        // 4a: per-turn UUID for env + snapshot keying.
        turnID := uuid.New()

        // 4b: build the request.
        req := llm.Request{
            Model:          a.opts.Model,
            Messages:       msgs,
            Tools:          a.opts.Tools.Defs(),
            MaxTokens:      a.opts.MaxTokens,
            Temperature:    a.opts.Temperature,
        }

        // 4c: stream-start.
        events, err := a.opts.Client.Stream(ctx, req)
        if err != nil {
            // Stream-start errors are unrecoverable for this turn (provider already
            // applied 429/5xx retries; see ADR retry section).
            emit(out, Event{Kind: EventError, Err: err})
            emit(out, Event{Kind: EventEnd, EndReason: EndReasonError})
            return
        }

        // 4d: drain. Forwards TextDelta verbatim, accumulates tool calls
        // and usage, and watches for EventError mid-stream.
        dr := drain(ctx, events, out)
        if dr.midError != nil {
            emit(out, Event{Kind: EventEnd, EndReason: EndReasonError})
            return
        }

        // 4e: append assistant message to history.
        msgs = append(msgs, assistantMessageFromTurn(dr.text, dr.calls))

        if dr.usage != nil {
            emit(out, Event{Kind: EventUsage, Usage: dr.usage})
        }

        // ARCHITECTURE.md step 5: termination on no-tool-calls.
        if len(dr.calls) == 0 {
            emit(out, Event{Kind: EventEnd, EndReason: EndReasonStop})
            return
        }

        // ARCHITECTURE.md step 6: dispatch tool calls (optionally inside a tx).
        env := tools.Env{
            Storage: a.opts.Storage,
            Project: projectID,
            TurnID:  turnID,
            Logger:  a.opts.Logger,
            Now:     time.Now,
        }
        var tx vfs.Tx
        if hasTx {
            t, err := txStorage.BeginTx(ctx, projectID)
            if err != nil {
                // Tx unavailable for this turn; fall back to base storage. We
                // emit no event for this — the model never knows the difference.
                a.opts.Logger.Warn("agent: BeginTx failed; running without tx", "err", err)
            } else {
                tx = t
                env.Storage = tx
            }
        }

        anyToolErr := false
        var changedPaths []string
        for _, call := range dr.calls {
            res, err := a.opts.Tools.Dispatch(ctx, call, env)
            tr := buildToolResult(call, res, err)
            emit(out, Event{Kind: EventToolCall,   ToolCall:   &call})
            emit(out, Event{Kind: EventToolResult, ToolResult: tr})
            msgs = append(msgs, toolMessageFromResult(call.ID, tr))
            if !tr.OK { anyToolErr = true }
            changedPaths = append(changedPaths, res.ChangedPaths...)
        }

        // ARCHITECTURE.md step 7: commit-or-rollback.
        if tx != nil {
            commitOrRollback(ctx, a.opts.Storage, projectID, turnID, tx, anyToolErr, changedPaths, a.opts.Logger)
        }

        // Loop iterates: model gets the tool results; produces the next assistant
        // turn (which may be a final answer or further tool calls).
    }

    // ARCHITECTURE.md step 8: max turns exhausted.
    emit(out, Event{Kind: EventEnd, EndReason: EndReasonMaxTurns})
}
```

Helper functions (the 5–7 internal helpers required by the prompt):

```go
// agent/run.go (helpers)

func emit(out chan<- Event, ev Event) {
    // Best-effort send; a slow consumer is the consumer's problem to handle
    // (per ARCHITECTURE.md "buffered channel (cap 16) blocks the producer").
    out <- ev
}

func drain(ctx context.Context, events <-chan llm.Event, out chan<- Event) drainResult {
    var r drainResult
    var b strings.Builder
    for ev := range events {
        switch ev.Kind {
        case llm.EventTextDelta:
            b.WriteString(ev.Text)
            emit(out, Event{Kind: EventText, Text: ev.Text})
        case llm.EventToolCall:
            r.calls = append(r.calls, *ev.Tool)
            // Tool call is held back from out; the agent emits EventToolCall
            // *after* dispatch so the order is "call → result" on the channel.
            // (Alternative: emit on receipt and trust the consumer — but
            // simpler debugging if call+result are adjacent.)
        case llm.EventUsage:
            r.usage = ev.Usage
        case llm.EventError:
            // Provider mid-stream error.  Surface to caller as agent.EventError;
            // the loop will emit EventEnd("error") after observing midError.
            emit(out, Event{Kind: EventError, Err: ev.Err})
            r.midError = ev.Err
        case llm.EventEnd:
            // llm provider ends the stream; nothing for the agent to do — the
            // outer for-range will close on channel close.
        }
    }
    r.text = b.String()
    return r
}

func buildToolResult(call llm.ToolCall, res tools.Result, err error) *ToolResult {
    if err != nil {
        return &ToolResult{
            CallID: call.ID,
            Name:   call.Name,
            OK:     false,
            Error:  err.Error(),
        }
    }
    return &ToolResult{
        CallID:  call.ID,
        Name:    call.Name,
        OK:      true,
        Content: res.Content,
    }
}

func assistantMessageFromTurn(text string, calls []llm.ToolCall) llm.Message {
    return llm.Message{
        Role:      llm.RoleAssistant,
        Content:   text,       // may be empty when len(calls) > 0
        ToolCalls: calls,
    }
}

func toolMessageFromResult(callID string, tr *ToolResult) llm.Message {
    body, _ := json.Marshal(tr.Content) // "" Content marshals to `null`; fine
    if !tr.OK {
        body = []byte(tr.Error) // tool errors go raw so the model reads English
    }
    return llm.Message{
        Role:       llm.RoleTool,
        ToolCallID: callID,
        Content:    string(body),
    }
}

func commitOrRollback(
    ctx context.Context,
    base vfs.Storage,
    projectID, turnID uuid.UUID,
    tx vfs.Tx,
    anyErr bool,
    changedPaths []string,
    logger *slog.Logger,
) {
    if anyErr {
        if rerr := tx.Rollback(); rerr != nil {
            logger.Warn("agent: tx rollback failed", "err", rerr)
        }
        return
    }
    // Snapshot before commit so post-commit Restore(snapshotID) is meaningful.
    if _, serr := base.Snapshot(ctx, projectID, turnID, changedPaths); serr != nil {
        logger.Warn("agent: snapshot failed", "err", serr)
        // commit anyway — snapshot is observability, not correctness.
    }
    if cerr := tx.Commit(); cerr != nil {
        logger.Warn("agent: tx commit failed", "err", cerr)
    }
}
```

`acquireLock` and `releaseLock` are in `safety.go` and are the per-project mutex pair.

That's seven internal helpers total: `emit`, `drain`, `buildToolResult`, `assistantMessageFromTurn`, `toolMessageFromResult`, `commitOrRollback`, plus `acquireLock` (and its returned release closure). The user prompt called for 5–7; this lands at 7 with acquireLock counted as one helper that produces a closure.

### EndReason emission rules

Exactly one `EventEnd` is emitted per `Run` call, always last, always before the channel closes. Reasons map to Go-side conditions:

| `EndReason` | Emitted when | Code site |
|---|---|---|
| `"stop"` | LLM stream completes for the turn and the assistant message has zero tool calls (`len(dr.calls) == 0` in step 5). | After `drain` returns successfully; `dr.calls` is empty. |
| `"max_turns"` | The for-loop completes `opts.MaxTurns` iterations without a stop. | After the for-loop exits naturally. |
| `"timeout"` | `runCtx.Err() == context.DeadlineExceeded` because `opts.TurnTimeout` elapsed, propagated through `ctx` and observed by either `Stream` or any tool. | Detected by checking `errors.Is(ctx.Err(), context.DeadlineExceeded)` in the EventError emit branch — if the err is a deadline, emit `EndReason="timeout"` instead of `"error"`. |
| `"error"` | Stream-start error, mid-stream error from the provider, or any other unrecoverable provider error. NOT tool errors — those continue the loop per SECURITY.md "Tool error containment". | After emitting `EventError`. |

The timeout branch needs a small post-check to differentiate from generic errors:

```go
endReason := EndReasonError
if errors.Is(ctx.Err(), context.DeadlineExceeded) {
    endReason = EndReasonTimeout
}
emit(out, Event{Kind: EventEnd, EndReason: endReason})
```

Apply this discrimination at every `EventEnd("error")` emit site in the loop body. The cleanest implementation is a tiny helper:

```go
func endReasonForCtx(ctx context.Context) string {
    if errors.Is(ctx.Err(), context.DeadlineExceeded) {
        return EndReasonTimeout
    }
    return EndReasonError
}
```

### Per-project mutex implementation detail

The pattern is `sync.Map[uuid.UUID]*sync.Mutex` — but `sync.Map` is not generic, so the value is `any`:

```go
type Agent struct {
    opts  *Options
    locks *sync.Map // logical type: map[uuid.UUID]*sync.Mutex
}

func (a *Agent) acquireLock(projectID uuid.UUID) func() {
    v, _ := a.locks.LoadOrStore(projectID, &sync.Mutex{})
    mu := v.(*sync.Mutex)
    mu.Lock()
    return mu.Unlock
}
```

`LoadOrStore` is the entire correctness guarantee. Two concurrent `Run` calls for the same unseen projectID both call `LoadOrStore(id, &sync.Mutex{})`; exactly one's pointer is stored, the other's is discarded; both get back the same pointer; both then call `Lock()` on the same mutex; one blocks. (Per ADR-009 + REFERENCE-CODE.md §1.)

Mutex pointers are never removed from `sync.Map`. v1 deployments are single-instance; even at 10k projects per process, the cost is ~16 KB of mutex pointers. Acceptable. v3 swaps this for `agent.Locker` interface and Redis backing.

### TxStorage detection

One line, exactly one place:

```go
txStorage, hasTx := a.opts.Storage.(vfs.TxStorage)
```

at the top of `runLoop`. The assertion happens once per `Run` call, not once per turn — `Storage` is fixed for the agent's lifetime, so re-asserting per turn is wasted work. `txStorage` is captured by the closure; per turn the loop only checks `hasTx` and calls `txStorage.BeginTx(...)` if true.

If `BeginTx` fails for a particular turn (rare — bad projectID), the agent logs and falls back to `base`. The model is unaware. This is the "best-effort semantics" stance from ADR-008 carried forward.

### Stateless agent + retry stance

Per ADR-011 (stateless): `Run` builds history from `userText`; `RunWithMessages` takes history from the caller. Neither persists. The `Agent` struct holds `opts` and `locks` only — no mutable state per project, no message buffers. Two agents over the same storage backend can coexist; coordination is purely through the per-project mutex.

Per ADR-006 (retry): the agent never retries mid-stream. The provider client (`llm/openrouter`) handles 429 + 5xx retry at stream-start via `retry.Do` (W3). Once `Stream` returns events, the agent forwards them and tolerates mid-stream errors as `EventError + EventEnd("error")`. There is no agent-side retry loop, no `for { try again }` shape, no exponential backoff. This is intentional: streaming retries replay tokens and re-bill the user.

If a future caller wants whole-turn retry (call `Run` again on `EndReason="error"`), they can build it themselves on top of the public API — but the library does not assume the LLM call is idempotent.

### Stub client design

The user prompt calls for explicit `StubClient` design. Reproducing the engineering shape here in detail because three waves depend on getting this right.

```go
// internal/testutil/stub_client.go

// StubClient is a scriptable llm.Client.
//
// Usage:
//
//   stub := testutil.NewStubClient(
//       // Turn 1: model emits text + a tool call, then ends.
//       []llm.Event{
//           {Kind: llm.EventTextDelta, Text: "let me check"},
//           {Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: "1", Name: "read_file",
//               Arguments: `{"path":"notes.md"}`}},
//           {Kind: llm.EventUsage, Usage: &llm.Usage{PromptTokens: 100, CompletionTokens: 20}},
//           {Kind: llm.EventEnd},
//       },
//       // Turn 2: model emits final text and ends; agent sees no tool calls and stops.
//       []llm.Event{
//           {Kind: llm.EventTextDelta, Text: "the file contains: ..."},
//           {Kind: llm.EventUsage, Usage: &llm.Usage{PromptTokens: 200, CompletionTokens: 30}},
//           {Kind: llm.EventEnd},
//       },
//   )
//   agent.Run(ctx, projectID, "what's in notes.md?")
type StubClient struct {
    turnIndex atomic.Int32
    scripts   [][]llm.Event
}

func NewStubClient(scripts ...[]llm.Event) *StubClient {
    return &StubClient{scripts: scripts}
}

func (s *StubClient) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
    i := s.turnIndex.Add(1) - 1
    if int(i) >= len(s.scripts) {
        return nil, fmt.Errorf("stubclient: no script for turn %d (have %d turns)", i, len(s.scripts))
    }
    script := s.scripts[i]
    out := make(chan llm.Event, len(script)+1)
    go func() {
        defer close(out)
        for _, ev := range script {
            select {
            case <-ctx.Done():
                return
            case out <- ev:
            }
        }
    }()
    return out, nil
}

var _ llm.Client = (*StubClient)(nil)
```

Two extra knobs the test suites need:

```go
// PushScript appends a turn script after construction. Useful when a test
// wants to script turn N+1 based on what happened in turn N.
func (s *StubClient) PushScript(events []llm.Event) {
    s.scripts = append(s.scripts, events)
}

// BlockingScript replaces the next script with one that blocks forever
// (until ctx cancellation). Used to test TurnTimeout.
func (s *StubClient) BlockingScript() {
    blockCh := make(chan struct{})
    s.scripts = append(s.scripts, []llm.Event{
        // signaled by a custom Kind that blocks; or use a separate path
        // through a different stub if this overcomplicates the design.
    })
    _ = blockCh
}
```

`BlockingScript` is fiddly because the regular for-loop drains the script linearly. Cleanest implementation: have a separate `BlockingClient` type that satisfies `llm.Client` and never sends on the out channel until ctx cancels. Two types instead of one is fine; tests pick the one that fits.

### Test scenarios

| Test name | Setup | Assertion |
|---|---|---|
| `Run_HappyPath_NoTools` | StubClient with 1 turn, text only, no tool calls. Empty registry. | Channel order: `[EventText, EventUsage?, EventEnd("stop")]`. Channel closes after `EventEnd`. `RunWithMessages` history length increments by exactly the assistant message. |
| `Run_HappyPath_OneToolThenStop` | StubClient with 2 scripts: turn 1 = tool call + end; turn 2 = text + end. Registry has one tool that returns `Result{Content: "ok"}`. | Channel order: `[EventText (if any), EventToolCall, EventToolResult{OK:true}, EventUsage?, EventText, EventUsage?, EventEnd("stop")]`. The history at exit contains 5 messages (system, user, assistant-with-toolcall, tool-result, assistant-final). |
| `Run_ToolError_Continues` | Stub: turn 1 tool call, turn 2 text + end. Registry handler returns `errors.New("nope")`. | EventToolResult.OK is false; Error is `"nope"`. Loop does NOT abort; turn 2 runs; final EndReason is `"stop"`. |
| `Run_MidStreamError_EndsWithError` | Stub script ends with `EventError`. | Sequence ends in `[EventError, EventEnd("error")]`; channel closes; no further turns. |
| `Run_StreamStartError` | StubClient that returns `(nil, error)` from Stream(). | Sequence is `[EventError, EventEnd("error")]`. The error event carries the original error wrapped through `errors.Is`. |
| `Run_MaxTurns` | Stub script that always emits a tool call (no end-without-tool). MaxTurns=3. | After 3 turns, agent emits `EventEnd("max_turns")`. Channel closes. The 4th turn never starts (StubClient.turnIndex confirms). |
| `Run_Timeout` | Use `BlockingClient` whose Stream blocks until ctx done. `TurnTimeout = 50ms`. | Caller's `<-channel` receives `EventEnd("timeout")` within ~100ms. `runCtx.Err() == context.DeadlineExceeded`. |
| `Run_Concurrency_SameProject_Serializes` | Two `Run` calls on same projectID. Each script: 1 turn, ~50ms blocking. | Total wall-clock ≥ 100ms (i.e., they didn't run in parallel). Use timestamps captured inside the stub. |
| `Run_Concurrency_DifferentProjects_Parallel` | Two `Run` calls on different projectIDs. Each script blocks 50ms. | Total wall-clock ≤ 80ms (ran in parallel). |
| `Run_TxStorage_CommitsOnSuccess` | Storage is `vfs/memory` (TxStorage). Tool writes a file. | Post-Run, `Storage.ReadFile` returns the written content (commit happened). Snapshot was created with the right `changedPaths`. |
| `Run_TxStorage_RollsBackOnToolError` | Same storage. Tool writes a file then a second tool errors. | Post-Run, the file is NOT present (rolled back). |
| `Run_NonTxStorage_BestEffort` | Storage that satisfies `Storage` only, NOT `TxStorage`. Tool writes a file then tool errors. | Post-Run, the file IS present (no rollback because no tx). Test passes; this confirms the capability detection branch is exercised. |
| `Run_NilProjectID_Errors` | `Run(ctx, uuid.Nil, "x")`. | Returns `(nil, error)` synchronously; no channel created. |
| `Run_RunWithMessages_RespectsHistory` | History supplied with prior assistant + tool messages already. Stub script: text + end. | The first request to the stub has the full history including the supplied prior turns. |
| `NewAgent_ValidatesOptions` | NewAgent(nil), NewAgent(opts with nil Client/Storage/empty Model). | Each returns `(nil, error)` with a useful message. |
| `Drain_TextOnly` | unit test of `drain` directly. Channel: text-only. | Returns text concatenated correctly; no calls; no usage. |
| `Drain_AssemblesCalls` | drain channel: text + 2 tool calls + usage. | `r.calls` has 2 entries in order; `r.text` is the text; `r.usage` is non-nil. |

Total: ~18 tests, ~700 LOC including helpers.

### Wave dependencies

- **Consumes from:** W1 (`llm.Client`, `llm.Message`, `llm.Event`, `llm.Request`, `llm.Usage`), W4 (`tools.Registry`, `tools.Env`, `tools.Result`, `tools.Handler`), W5 (`vfs.Storage`, `vfs.TxStorage`, `vfs.Tx`).
- **Provides to:** W7 (`agent.Run` calls into `tools.Registry` which W7 populates with built-ins). W8 (`notes.Notebook` is referenced from `agent.Options.Notes`, though the agent does nothing with it directly — it's a convenience hook for callers who want to register note tools through the same Options struct). The `examples/agent-vfs` example W7 builds depends on W6 working.

### Risks (concrete mitigations)

- **Risk:** Race between `LoadOrStore` and a freshly-created mutex if two goroutines hit an unseen projectID simultaneously. **Mitigation:** `LoadOrStore` is documented atomic — exactly one stored value wins, both goroutines get the same pointer. Already addressed by the API choice. Add a regression test that deliberately races 50 goroutines on the same unseen projectID and asserts they all serialize correctly.
- **Risk:** `drain` deadlocks if the provider's event channel is buffered and the producer fills it, while `out` is full and the consumer is slow. **Mitigation:** the agent's `out` is buffered cap-16; the provider's channel buffering is the provider's design. Both have producers that block on full — this is the expected backpressure contract per ARCHITECTURE.md "Streaming model". Document and accept.
- **Risk:** TxStorage detection misses a Storage that *should* satisfy it but does not because of a Go embedding mistake. **Mitigation:** the `vfs/memory` package has `var _ vfs.TxStorage = (*Storage)(nil)` at top of file; if the embedding ever breaks, compilation fails. Same belt-and-suspenders pattern as REFERENCE-CODE.md §10.
- **Risk:** Tool dispatch order is the order tool calls came back from the model — but parallel tool calls (per OpenRouter default) may have implicit ordering preferences. **Mitigation:** v1 dispatches sequentially in the order received. Sequential dispatch is correct under tx (the second tool sees the first's writes). v2 may add parallel dispatch as a `tools.Registry` capability flag.
- **Risk:** Provider returns malformed JSON for tool arguments — the agent doesn't validate, just forwards to `Dispatch`. **Mitigation:** `tools.Registry.Dispatch` is responsible for `json.Unmarshal` errors per W4; the resulting error becomes a `ToolResult{OK:false}` which the model reads. The loop continues. Already covered by SECURITY.md "Tool error containment".
- **Risk:** The agent's per-turn ctx (with TurnTimeout) is the same ctx the LLM Stream sees — if the timeout fires mid-stream, the provider's goroutine sees ctx.Done and the channel closes, which `drain` then reads as a clean close, missing the timeout signal. **Mitigation:** check `ctx.Err()` after `drain` returns; if `errors.Is(ctx.Err(), context.DeadlineExceeded)`, emit `EventEnd("timeout")` regardless of `dr.midError`.

### Exit criteria

- [ ] `go test ./agent/... -race` passes.
- [ ] All 18 test scenarios green.
- [ ] `examples/agent-vfs/main.go` builds — but at this point uses an empty `tools.Registry`, so the agent runs but cannot do anything useful. (W7 fills in the registry.)
- [ ] `runLoop` is under 200 LOC including comments. The total `agent` package is under 500 LOC.
- [ ] `EndReason` constants are exported as `EndReasonStop`, `EndReasonMaxTurns`, `EndReasonTimeout`, `EndReasonError` — typed-string-like usage even though the field is plain `string`.
- [ ] `internal/testutil/StubClient` is exported (in the unexported sense — it's in `internal/`, but accessible from any package inside aikido).

---

## W7 — VFS-aware built-in tools

**Estimated LOC:** ~400 go + ~500 test | **Estimated effort:** 8–10 hours focused | **Status:** not started

### File tree

```
agent/
  tools_builtin.go         — handler functions for the five built-in tools
  tools_register.go        — RegisterVFSTools(reg, opts), VFSToolOptions
  tools_builtin_test.go    — per-tool happy + error paths, integrated through stub agent
  tools_register_test.go   — registration round-trip; option propagation
  testdata/
    tools_descriptions.md  — long-form tool descriptions copied here for tweaking
                             without recompiling — read by Description fields
                             OR inlined as Go strings; see "open question" below
```

### Public API delivered

Locked by API.md L452–L461.

```go
type VFSToolOptions struct {
    HideHiddenPaths   bool
    AllowedExtensions []string
    MaxFileBytes      int64
}

func RegisterVFSTools(reg *tools.Registry, opts VFSToolOptions) error
```

That is the entire W7 public surface. Five tool *names* and *parameters* are observable to the model but not part of the Go public surface — they are JSON Schemas.

### Internal types (unexported)

```go
// agent/tools_builtin.go

// Each handler is constructed via a "make" function that closes over opts so
// the per-call validation can see the user's caps and whitelist without a
// global. This mirrors REFERENCE-CODE.md §7's "makeXHandler(opts)" snippet.

type readFileArgs struct {
    Path string `json:"path"`
}
type writeFileArgs struct {
    Path        string `json:"path"`
    Content     string `json:"content"`
    ContentType string `json:"content_type,omitempty"`
}
type listFilesArgs struct {
    // empty in v1; may grow with prefix filter etc. in v2
}
type deleteFileArgs struct {
    Path string `json:"path"`
}
type searchFilesArgs struct {
    Query string `json:"query"`
    Glob  string `json:"glob,omitempty"`
}

// Per-tool result structs. These are structured so the agent's serialization
// produces predictable JSON for the model to read.

type readFileResult struct {
    Content     string `json:"content"`
    ContentType string `json:"content_type"`
    Size        int64  `json:"size"`
}
type writeFileResult struct {
    Path string `json:"path"`
    Size int64  `json:"size"`
}
type listFilesResult struct {
    Files []listedFile `json:"files"`
}
type listedFile struct {
    Path      string    `json:"path"`
    Size      int64     `json:"size"`
    UpdatedAt time.Time `json:"updated_at"`
}
type deleteFileResult struct {
    Path string `json:"path"`
}
type searchFilesResult struct {
    Matches   []searchMatch `json:"matches"`
    Truncated bool          `json:"truncated,omitempty"`
}
type searchMatch struct {
    Path    string `json:"path"`
    Line    int    `json:"line"`
    Snippet string `json:"snippet"`
}
```

### Errors introduced

W7 does not introduce new error sentinels. Errors flow through:

- `vfs.ErrPathInvalid` / `vfs.ErrFileNotFound` from the storage layer (per W5).
- `vfs.ErrFileTooLarge` from per-tool size enforcement.
- `tools.ErrUnknownTool` / `tools.ErrDuplicateTool` from registry layer (per W4).
- Wrapped errors with package prefix: `fmt.Errorf("write_file: %w", err)`.

Per SECURITY.md "Tool error containment" and Q3 in REFERENCE-CODE.md, every handler error becomes a `ToolResult{OK:false, Error: err.Error()}` that the model reads as English text. The handlers themselves return `(tools.Result{}, error)` to signal this — they never panic, never return success-shaped Results with embedded errors.

### Per-tool sub-tables

#### `read_file`

**JSON Schema (explicit literal using W4 helpers):**

```go
tools.Object(map[string]any{
    "path": tools.String("Relative file path within the project. No '..' segments, no absolute paths."),
}, "path")
```

**Handler signature:**

```go
func makeReadFileHandler(opts VFSToolOptions) tools.Handler {
    return func(ctx context.Context, args json.RawMessage, env tools.Env) (tools.Result, error) {
        var p readFileArgs
        if err := json.Unmarshal(args, &p); err != nil {
            return tools.Result{}, fmt.Errorf("read_file: invalid args: %w", err)
        }
        if err := vfs.ValidatePath(p.Path); err != nil {
            return tools.Result{}, fmt.Errorf("read_file: %w", err)
        }
        // Storage-side size guard: tools refuse to load huge files even for read.
        // The tool checks the recorded size before reading; if the storage backend
        // does not expose size cheaply, ReadFile is called and the resulting size
        // is checked. v1 uses ReadFile + post-check.
        content, meta, err := env.Storage.ReadFile(ctx, env.Project, p.Path)
        if err != nil {
            return tools.Result{}, fmt.Errorf("read_file: %w", err)
        }
        if opts.MaxFileBytes > 0 && meta.Size > opts.MaxFileBytes {
            return tools.Result{}, fmt.Errorf("read_file: %w (size=%d, cap=%d)",
                vfs.ErrFileTooLarge, meta.Size, opts.MaxFileBytes)
        }
        return tools.Result{
            Content: readFileResult{
                Content:     string(content),
                ContentType: meta.ContentType,
                Size:        meta.Size,
            },
            Display: fmt.Sprintf("read %d bytes from %s", meta.Size, p.Path),
        }, nil
    }
}
```

**Error cases:**
- `path == ""` → `vfs.ErrPathInvalid` wrapped → `Result{OK:false, Error:"read_file: vfs: invalid path"}` for the model.
- `path` traversal → same.
- File not found → `vfs.ErrFileNotFound` wrapped → graceful error result; model reads `"read_file: vfs: file not found"` and decides to ask the user or call `list_files`.
- File over `MaxFileBytes` → `ErrFileTooLarge` wrapped with size + cap; model reads error text and chooses next action.

**Hidden-path filter:** does NOT apply — `read_file` is not a discovery tool; the model can read any path it knows.

**Extension whitelist:** does NOT apply — `AllowedExtensions` controls writes/deletes only.

**File-size cap:** YES — enforced on `meta.Size`. The storage's recorded size is the source of truth (tools refuse to load huge files; the read happens because the storage backend does the load anyway, but the result is rejected before being marshalled to the model).

#### `write_file`

**JSON Schema:**

```go
tools.Object(map[string]any{
    "path":         tools.String("Relative file path within the project. No '..' segments, no absolute paths."),
    "content":      tools.String("The file content as a string. UTF-8 encoded."),
    "content_type": tools.String("Optional MIME type. Default: text/markdown for .md, text/plain otherwise."),
}, "path", "content")
```

**Handler:**

```go
func makeWriteFileHandler(opts VFSToolOptions) tools.Handler {
    return func(ctx context.Context, args json.RawMessage, env tools.Env) (tools.Result, error) {
        var p writeFileArgs
        if err := json.Unmarshal(args, &p); err != nil {
            return tools.Result{}, fmt.Errorf("write_file: invalid args: %w", err)
        }
        if err := vfs.ValidatePath(p.Path); err != nil {
            return tools.Result{}, fmt.Errorf("write_file: %w", err)
        }
        if !extensionAllowed(p.Path, opts.AllowedExtensions) {
            return tools.Result{}, fmt.Errorf(
                "write_file: extension not allowed (allowed=%v)", opts.AllowedExtensions)
        }
        size := int64(len(p.Content))
        if opts.MaxFileBytes > 0 && size > opts.MaxFileBytes {
            return tools.Result{}, fmt.Errorf("write_file: %w (size=%d, cap=%d)",
                vfs.ErrFileTooLarge, size, opts.MaxFileBytes)
        }
        ct := p.ContentType
        if ct == "" {
            ct = inferContentType(p.Path)
        }
        if err := env.Storage.WriteFile(ctx, env.Project, p.Path, []byte(p.Content), ct); err != nil {
            return tools.Result{}, fmt.Errorf("write_file: %w", err)
        }
        return tools.Result{
            Content:      writeFileResult{Path: p.Path, Size: size},
            Display:      fmt.Sprintf("wrote %d bytes to %s", size, p.Path),
            ChangedPaths: []string{p.Path},
        }, nil
    }
}
```

**Error cases:** invalid path, disallowed extension, oversize, storage error. All map to `OK:false`.

**Hidden-path filter:** does NOT apply — model can deliberately write to `_*` paths if it constructs them.

**Extension whitelist:** YES — when `opts.AllowedExtensions != nil`, the path's extension (lowercased) must be in the list. nil list = allow all (default).

**File-size cap:** YES — enforced on `len(content)` BEFORE storage write. Cheaper than after.

`ChangedPaths` is set to `[p.Path]` so the agent's `commitOrRollback` can include it in `Snapshot.changedPaths`.

#### `list_files`

**JSON Schema:**

```go
tools.Object(map[string]any{}, /* required */)
// No required fields. The empty object {} schema is valid; model passes {}.
```

**Handler:**

```go
func makeListFilesHandler(opts VFSToolOptions) tools.Handler {
    return func(ctx context.Context, args json.RawMessage, env tools.Env) (tools.Result, error) {
        metas, err := env.Storage.ListFiles(ctx, env.Project)
        if err != nil {
            return tools.Result{}, fmt.Errorf("list_files: %w", err)
        }
        out := make([]listedFile, 0, len(metas))
        for _, m := range metas {
            if opts.HideHiddenPaths && isHiddenPath(m.Path) {
                continue
            }
            out = append(out, listedFile{
                Path: m.Path, Size: m.Size, UpdatedAt: m.UpdatedAt,
            })
        }
        return tools.Result{
            Content: listFilesResult{Files: out},
            Display: fmt.Sprintf("listed %d files", len(out)),
        }, nil
    }
}

func isHiddenPath(path string) bool {
    if path == "" { return false }
    base := lastSegment(path)
    return strings.HasPrefix(base, "_") || strings.HasPrefix(base, ".")
}
```

**Error cases:** project not found (graceful — model gets the error). Otherwise inert.

**Hidden-path filter:** YES — `_*` and `.*` filtered when `opts.HideHiddenPaths` is true (the default per API.md). Filter is per-segment via `lastSegment`, not just the leading char of the whole path. (E.g., `docs/_internal.md` is hidden; `docs/internal.md` is not.) Per SECURITY.md "Hidden-path convention", this is *cosmetic*, not security — the model can still construct the path literally and write to it.

**Extension whitelist:** does NOT apply.

**File-size cap:** does NOT apply.

#### `delete_file`

**JSON Schema:**

```go
tools.Object(map[string]any{
    "path": tools.String("Relative file path to delete."),
}, "path")
```

**Handler:**

```go
func makeDeleteFileHandler(opts VFSToolOptions) tools.Handler {
    return func(ctx context.Context, args json.RawMessage, env tools.Env) (tools.Result, error) {
        var p deleteFileArgs
        if err := json.Unmarshal(args, &p); err != nil {
            return tools.Result{}, fmt.Errorf("delete_file: invalid args: %w", err)
        }
        if err := vfs.ValidatePath(p.Path); err != nil {
            return tools.Result{}, fmt.Errorf("delete_file: %w", err)
        }
        if !extensionAllowed(p.Path, opts.AllowedExtensions) {
            return tools.Result{}, fmt.Errorf("delete_file: extension not allowed (allowed=%v)",
                opts.AllowedExtensions)
        }
        if err := env.Storage.DeleteFile(ctx, env.Project, p.Path); err != nil {
            return tools.Result{}, fmt.Errorf("delete_file: %w", err)
        }
        return tools.Result{
            Content:      deleteFileResult{Path: p.Path},
            Display:      fmt.Sprintf("deleted %s", p.Path),
            ChangedPaths: []string{p.Path},
        }, nil
    }
}
```

**Error cases:** invalid path, disallowed extension, file not found, storage error. All graceful.

**Hidden-path filter:** does NOT apply (delete works regardless of visibility).

**Extension whitelist:** YES — same enforcement as `write_file`.

**File-size cap:** does NOT apply (delete has no payload).

#### `search_files`

**JSON Schema:**

```go
tools.Object(map[string]any{
    "query": tools.String("Case-insensitive substring to search for in file content. v1 does NOT support regex."),
    "glob":  tools.String("Optional glob pattern restricting which paths are searched. e.g. '*.md', 'docs/*'. Default: search all files."),
}, "query")
```

The description text explicitly tells the model that v1 is substring-only. The model, reading this on every turn, will not waste tokens trying regex.

**Handler:**

```go
func makeSearchFilesHandler(opts VFSToolOptions) tools.Handler {
    return func(ctx context.Context, args json.RawMessage, env tools.Env) (tools.Result, error) {
        var p searchFilesArgs
        if err := json.Unmarshal(args, &p); err != nil {
            return tools.Result{}, fmt.Errorf("search_files: invalid args: %w", err)
        }
        if p.Query == "" {
            return tools.Result{}, fmt.Errorf("search_files: query required")
        }
        metas, err := env.Storage.ListFiles(ctx, env.Project)
        if err != nil {
            return tools.Result{}, fmt.Errorf("search_files: %w", err)
        }

        const cap = 50
        matches := []searchMatch{}
        truncated := false

        q := strings.ToLower(p.Query)
        for _, m := range metas {
            if opts.HideHiddenPaths && isHiddenPath(m.Path) { continue }
            if p.Glob != "" {
                ok, _ := filepath.Match(p.Glob, m.Path)
                if !ok { continue }
            }
            content, _, err := env.Storage.ReadFile(ctx, env.Project, m.Path)
            if err != nil { continue }
            // Skip files larger than MaxFileBytes for search to avoid loading huge blobs.
            if opts.MaxFileBytes > 0 && int64(len(content)) > opts.MaxFileBytes { continue }

            for ln, line := range strings.Split(string(content), "\n") {
                if strings.Contains(strings.ToLower(line), q) {
                    matches = append(matches, searchMatch{
                        Path: m.Path, Line: ln + 1, Snippet: snippetAround(line, p.Query),
                    })
                    if len(matches) >= cap {
                        truncated = true
                        break
                    }
                }
            }
            if truncated { break }
        }
        return tools.Result{
            Content: searchFilesResult{Matches: matches, Truncated: truncated},
            Display: fmt.Sprintf("found %d matches%s", len(matches),
                ifThen(truncated, " (truncated)")),
        }, nil
    }
}
```

**Error cases:** empty query (rejected), storage list error.

**Hidden-path filter:** YES — same as `list_files`.

**Extension whitelist:** does NOT apply (search is read-only at the file level).

**File-size cap:** YES — files larger than `MaxFileBytes` are silently skipped during search (avoids loading multi-MB files into memory just to grep them). The skip is silent because the model has no actionable response — it can't make the file smaller. If the matches are surprisingly empty, the model can fall back to reading specific paths.

**v1 semantics:** case-insensitive substring across file content; optional glob filter restricts the corpus by path. v2 may add regex (with a `regex: true` flag); `regex` is not in the v1 schema. The tool description tells the model exactly this so it doesn't try regex syntax.

### `RegisterVFSTools` shape

Mirrors REFERENCE-CODE.md §7 "Snippet to mirror":

```go
func RegisterVFSTools(reg *tools.Registry, opts VFSToolOptions) error {
    if reg == nil { return errors.New("agent: registry required") }
    if opts.MaxFileBytes == 0 { opts.MaxFileBytes = 1 << 20 } // 1 MiB
    // HideHiddenPaths default true is the field's documented default but Go zero
    // value is false; callers who want it explicitly off can set HideHiddenPaths
    // explicitly. Per API.md L455 the default is true — apply that here.
    // We use a separate "set" flag pattern: we cannot tell zero from explicit-false.
    // Pragmatic v1: callers either accept the default (use zero VFSToolOptions{}, get
    // HideHiddenPaths=true via this defaulting) or pass HideHiddenPaths=true explicitly.
    // If a caller wants HideHiddenPaths=false they must... see "open question" below.

    type spec struct {
        name, desc string
        params     json.RawMessage
        h          tools.Handler
    }
    specs := []spec{
        {
            name: "read_file",
            desc: "Read the contents of a file at the given path. Returns content, content_type, and size. Use this to inspect existing files before deciding whether to modify them.",
            params: tools.Object(map[string]any{
                "path": tools.String("Relative file path"),
            }, "path"),
            h: makeReadFileHandler(opts),
        },
        {
            name: "write_file",
            desc: "Write content to a file at the given path, creating it if missing or overwriting if present. Returns the path and number of bytes written. Use this to create new files or update existing ones.",
            params: tools.Object(map[string]any{
                "path":         tools.String("Relative file path"),
                "content":      tools.String("File content as a UTF-8 string"),
                "content_type": tools.String("Optional MIME type, e.g. text/markdown"),
            }, "path", "content"),
            h: makeWriteFileHandler(opts),
        },
        {
            name: "list_files",
            desc: "List all files in the current project. Returns an array of {path, size, updated_at}. Files starting with '_' or '.' are hidden by default.",
            params: tools.Object(map[string]any{}),
            h:      makeListFilesHandler(opts),
        },
        {
            name: "delete_file",
            desc: "Delete a file at the given path. Returns the deleted path. Errors if the file does not exist.",
            params: tools.Object(map[string]any{
                "path": tools.String("Relative file path"),
            }, "path"),
            h: makeDeleteFileHandler(opts),
        },
        {
            name: "search_files",
            desc: "Search for a case-insensitive substring across file content in the project. Optional glob filter restricts which paths are searched. Returns up to 50 matches as {path, line, snippet}. v1 does NOT support regex.",
            params: tools.Object(map[string]any{
                "query": tools.String("Case-insensitive substring"),
                "glob":  tools.String("Optional glob, e.g. '*.md'"),
            }, "query"),
            h: makeSearchFilesHandler(opts),
        },
    }
    for _, s := range specs {
        if err := reg.Register(llm.ToolDef{
            Name: s.name, Description: s.desc, Parameters: s.params,
        }, s.h); err != nil {
            return fmt.Errorf("agent: register %s: %w", s.name, err)
        }
    }
    return nil
}
```

The descriptions copy the *style* of `asolabs/hub/internal/nexi/tools.go` (REFERENCE-CODE.md §7): 1–2 sentences, mention required vs optional, name what's returned, surface limits.

### Test scenarios

| Test name | Setup | Assertion |
|---|---|---|
| `RegisterVFSTools_RegistersAllFive` | empty registry | post-call, registry has `read_file`, `write_file`, `list_files`, `delete_file`, `search_files` (5 entries via `Defs()`) |
| `RegisterVFSTools_RejectsNilRegistry` | call with `nil` | returns error |
| `RegisterVFSTools_AppliesDefaults` | `opts := VFSToolOptions{}` | post-call (peek via reflection or via behavior tests), MaxFileBytes is 1 MiB |
| `ReadFile_HappyPath` | StubAgent that scripts `read_file` call; storage has `notes.md=hello` | `EventToolResult` carries `Content == readFileResult{Content:"hello", Size:5, ContentType:"text/markdown"}` |
| `ReadFile_InvalidPath` | script call with `path="../etc/passwd"` | Result OK=false; Error mentions "invalid path"; loop continues |
| `ReadFile_NotFound` | empty storage; script `read_file{path:"missing.md"}` | OK=false; Error mentions "file not found" |
| `ReadFile_OversizeRejected` | storage has 2 MiB file; opts.MaxFileBytes=1MiB | OK=false; Error mentions size + cap |
| `WriteFile_HappyPath` | script `write_file{path:"x.md",content:"y"}` | post-Run, `Storage.ReadFile("x.md")` returns "y"; ChangedPaths includes `x.md` |
| `WriteFile_InvalidPath` | script with `path="/abs"` | OK=false; storage unchanged |
| `WriteFile_ExtensionNotAllowed` | opts.AllowedExtensions=[".md"]; script writes `foo.bin` | OK=false; storage unchanged |
| `WriteFile_OversizeRejected` | content > MaxFileBytes | OK=false; storage unchanged |
| `WriteFile_AndRead_RoundTrip` | script: write, then read | both succeed; read returns the written content |
| `ListFiles_HappyPath` | storage has `a.md, b.md` | result has 2 entries sorted by path |
| `ListFiles_HidesUnderscore` | HideHiddenPaths=true; storage has `_notes/x.md, x.md` | result has only `x.md` |
| `ListFiles_HidesDot` | HideHiddenPaths=true; storage has `.gitignore, x.md` | result has only `x.md` |
| `ListFiles_ShowsHiddenWhenDisabled` | HideHiddenPaths=false; storage has `_x.md, x.md` | result has both |
| `DeleteFile_HappyPath` | storage has `x.md`; script delete | post-Run, storage does not have `x.md` |
| `DeleteFile_NotFound` | empty storage; script delete | OK=false |
| `DeleteFile_ExtensionNotAllowed` | AllowedExtensions=[".md"]; script delete `x.bin` | OK=false; if file existed, still exists |
| `SearchFiles_Substring` | storage has `a.md="The quick brown fox"`, `b.md="lazy dog"`; query=`"BROWN"` | match in `a.md` line 1; case-insensitive |
| `SearchFiles_GlobFilter` | storage has `notes/x.md, docs/y.md`; query="x"; glob=`docs/*` | only `docs/y.md` searched, no matches |
| `SearchFiles_HidesHidden` | storage has `_secret.md` containing "x"; query="x"; HideHiddenPaths=true | no match (file skipped) |
| `SearchFiles_TruncatedAt50` | 60 files all matching | result.Matches has exactly 50 entries; Truncated=true |
| `SearchFiles_SkipsOversize` | storage has 2 MiB file matching; MaxFileBytes=1MiB | file skipped silently; no error |
| `SearchFiles_EmptyQuery` | query="" | OK=false; Error mentions query required |
| `EndToEnd_AgentCreatesAndReads` | StubClient scripts: turn 1=write_file; turn 2=read_file; turn 3=text+end; uses real `vfs/memory` | final assistant text matches expected; storage state matches expected |

~22 tests, ~500 LOC.

### Wave dependencies

- **Consumes from:** W4 (`tools.Registry`, `tools.Handler`, `tools.Result`, `tools.Object`, `tools.String`), W5 (`vfs.Storage`, `vfs.ValidatePath`, `vfs.ErrFileNotFound`, etc.), W6 (`agent.Run` is what tests invoke; `agent.VFSToolOptions` is co-located).
- **Provides to:** W8's `notes.RegisterTools` registers note tools alongside W7's VFS tools — both can coexist in the same `tools.Registry`. `examples/agent-vfs/main.go` becomes runnable end-to-end after W7 lands.

### Risks (concrete mitigations)

- **Risk:** Search is O(N·M) where N=files and M=avg file bytes. Pathological for projects with many large files. **Mitigation:** v1 is single-instance, project-scoped, capped at 50 results, skips oversize files. Document this in the tool description and SECURITY.md if asked.
- **Risk:** `filepath.Match` glob is platform-specific in subtle ways (Windows path separators). **Mitigation:** the VFS namespace uses `/` exclusively (paths are validated by `vfs.ValidatePath` to not be absolute or have `..`); `filepath.Match` works on `/`-separated strings on all platforms when the pattern itself uses `/`. Document.
- **Risk:** `HideHiddenPaths` is a cosmetic filter — model can write to `_x.md` if it constructs the path. **Mitigation:** documented in SECURITY.md "Hidden-path convention" already; not a security boundary. The notes package depends on this being documented, not a flaw.
- **Risk:** Extension whitelist is path-suffix based, not content-based — model can name a binary file `foo.md`. **Mitigation:** v1 documents this. `AllowedExtensions` is a hint, not a sandbox. Defense-in-depth lives at the storage layer where users can refuse known-bad content types.
- **Risk:** `inferContentType` from extension may collide with caller intent. **Mitigation:** caller can pass `content_type` explicitly; the inference is only a fallback for the convenience case (model writes a `.md` and gets `text/markdown` for free).
- **Risk:** The `search_files` tool reads every file in the project on every call — wasted I/O. **Mitigation:** v1 is fine; v2 may add an indexed `vfs.Searchable` capability interface for backends that can do server-side search. Not blocking for v1.

### Exit criteria

- [ ] `go test ./agent/... -race` passes including all W7 tests.
- [ ] `examples/agent-vfs/main.go` runs end-to-end with a real OpenRouter client, creates a file, lists, reads, reports back. (Manual smoke; not in CI.)
- [ ] Each of the five tools has a happy-path test, an error-path test, and an option-respecting test (HideHiddenPaths / AllowedExtensions / MaxFileBytes).
- [ ] Tool descriptions include the v1 limit notes (e.g., "v1 does NOT support regex" in `search_files`).
- [ ] No new package-level errors introduced — all errors flow through existing `vfs.ErrX` sentinels and `fmt.Errorf("$tool: %w", ...)` wrapping.

---

## W8 — `notes` package

**Estimated LOC:** ~350 go + ~400 test | **Estimated effort:** 7–9 hours focused | **Status:** not started

### File tree

```
notes/
  doc.go            — package godoc explaining the note-then-consolidate flow
  notebook.go       — Options, Notebook, NewNotebook, Add, List, Read, Consolidate
  tools.go          — RegisterTools, the three tool definitions and handler closures
  prompts.go        — defaultConsolidationPrompt constant
  notebook_test.go  — Notebook unit tests against vfs/memory
  tools_test.go     — RegisterTools + tool dispatch round-trips
  consolidate_test.go — Consolidate happy path + edge cases (empty notes, missing target)
examples/notes-consolidate/
  main.go           — runnable example: 4 notes → consolidate → produce profile.md
```

### Public API delivered

Locked by API.md L466–L532.

```go
package notes

import (
    "context"
    "time"

    "github.com/google/uuid"
    "github.com/mxcd/aikido/llm"
    "github.com/mxcd/aikido/tools"
    "github.com/mxcd/aikido/vfs"
)

type Options struct {
    Storage  vfs.Storage // required
    Path     string      // default "_notes/"
    DocsPath string      // default "docs/"
    Client   llm.Client  // required for Consolidate
    Model    string      // required for Consolidate
}

type Notebook struct{ /* ... */ }

func NewNotebook(opts *Options) (*Notebook, error)

type NoteRef struct {
    Path      string
    TurnID    uuid.UUID
    UpdatedAt time.Time
    Preview   string
}

type ConsolidationResult struct {
    DocPath       string
    NewContent    string
    ConsumedNotes []string
    Usage         *llm.Usage
}

func (n *Notebook) Add(ctx context.Context, projectID, turnID uuid.UUID, body string) error
func (n *Notebook) List(ctx context.Context, projectID uuid.UUID) ([]NoteRef, error)
func (n *Notebook) Read(ctx context.Context, projectID uuid.UUID) (string, error)
func (n *Notebook) Consolidate(ctx context.Context, projectID uuid.UUID, target string, instructions string) (ConsolidationResult, error)

func RegisterTools(reg *tools.Registry, nb *Notebook) error
```

### Internal types (unexported)

```go
// notes/notebook.go
type Notebook struct {
    storage  vfs.Storage
    path     string // always normalized to end with "/"
    docsPath string
    client   llm.Client
    model    string
}

// notes/tools.go — handler closures capture *Notebook; no separate types.
```

### Errors introduced

W8 reuses existing sentinels (`vfs.ErrPathInvalid`, `vfs.ErrFileNotFound`, `tools.ErrDuplicateTool`). Setup errors from `NewNotebook` are plain `errors.New("notes: storage required")` style.

### Implementation order within the wave

1. **`notes/prompts.go`** — the constant.
2. **`notes/notebook.go`** — types + `NewNotebook` + `Add` + `List` + `Read`. These are storage-only; no LLM. Test against `vfs/memory`.
3. **`notes/tools.go`** — `add_note` and `list_notes` handlers (storage-only).
4. **`notes/notebook.go` `Consolidate`** — the LLM-using method. Depends on `llm.Collect` (W1) and a stub LLM client for tests.
5. **`notes/tools.go` `consolidate_notes_into_doc` handler** — closes over the Notebook.
6. **`notes/tools.go` `RegisterTools`** — dispatches to W4 registry.
7. **`examples/notes-consolidate/main.go`** — manual smoke.

### `Notebook.Add` semantics

```go
func (n *Notebook) Add(ctx context.Context, projectID, turnID uuid.UUID, body string) error {
    if turnID == uuid.Nil { return errors.New("notes: turnID required") }
    path := n.path + turnID.String() + ".md"
    // Read-modify-append-write so multiple Add() calls in the same turn
    // accumulate into one file. The read-modify-append happens through env.Storage,
    // which inside an agent turn is the active Tx — so multi-add atomicity is the
    // tx's responsibility, not ours.
    var prev []byte
    if existing, _, err := n.storage.ReadFile(ctx, projectID, path); err == nil {
        prev = existing
        prev = append(prev, []byte("\n\n")...)
    } else if !errors.Is(err, vfs.ErrFileNotFound) {
        return fmt.Errorf("notes: %w", err)
    }
    body = strings.TrimSpace(body)
    full := append(prev, []byte(body)...)
    if err := n.storage.WriteFile(ctx, projectID, path, full, "text/markdown"); err != nil {
        return fmt.Errorf("notes: %w", err)
    }
    return nil
}
```

Per the prompt: writes to `_notes/{turn-uuid}.md`. Multiple `Add()` in the same turn perform read-modify-append-write. Inside a `TxStorage` agent turn, this happens through the active tx so the consolidation can roll back atomically — the `Notebook.storage` reference IS the env.Storage from the active turn IF callers wire it that way.

That last point matters: the `Notebook` is constructed once with a `Storage` reference at `NewNotebook` time. If that's the base `Storage`, `Add` always writes to base — no tx isolation. If callers want tx isolation for Add, they must construct a fresh `Notebook` per turn pointing at the active `tools.Env.Storage` (which IS the tx during a turn). The cleaner path: the three notebook tools take a `*Notebook` reference but their handlers use `env.Storage`, not `nb.storage`, for the actual storage call. Implementation detail follows.

```go
// notes/tools.go (simplified)
func makeAddNoteHandler(nb *Notebook) tools.Handler {
    return func(ctx context.Context, args json.RawMessage, env tools.Env) (tools.Result, error) {
        var p struct{ Body string `json:"body"` }
        if err := json.Unmarshal(args, &p); err != nil {
            return tools.Result{}, fmt.Errorf("add_note: invalid args: %w", err)
        }
        if strings.TrimSpace(p.Body) == "" {
            return tools.Result{}, errors.New("add_note: body required")
        }
        // Use env.Storage (which is the tx when one is active) so the note write
        // participates in the agent's per-turn tx.
        path := nb.path + env.TurnID.String() + ".md"
        var prev []byte
        if existing, _, err := env.Storage.ReadFile(ctx, env.Project, path); err == nil {
            prev = append(existing, []byte("\n\n")...)
        }
        full := append(prev, []byte(strings.TrimSpace(p.Body))...)
        if err := env.Storage.WriteFile(ctx, env.Project, path, full, "text/markdown"); err != nil {
            return tools.Result{}, fmt.Errorf("add_note: %w", err)
        }
        return tools.Result{
            Content:      map[string]any{"path": path, "size": len(full)},
            Display:      "noted",
            ChangedPaths: []string{path},
        }, nil
    }
}
```

The `Notebook.Add` method (called outside the tool path) uses `nb.storage` — for callers using the Notebook programmatically, not through the agent. Two paths, same behavior, different `Storage` source.

### `Notebook.List` semantics

```go
func (n *Notebook) List(ctx context.Context, projectID uuid.UUID) ([]NoteRef, error) {
    metas, err := n.storage.ListFiles(ctx, projectID)
    if err != nil { return nil, fmt.Errorf("notes: %w", err) }

    refs := []NoteRef{}
    for _, m := range metas {
        if !strings.HasPrefix(m.Path, n.path) { continue }
        if !strings.HasSuffix(m.Path, ".md") { continue }
        turnIDStr := strings.TrimSuffix(strings.TrimPrefix(m.Path, n.path), ".md")
        turnID, err := uuid.Parse(turnIDStr)
        if err != nil { continue } // skip non-UUID-named files defensively
        content, _, err := n.storage.ReadFile(ctx, projectID, m.Path)
        if err != nil { continue }
        preview := string(content)
        if len(preview) > 200 { preview = preview[:200] }
        refs = append(refs, NoteRef{
            Path: m.Path, TurnID: turnID, UpdatedAt: m.UpdatedAt, Preview: preview,
        })
    }
    sort.Slice(refs, func(i, j int) bool {
        return refs[i].UpdatedAt.Before(refs[j].UpdatedAt)
    })
    return refs, nil
}
```

Reads files under `_notes/` prefix; returns chronological by `UpdatedAt`. `NoteRef.Preview = first 200 chars`. Per the user prompt's spec for the `notes` package.

### `Notebook.Read` semantics

```go
func (n *Notebook) Read(ctx context.Context, projectID uuid.UUID) (string, error) {
    refs, err := n.List(ctx, projectID)
    if err != nil { return "", err }
    if len(refs) == 0 { return "", nil }

    parts := make([]string, 0, len(refs))
    for _, r := range refs {
        body, _, err := n.storage.ReadFile(ctx, projectID, r.Path)
        if err != nil { continue }
        parts = append(parts, string(body))
    }
    return strings.Join(parts, "\n\n---\n\n"), nil
}
```

Concatenates all notes in chronological order, separated by `\n\n---\n\n`. Per the user prompt's spec.

### `Notebook.Consolidate` — full algorithm

Follows the user prompt's six-step spec literally.

```go
func (n *Notebook) Consolidate(ctx context.Context, projectID uuid.UUID, target, instructions string) (ConsolidationResult, error) {
    // 1. Read target file (empty if missing).
    var existingTarget string
    if body, _, err := n.storage.ReadFile(ctx, projectID, target); err == nil {
        existingTarget = string(body)
    } else if !errors.Is(err, vfs.ErrFileNotFound) {
        return ConsolidationResult{}, fmt.Errorf("notes consolidate: read target: %w", err)
    }

    // 2. Read all notes via List + per-file ReadFile.
    refs, err := n.List(ctx, projectID)
    if err != nil { return ConsolidationResult{}, err }
    consumed := make([]string, 0, len(refs))
    notesText := strings.Builder{}
    for _, r := range refs {
        body, _, err := n.storage.ReadFile(ctx, projectID, r.Path)
        if err != nil { continue }
        consumed = append(consumed, r.Path)
        notesText.WriteString(string(body))
        notesText.WriteString("\n\n---\n\n")
    }

    // 3. Build llm.Request: default consolidation prompt + instructions appended.
    sys := defaultConsolidationPrompt
    if strings.TrimSpace(instructions) != "" {
        sys = sys + "\n\n## Caller-supplied instructions\n\n" + instructions
    }
    user := fmt.Sprintf(
        "## Existing target document (`%s`)\n\n%s\n\n## Notes to consolidate\n\n%s",
        target, existingTarget, notesText.String(),
    )
    req := llm.Request{
        Model:    n.model,
        Messages: []llm.Message{
            {Role: llm.RoleSystem, Content: sys},
            {Role: llm.RoleUser, Content: user},
        },
        MaxTokens: 8192,
    }

    // 4. Call llm.Collect — non-streaming consolidation, single LLM call.
    text, _, usage, err := llm.Collect(ctx, n.client, req)
    if err != nil { return ConsolidationResult{}, fmt.Errorf("notes consolidate: %w", err) }

    // 5. Write target with the resulting text.
    if err := n.storage.WriteFile(ctx, projectID, target, []byte(text), "text/markdown"); err != nil {
        return ConsolidationResult{}, fmt.Errorf("notes consolidate: write target: %w", err)
    }

    // 6. Delete consumed note files.
    for _, p := range consumed {
        _ = n.storage.DeleteFile(ctx, projectID, p) // best-effort; target is already written
    }

    // 7. Return result.
    return ConsolidationResult{
        DocPath:       target,
        NewContent:    text,
        ConsumedNotes: consumed,
        Usage:         usage,
    }, nil
}
```

Step-by-step:

1. **Read target file (empty if missing).** `vfs.ErrFileNotFound` → empty string; any other error → fail.
2. **Read all notes via List + per-file ReadFile.** Errors per-file are skipped silently; the list is best-effort.
3. **Build llm.Request with default consolidation prompt + opts.Model + caller's `instructions` appended.** The user message contains the existing target + concatenated notes; the system message is the default prompt with caller instructions appended (or just the default if instructions is empty).
4. **Call llm.Collect (non-streaming consolidation — single LLM call).** Per ADR-006, `Collect` drains the stream into final text + calls + usage. Consolidation has no tool calls.
5. **Write target with the resulting text.** Content type `text/markdown`. If write fails, the consumed notes are NOT deleted (we still have the source-of-truth). If write succeeds but a delete fails, target is already saved — the orphaned notes are recoverable manually.
6. **Delete consumed note files (DeleteFile per note path).** Best-effort; we log failures via the storage's own errors but do not surface them.
7. **Return ConsolidationResult{DocPath, NewContent, ConsumedNotes, Usage}.**

### Default consolidation prompt

`notes/prompts.go`:

```go
package notes

const defaultConsolidationPrompt = `You are a consolidation agent.

Your job is to merge a set of short atomic notes into the existing target
document. The notes were captured during prior interactions and represent
discrete facts or observations. The target document is the long-form synthesis
that the user reads.

Produce a single coherent document that:

- Preserves every fact present in the notes or the existing target. Drop
  conjecture, speculation, and uncertain claims.
- Deduplicates: if two notes (or a note and the existing target) say the same
  thing, merge them into one statement.
- Maintains the markdown structure of the existing target where it exists. Add
  new sections only when the notes introduce a new topic with no current home.
- Uses concise, direct prose. No filler. No "based on the notes" preamble — the
  output IS the document, not a report about it.
- Returns markdown only. No code fences around the whole document. No JSON.

Output the full updated document, ready to overwrite the target file.`
```

~165 words. Opinionated per ADR-007: produce a single coherent document, deduplicate, preserve facts, drop conjecture, keep markdown structure consistent. Callers override behavior by appending `instructions` — the default prompt is concatenated with caller instructions, not replaced.

### The three tools — JSON Schemas

#### `add_note`

```go
llm.ToolDef{
    Name: "add_note",
    Description: "Append a short atomic note to the project's notebook. Use this whenever you learn a discrete fact about the user, the project, or the work in progress. Notes are consolidated into a target document later via consolidate_notes_into_doc.",
    Parameters: tools.Object(map[string]any{
        "body": tools.String("The note body. Markdown supported. Keep it short — one observation per call."),
    }, "body"),
}
```

#### `list_notes`

```go
llm.ToolDef{
    Name: "list_notes",
    Description: "List all pending notes in the project. Returns an array of {path, turn_id, updated_at, preview}. Use this to inspect what has been noted before deciding whether to consolidate.",
    Parameters: tools.Object(map[string]any{}),
}
```

#### `consolidate_notes_into_doc`

```go
llm.ToolDef{
    Name: "consolidate_notes_into_doc",
    Description: "Read all pending notes plus the existing target document, ask the model to merge them into a single coherent document, write the result back to target_path, and delete the consumed notes. Use this when you have accumulated enough notes to warrant a synthesis pass.",
    Parameters: tools.Object(map[string]any{
        "target_path":  tools.String("The relative path of the document to merge into. Created if missing."),
        "instructions": tools.String("Optional extra instructions appended to the default consolidation prompt. Use this to bias the merge for tone, format, or focus."),
    }, "target_path"),
}
```

`consolidate_notes_into_doc` args literal: `{target_path: string, instructions?: string}` — required `target_path`, optional `instructions`.

### `RegisterTools` shape

```go
func RegisterTools(reg *tools.Registry, nb *Notebook) error {
    if reg == nil { return errors.New("notes: registry required") }
    if nb == nil { return errors.New("notes: notebook required") }
    if err := reg.Register(addNoteDef, makeAddNoteHandler(nb)); err != nil {
        return fmt.Errorf("notes: register add_note: %w", err)
    }
    if err := reg.Register(listNotesDef, makeListNotesHandler(nb)); err != nil {
        return fmt.Errorf("notes: register list_notes: %w", err)
    }
    if err := reg.Register(consolidateDef, makeConsolidateHandler(nb)); err != nil {
        return fmt.Errorf("notes: register consolidate_notes_into_doc: %w", err)
    }
    return nil
}
```

### Test scenarios

| Test name | Setup | Assertion |
|---|---|---|
| `NewNotebook_ValidatesStorage` | nil storage | returns error |
| `NewNotebook_ValidatesClient` | nil client | returns error |
| `NewNotebook_AppliesDefaults` | minimal opts (Storage, Client, Model only) | Path == "_notes/", DocsPath == "docs/" |
| `Add_WritesToCorrectPath` | turnID = fixed UUID | post-Add, `_notes/{uuid}.md` exists with expected body |
| `Add_MultipleInSameTurn_Append` | Add twice with same turnID | post-second-Add, file contains both bodies separated by blank lines |
| `Add_AcrossTurns_SeparateFiles` | Add with turnID-A, then with turnID-B | both files exist independently |
| `Add_RejectsNilTurnID` | turnID = uuid.Nil | returns error |
| `Add_RejectsEmptyBody` | body = "" or whitespace | tool handler rejects (body validation in handler, not in `Add` directly — but mirror in `Add` too) |
| `List_EmptyProject` | new project, no notes | returns empty slice, nil error |
| `List_ChronologicalOrder` | three Adds at different times (manipulate clock) | returns 3 NoteRefs sorted by UpdatedAt ascending |
| `List_PreviewIs200Chars` | Add 500-char body | NoteRef.Preview is first 200 chars |
| `List_IgnoresNonUUIDNamedFiles` | manually `Storage.WriteFile("_notes/garbage.md", ...)` | List skips it |
| `Read_EmptyProject` | no notes | returns "" (empty string) |
| `Read_ConcatenatesChronological` | three Adds | returns A + sep + B + sep + C in order |
| `Read_SeparatorIsCorrect` | two Adds | result contains `\n\n---\n\n` once |
| `Consolidate_HappyPath` | three notes, empty target, stub LLM returning "merged doc" | target file exists with content "merged doc"; ConsumedNotes has 3 paths; the 3 note files are deleted; Usage non-nil |
| `Consolidate_ExistingTarget` | three notes, target with prior content "old", stub returns "old + new merged" | target updated; user-message in stub captured shows existing target + 3 notes |
| `Consolidate_NoNotes` | empty notes, target exists | LLM still called; returns merged result; ConsumedNotes is empty |
| `Consolidate_MissingTarget` | notes exist, no target file | works (treated as empty existing); target created |
| `Consolidate_LLMError` | stub returns error | Consolidate returns error; target NOT written; notes NOT deleted |
| `Consolidate_WritesAfterLLM` | LLM succeeds; storage.WriteFile errors | error returned; notes NOT deleted (preserves source) |
| `Consolidate_InstructionsAppended` | instructions = "be terse" | stub captures system prompt containing "be terse" |
| `RegisterTools_RegistersAll` | empty registry | post-call has 3 tool defs |
| `RegisterTools_DispatchAddNote` | full agent + stub LLM scripting add_note → end | post-Run, `_notes/{turn-uuid}.md` exists |
| `RegisterTools_DispatchConsolidate` | agent + stub LLM scripting add×3 → consolidate → end; consolidation stub LLM returns merged doc | post-Run, target file exists with merged content; notes deleted |
| `EndToEnd_ScriptedAcross3Turns` | 3 turns: turn-1 add×2; turn-2 add×1; turn-3 consolidate | per-turn note files written; consolidation works across all 3 turn UUIDs |

~25 tests, ~400 LOC.

### Wave dependencies

- **Consumes from:** W1 (`llm.Request`, `llm.Message`, `llm.Collect`, `llm.Client`), W4 (`tools.Registry`, `tools.Result`, `tools.Object`), W5 (`vfs.Storage`, `vfs.ErrFileNotFound`), W6 (`tools.Env.TurnID` is the keying, available inside the active turn).
- **Provides to:** `examples/notes-consolidate/main.go` (the headline v1 example). `agent.Options.Notes` is the convenience hook for callers who want note-tool registration co-located in their Agent setup; the agent does nothing with it directly — the caller calls `notes.RegisterTools(opts.Tools, opts.Notes)` themselves before constructing the agent.

### Risks (concrete mitigations)

- **Risk:** `Notebook.Add` outside the agent (programmatic) writes through `nb.storage`, which is the base storage — not a tx. Concurrent agent turns and Notebook.Add calls can race. **Mitigation:** v1 documents that `Notebook.Add` is single-threaded and not meant to be called concurrently with agent turns on the same project. Multi-instance coordination is v3.
- **Risk:** Consolidation prompt is too opinionated for some domains. **Mitigation:** `instructions` parameter appends to the default; callers can effectively override behavior. If demand emerges for full prompt replacement, a v1.x additive `Options.ConsolidationPrompt string` can replace the default — additive, non-breaking. For now, keep simple.
- **Risk:** Consolidation read-modify-write is not atomic — between read-target and write-target, another process could update target. **Mitigation:** v1 is single-instance; per-project mutex serializes turns. Multi-instance is v3. Document.
- **Risk:** Notes accumulate forever if no one ever consolidates. **Mitigation:** model behavior — the tool description nudges the model to consolidate when notes have accumulated. v2 may add `Notebook.Sweep(olderThan time.Duration)` for housekeeping. Not v1.
- **Risk:** Note path collision if turn-UUID collision (vanishingly unlikely) — but if `tools.Env.TurnID` is reused (e.g., a buggy agent), Add appends to the same file. **Mitigation:** UUID v4 collision probability is negligible; the agent generates a fresh `uuid.New()` per turn (W6 step 4a). Not a real risk.

### Exit criteria

- [ ] `go test ./notes/...` passes.
- [ ] `examples/notes-consolidate/main.go` runs end-to-end with real OpenRouter + memory storage. Manually verified: 4 input messages → notes accrue → consolidation produces a coherent `profile.md`.
- [ ] Default consolidation prompt is committed in `notes/prompts.go`; godoc on `Notebook.Consolidate` mentions that callers can append via `instructions`.
- [ ] All 25 test scenarios green.
- [ ] `notes` package has no transitive dependency on `agent` (clean dependency graph: `notes` → `llm`, `tools`, `vfs` only).

---

## Post-W8 — Tagging v0.1.0

Once all eight waves are merged and `just check` is green on `main`, aikido is ready to be tagged. This step is mostly mechanical; do not skip the manual smoke runs.

**Estimated effort:** 1 hour (most of it waiting for the smoke runs to complete) | **Status:** not started

### Step-by-step protocol

#### 1. Confirm `just check` green on main

```sh
git checkout main
git pull
just check
```

`just check` runs `go vet`, `golangci-lint run`, and `go test ./...`. All three must be clean. Address any flake or warning — do not tag through warnings. If a test is flaky, fix it or quarantine via `t.Skip` with a TODO entry referencing the issue. Do not tag through skipped tests without that audit trail.

#### 2. Manual smoke run of all three examples

```sh
export OPENROUTER_API_KEY=sk-or-v1-...
go run ./examples/chat-oneshot && \
go run ./examples/agent-vfs && \
go run ./examples/notes-consolidate
```

Each example must produce sensible output. Specifically:

**`examples/chat-oneshot`** — should print model output to stdout and a token-count summary at the end (something like `prompt=X completion=Y total=Z`). The text content matters less than the shape: did we get usage telemetry? Did the program exit cleanly? If the example hangs, the streaming code in W3 has a bug (channel never closes); investigate before tagging.

**`examples/agent-vfs`** — should print a sequence of agent events: text deltas, tool calls (e.g., `write_file path=hello.md`), tool results (`OK=true`), and `EndReason=stop`. Final exit code 0. Verify the file was actually created in the in-memory VFS by reading the example's output (it should print the file's contents at the end).

**`examples/notes-consolidate`** — should produce a `profile.md`-like document from 4 simulated input messages. The example feeds 4 messages, consolidates, prints the resulting document. The document should be coherent markdown — not a fragment, not JSON, not a wrapped code block.

If any of the three fail or produce surprising output, do NOT tag. Fix and re-run.

#### 3. Update `README.md` "Status" section

The repo has a top-level `README.md` (created in W0). It includes a "Status" section near the top — pre-tag, it says something like "v0.1 in progress". Update to:

```md
## Status

aikido v0.1.0 was tagged on dd.MM.yyyy. The library is now usable; pin against
the tag in `go.mod`:

    go get github.com/mxcd/aikido@v0.1.0

This is a minimum-viable-shape release. The v1 surface (per [docs/v1/API.md](docs/v1/API.md))
is locked; future v1 patches are additive and backwards-compatible.

For the v2 roadmap (image, audio, direct providers, CLI), see [docs/v2/SCOPE.md](docs/v2/SCOPE.md).
```

Commit:

```sh
git add README.md
git commit -m "docs: mark v0.1.0 status"
git push
```

(No Co-Authored-By trailer per MaPa's global rules in CLAUDE.md.)

#### 4. Tag

```sh
git tag -a v0.1.0 -m "v0.1.0 — first usable aikido release"
git push origin v0.1.0
```

Annotated tag (`-a`) so the commit object carries the message; lightweight tags get rejected by some module proxies under edge conditions.

#### 5. Verify Go module proxy ingests the tag

```sh
mkdir /tmp/aikido-tag-check && cd /tmp/aikido-tag-check
go mod init throwaway
go list -m github.com/mxcd/aikido@v0.1.0
```

Expected output:

```
github.com/mxcd/aikido v0.1.0
```

If it returns `module github.com/mxcd/aikido: ...` errors, the proxy hasn't picked the tag up yet — wait 60s and retry. If it persists past 5 minutes, check that the tag is annotated and pushed (`git ls-remote --tags origin v0.1.0` should show a SHA).

Then a final import sanity check:

```sh
cat > main.go <<'EOF'
package main
import (
    "fmt"
    _ "github.com/mxcd/aikido/llm"
    _ "github.com/mxcd/aikido/agent"
)
func main() { fmt.Println("aikido imports clean") }
EOF
go mod tidy
go run .
```

Expected: prints `aikido imports clean`. If it fails to download or compile, the tag has a problem; either re-tag (if the issue is resolvable in main and a fast-forward will work) or move to v0.1.1 with a fix (preferred — never re-tag a published version).

#### Notes for the tagger

- Per MaPa's global rules in `~/.claude/CLAUDE.md`: **NEVER add a "Co-Authored-By" line to commit messages** (including the README update commit and the tag annotation if it had any reference to authorship — but since `git tag -m` is a tag annotation, not a commit, the rule does not strictly apply; it does apply to the README commit in step 3).
- Per CONTRIBUTING.md: do not push to remote without confirming `just check` green. `git push origin v0.1.0` is the explicit confirmation step.
- Do NOT amend the tag commit. If a fix is needed, it goes into a follow-up commit and either v0.1.1 or, if the tag has not propagated yet, a force-push of the v0.1.0 tag. Force-pushing a tag is destructive and invalidates any module-proxy entries that already cached it; default to v0.1.1 unless absolutely necessary.

aikido is now tagged. v2 work begins from a stable v1 surface that callers can pin against.

---

## Open questions surfaced during late-wave detailing

The user prompt told me to stick to API.md signatures and footnote anything missing as `Open Question — ADR addendum?`. The four below come up specifically during W5–W8 detailing:

- **VFSToolOptions zero-value vs API.md default for `HideHiddenPaths`.** API.md L455 says the default is `true`. Go zero value is `false`. Without a tri-state (`*bool` or a `set` flag), callers cannot distinguish "explicitly false" from "didn't set it". v1 should accept the asymmetry: callers passing `VFSToolOptions{}` get `HideHiddenPaths=true` after defaulting; callers wanting explicit-false set `HideHiddenPaths: false` AND understand the default-applier inverts it. The cleanest fix is `HideHiddenPaths *bool` but that changes API.md. **Open Question — ADR addendum?**
- **`Notebook.Add` storage source — base or active tx?** The `Notebook` constructor takes one `vfs.Storage`. The agent's tools want to write through the active tx. v1 resolves this by having the *handlers* use `env.Storage` and the *programmatic API* use `nb.storage`. Both work. The dual path is documented but undocumented in API.md — could be made explicit in a godoc note. **Open Question — ADR addendum?**
- **EndReason as constants vs typed string.** API.md L440 declares `EndReason string` with documented values. v1 should export `const EndReasonStop = "stop"`, etc. for typo safety but not re-type the field. Confirm the convention is OK as-described, no API.md change needed.
- **Default consolidation prompt — locked or extensible?** v1 ships one default. If callers need a fully different default, today they pass full `instructions`; the default is concatenated. v1.x could add `Options.ConsolidationPrompt string` (additive, non-breaking). Not blocking for v0.1.0.

---

## Cross-wave map

For someone implementing late waves in sequence, the rough chain of code introduced (per file, in dependency order):

```
W5: vfs/errors.go → vfs/path.go → vfs/hash.go → vfs/storage.go → vfs/txstorage.go
    → vfs/conformance.go + conformance_tx.go → vfs/memory/storage.go + storage_tx.go
W6: agent/event.go → agent/agent.go → agent/safety.go → internal/testutil/stub_client.go
    → agent/run.go (helpers + drain + runLoop + Run/RunWithMessages)
W7: agent/tools_builtin.go (5 makeXHandler closures) → agent/tools_register.go
W8: notes/prompts.go → notes/notebook.go (types + Add + List + Read + Consolidate)
    → notes/tools.go (3 handler closures + RegisterTools)
    → examples/notes-consolidate/main.go
Tag: README.md update commit → annotated tag → push → proxy verify → import smoke
```

Total LOC for late waves: ~1850 go + ~2200 test = ~4050 LOC. Within the "200–300 LOC per wave for the agent loop alone" budget called out in ADR-003, with W7/W8 sharing the discipline.
