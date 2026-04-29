# Go basics

Go is a statically typed, compiled programming language designed at Google in 2007 and publicly released in 2009. The language emphasizes simplicity, fast compilation, and strong tooling.

## Type system

- Static and strongly typed; no implicit conversions between numeric types.
- Structural interfaces — a type satisfies an interface by implementing its methods, without an explicit `implements` keyword.
- Zero values: every type has a well-defined zero value, so uninitialized variables are never undefined.
- Generics arrived in Go 1.18 (March 2022) via type parameters on functions and types.

## Compilation and tooling

- The `go` command bundles the compiler, formatter, test runner, and module tooling.
- Builds produce a single statically linked binary; cross-compilation is a one-flag operation.
- `gofmt` enforces a single canonical style; tabs for indentation, no flag debate.
- The standard library is rich and stable; Go's compatibility promise extends across minor versions.

## When to choose Go

- High-throughput network services and CLIs benefit from the simple concurrency model and fast cold start.
- Single-binary deployment is a major operational win for containers and edge runtimes.
- Teams that value readability and onboarding speed over expressive power tend to like Go.
