# Concurrency in Go

Go's concurrency primitives are lightweight goroutines plus channels — the language-level realization of CSP (Communicating Sequential Processes).

## Goroutines

A goroutine is a function executed concurrently with other goroutines in the same process. They are not OS threads; the Go runtime multiplexes thousands of goroutines onto a small pool of OS threads. Stacks start at ~2 KiB and grow as needed.

Launch with the `go` keyword:

```go
go process(item)
```

The function returns immediately; the goroutine runs independently.

## Channels

Channels are typed conduits for sending values between goroutines. They synchronize implicitly: a send blocks until a receiver is ready (for unbuffered channels), and vice versa.

```go
ch := make(chan int)
go func() { ch <- 42 }()
got := <-ch  // got == 42
```

Buffered channels (`make(chan int, 8)`) act as bounded queues.

## Select

`select` waits on multiple channel operations at once. Combined with `context.Context`, it is the canonical way to express cancellation and timeouts.

```go
select {
case msg := <-incoming:
    handle(msg)
case <-ctx.Done():
    return ctx.Err()
}
```

## Common patterns

- **Fan-out / fan-in:** distribute work across N worker goroutines, then collect results on a shared channel.
- **Pipeline:** chained stages where each stage reads from one channel and writes to the next.
- **Cancellation:** propagate a `context.Context` through every blocking call.
