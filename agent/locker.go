package agent

import (
	"context"
	"sync"
)

// Locker provides mutual exclusion keyed by session ID.
//
// The Session acquires a lock on its ID before each Run begins (covering
// History.Read at turn start) and releases it after EventEnd. The unlock
// function returned by Lock must be called exactly once; the agent always
// calls it via defer.
type Locker interface {
	Lock(ctx context.Context, sessionID string) (unlock func(), err error)
}

// LocalLocker is the in-process implementation of Locker. Suitable for
// single-replica deployments and tests. Memory grows with the number of
// distinct session IDs seen; call Forget(id) to drop a key once the session
// is finished.
type LocalLocker struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

var _ Locker = (*LocalLocker)(nil)

// NewLocalLocker returns a fresh in-process Locker.
func NewLocalLocker() *LocalLocker {
	return &LocalLocker{locks: make(map[string]*sync.Mutex)}
}

// Lock blocks until the per-id lock is acquired or ctx cancels.
func (l *LocalLocker) Lock(ctx context.Context, sessionID string) (func(), error) {
	l.mu.Lock()
	m, ok := l.locks[sessionID]
	if !ok {
		m = &sync.Mutex{}
		l.locks[sessionID] = m
	}
	l.mu.Unlock()

	// Acquire with ctx awareness.
	acquired := make(chan struct{})
	go func() {
		m.Lock()
		close(acquired)
	}()
	select {
	case <-ctx.Done():
		// Wait for the goroutine to finish acquiring (otherwise it'll dangle),
		// then immediately release. This is rare-path; the cost is acceptable.
		go func() {
			<-acquired
			m.Unlock()
		}()
		return nil, ctx.Err()
	case <-acquired:
	}

	var once sync.Once
	return func() { once.Do(m.Unlock) }, nil
}

// Forget drops the per-id mutex for the given session id. Safe to call after
// all Run calls for the session have completed.
func (l *LocalLocker) Forget(id string) {
	l.mu.Lock()
	delete(l.locks, id)
	l.mu.Unlock()
}
