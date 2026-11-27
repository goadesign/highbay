// These tests cover a viewer's attach stream against the fake core.
package hc

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// attachFake attaches to terminal t7 on a fake core that answers with reply.
func attachFake(t *testing.T, c *Client, core *fakeCore, reply string) *AttachStream {
	t.Helper()
	attached := async(func() (*AttachStream, error) {
		return c.Attach(context.Background(), AttachRequest{Terminal: "t7", Cols: 80, Rows: 24})
	})
	core.reply(core.call("terminal.attach"), reply)
	stream, err := await(t, attached)
	require.NoError(t, err)
	require.NotNil(t, stream)
	return stream
}

// recv returns the stream's next event.
func recv(t *testing.T, stream *AttachStream) (AttachEvent, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	return stream.Recv(ctx)
}

// TestAttachStream follows one viewer from attach to the program's exit: the
// request, each kind of frame in both directions, and the end of the stream.
func TestAttachStream(t *testing.T) {
	c, core := dialFake(t, "resize_notifications", "automatic_input")
	attached := async(func() (*AttachStream, error) {
		return c.Attach(context.Background(), AttachRequest{
			Terminal: "t7",
			Cols:     120,
			Rows:     40,
			History:  200,
			Observe:  true,
			Display:  &Display{FG: "#ffffff", BG: "#000000", Cell: &[2]uint32{9, 18}},
		})
	})
	r := core.call("terminal.attach")
	assert.JSONEq(t, `{"terminal":"t7","cols":120,"rows":40,"history":200,"observe":true,`+
		`"resize_notifications":true,"display":{"fg":"#ffffff","bg":"#000000","cell":[9,18]}}`, string(r.Params))
	core.reply(r, attachReply(5, true, true))
	stream, err := await(t, attached)
	require.NoError(t, err)
	assert.Equal(t, wantTerminal, stream.Terminal)
	assert.True(t, stream.ResizeNotifications)
	assert.True(t, stream.AutomaticInput)

	// The core announces the grid, then paints.
	core.send(5, kindResize, size(100, 30))
	core.send(5, kindData, []byte("paint"))
	core.send(5, 200, []byte("a kind from a newer core"))
	core.send(5, kindData, []byte("more"))
	for _, want := range []AttachEvent{
		TerminalResized{Cols: 100, Rows: 30},
		TerminalOutput{Data: []byte("paint")},
		TerminalOutput{Data: []byte("more")},
	} {
		got, err := recv(t, stream)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}

	// Everything the viewer sends goes out on its channel.
	require.NoError(t, stream.GrantCredit(9))
	require.NoError(t, stream.GrantCredit(0))
	require.NoError(t, stream.WriteInput([]byte("ls\n")))
	require.NoError(t, stream.WriteAutoInput([]byte("\x1b[?1;2c")))
	require.NoError(t, stream.Resize(132, 43))
	require.NoError(t, stream.Focus(true))
	require.NoError(t, stream.Focus(false))
	for _, want := range []frame{
		{channel: 5, kind: kindCredit, payload: u32(9)},
		{channel: 5, kind: kindData, payload: []byte("ls\n")},
		{channel: 5, kind: kindAutomaticInput, payload: []byte("\x1b[?1;2c")},
		{channel: 5, kind: kindResize, payload: size(132, 43)},
		{channel: 5, kind: kindFocus, payload: []byte{1}},
		{channel: 5, kind: kindFocus, payload: []byte{0}},
	} {
		assert.Equal(t, want, core.next())
	}

	// The program exits: the core's closing frame carries how.
	core.send(5, kindData, []byte("bye"))
	core.send(5, kindClose, []byte(`{"reason":"exited","exit_code":0,"signal":null,"finished_ms":1700000000123}`))
	got, err := recv(t, stream)
	require.NoError(t, err)
	assert.Equal(t, TerminalOutput{Data: []byte("bye")}, got)
	got, err = recv(t, stream)
	require.NoError(t, err)
	exit := int32(0)
	assert.Equal(t, TerminalClosed{Reason: CloseReason{Reason: "exited", ExitCode: &exit, FinishedMS: 1700000000123}}, got)
	_, err = recv(t, stream)
	assert.ErrorIs(t, err, io.EOF)

	// A stream the core ended takes no more input and sends nothing on Close.
	assert.ErrorIs(t, stream.WriteInput([]byte("x")), ErrStreamEnded)
	assert.ErrorIs(t, stream.Resize(1, 1), ErrStreamEnded)
	assert.NoError(t, stream.GrantCredit(3))
	assert.NoError(t, stream.Close())
	status := async(func() (ServerStatus, error) { return c.ServerStatus(context.Background()) })
	core.reply(core.call("server.status"), `{"version":"0.1.22"}`)
	_, err = await(t, status)
	require.NoError(t, err)
}

