# Go tooling

Go ships a complete toolchain in a single `go` command. Below are the everyday subcommands.

## Build, test, run

- `go build` compiles the current package and its dependencies.
- `go run main.go` compiles and runs a one-off program without producing a binary.
- `go test ./...` runs tests recursively. Add `-race` for the race detector; add `-cover` for coverage.

## Modules

- `go mod init <module-path>` creates `go.mod`.
- `go get <pkg>@<version>` adds or upgrades a dependency.
- `go mod tidy` removes unused requires and adds missing ones; idempotent.
- `go.sum` records cryptographic checksums for every module version used.

## Static analysis

- `go vet ./...` catches common mistakes the compiler does not (printf format mismatches, unreachable code).
- `golangci-lint run ./...` runs a configurable suite of linters (errcheck, staticcheck, gocritic, revive…).
- `gofmt -l .` lists files that are not canonically formatted; `gofmt -w` rewrites them.

## Workspaces (Go 1.18+)

`go work` lets you develop multiple modules in lockstep without `replace` directives. Run `go work init ./moduleA ./moduleB` to create a workspace; the `go` command picks up `go.work` automatically.

## Profiling

- `go test -bench=.` runs benchmarks.
- `pprof` consumes CPU/heap profiles emitted by tests or `net/http/pprof`.
- The `go tool trace` viewer renders an interactive timeline of goroutine scheduling.
