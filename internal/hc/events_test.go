// These tests cover the event subscription and its decoder against the fake
// core.
package hc

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eventLine is one notification line as the core writes it.
func eventLine(seq uint64, kind, terminal, data string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","method":"event","params":{"seq":%d,"type":%q,"terminal":%q,"data":%s}}`+"\n",
		seq, kind, terminal, data)
}

// subscribe starts SubscribeEvents on its own goroutine and returns the
// updates it emits and its final result.
func subscribe(ctx context.Context, c *Client, labels map[string]string) (<-chan StreamUpdate, <-chan outcome[struct{}]) {
	updates := make(chan StreamUpdate, 64)
	done := async(func() (struct{}, error) {
		return struct{}{}, c.SubscribeEvents(ctx, labels, func(update StreamUpdate) { updates <- update })
	})
	return updates, done
}

// nextUpdate returns the next update the subscription emitted.
func nextUpdate(t *testing.T, updates <-chan StreamUpdate) StreamUpdate {
	t.Helper()
	select {
	case update := <-updates:
		return update
	case <-time.After(testWait):
		require.FailNow(t, "no update from the subscription")
		return nil
	}
}

// TestSubscribeEvents follows one subscription: the request, an event that
// arrives before the reply naming its channel, lines split across frames,
// recovery after the core reports missed events, the credit returned for
// every byte, and the closing frame when the subscriber leaves.
func TestSubscribeEvents(t *testing.T) {
	c, core := dialFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates, done := subscribe(ctx, c, map[string]string{"highbay.project": "p1"})

	r := core.call("events.subscribe")
	assert.JSONEq(t, `{"labels":{"highbay.project":"p1"}}`, string(r.Params))

	// The core opens an event channel before it replies, so its first event
	// can overtake the reply.
	early := eventLine(11, "title", "t7", `{"title":"early"}`)
	core.send(3, kindData, []byte(early))
	core.reply(r, `{"seq":10,"terminals":[`+terminalJSON+`],"channel":3}`)

	split := eventLine(12, "turn", "t7", `{"turn":4,"state":"done"}`)
	stale := eventLine(20, "title", "t7", `{"title":"already in the snapshot"}`)
	rest := split[20:] +
		`{"jsonrpc":"2.0","method":"heartbeat","params":{"seq":12}}` + "\n" +
		`{"jsonrpc":"2.0","method":"someday","params":{"seq":13}}` + "\n" +
		"\n" +
		`{"jsonrpc":"2.0","method":"missed","params":{"seq":20,"terminals":[]}}` + "\n" +
		stale +
		eventLine(21, "removed", "t7", `{}`)
	core.send(3, kindData, []byte(split[:20]))
	core.send(3, kindCredit, u32(1))
	core.send(3, kindData, []byte(rest))

	for _, want := range []StreamUpdate{
		Snapshot{Seq: 10, Terminals: []Terminal{wantTerminal}},
		Event{Seq: 11, Terminal: "t7", Change: TitleChanged{Title: "early"}},
		Event{Seq: 12, Terminal: "t7", Change: TurnEnded{Turn: 4, State: "done"}},
		Heartbeat{Seq: 12},
		Missed{Seq: 20, Terminals: []Terminal{}},
		Event{Seq: 21, Terminal: "t7", Change: Removed{}},
	} {
		assert.Equal(t, want, nextUpdate(t, updates))
	}

	// Each frame is acknowledged in full once its lines were delivered.
	for _, sent := range []int{len(early), 20, len(rest)} {
		assert.Equal(t, frame{channel: 3, kind: kindCredit, payload: u32(uint32(sent))}, core.next())
	}

	cancel()
	_, err := await(t, done)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, frame{channel: 3, kind: kindClose, payload: []byte{}}, core.next())
	assert.NoError(t, c.Err())
}

