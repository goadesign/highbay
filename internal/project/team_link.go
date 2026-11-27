// This file holds the project server's one connection to herdr-core and its
// resolved Chorus client, replacing either when it stops working.
package project

import (
	"context"
	"errors"
	"sync"

	"go.opentelemetry.io/otel/trace"
)

type (
	// terminalLink holds the one connection to herdr-core that every team
	// operation and the supervisor share. A connection that ended is closed
	// and replaced on the next use.
	terminalLink struct {
		// mu serializes dialing, so concurrent users share one connection.
		mu sync.Mutex
		// current is the connection in use; nil before the first dial and
		// after close.
		current TeamTerminals
		// closed is true once the server closed the link. No connection is
		// opened afterwards: a dial would start a herdr-core client process
		// that nothing ends.
		closed bool
	}

	// chorusLink holds the Chorus client resolved for the running Chorus
	// server. A failed call forgets it, so the next use resolves the server
	// again, which also starts it when it stopped.
	chorusLink struct {
		// mu serializes resolution.
		mu sync.Mutex
		// current is the resolved client; nil until resolved and after a
		// failed call.
		current TeamChorus
	}
)

// errTeamClosed is returned for a herdr-core connection asked of a server
// that is shutting down.
var errTeamClosed = errors.New("the project server is stopping")

// client returns the live connection to herdr-core, dialing one when none is
// open or the last one ended.
func (l *terminalLink) client(ctx context.Context, dial func(context.Context) (TeamTerminals, error)) (TeamTerminals, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, errTeamClosed
	}
	var ended error
	if l.current != nil {
		select {
		case <-l.current.Done():
			ended = l.current.Close()
			l.current = nil
		default:
			return l.current, nil
		}
	}
	client, err := dial(ctx)
	if err != nil {
		return nil, errors.Join(err, ended)
	}
	if ended != nil {
		// How the previous connection ended explains the reconnect.
		trace.SpanFromContext(ctx).RecordError(ended)
	}
	l.current = client
	return client, nil
}

// close ends the connection, if any, for good: every later client call
// fails with errTeamClosed.
func (l *terminalLink) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.current == nil {
		return nil
	}
	err := l.current.Close()
	l.current = nil
	return err
}

// client returns the resolved Chorus client, resolving the server first when
// needed.
func (l *chorusLink) client(ctx context.Context, connect func(context.Context) (TeamChorus, error)) (TeamChorus, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.current != nil {
		return l.current, nil
	}
	client, err := connect(ctx)
	if err != nil {
		return nil, err
	}
	l.current = client
	return client, nil
}

// forget drops failed, so the next use resolves the Chorus server again. A
// client that another caller already replaced is left alone.
func (l *chorusLink) forget(failed TeamChorus) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.current == failed {
		l.current = nil
	}
}
