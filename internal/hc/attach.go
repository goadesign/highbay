// This file is a viewer's live stream to one terminal: output, resize and
// close events from the core, and input, size, focus and credit to it.

package hc

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

type (
	// AttachStream is a live stream to one terminal, opened by Attach. One
	// goroutine reads it with Recv; any goroutine may write. The core sends
	// output only while the stream has credit, so the reader must call
	// GrantCredit for the output it has passed on, or the stream stalls.
	AttachStream struct {
		// Terminal is the terminal's record when the stream opened.
		Terminal Terminal
		// ResizeNotifications reports whether the core sends TerminalResized
		// on this stream. The core confirms it only for an observing viewer;
		// when false, track the grid through size events instead.
		ResizeNotifications bool
		// AutomaticInput says whether WriteAutoInput tags its bytes as not
		// typed by a person. Attach sets it true only when both the core's
		// hello and the attach reply confirm support; when false the bytes go
		// as plain input.
		AutomaticInput bool

		// stream is the channel the core opened for this viewer.
		stream *stream
	}

	// AttachEvent is what Recv returns: TerminalOutput, TerminalResized or
	// TerminalClosed.
	AttachEvent interface {
		attachEvent()
	}

	// TerminalOutput carries bytes to write to the viewer's terminal: the
	// first paint, then the program's raw output.
	TerminalOutput struct {
		Data []byte
	}

	// TerminalResized announces the terminal's grid. The viewer must apply it
	// before the output that follows, which repaints the screen at that size.
	TerminalResized struct {
		Cols, Rows uint16
	}

	// TerminalClosed is the stream's last event: the program ended, the
	// terminal was lost, or the core closed the stream. Reason is the zero
	// value when the core gave none.
	TerminalClosed struct {
		Reason CloseReason
	}
)

// Attach opens a live stream to a terminal on this connection. The error
// matches ErrNotFound, ErrFinished or ErrUnavailable when the terminal cannot
// be attached. The caller must Close the stream.
func (c *Client) Attach(ctx context.Context, req AttachRequest) (*AttachStream, error) {
	var result attachResult
	s, err := c.open(ctx, "terminal.attach", attachParams{
		Terminal:            req.Terminal,
		Cols:                req.Cols,
		Rows:                req.Rows,
		History:             req.History,
		Observe:             req.Observe,
		ResizeNotifications: true,
		Display:             req.Display,
	}, &result)
	if err != nil {
		return nil, err
	}
	return &AttachStream{
		Terminal:            result.Terminal,
		ResizeNotifications: result.ResizeNotifications,
		AutomaticInput:      c.autoInput && result.AutomaticInput,
		stream:              s,
	}, nil
}

// Recv returns the stream's next event, waiting for one if needed. After
// TerminalClosed it returns io.EOF. It returns ErrStreamEnded after Close,
// ctx's error when ctx is over (leaving any waiting event for a later Recv),
// and the connection's error when the connection ended.
func (a *AttachStream) Recv(ctx context.Context) (AttachEvent, error) {
	for {
		f, err := a.stream.next(ctx)
		if err == errCoreClosed {
			return nil, io.EOF
		}
		if err != nil {
			return nil, err
		}
		switch f.kind {
		case kindData:
			return TerminalOutput{Data: f.payload}, nil
		case kindResize:
			if len(f.payload) != 4 {
				return nil, fmt.Errorf("%w: resize of %d bytes", ErrProtocol, len(f.payload))
			}
			return TerminalResized{
				Cols: binary.LittleEndian.Uint16(f.payload),
				Rows: binary.LittleEndian.Uint16(f.payload[2:]),
			}, nil
		case kindClose:
			var closed TerminalClosed
			if len(f.payload) > 0 {
				if err := json.Unmarshal(f.payload, &closed.Reason); err != nil {
					return nil, fmt.Errorf("%w: close reason: %v", ErrProtocol, err)
				}
			}
			return closed, nil
		}
		// A kind this client does not know: a newer core. Skip it.
	}
}

// WriteInput sends bytes a person typed or pasted. The core passes them to
// the program unchanged. It returns ErrStreamEnded when the stream is over.
func (a *AttachStream) WriteInput(p []byte) error {
	return a.writeChunks(kindData, p)
}

// WriteAutoInput sends bytes the viewer's terminal emulator produced by
// itself, such as answers to the program's queries. When AutomaticInput is
// true the core does not treat them as a person being active; otherwise they
// are sent as plain input, which older cores require.
func (a *AttachStream) WriteAutoInput(p []byte) error {
	if a.AutomaticInput {
		return a.writeChunks(kindAutomaticInput, p)
	}
	return a.writeChunks(kindData, p)
}

// Resize tells the core the viewer's size in cells. The last active viewer
// sets the terminal's size; an observing viewer's size is ignored.
func (a *AttachStream) Resize(cols, rows uint16) error {
	var payload [4]byte
	binary.LittleEndian.PutUint16(payload[:], cols)
	binary.LittleEndian.PutUint16(payload[2:], rows)
	return a.stream.write(kindResize, payload[:])
}

// Focus tells the core whether the viewer gained or lost keyboard focus.
func (a *AttachStream) Focus(focused bool) error {
	payload := []byte{0}
	if focused {
		payload[0] = 1
	}
	return a.stream.write(kindFocus, payload)
}

// GrantCredit tells the core that the viewer has passed on this many output
// bytes, so the core may send that many more. Grant only bytes received in
// TerminalOutput and already written out: the credit is what bounds the
// output held in memory. It does nothing once the stream is over.
func (a *AttachStream) GrantCredit(bytes uint32) error {
	return a.stream.credit(bytes)
}

// Close detaches the viewer. Recv then returns ErrStreamEnded. Closing a
// stream that is already over, or closing twice, does nothing.
func (a *AttachStream) Close() error {
	return a.stream.close()
}

// writeChunks sends p as frames of at most inputChunk bytes. The core accepts
// input split anywhere.
func (a *AttachStream) writeChunks(kind frameKind, p []byte) error {
	for len(p) > 0 {
		n := min(len(p), inputChunk)
		if err := a.stream.write(kind, p[:n]); err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

func (TerminalOutput) attachEvent()  {}
func (TerminalResized) attachEvent() {}
func (TerminalClosed) attachEvent()  {}