// TestSubscribeEventsCancelledWhileBusy checks that a subscriber who leaves
// in the middle of a frame hears nothing more, acknowledges nothing more, and
// returns although more events are waiting.
func TestSubscribeEventsCancelledWhileBusy(t *testing.T) {
	c, core := dialFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var seen []StreamUpdate
	done := async(func() (struct{}, error) {
		return struct{}{}, c.SubscribeEvents(ctx, nil, func(update StreamUpdate) {
			seen = append(seen, update)
			if _, isEvent := update.(Event); isEvent {
				cancel()
			}
		})
	})
	r := core.call("events.subscribe")
	// Three events in one frame and another frame behind it, all before the
	// reply, so everything is waiting when the subscriber starts.
	core.send(3, kindData, []byte(
		eventLine(11, "title", "t7", `{"title":"one"}`)+
			eventLine(12, "title", "t7", `{"title":"two"}`)+
			eventLine(13, "title", "t7", `{"title":"three"}`)))
	core.send(3, kindData, []byte(eventLine(14, "title", "t7", `{"title":"four"}`)))
	core.reply(r, `{"seq":10,"terminals":[],"channel":3}`)

	_, err := await(t, done)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []StreamUpdate{
		Snapshot{Seq: 10, Terminals: []Terminal{}},
		Event{Seq: 11, Terminal: "t7", Change: TitleChanged{Title: "one"}},
	}, seen)
	assert.Equal(t, frame{channel: 3, kind: kindClose, payload: []byte{}}, core.next(),
		"no credit is returned for a frame the subscriber left in")
}

// TestSubscribeEventsEarlyFrames checks the frames the core may send before
// its reply names the channel: a whole window of events plus frames that
// carry no stream bytes, which do not count against that window.
func TestSubscribeEventsEarlyFrames(t *testing.T) {
	line := []byte(`{"jsonrpc":"2.0","method":"heartbeat","params":{"seq":10}}`)
	line = append(line, bytes.Repeat([]byte{' '}, 4096-len(line)-1)...)
	line = append(line, '\n')
	full := bytes.Repeat(line, window/len(line))
	require.Len(t, full, window)

	c, core := dialFake(t)
	updates, done := subscribe(context.Background(), c, nil)
	r := core.call("events.subscribe")
	core.send(3, kindData, full)
	core.send(3, kindResize, size(80, 24))
	core.send(3, kindClose, []byte(`{"reason":"server_stop"}`))
	core.send(3, kindData, []byte("after the close: dropped\n"))
	core.reply(r, `{"seq":10,"terminals":[],"channel":3}`)

	assert.Equal(t, Snapshot{Seq: 10, Terminals: []Terminal{}}, nextUpdate(t, updates))
	for range window / len(line) {
		assert.Equal(t, Heartbeat{Seq: 10}, nextUpdate(t, updates))
	}
	_, err := await(t, done)
	assert.ErrorIs(t, err, ErrStreamEnded)
	assert.NoError(t, c.Err(), "a full window plus frames without stream bytes is within the contract")

	t.Run("a subscriber who left and a channel the core already closed", func(t *testing.T) {
		c, core := dialFake(t)
		ctx, cancel := context.WithCancel(context.Background())
		_, done := subscribe(ctx, c, nil)
		r := core.call("events.subscribe")
		cancel()
		_, err := await(t, done)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, "$/cancelRequest", core.request().Method)
		core.send(3, kindClose, nil)
		core.reply(r, `{"seq":10,"terminals":[],"channel":3}`)

		// The client sends no closing frame for a channel that is already
		// over: the next frame is the next call.
		next := async(func() (Terminal, error) { return c.Get(context.Background(), "t7") })
		core.reply(core.call("terminal.get"), terminalJSON)
		_, err = await(t, next)
		require.NoError(t, err)
		c.mu.Lock()
		assert.Empty(t, c.closing)
		c.mu.Unlock()
	})
}