// TestAttachAutomaticInput checks that terminal-generated replies are tagged
// only when both the hello and the attach reply confirm support, and fall
// back to plain input otherwise.
func TestAttachAutomaticInput(t *testing.T) {
	for _, test := range []struct {
		name      string
		advertise bool
		confirm   bool
		want      frameKind
	}{
		{name: "core and attach path support it", advertise: true, confirm: true, want: kindAutomaticInput},
		{name: "attach path does not confirm", advertise: true, confirm: false, want: kindData},
		{name: "core does not advertise it", advertise: false, confirm: true, want: kindData},
		{name: "neither", advertise: false, confirm: false, want: kindData},
	} {
		t.Run(test.name, func(t *testing.T) {
			var features []string
			if test.advertise {
				features = []string{"automatic_input"}
			}
			c, core := dialFake(t, features...)
			stream := attachFake(t, c, core, attachReply(5, false, test.confirm))
			assert.Equal(t, test.want == kindAutomaticInput, stream.AutomaticInput)
			assert.False(t, stream.ResizeNotifications)

			require.NoError(t, stream.WriteAutoInput([]byte("reply")))
			assert.Equal(t, frame{channel: 5, kind: test.want, payload: []byte("reply")}, core.next())
			require.NoError(t, stream.WriteInput([]byte("typed")))
			assert.Equal(t, frame{channel: 5, kind: kindData, payload: []byte("typed")}, core.next())
		})
	}
}

// TestAttachClose checks a viewer detaching: the closing frame, output still
// in flight being dropped, and the stream's state afterwards.
func TestAttachClose(t *testing.T) {
	c, core := dialFake(t)
	stream := attachFake(t, c, core, attachReply(5, false, false))
	core.send(5, kindData, []byte("unread"))

	require.NoError(t, stream.Close())
	assert.Equal(t, frame{channel: 5, kind: kindClose, payload: []byte{}}, core.next())
	_, err := recv(t, stream)
	assert.ErrorIs(t, err, ErrStreamEnded)
	assert.ErrorIs(t, stream.WriteInput([]byte("x")), ErrStreamEnded)
	assert.NoError(t, stream.Close(), "closing twice sends nothing more")

	// Output the core sent before it saw the close, then its confirmation.
	core.send(5, kindData, bytes.Repeat([]byte{'x'}, window))
	core.send(5, kindData, bytes.Repeat([]byte{'x'}, window))
	core.send(5, kindClose, nil)
	// A second close of the same channel, as when both sides closed at once.
	core.send(5, kindClose, nil)

	next := attachFake(t, c, core, attachReply(6, false, false))
	core.send(6, kindData, []byte("fresh"))
	got, err := recv(t, next)
	require.NoError(t, err)
	assert.Equal(t, TerminalOutput{Data: []byte("fresh")}, got)
	assert.NoError(t, c.Err())
}

// TestAttachInputChunks checks that a large paste is split into bounded
// frames in order.
func TestAttachInputChunks(t *testing.T) {
	c, core := dialFake(t)
	stream := attachFake(t, c, core, attachReply(5, false, false))
	paste := make([]byte, 2*inputChunk+100)
	for i := range paste {
		paste[i] = byte(i)
	}
	written := async(func() (struct{}, error) { return struct{}{}, stream.WriteInput(paste) })
	var got []byte
	for _, want := range []int{inputChunk, inputChunk, 100} {
		f := core.next()
		assert.Equal(t, uint32(5), f.channel)
		assert.Equal(t, kindData, f.kind)
		assert.Len(t, f.payload, want)
		got = append(got, f.payload...)
	}
	_, err := await(t, written)
	require.NoError(t, err)
	assert.Equal(t, paste, got)
}

// TestAttachRefused covers attach calls the core answers with an error and
// stream frames that break the contract.
func TestAttachRefused(t *testing.T) {
	for _, test := range []struct {
		kind string
		want error
	}{
		{kind: "not_found", want: ErrNotFound},
		{kind: "finished", want: ErrFinished},
		{kind: "unavailable", want: ErrUnavailable},
	} {
		t.Run(test.kind, func(t *testing.T) {
			c, core := dialFake(t)
			attached := async(func() (*AttachStream, error) {
				return c.Attach(context.Background(), AttachRequest{Terminal: "t7", Cols: 80, Rows: 24})
			})
			core.refuse(core.call("terminal.attach"), test.kind, "t7 cannot be attached", "")
			stream, err := await(t, attached)
			assert.Nil(t, stream)
			assert.ErrorIs(t, err, test.want)
		})
	}

	t.Run("reply names no channel", func(t *testing.T) {
		c, core := dialFake(t)
		attached := async(func() (*AttachStream, error) {
			return c.Attach(context.Background(), AttachRequest{Terminal: "t7", Cols: 80, Rows: 24})
		})
		core.reply(core.call("terminal.attach"), `{"terminal":`+terminalJSON+`}`)
		stream, err := await(t, attached)
		assert.Nil(t, stream)
		assert.ErrorIs(t, err, ErrProtocol)
	})

	for _, test := range []struct {
		name    string
		kind    frameKind
		payload []byte
	}{
		{name: "resize of the wrong length", kind: kindResize, payload: []byte{80, 0, 24}},
		{name: "close reason that is not JSON", kind: kindClose, payload: []byte("gone")},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, core := dialFake(t)
			stream := attachFake(t, c, core, attachReply(5, true, false))
			core.send(5, test.kind, test.payload)
			_, err := recv(t, stream)
			assert.ErrorIs(t, err, ErrProtocol)
		})
	}
}

