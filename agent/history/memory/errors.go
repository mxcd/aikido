package memory

import "errors"

// ErrEmptySessionID is returned by Append/Read when sessionID is empty.
var ErrEmptySessionID = errors.New("aikido/agent/history/memory: empty sessionID")
