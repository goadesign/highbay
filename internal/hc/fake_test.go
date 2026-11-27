// This file is a fake herdr-core over an in-memory pipe, shared by the unit
// tests of this package.
package hc

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type (
	// fakeCore plays herdr-core's side of one connection over an in-memory
	// pipe. It reads the hello line, answers it, and then collects every
	// frame the client sends so a test can inspect them in order.
	fakeCore struct {
		t *testing.T
		// client and core are the two ends of the pipe.
		client, core net.Conn
		// hello receives the client's handshake line.
		hello chan []byte
		// frames receives the client's frames after the handshake; it is
		// closed when the connection ends.
		frames chan frame
		// write serializes the test's frames onto the pipe.
		write sync.Mutex
	}

	// request is one JSON-RPC request the fake core received on channel 0.
	request struct {
		ID     *uint64         `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}

	// outcome is the result of a client call made on another goroutine.
	outcome[T any] struct {
		value T
		err   error
	}
)

const (
	// testWait bounds every wait for the other side, so a broken test fails
	// instead of hanging. No passing test waits this long.
	testWait = 5 * time.Second
	// terminalJSON is a terminal record as the core sends it.
	terminalJSON = `{"address":"t7","name":null,"labels":{"highbay.role":"guide"},"argv":["claude"],` +
		`"cwd":"/work","state":"live","pid":4242,"created_ms":1700000000000,"screen":"exact",` +
		`"cols":120,"rows":40,"title":"guide","turn":3,"reported":true,` +
		`"status":{"state":"working","app":"claude","title":"","msg":""},` +
		`"foreground":{"pid":4243,"name":"node","argv":["node","claude"],"cwd":"/work"}}`
)

// wantTerminal is terminalJSON decoded.
var wantTerminal = Terminal{
	Address:    "t7",
	Labels:     map[string]string{"highbay.role": "guide"},
	Argv:       []string{"claude"},
	Cwd:        "/work",
	State:      StateLive,
	PID:        4242,
	CreatedMS:  1700000000000,
	Cols:       120,
	Rows:       40,
	Title:      "guide",
	Turn:       3,
	Reported:   true,
	Status:     &Status{State: "working", App: "claude"},
	Foreground: &Foreground{PID: 4243, Name: "node", Argv: []string{"node", "claude"}, Cwd: "/work"},
}

// helloAnswer is the core's one-line answer to hello, advertising features.
func helloAnswer(features ...string) string {
	list, err := json.Marshal(append([]string{"framed", "attach", "events"}, features...))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":0,"result":{"generation":1,"version":"0.1.22",`+
		`"core":{"id":"core-id","name":"box"},"framed":true,"methods":[],"features":%s}}`+"\n", list)
}

// newFakeCore starts a fake core. After reading the hello line it calls
// answer with its end of the pipe; answer writes whatever the scenario's core
// says next. It then collects frames until the connection ends.
func newFakeCore(t *testing.T, answer func(core net.Conn)) *fakeCore {
	t.Helper()
	client, core := net.Pipe()
	f := &fakeCore{
		t:      t,
		client: client,
		core:   core,
		hello:  make(chan []byte, 1),
		frames: make(chan frame, 1024),
	}
	t.Cleanup(func() { require.NoError(t, core.Close()) })
	go f.serve(answer)
	return f
}

// dialFake dials a Client against a fake core that advertises features.
func dialFake(t *testing.T, features ...string) (*Client, *fakeCore) {
	t.Helper()
	f := newFakeCore(t, func(core net.Conn) {
		// A failed write means the client already went away; the test's own
		// assertions report that.
		if _, err := io.WriteString(core, helloAnswer(features...)); err != nil {
			return
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	c, err := Dial(ctx, f.connector())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	return c, f
}

// connector hands out the client end of the pipe.
func (f *fakeCore) connector() Connector {
	return func(context.Context) (io.ReadWriteCloser, func() error, error) {
		return f.client, func() error { return nil }, nil
	}
}

// serve reads the hello line, lets answer reply, then collects frames.
func (f *fakeCore) serve(answer func(core net.Conn)) {
	defer close(f.frames)
	r := bufio.NewReader(f.core)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return
	}
	f.hello <- line
	answer(f.core)
	for {
		fr, err := readFrame(r)
		if err != nil {
			return
		}
		f.frames <- fr
	}
}

// next returns the next frame the client sent.
func (f *fakeCore) next() frame {
	f.t.Helper()
	select {
	case fr, open := <-f.frames:
		require.True(f.t, open, "the client closed the connection")
		return fr
	case <-time.After(testWait):
		require.FailNow(f.t, "no frame from the client")
		return frame{}
	}
}

// request returns the next frame, which must be a channel-0 JSON-RPC message.
func (f *fakeCore) request() request {
	f.t.Helper()
	fr := f.next()
	require.Equal(f.t, uint32(0), fr.channel)
	require.Equal(f.t, kindData, fr.kind)
	var r request
	require.NoError(f.t, json.Unmarshal(fr.payload, &r))
	return r
}

// call returns the next request, which must be a call of method.
func (f *fakeCore) call(method string) request {
	f.t.Helper()
	r := f.request()
	require.Equal(f.t, method, r.Method)
	require.NotNil(f.t, r.ID)
	return r
}

// send writes one frame to the client.
func (f *fakeCore) send(channel uint32, kind frameKind, payload []byte) {
	f.t.Helper()
	f.write.Lock()
	defer f.write.Unlock()
	require.NoError(f.t, f.core.SetWriteDeadline(time.Now().Add(testWait)))
	_, err := f.core.Write(appendFrame(nil, channel, kind, payload))
	require.NoError(f.t, err)
}

// reply answers call r with result, given as JSON.
func (f *fakeCore) reply(r request, result string) {
	f.t.Helper()
	f.send(0, kindData, fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%d,"result":%s}`, *r.ID, result))
}

// refuse answers call r with the core's error of the given kind.
func (f *fakeCore) refuse(r request, kind, message, hint string) {
	f.t.Helper()
	f.send(0, kindData, fmt.Appendf(nil,
		`{"jsonrpc":"2.0","id":%d,"error":{"code":-32000,"message":%q,"data":{"kind":%q,"hint":%q}}}`,
		*r.ID, message, kind, hint))
}

// async runs a client call on its own goroutine.
func async[T any](call func() (T, error)) <-chan outcome[T] {
	done := make(chan outcome[T], 1)
	go func() {
		value, err := call()
		done <- outcome[T]{value: value, err: err}
	}()
	return done
}

// await returns the result of an async call.
func await[T any](t *testing.T, done <-chan outcome[T]) (T, error) {
	t.Helper()
	select {
	case o := <-done:
		return o.value, o.err
	case <-time.After(testWait):
		require.FailNow(t, "the call did not return")
		var zero T
		return zero, nil
	}
}

// waitDone waits for the client's connection to end.
func waitDone(t *testing.T, c *Client) {
	t.Helper()
	select {
	case <-c.Done():
	case <-time.After(testWait):
		require.FailNow(t, "the connection did not end")
	}
}

// u32 encodes a little-endian u32 payload.
func u32(n uint32) []byte {
	return binary.LittleEndian.AppendUint32(nil, n)
}

// size encodes a resize payload: columns then rows, little-endian u16.
func size(cols, rows uint16) []byte {
	return binary.LittleEndian.AppendUint16(binary.LittleEndian.AppendUint16(nil, cols), rows)
}
