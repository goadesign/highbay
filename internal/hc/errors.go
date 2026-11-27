// This file declares the errors callers match: the core's error kinds and the
// ways a connection or stream ends.

package hc

import (
	"errors"
	"fmt"
)

// KindError is an error herdr-core returned for one call. Kind is the core's
// stable word for what went wrong ("not_found", "blocked", ...); Message and
// Hint are its text for a person. Match a kind with errors.Is and the Err*
// values below, or read Kind for one this package does not name.
type KindError struct {
	// Code is the JSON-RPC error code: -32000 for the core's own errors.
	Code int
	// Kind is the core's stable error word. It is "internal" when the core
	// sent none.
	Kind string
	// Message says what went wrong, naming the terminal concerned.
	Message string
	// Hint says what the caller can do next. It may be empty.
	Hint string
}

var (
	// ErrNotFound matches a KindError of kind "not_found": no such terminal.
	ErrNotFound = errors.New("hc: not_found")
	// ErrFinished matches kind "finished": the terminal's program has ended,
	// so it takes no input and cannot be attached.
	ErrFinished = errors.New("hc: finished")
	// ErrUnavailable matches kind "unavailable": the core cannot act on the
	// terminal now (it is lost and waiting for a new shell, or still
	// starting). The call may succeed later.
	ErrUnavailable = errors.New("hc: unavailable")
	// ErrBlocked matches kind "blocked": the program is waiting on a dialog,
	// so Send typed nothing.
	ErrBlocked = errors.New("hc: blocked")
	// ErrStatusMismatch matches kind "status_mismatch": the terminal's status
	// was not the one SendRequest.If required, so Send typed nothing.
	ErrStatusMismatch = errors.New("hc: status_mismatch")

	// ErrClosed reports that the connection to the core is over: Close was
	// called, the core or its "hc connect" process went away, or the client
	// ended the connection after a protocol violation. Every call and stream
	// on that connection then fails with an error matching it.
	ErrClosed = errors.New("hc: connection closed")
	// ErrProtocol reports bytes from the core that break the wire contract.
	// When framing or flow control is broken, such as an oversized frame or
	// stream data beyond the granted credit, the client ends the connection.
	// When one reply or one event cannot be decoded, only that call or that
	// subscription fails with it.
	ErrProtocol = errors.New("hc: protocol violation")
	// ErrStreamEnded reports that one stream is over while its connection
	// lives on: a write to an attach stream whose program ended or that the
	// caller closed, a Recv after Close, or an event subscription the core
	// closed.
	ErrStreamEnded = errors.New("hc: stream ended")
)

// expectedKinds maps the kinds callers branch on to their Err* values. These
// are states of a terminal, not failures of the call.
var expectedKinds = map[string]error{
	"not_found":       ErrNotFound,
	"finished":        ErrFinished,
	"unavailable":     ErrUnavailable,
	"blocked":         ErrBlocked,
	"status_mismatch": ErrStatusMismatch,
}

// Error returns the core's message with its kind and, when present, its hint.
func (e *KindError) Error() string {
	if e.Hint == "" {
		return fmt.Sprintf("hc: %s: %s", e.Kind, e.Message)
	}
	return fmt.Sprintf("hc: %s: %s (%s)", e.Kind, e.Message, e.Hint)
}

// Is reports whether target is the Err* value for this error's kind.
func (e *KindError) Is(target error) bool {
	return target != nil && target == expectedKinds[e.Kind]
}
