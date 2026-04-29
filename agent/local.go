package agent

import (
	historymem "github.com/mxcd/aikido/agent/history/memory"
)

// NewLocalSession is a convenience constructor for single-replica deployments
// and tests. It auto-supplies in-memory implementations for History and
// Locker if the caller leaves them unset.
//
// For multi-replica deployments, or any setup needing a custom Locker (e.g.,
// Redis) or a persistent History (e.g., Postgres), use NewSession directly
// and supply your own plug-ins.
//
// History defaults to an in-memory store; conversation is lost when the
// process restarts.
func NewLocalSession(opts *SessionOptions) (*Session, error) {
	if opts == nil {
		return NewSession(opts)
	}
	cp := *opts
	if cp.History == nil {
		cp.History = historymem.NewHistory()
	}
	if cp.Locker == nil {
		cp.Locker = NewLocalLocker()
	}
	return NewSession(&cp)
}