// TestSubscribeEventsEnds covers every way a subscription ends other than
// its caller leaving.
func TestSubscribeEventsEnds(t *testing.T) {
	for _, test := range []struct {
		name string
		end  func(t *testing.T, core *fakeCore, r request)
		want error
		// updates are what the subscriber saw after its snapshot.
		updates []StreamUpdate
	}{
		{
			name: "core refuses the subscription",
			end: func(_ *testing.T, core *fakeCore, r request) {
				core.refuse(r, "not_found", "no terminal t9", "")
			},
			want: ErrNotFound,
		},
		{
			name: "reply names no channel",
			end: func(_ *testing.T, core *fakeCore, r request) {
				core.reply(r, `{"seq":10,"terminals":[]}`)
			},
			want: ErrProtocol,
		},
		{
			name: "core closes the channel",
			end: func(_ *testing.T, core *fakeCore, r request) {
				core.reply(r, `{"seq":10,"terminals":[],"channel":3}`)
				core.send(3, kindClose, nil)
			},
			want: ErrStreamEnded,
		},
		{
			name: "core closes the channel before it replies",
			end: func(_ *testing.T, core *fakeCore, r request) {
				core.send(3, kindData, []byte(eventLine(11, "title", "t7", `{"title":"early"}`)))
				core.send(3, kindClose, nil)
				core.send(3, kindData, []byte("dropped: the channel is over\n"))
				core.reply(r, `{"seq":10,"terminals":[],"channel":3}`)
			},
			want:    ErrStreamEnded,
			updates: []StreamUpdate{Event{Seq: 11, Terminal: "t7", Change: TitleChanged{Title: "early"}}},
		},
		{
			name: "connection is lost",
			end: func(t *testing.T, core *fakeCore, r request) {
				core.reply(r, `{"seq":10,"terminals":[],"channel":3}`)
				require.NoError(t, core.core.Close())
			},
			want: ErrClosed,
		},
		{
			name: "line is not JSON",
			end: func(_ *testing.T, core *fakeCore, r request) {
				core.reply(r, `{"seq":10,"terminals":[],"channel":3}`)
				core.send(3, kindData, []byte("not json\n"))
			},
			want: ErrProtocol,
		},
		{
			name: "event data of the wrong shape",
			end: func(_ *testing.T, core *fakeCore, r request) {
				core.reply(r, `{"seq":10,"terminals":[],"channel":3}`)
				core.send(3, kindData, []byte(eventLine(11, "size", "t7", `"wide"`)))
			},
			want: ErrProtocol,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, core := dialFake(t)
			updates, done := subscribe(context.Background(), c, nil)
			r := core.call("events.subscribe")
			assert.JSONEq(t, `{}`, string(r.Params), "no labels means every terminal")
			test.end(t, core, r)
			_, err := await(t, done)
			assert.ErrorIs(t, err, test.want)
			for _, want := range test.updates {
				assert.IsType(t, Snapshot{}, nextUpdate(t, updates))
				assert.Equal(t, want, nextUpdate(t, updates))
			}
		})
	}
}