// TestAttachAbandoned checks that a viewer who stops waiting for the attach
// reply leaves no stream behind: the channel the late reply names is closed.
func TestAttachAbandoned(t *testing.T) {
	c, core := dialFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	attached := async(func() (*AttachStream, error) {
		return c.Attach(ctx, AttachRequest{Terminal: "t7", Cols: 80, Rows: 24})
	})
	r := core.call("terminal.attach")
	cancel()
	stream, err := await(t, attached)
	assert.Nil(t, stream)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, "$/cancelRequest", core.request().Method)

	core.reply(r, attachReply(9, false, false))
	assert.Equal(t, frame{channel: 9, kind: kindClose, payload: []byte{}}, core.next())
	core.send(9, kindData, []byte("first paint nobody reads"))
	core.send(9, kindClose, nil)

	// The connection keeps working.
	next := attachFake(t, c, core, attachReply(10, false, false))
	core.send(10, kindData, []byte("fresh"))
	got, err := recv(t, next)
	require.NoError(t, err)
	assert.Equal(t, TerminalOutput{Data: []byte("fresh")}, got)
}

// TestAttachConnectionLost checks what a viewer sees when the connection
// ends: queued output first, then the connection's error.
func TestAttachConnectionLost(t *testing.T) {
	c, core := dialFake(t)
	stream := attachFake(t, c, core, attachReply(5, false, false))
	core.send(5, kindData, []byte("last"))
	// A call answered after the output proves the reader has queued it.
	status := async(func() (ServerStatus, error) { return c.ServerStatus(context.Background()) })
	core.reply(core.call("server.status"), `{"version":"0.1.22"}`)
	_, err := await(t, status)
	require.NoError(t, err)
	require.NoError(t, core.core.Close())
	waitDone(t, c)

	got, err := recv(t, stream)
	require.NoError(t, err)
	assert.Equal(t, TerminalOutput{Data: []byte("last")}, got)
	_, err = recv(t, stream)
	assert.ErrorIs(t, err, ErrClosed)
	assert.ErrorIs(t, stream.WriteInput([]byte("x")), ErrClosed)
	assert.NoError(t, stream.Close())

	t.Run("receive gives up with its context", func(t *testing.T) {
		c, core := dialFake(t)
		stream := attachFake(t, c, core, attachReply(5, false, false))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := stream.Recv(ctx)
		assert.ErrorIs(t, err, context.Canceled)

		// A context that is over wins even when output is waiting, and the
		// output stays for the next receive.
		core.send(5, kindData, []byte("waiting"))
		status := async(func() (ServerStatus, error) { return c.ServerStatus(context.Background()) })
		core.reply(core.call("server.status"), `{"version":"0.1.22"}`)
		_, err = await(t, status)
		require.NoError(t, err)
		_, err = stream.Recv(ctx)
		assert.ErrorIs(t, err, context.Canceled)
		got, err := recv(t, stream)
		require.NoError(t, err)
		assert.Equal(t, TerminalOutput{Data: []byte("waiting")}, got)
	})
}

// TestAttachUndecodableReply checks that a reply which opens a channel but
// cannot be decoded leaves no viewer behind on either side.
func TestAttachUndecodableReply(t *testing.T) {
	c, core := dialFake(t)
	attached := async(func() (*AttachStream, error) {
		return c.Attach(context.Background(), AttachRequest{Terminal: "t7", Cols: 80, Rows: 24})
	})
	core.reply(core.call("terminal.attach"), `{"channel":5,"terminal":"not a record"}`)
	stream, err := await(t, attached)
	assert.Nil(t, stream)
	assert.ErrorIs(t, err, ErrProtocol)
	assert.Equal(t, frame{channel: 5, kind: kindClose, payload: []byte{}}, core.next())
	c.mu.Lock()
	assert.Empty(t, c.streams)
	c.mu.Unlock()
	assert.NoError(t, c.Err())
}
