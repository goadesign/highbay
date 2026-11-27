// These tests cover the handshake, calls on channel 0 and how the connection
// ends, against the fake core.
package hc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestDialHandshake checks the plain hello line the client sends and that the
// connection then speaks frames.
func TestDialHandshake(t *testing.T) {
	c, core := dialFake(t, "resize_notifications", "automatic_input")

	assert.JSONEq(t, `{"jsonrpc":"2.0","id":0,"method":"hello","params":{"framed":true,"generation":1,`+
		`"features":["resize_notifications","automatic_input"]}}`, string(<-core.hello))
	assert.Equal(t, Hello{
		Generation: 1,
		Version:    "0.1.22",
		Features:   []string{"framed", "attach", "events", "resize_notifications", "automatic_input"},
		Framed:     true,
	}, c.Hello())
	assert.NoError(t, c.Err())

	status := async(func() (ServerStatus, error) { return c.ServerStatus(context.Background()) })
	r := core.call("server.status")
	assert.Empty(t, r.Params, "a call without parameters sends none")
	core.reply(r, `{"core":{"id":"core-id","name":"box"},"version":"0.1.22","pid":77,"uptime_ms":9000,`+
		`"holders":[],"live":2,"finished":1}`)
	got, err := await(t, status)
	require.NoError(t, err)
	assert.Equal(t, ServerStatus{
		Core:     CoreInfo{ID: "core-id", Name: "box"},
		Version:  "0.1.22",
		PID:      77,
		UptimeMS: 9000,
		Live:     2,
		Finished: 1,
	}, got)
}

// TestDialRefused covers every way the handshake fails.
func TestDialRefused(t *testing.T) {
	write := func(text string) func(net.Conn) {
		return func(core net.Conn) {
			// The client may stop reading before the whole answer is out.
			if _, err := io.WriteString(core, text); err != nil {
				return
			}
		}
	}
	for _, test := range []struct {
		name   string
		answer func(core net.Conn)
		cancel bool
		check  func(t *testing.T, err error)
	}{
		{
			name: "core speaks another generation",
			answer: write(`{"jsonrpc":"2.0","id":0,"error":{"code":-32000,"message":"protocol generation 1 not supported",` +
				`"data":{"kind":"unsupported_generation","hint":"this core speaks generation 2"}}}` + "\n"),
			check: func(t *testing.T, err error) {
				var kind *KindError
				require.ErrorAs(t, err, &kind)
				assert.Equal(t, "unsupported_generation", kind.Kind)
			},
		},
		{
			name: "core stays in plain mode",
			answer: write(`{"jsonrpc":"2.0","id":0,"result":{"generation":1,"version":"0.1.22","framed":false,` +
				`"methods":[],"features":[]}}` + "\n"),
			check: func(t *testing.T, err error) { assert.ErrorIs(t, err, ErrProtocol) },
		},
		{
			name:   "answer is not JSON",
			answer: write("hc: no core\n"),
			check:  func(t *testing.T, err error) { assert.ErrorIs(t, err, ErrProtocol) },
		},
		{
			name: "answer never ends its line",
			// The limit is checked a read buffer at a time.
			answer: write(strings.Repeat("a", handshakeLineLimit+readBuffer)),
			check:  func(t *testing.T, err error) { assert.ErrorIs(t, err, ErrProtocol) },
		},
		{
			name: "connection closes before the answer",
			answer: func(core net.Conn) {
				if err := core.Close(); err != nil {
					return
				}
			},
			check: func(t *testing.T, err error) { assert.ErrorIs(t, err, io.EOF) },
		},
		{
			name:   "caller gives up waiting",
			answer: func(net.Conn) {},
			cancel: true,
			check:  func(t *testing.T, err error) { assert.ErrorIs(t, err, context.Canceled) },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			core := newFakeCore(t, test.answer)
			ctx, cancel := context.WithTimeout(context.Background(), testWait)
			defer cancel()
			if test.cancel {
				go func() {
					<-core.hello
					cancel()
				}()
			}
			c, err := Dial(ctx, core.connector())
			require.Error(t, err)
			assert.Nil(t, c)
			test.check(t, err)
		})
	}

	t.Run("stream cannot be opened", func(t *testing.T) {
		refused := errors.New("no hc on PATH")
		c, err := Dial(context.Background(), func(context.Context) (io.ReadWriteCloser, func() error, error) {
			return nil, nil, refused
		})
		assert.Nil(t, c)
		assert.ErrorIs(t, err, refused)
	})
}