// TestDecodeChange checks the data of every event type the client decodes.
func TestDecodeChange(t *testing.T) {
	exit, signal := int32(1), int32(9)
	progress := uint8(40)
	for _, test := range []struct {
		kind string
		data string
		want Change
	}{
		{kind: "created", data: terminalJSON, want: Created{Terminal: wantTerminal}},
		{kind: "restarted", data: terminalJSON, want: Restarted{Terminal: wantTerminal}},
		{
			kind: "lost",
			data: `{"reason":"signaled","exit_code":null,"signal":9,"finished_ms":null}`,
			want: Lost{End: CloseReason{Reason: "signaled", Signal: &signal}},
		},
		{
			kind: "finished",
			data: `{"reason":"exited","exit_code":1,"signal":null,"finished_ms":1700000000123}`,
			want: Finished{End: CloseReason{Reason: "exited", ExitCode: &exit, FinishedMS: 1700000000123}},
		},
		{kind: "removed", data: `{}`, want: Removed{}},
		{
			kind: "status",
			data: `{"status":{"state":"blocked","kind":"permission","app":"claude","title":"Run tests?","msg":"",` +
				`"progress":40},"records":{"sub":{"state":"working"}},"reported":true}`,
			want: StatusChanged{
				Status:   &Status{State: "blocked", Kind: "permission", App: "claude", Title: "Run tests?", Progress: &progress},
				Records:  map[string]Status{"sub": {State: "working"}},
				Reported: true,
			},
		},
		{kind: "status", data: `{"status":null,"records":{},"reported":true}`, want: StatusChanged{Records: map[string]Status{}, Reported: true}},
		{kind: "turn", data: `{"turn":5,"state":"idle"}`, want: TurnEnded{Turn: 5, State: "idle"}},
		{kind: "title", data: `{"title":"guide"}`, want: TitleChanged{Title: "guide"}},
		{
			kind: "labels",
			data: `{"labels":{"highbay.role":"guide"}}`,
			want: LabelsChanged{Labels: map[string]string{"highbay.role": "guide"}},
		},
		{
			kind: "labels",
			data: `{"labels":{"highbay.role":"guide"},"record":` + terminalJSON + `}`,
			want: LabelsChanged{Labels: map[string]string{"highbay.role": "guide"}, Record: &wantTerminal},
		},
		{kind: "size", data: `{"cols":132,"rows":43}`, want: SizeChanged{Cols: 132, Rows: 43}},
		{
			kind: "foreground",
			data: `{"pid":4243,"name":"node","argv":["node","claude"],"cwd":"/work"}`,
			want: ForegroundChanged{Foreground: wantTerminal.Foreground},
		},
		{kind: "foreground", data: `null`, want: ForegroundChanged{}},
		{kind: "keep", data: `{"keep":true}`, want: OtherChange{Type: "keep", Data: json.RawMessage(`{"keep":true}`)}},
		{kind: "doc", data: `{"name":"app","version":2}`, want: OtherChange{Type: "doc", Data: json.RawMessage(`{"name":"app","version":2}`)}},
	} {
		t.Run(test.kind+" "+test.data, func(t *testing.T) {
			got, err := decodeChange(test.kind, json.RawMessage(test.data))
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}

	for _, kind := range []string{"created", "restarted", "lost", "finished", "status", "turn", "title", "labels", "size", "foreground"} {
		t.Run(kind+" without data", func(t *testing.T) {
			_, err := decodeChange(kind, nil)
			assert.Error(t, err)
		})
	}
}

// TestEventDecoderLineLimit checks that a line is refused once it passes the
// frame limit, at the limit's edge.
func TestEventDecoderLineLimit(t *testing.T) {
	emit := func(StreamUpdate) { t.Fatal("no line was completed") }
	half := bytes.Repeat([]byte{' '}, MaxPayload/2)

	var decoder eventDecoder
	ctx := context.Background()
	require.NoError(t, decoder.feed(ctx, half, emit))
	require.NoError(t, decoder.feed(ctx, half, emit), "a line of exactly the limit is kept")
	assert.ErrorIs(t, decoder.feed(ctx, []byte{' '}, emit), ErrProtocol)
}

// TestSubscribeEventsCredit checks that the core's own flow control holds for
// a subscription: events beyond the initial credit are a violation until the
// subscriber acknowledges what it consumed.
func TestSubscribeEventsCredit(t *testing.T) {
	c, core := dialFake(t)
	updates, _ := subscribe(context.Background(), c, nil)
	core.reply(core.call("events.subscribe"), `{"seq":0,"terminals":[],"channel":3}`)
	assert.Equal(t, Snapshot{Seq: 0, Terminals: []Terminal{}}, nextUpdate(t, updates))

	// One whole window of heartbeats, acknowledged, then one more window.
	// Trailing spaces pad each line to 4 KiB so a window is 64 lines.
	line := []byte(`{"jsonrpc":"2.0","method":"heartbeat","params":{"seq":0}}`)
	line = append(line, bytes.Repeat([]byte{' '}, 4096-len(line)-1)...)
	line = append(line, '\n')
	chunk := bytes.Repeat(line, window/len(line))
	require.Len(t, chunk, window)
	for range 2 {
		core.send(3, kindData, chunk)
		for range window / len(line) {
			assert.Equal(t, Heartbeat{}, nextUpdate(t, updates))
		}
		credit := core.next()
		assert.Equal(t, kindCredit, credit.kind)
		assert.Equal(t, uint32(len(chunk)), binary.LittleEndian.Uint32(credit.payload))
	}
	assert.NoError(t, c.Err())
}