// TestCallsRunSideBySide sends calls concurrently and answers them in reverse
// order: each caller gets the reply to its own request.
func TestCallsRunSideBySide(t *testing.T) {
	c, core := dialFake(t)
	const calls = 8
	pending := make(map[string]<-chan outcome[Terminal])
	for i := range calls {
		address := fmt.Sprintf("t%d", i)
		pending[address] = async(func() (Terminal, error) { return c.Get(context.Background(), address) })
	}
	requests := make([]request, 0, calls)
	for range calls {
		requests = append(requests, core.call("terminal.get"))
	}
	for i := len(requests) - 1; i >= 0; i-- {
		var params terminalParams
		require.NoError(t, json.Unmarshal(requests[i].Params, &params))
		core.reply(requests[i], fmt.Sprintf(`{"address":%q,"state":"live"}`, params.Terminal))
	}
	for address, done := range pending {
		got, err := await(t, done)
		require.NoError(t, err)
		assert.Equal(t, address, got.Address)
	}
}

// TestCallEncoding checks, for each call, the request the core receives and
// the value decoded from its reply.
func TestCallEncoding(t *testing.T) {
	started := true
	for _, test := range []struct {
		method string
		call   func(ctx context.Context, c *Client) (any, error)
		params string
		result string
		want   any
	}{
		{
			method: "terminal.run",
			call: func(ctx context.Context, c *Client) (any, error) {
				return c.Run(ctx, RunRequest{
					Argv:          []string{"claude"},
					Cwd:           "/work",
					Env:           map[string]string{"HIGHBAY_ROLE": "guide"},
					Labels:        map[string]string{"highbay.role": "guide"},
					Name:          "guide",
					NoParent:      true,
					OutliveParent: true,
					NoNotify:      true,
					Size:          &Size{Cols: 120, Rows: 40},
				})
			},
			params: `{"argv":["claude"],"cwd":"/work","env":{"HIGHBAY_ROLE":"guide"},` +
				`"labels":{"highbay.role":"guide"},"name":"guide","no_parent":true,"outlive_parent":true,` +
				`"no_notify":true,"size":{"cols":120,"rows":40}}`,
			result: terminalJSON,
			want:   wantTerminal,
		},
		{
			method: "terminal.run",
			call:   func(ctx context.Context, c *Client) (any, error) { return c.Run(ctx, RunRequest{}) },
			params: `{}`,
			result: terminalJSON,
			want:   wantTerminal,
		},
		{
			method: "terminal.list",
			call: func(ctx context.Context, c *Client) (any, error) {
				return c.List(ctx, ListRequest{All: true, Labels: map[string]string{"highbay.project": "p1"}})
			},
			params: `{"all":true,"labels":{"highbay.project":"p1"}}`,
			result: `{"terminals":[` + terminalJSON + `],"hidden_finished":0,"machines":[]}`,
			want:   []Terminal{wantTerminal},
		},
		{
			method: "terminal.get",
			call:   func(ctx context.Context, c *Client) (any, error) { return c.Get(ctx, "t7") },
			params: `{"terminal":"t7"}`,
			result: terminalJSON,
			want:   wantTerminal,
		},
		{
			method: "terminal.label",
			call: func(ctx context.Context, c *Client) (any, error) {
				return c.Label(ctx, "t7", map[string]string{"highbay.role": "guide", "stale": ""})
			},
			params: `{"terminal":"t7","labels":{"highbay.role":"guide","stale":""}}`,
			result: terminalJSON,
			want:   wantTerminal,
		},
		{
			method: "terminal.send",
			call: func(ctx context.Context, c *Client) (any, error) {
				return c.Send(ctx, SendRequest{Terminal: "t7", Text: "read your mail", If: "idle", Force: true})
			},
			params: `{"terminal":"t7","text":"read your mail","if":"idle","force":true}`,
			result: `{"turn":4,"status":"idle","started":true}`,
			want:   SendResult{Turn: 4, Status: "idle", Started: &started},
		},
		{
			method: "terminal.send",
			call: func(ctx context.Context, c *Client) (any, error) {
				return c.Send(ctx, SendRequest{Terminal: "t7", Text: "hello"})
			},
			params: `{"terminal":"t7","text":"hello"}`,
			result: `{"turn":4,"status":null,"started":null}`,
			want:   SendResult{Turn: 4},
		},
		{
			method: "terminal.keys",
			call: func(ctx context.Context, c *Client) (any, error) {
				return c.Keys(ctx, "t7", []string{"enter", "ctrl+c"})
			},
			params: `{"terminal":"t7","keys":["enter","ctrl+c"]}`,
			result: `{"turn":4,"status":"working"}`,
			want:   KeysResult{Turn: 4, Status: "working"},
		},
		{
			method: "terminal.interrupt",
			call:   func(ctx context.Context, c *Client) (any, error) { return c.Interrupt(ctx, "t7") },
			params: `{"terminal":"t7"}`,
			result: `{"outcome":"stopped","key":"esc"}`,
			want:   InterruptResult{Outcome: "stopped", Key: "esc"},
		},
		{
			method: "terminal.kill",
			call:   func(ctx context.Context, c *Client) (any, error) { return c.Kill(ctx, "t7") },
			params: `{"terminal":"t7"}`,
			result: terminalJSON,
			want:   wantTerminal,
		},
		{
			method: "terminal.rm",
			call:   func(ctx context.Context, c *Client) (any, error) { return nil, c.Remove(ctx, "t7") },
			params: `{"terminal":"t7"}`,
			result: `{"removed":"t7"}`,
			want:   nil,
		},
	} {
		t.Run(test.method+" "+test.params, func(t *testing.T) {
			c, core := dialFake(t)
			done := async(func() (any, error) { return test.call(context.Background(), c) })
			r := core.call(test.method)
			assert.JSONEq(t, test.params, string(r.Params))
			core.reply(r, test.result)
			got, err := await(t, done)
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

// TestCallErrors checks how the core's error kinds reach the caller.
func TestCallErrors(t *testing.T) {
	sentinels := []error{ErrNotFound, ErrFinished, ErrUnavailable, ErrBlocked, ErrStatusMismatch}
	for _, test := range []struct {
		kind string
		is   error
	}{
		{kind: "not_found", is: ErrNotFound},
		{kind: "finished", is: ErrFinished},
		{kind: "unavailable", is: ErrUnavailable},
		{kind: "blocked", is: ErrBlocked},
		{kind: "status_mismatch", is: ErrStatusMismatch},
		{kind: "live"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			c, core := dialFake(t)
			done := async(func() (Terminal, error) { return c.Get(context.Background(), "t9") })
			core.refuse(core.call("terminal.get"), test.kind, "no terminal t9", "see `hc ls --all`")
			_, err := await(t, done)
			var kind *KindError
			require.ErrorAs(t, err, &kind)
			assert.Equal(t, KindError{Code: -32000, Kind: test.kind, Message: "no terminal t9", Hint: "see `hc ls --all`"}, *kind)
			assert.Equal(t, "hc: "+test.kind+": no terminal t9 (see `hc ls --all`)", err.Error())
			for _, sentinel := range sentinels {
				assert.Equal(t, sentinel == test.is, errors.Is(err, sentinel), sentinel.Error())
			}
		})
	}

	t.Run("error without a kind", func(t *testing.T) {
		c, core := dialFake(t)
		done := async(func() (Terminal, error) { return c.Get(context.Background(), "t9") })
		r := core.call("terminal.get")
		core.send(0, kindData, fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32603,"message":"boom"}}`, *r.ID))
		_, err := await(t, done)
		var kind *KindError
		require.ErrorAs(t, err, &kind)
		assert.Equal(t, KindError{Code: -32603, Kind: "internal", Message: "boom"}, *kind)
		assert.Equal(t, "hc: internal: boom", err.Error())
	})

	t.Run("result of the wrong shape", func(t *testing.T) {
		c, core := dialFake(t)
		done := async(func() (Terminal, error) { return c.Get(context.Background(), "t9") })
		core.reply(core.call("terminal.get"), `"t9"`)
		_, err := await(t, done)
		assert.ErrorIs(t, err, ErrProtocol)
		assert.NoError(t, c.Err(), "one undecodable result does not end the connection")
	})
}

// TestCallCancelled checks that a caller who stops waiting returns at once,
// that the core is told, and that the late reply disturbs nothing.
func TestCallCancelled(t *testing.T) {
	c, core := dialFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := async(func() (SendResult, error) {
		return c.Send(ctx, SendRequest{Terminal: "t7", Text: "hello"})
	})
	sent := core.call("terminal.send")
	cancel()
	_, err := await(t, done)
	assert.ErrorIs(t, err, context.Canceled)

	note := core.request()
	assert.Nil(t, note.ID, "a cancellation expects no reply")
	assert.Equal(t, "$/cancelRequest", note.Method)
	assert.JSONEq(t, fmt.Sprintf(`{"id":%d}`, *sent.ID), string(note.Params))

	core.refuse(sent, "cancelled", "send cancelled", "")
	next := async(func() (Terminal, error) { return c.Get(context.Background(), "t7") })
	core.reply(core.call("terminal.get"), terminalJSON)
	got, err := await(t, next)
	require.NoError(t, err)
	assert.Equal(t, wantTerminal, got)
}

// TestCallNotSentWhenAlreadyCancelled checks that a caller whose context is
// already over sends nothing: the core cannot take back a Run it received.
func TestCallNotSentWhenAlreadyCancelled(t *testing.T) {
	c, core := dialFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Run(ctx, RunRequest{Argv: []string{"claude"}})
	assert.ErrorIs(t, err, context.Canceled)
	_, err = c.Attach(ctx, AttachRequest{Terminal: "t7", Cols: 80, Rows: 24})
	assert.ErrorIs(t, err, context.Canceled)

	// The next frame the core sees is the next call, not either of those.
	next := async(func() (Terminal, error) { return c.Get(context.Background(), "t7") })
	core.reply(core.call("terminal.get"), terminalJSON)
	_, err = await(t, next)
	require.NoError(t, err)
	c.mu.Lock()
	assert.Zero(t, c.openers)
	c.mu.Unlock()
}

// TestConnectionLost checks what callers see when the core goes away.
func TestConnectionLost(t *testing.T) {
	c, core := dialFake(t)
	done := async(func() (Terminal, error) { return c.Get(context.Background(), "t7") })
	core.call("terminal.get")
	require.NoError(t, core.core.Close())

	_, err := await(t, done)
	assert.ErrorIs(t, err, ErrClosed)
	waitDone(t, c)
	assert.ErrorIs(t, c.Err(), ErrClosed)
	assert.ErrorIs(t, c.Err(), io.EOF, "the cause stays visible")

	_, err = c.Get(context.Background(), "t7")
	assert.ErrorIs(t, err, ErrClosed)
	assert.NoError(t, c.Close())
	assert.NoError(t, c.Close(), "closing twice is safe")
}

// TestClose checks that Close ends calls in flight.
func TestClose(t *testing.T) {
	c, core := dialFake(t)
	done := async(func() (Terminal, error) { return c.Get(context.Background(), "t7") })
	core.call("terminal.get")
	require.NoError(t, c.Close())
	_, err := await(t, done)
	assert.ErrorIs(t, err, ErrClosed)
	waitDone(t, c)
	assert.ErrorIs(t, c.Err(), ErrClosed)
}

// TestProtocolViolations checks that bytes breaking the wire contract end the
// connection with ErrProtocol.
func TestProtocolViolations(t *testing.T) {
	for _, test := range []struct {
		name    string
		violate func(t *testing.T, c *Client, core *fakeCore)
	}{
		{
			name: "frame longer than the limit",
			violate: func(t *testing.T, _ *Client, core *fakeCore) {
				header := appendFrame(nil, 0, kindData, nil)
				copy(header[5:], u32(MaxPayload+1))
				_, err := core.core.Write(header)
				require.NoError(t, err)
			},
		},
		{
			name: "channel 0 message that is not JSON",
			violate: func(_ *testing.T, _ *Client, core *fakeCore) {
				core.send(0, kindData, []byte("not json"))
			},
		},
		{
			name: "stream output beyond its credit",
			violate: func(t *testing.T, c *Client, core *fakeCore) {
				attached := async(func() (*AttachStream, error) {
					return c.Attach(context.Background(), AttachRequest{Terminal: "t7", Cols: 80, Rows: 24})
				})
				core.reply(core.call("terminal.attach"), attachReply(5, false, true))
				_, err := await(t, attached)
				require.NoError(t, err)
				chunk := bytes.Repeat([]byte{'x'}, window/2)
				core.send(5, kindData, chunk)
				core.send(5, kindData, chunk)
				core.send(5, kindData, []byte{'x'})
			},
		},
		{
			name: "more early bytes than one unopened channel may carry",
			violate: func(_ *testing.T, c *Client, core *fakeCore) {
				// The reply never comes: the subscription waits while frames
				// for its future channel exceed the initial credit.
				async(func() (struct{}, error) {
					return struct{}{}, c.SubscribeEvents(context.Background(), nil, func(StreamUpdate) {})
				})
				core.call("events.subscribe")
				core.send(3, kindData, bytes.Repeat([]byte{'x'}, window))
				core.send(3, kindData, []byte{'x'})
			},
		},
		{
			name: "more early bytes than the calls in flight may open channels for",
			violate: func(_ *testing.T, c *Client, core *fakeCore) {
				async(func() (struct{}, error) {
					return struct{}{}, c.SubscribeEvents(context.Background(), nil, func(StreamUpdate) {})
				})
				core.call("events.subscribe")
				core.send(3, kindData, bytes.Repeat([]byte{'x'}, window))
				core.send(4, kindData, []byte{'x'})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, core := dialFake(t)
			test.violate(t, c, core)
			waitDone(t, c)
			assert.ErrorIs(t, c.Err(), ErrProtocol)
			assert.ErrorIs(t, c.Err(), ErrClosed, "whatever ended the connection, it is closed")
			_, err := c.Get(context.Background(), "t7")
			assert.ErrorIs(t, err, ErrClosed)
		})
	}
}

// TestFrameCodec checks the 9-byte header layout and how reads end.
func TestFrameCodec(t *testing.T) {
	encoded := appendFrame(nil, 0x01020304, kindResize, []byte{0xaa, 0xbb})
	assert.Equal(t, []byte{0x04, 0x03, 0x02, 0x01, 3, 2, 0, 0, 0, 0xaa, 0xbb}, encoded)

	for _, test := range []struct {
		name  string
		input []byte
		want  frame
		err   error
	}{
		{name: "whole frame", input: encoded, want: frame{channel: 0x01020304, kind: kindResize, payload: []byte{0xaa, 0xbb}}},
		{name: "empty payload", input: appendFrame(nil, 7, kindClose, nil), want: frame{channel: 7, kind: kindClose, payload: []byte{}}},
		{name: "end between frames", input: nil, err: io.EOF},
		{name: "end inside the header", input: encoded[:4], err: io.ErrUnexpectedEOF},
		{name: "end inside the payload", input: encoded[:10], err: io.ErrUnexpectedEOF},
		{name: "end before any payload", input: encoded[:9], err: io.ErrUnexpectedEOF},
		{name: "payload at the limit is read", input: append(appendFrame(nil, 1, kindData, nil)[:5], u32(MaxPayload)...), err: io.ErrUnexpectedEOF},
		{name: "payload over the limit", input: append(appendFrame(nil, 1, kindData, nil)[:5], u32(MaxPayload+1)...), err: ErrProtocol},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := readFrame(bytes.NewReader(test.input))
			if test.err != nil {
				assert.ErrorIs(t, err, test.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

// TestCallSpans checks which call outcomes a span marks as failures: the
// kinds callers branch on are states, everything else is an error.
func TestCallSpans(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status codes.Code
		kind   string
	}{
		{name: "success", status: codes.Unset},
		{name: "expected kind", err: &KindError{Kind: "not_found", Message: "no terminal t9"}, status: codes.Unset, kind: "not_found"},
		{name: "other kind", err: &KindError{Kind: "spawn_failed", Message: "no such program"}, status: codes.Error, kind: "spawn_failed"},
		{name: "connection lost", err: ErrClosed, status: codes.Error},
	} {
		t.Run(test.name, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			_, span := provider.Tracer("test").Start(context.Background(), "highbay.hc.call")
			endCall(span, test.err)

			spans := exporter.GetSpans()
			require.Len(t, spans, 1)
			assert.Equal(t, test.status, spans[0].Status.Code)
			kind := ""
			for _, attribute := range spans[0].Attributes {
				if attribute.Key == "hc.error_kind" {
					kind = attribute.Value.AsString()
				}
			}
			assert.Equal(t, test.kind, kind)
			assert.Equal(t, test.status == codes.Error, len(spans[0].Events) == 1, "a failure records its error")
		})
	}
}

// attachReply is a terminal.attach result naming channel.
func attachReply(channel uint32, resizeNotifications, automaticInput bool) string {
	return fmt.Sprintf(`{"channel":%d,"terminal":%s,"resize_notifications":%t,"automatic_input":%t}`,
		channel, terminalJSON, resizeNotifications, automaticInput)
}
