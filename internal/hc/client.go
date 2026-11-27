// Package hc is Highbay's client for herdr-core, the local daemon that runs
// each agent in a terminal. One Client holds one connection, normally the
// standard input and output of an "hc connect" process, and multiplexes
// everything Highbay asks of the core over it:
//
//   - calls such as Run, Get, Send and Kill, which may run side by side;
//   - SubscribeEvents, an ordered stream of changes to matching terminals;
//   - Attach, a live byte stream to one terminal for a viewer.
//
// The connection starts with one line of JSON, the "hello" call, and then
// switches to herdr-core's framed mode: every message is a 9-byte header
// (channel, kind, payload length) followed by its payload. Channel 0 carries
// JSON-RPC 2.0 calls and replies; each event subscription and each attached
// terminal gets a channel of its own with its own flow control, so a viewer
// that stops reading never delays a call.
//
// A Client does not reconnect. When the core restarts, Done is closed and
// Err says why; the owner closes the Client, dials again and subscribes
// again, because event sequence numbers restart with each core process.
package hc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type (
	// Connector opens the byte stream to the core. It returns the stream and
	// a release function. The Client closes the stream first and then calls
	// release exactly once; together they must unblock a pending Read and
	// free whatever backs the stream, such as a child process. ctx bounds
	// opening the stream only, not its lifetime.
	Connector func(ctx context.Context) (io.ReadWriteCloser, func() error, error)

	// Client is one framed connection to herdr-core. Its methods are safe for
	// concurrent use. The owner must call Close, also after Done is closed,
	// to release the connection.
	Client struct {
		// conn is the byte stream; release frees what backs it.
		conn    io.ReadWriteCloser
		release func() error
		// reader buffers conn for the handshake line and then for frames.
		reader *bufio.Reader
		// hello is the core's handshake answer, fixed once Dial returns.
		hello Hello
		// autoInput records that the core advertised automatic_input.
		autoInput bool

		// write serializes whole frames onto conn.
		write sync.Mutex

		// mu guards the fields below.
		mu sync.Mutex
		// nextID is the last JSON-RPC id used on channel 0.
		nextID uint64
		// calls holds calls waiting for their reply, by id.
		calls map[uint64]*pendingCall
		// openers counts calls in flight whose reply will name a new channel.
		openers int
		// streams holds open channels by number.
		streams map[uint32]*stream
		// closing holds channels this side closed and whose closing frame
		// from the core has not arrived; frames still in flight are dropped.
		closing map[uint32]bool
		// early holds frames for channels no reply has named yet, by channel.
		// The core may send an event before the reply that announces its
		// channel.
		early map[uint32]*earlyFrames
		// earlyData is the total of stream bytes held in early.
		earlyData int
		// err is why the connection ended; nil while it is open.
		err error

		// done is closed when the connection ends.
		done chan struct{}
		// readerDone is closed when the reading goroutine has returned.
		readerDone chan struct{}
		// shutOnce closes conn once; shutErr is that close's error.
		shutOnce sync.Once
		shutErr  error
		// closeOnce runs Close once; closeErr is its result.
		closeOnce sync.Once
		closeErr  error
	}

	// earlyFrames holds the frames of one channel no reply has named yet.
	earlyFrames struct {
		frames []frame
		// data is the stream bytes among frames: the only bytes the core's
		// credit window counts.
		data int
		// closed records the core's closing frame; later frames are dropped.
		closed bool
	}

	// pendingCall is one channel-0 call waiting for its reply.
	pendingCall struct {
		// done is closed once result, stream or err is set.
		done chan struct{}
		// opens marks a call whose reply names a new channel.
		opens bool
		// abandoned marks an opening call whose caller stopped waiting: the
		// channel in its reply must be closed, not delivered.
		abandoned bool
		// result is the reply's result.
		result json.RawMessage
		// stream is the channel an opening call's reply named.
		stream *stream
		// err is the core's error or why the connection ended.
		err error
	}
)

const (
	// generation is the protocol generation this client speaks.
	generation = 1
	// featureResizeNotifications and featureAutomaticInput are the optional
	// protocol features this client understands and announces in hello.
	featureResizeNotifications = "resize_notifications"
	featureAutomaticInput      = "automatic_input"
	// handshakeLineLimit bounds the core's one-line answer to hello.
	handshakeLineLimit = 1 << 20
	// readBuffer sizes the buffered reader over the connection.
	readBuffer = 64 << 10
)

// Dial opens a connection through connect, sends hello and switches the
// connection to framed mode. ctx bounds the whole handshake. The error is a
// *KindError when the core refuses the hello, for example because it speaks
// another protocol generation.
func Dial(ctx context.Context, connect Connector) (*Client, error) {
	ctx, span := otel.Tracer("highbay/hc").Start(ctx, "highbay.hc.dial")
	defer span.End()
	c, err := dial(ctx, connect)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	span.SetAttributes(attribute.String("hc.version", c.hello.Version))
	return c, nil
}

// Hello returns the core's handshake answer.
func (c *Client) Hello() Hello {
	return c.hello
}

// Done is closed when the connection ends for any reason. Err then says why.
func (c *Client) Done() <-chan struct{} {
	return c.done
}

// Err returns why the connection ended, or nil while it is open. The error
// always matches ErrClosed; it also matches ErrProtocol when the core broke
// the wire contract, and wraps the read or write error otherwise.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close ends the connection and releases what backs it. Calls in flight and
// open streams end with ErrClosed. It returns the error of closing the stream
// or of its release function, and the same result on every later call.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.fail(ErrClosed)
		c.shut()
		releaseErr := c.release()
		<-c.readerDone
		c.closeErr = errors.Join(c.shutErr, releaseErr)
	})
	return c.closeErr
}

// ServerStatus returns the core's identity, version and terminal counts.
func (c *Client) ServerStatus(ctx context.Context) (ServerStatus, error) {
	var result ServerStatus
	err := c.call(ctx, "server.status", nil, &result)
	return result, err
}

// Run starts a program in a new terminal and returns its record once the
// program runs.
func (c *Client) Run(ctx context.Context, req RunRequest) (Terminal, error) {
	var result Terminal
	err := c.call(ctx, "terminal.run", req, &result)
	return result, err
}

// List returns live terminals, and finished ones with req.All, that carry
// every label in req.Labels.
func (c *Client) List(ctx context.Context, req ListRequest) ([]Terminal, error) {
	var result listResult
	err := c.call(ctx, "terminal.list", req, &result)
	return result.Terminals, err
}

// Get returns one terminal's record, live or finished. The error matches
// ErrNotFound when there is no such terminal.
func (c *Client) Get(ctx context.Context, terminal string) (Terminal, error) {
	var result Terminal
	err := c.call(ctx, "terminal.get", terminalParams{Terminal: terminal}, &result)
	return result, err
}

// Label sets labels on a terminal and returns its updated record. A label
// with an empty value is removed.
func (c *Client) Label(ctx context.Context, terminal string, labels map[string]string) (Terminal, error) {
	var result Terminal
	err := c.call(ctx, "terminal.label", labelParams{Terminal: terminal, Labels: labels}, &result)
	return result, err
}

// Send pastes text into a terminal and presses Enter. The core waits for a
// quiet moment first, so the call can take several seconds; cancelling ctx
// asks the core to give up while it still waits. The error matches ErrBlocked
// when the program waits on a dialog and ErrStatusMismatch when req.If did not
// hold; in both cases nothing was typed.
func (c *Client) Send(ctx context.Context, req SendRequest) (SendResult, error) {
	var result SendResult
	err := c.call(ctx, "terminal.send", req, &result)
	return result, err
}

// Keys presses named keys in a terminal, such as "enter", "esc" or "ctrl+c".
func (c *Client) Keys(ctx context.Context, terminal string, keys []string) (KeysResult, error) {
	var result KeysResult
	err := c.call(ctx, "terminal.keys", keysParams{Terminal: terminal, Keys: keys}, &result)
	return result, err
}

// Interrupt presses the terminal's stop key and reports what the core then
// observed. It never escalates to killing the program.
func (c *Client) Interrupt(ctx context.Context, terminal string) (InterruptResult, error) {
	var result InterruptResult
	err := c.call(ctx, "terminal.interrupt", terminalParams{Terminal: terminal}, &result)
	return result, err
}

// Kill ends a terminal's program and returns the record with the observed
// result.
func (c *Client) Kill(ctx context.Context, terminal string) (Terminal, error) {
	var result Terminal
	err := c.call(ctx, "terminal.kill", terminalParams{Terminal: terminal}, &result)
	return result, err
}

// Remove deletes a finished terminal's record. The core refuses a live
// terminal with a *KindError of kind "live".
func (c *Client) Remove(ctx context.Context, terminal string) error {
	return c.call(ctx, "terminal.rm", terminalParams{Terminal: terminal}, nil)
}

// dial opens the stream and runs the handshake under ctx. On any failure it
// closes the stream and releases it, so nothing outlives a failed Dial.
func dial(ctx context.Context, connect Connector) (*Client, error) {
	conn, release, err := connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("hc: connect: %w", err)
	}
	c := &Client{
		conn:       conn,
		release:    release,
		reader:     bufio.NewReaderSize(conn, readBuffer),
		calls:      make(map[uint64]*pendingCall),
		streams:    make(map[uint32]*stream),
		closing:    make(map[uint32]bool),
		early:      make(map[uint32]*earlyFrames),
		done:       make(chan struct{}),
		readerDone: make(chan struct{}),
	}
	outcome := make(chan error, 1)
	go c.exchangeHello(outcome)
	select {
	case err = <-outcome:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		// Closing and releasing the stream also unblocks exchangeHello.
		return nil, errors.Join(fmt.Errorf("hc: handshake: %w", err), conn.Close(), release())
	}
	go c.readLoop()
	return c, nil
}

// exchangeHello runs the handshake and reports its result on outcome, which
// must be buffered: Dial may have stopped waiting.
func (c *Client) exchangeHello(outcome chan<- error) {
	outcome <- c.handshake()
}

// handshake sends hello as one plain JSON line and reads the one-line answer.
// Frames start right after that line in both directions.
func (c *Client) handshake() error {
	id := uint64(0)
	line, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "hello",
		Params: helloParams{
			Framed:     true,
			Generation: generation,
			Features:   []string{featureResizeNotifications, featureAutomaticInput},
		},
	})
	if err != nil {
		return err
	}
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		return err
	}
	answer, err := readLine(c.reader, handshakeLineLimit)
	if err != nil {
		return err
	}
	var message rpcMessage
	if err := json.Unmarshal(answer, &message); err != nil {
		return fmt.Errorf("%w: hello answer: %v", ErrProtocol, err)
	}
	if message.Error != nil {
		return message.Error.kindError()
	}
	if err := json.Unmarshal(message.Result, &c.hello); err != nil {
		return fmt.Errorf("%w: hello result: %v", ErrProtocol, err)
	}
	if !c.hello.Framed {
		return fmt.Errorf("%w: the core did not switch to framed mode", ErrProtocol)
	}
	c.autoInput = slices.Contains(c.hello.Features, featureAutomaticInput)
	return nil
}

// readLoop is the connection's only reader. It routes every frame until the
// stream ends or breaks the protocol, then ends the connection.
func (c *Client) readLoop() {
	defer close(c.readerDone)
	for {
		f, err := readFrame(c.reader)
		if err != nil {
			c.fail(err)
			return
		}
		if err := c.dispatch(f); err != nil {
			c.fail(err)
			return
		}
	}
}

// dispatch routes one frame: channel 0 to the call it answers, any other
// channel to its stream. A frame for a channel nobody holds is dropped, unless
// a call that opens a channel is in flight: then it is kept for that call's
// reply. The core has one window of credit for a channel nobody acknowledged
// yet, so more stream bytes than that for one channel, or more than one
// window per opening call in total, breaks flow control. Only stream bytes
// count: the core sends resize and closing frames without credit.
func (c *Client) dispatch(f frame) error {
	if f.channel == 0 {
		if f.kind != kindData {
			return nil
		}
		return c.dispatchReply(f.payload)
	}
	c.mu.Lock()
	if s, open := c.streams[f.channel]; open {
		if f.kind == kindClose {
			delete(c.streams, f.channel)
		}
		c.mu.Unlock()
		return s.push(f)
	}
	defer c.mu.Unlock()
	if c.closing[f.channel] {
		if f.kind == kindClose {
			delete(c.closing, f.channel)
		}
		return nil
	}
	if c.openers == 0 {
		return nil
	}
	held := c.early[f.channel]
	if held == nil {
		held = &earlyFrames{}
		c.early[f.channel] = held
	}
	if held.closed {
		return nil
	}
	switch f.kind {
	case kindData:
		held.data += len(f.payload)
		c.earlyData += len(f.payload)
		if held.data > window || c.earlyData > c.openers*window {
			return fmt.Errorf("%w: channel %d sent %d bytes before any reply named it",
				ErrProtocol, f.channel, held.data)
		}
	case kindClose:
		held.closed = true
	}
	held.frames = append(held.frames, f)
	return nil
}

// dispatchReply completes the call a channel-0 reply answers. Notifications
// and replies to calls nobody waits for are ignored.
func (c *Client) dispatchReply(payload []byte) error {
	var message rpcMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return fmt.Errorf("%w: channel 0 message: %v", ErrProtocol, err)
	}
	if message.ID == nil || message.Method != "" {
		return nil
	}
	c.mu.Lock()
	p, waiting := c.calls[*message.ID]
	if !waiting {
		c.mu.Unlock()
		return nil
	}
	delete(c.calls, *message.ID)
	var discard *uint32
	switch {
	case message.Error != nil:
		p.err = message.Error.kindError()
	case p.opens:
		discard, p.err = c.openChannel(p, message.Result)
	}
	p.result = message.Result
	if p.opens {
		c.openerDone()
	}
	c.mu.Unlock()
	close(p.done)
	if discard != nil {
		return c.send(*discard, kindClose, nil)
	}
	return nil
}

// openChannel registers the channel named by an opening call's reply and
// hands it the frames that arrived early. When the caller stopped waiting, it
// returns the channel for the reader to close instead, unless the core closed
// it already. c.mu is held.
func (c *Client) openChannel(p *pendingCall, result json.RawMessage) (discard *uint32, err error) {
	var named struct {
		Channel *uint32 `json:"channel"`
	}
	if err := json.Unmarshal(result, &named); err != nil {
		return nil, fmt.Errorf("%w: reply: %v", ErrProtocol, err)
	}
	if named.Channel == nil || *named.Channel == 0 {
		return nil, fmt.Errorf("%w: reply names no channel", ErrProtocol)
	}
	channel := *named.Channel
	early := c.early[channel]
	if early == nil {
		early = &earlyFrames{}
	}
	c.earlyData -= early.data
	delete(c.early, channel)
	if p.abandoned {
		if early.closed {
			// The core already ended the channel: there is nothing to close.
			return nil, nil
		}
		c.closing[channel] = true
		return &channel, nil
	}
	s := newStream(c, channel, early.frames)
	if !early.closed {
		c.streams[channel] = s
	}
	p.stream = s
	return nil, nil
}

// call makes one channel-0 call and decodes its result into result, which may
// be nil.
func (c *Client) call(ctx context.Context, method string, params, result any) error {
	_, err := c.invoke(ctx, method, params, result, false)
	return err
}

// open makes a call whose reply names a new channel and returns that channel's
// stream.
func (c *Client) open(ctx context.Context, method string, params, result any) (*stream, error) {
	return c.invoke(ctx, method, params, result, true)
}

// invoke sends one JSON-RPC request and waits for its reply or for ctx. A ctx
// that is already over sends nothing: the core cannot take back most calls
// once it has them. When ctx ends while the call is in flight, invoke tells
// the core to cancel the request and returns ctx's error; the call may still
// have taken effect, and a channel the late reply names is closed by the
// reader.
func (c *Client) invoke(ctx context.Context, method string, params, result any, opens bool) (s *stream, err error) {
	ctx, span := otel.Tracer("highbay/hc").Start(ctx, "highbay.hc.call",
		trace.WithAttributes(attribute.String("hc.method", method)))
	defer func() { endCall(span, err) }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p := &pendingCall{done: make(chan struct{}), opens: opens}
	id, err := c.register(p)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params})
	if err != nil {
		c.forget(id, p)
		return nil, fmt.Errorf("hc: encode %s: %w", method, err)
	}
	if err := c.send(0, kindData, payload); err != nil {
		c.forget(id, p)
		return nil, err
	}
	select {
	case <-p.done:
	case <-ctx.Done():
		if c.abandon(id, p) {
			return nil, errors.Join(ctx.Err(), c.cancel(id))
		}
		// The reply arrived first.
		<-p.done
	}
	if p.err != nil {
		return nil, p.err
	}
	if result != nil {
		if err := json.Unmarshal(p.result, result); err != nil {
			err = fmt.Errorf("%w: %s result: %v", ErrProtocol, method, err)
			if p.stream == nil {
				return nil, err
			}
			// The reply opened a channel nobody can use: close it.
			return nil, errors.Join(err, p.stream.close())
		}
	}
	return p.stream, nil
}

// register records a call as waiting and returns its id.
func (c *Client) register(p *pendingCall) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return 0, c.err
	}
	c.nextID++
	c.calls[c.nextID] = p
	if p.opens {
		c.openers++
	}
	return c.nextID, nil
}

// forget removes a call whose request never reached the core, so no reply
// will come for it.
func (c *Client) forget(id uint64, p *pendingCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls[id] != p {
		return
	}
	delete(c.calls, id)
	if p.opens {
		c.openerDone()
	}
}

// openerDone records that one opening call is over. Once none is in flight,
// frames held for channels no reply named belong to nobody and are dropped.
// c.mu is held.
func (c *Client) openerDone() {
	c.openers--
	if c.openers == 0 {
		clear(c.early)
		c.earlyData = 0
	}
}

// abandon stops waiting for a call's reply. It reports false when the reply
// or the connection's end got there first, so p.done is or will be closed. An
// opening call stays recorded: its reply may still name a channel to close.
func (c *Client) abandon(id uint64, p *pendingCall) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls[id] != p {
		return false
	}
	if p.opens {
		p.abandoned = true
	} else {
		delete(c.calls, id)
	}
	return true
}

// cancel tells the core that nobody waits for request id any more. The core
// acts on it for calls it can still stop, such as a Send holding for quiet.
func (c *Client) cancel(id uint64) error {
	payload, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		Method:  "$/cancelRequest",
		Params:  map[string]uint64{"id": id},
	})
	if err != nil {
		return err
	}
	return c.send(0, kindData, payload)
}

// send writes one frame. A failed write ends the connection.
func (c *Client) send(channel uint32, kind frameKind, payload []byte) error {
	if len(payload) > MaxPayload {
		return fmt.Errorf("hc: frame of %d bytes exceeds %d", len(payload), MaxPayload)
	}
	buf := appendFrame(make([]byte, 0, headerSize+len(payload)), channel, kind, payload)
	c.write.Lock()
	defer c.write.Unlock()
	if err := c.Err(); err != nil {
		return err
	}
	if _, err := c.conn.Write(buf); err != nil {
		c.fail(err)
		return c.Err()
	}
	return nil
}

// fail ends the connection with err, once: every waiting call and open stream
// ends with it, and the byte stream is closed so the reader returns. The
// recorded error always matches ErrClosed, whatever ended the connection.
func (c *Client) fail(err error) {
	if !errors.Is(err, ErrClosed) {
		err = fmt.Errorf("%w: %w", ErrClosed, err)
	}
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	calls, streams := c.calls, c.streams
	c.calls, c.streams = make(map[uint64]*pendingCall), make(map[uint32]*stream)
	clear(c.closing)
	clear(c.early)
	c.earlyData, c.openers = 0, 0
	close(c.done)
	c.mu.Unlock()
	for _, p := range calls {
		p.err = err
		close(p.done)
	}
	for _, s := range streams {
		s.end(err)
	}
	c.shut()
}

// shut closes the byte stream once. Close reports its error.
func (c *Client) shut() {
	c.shutOnce.Do(func() { c.shutErr = c.conn.Close() })
}

// endCall records a call's outcome on its span. The kinds callers branch on
// (ErrNotFound and the others) are states of a terminal and stay unmarked;
// every other error is a failure.
func endCall(span trace.Span, err error) {
	defer span.End()
	if err == nil {
		return
	}
	var kind *KindError
	if errors.As(err, &kind) {
		span.SetAttributes(attribute.String("hc.error_kind", kind.Kind))
		if expectedKinds[kind.Kind] != nil {
			return
		}
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// readLine reads through the next newline, refusing a line longer than limit.
func readLine(r *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > limit {
			return nil, fmt.Errorf("%w: line exceeds %d bytes", ErrProtocol, limit)
		}
		if err == nil {
			return line, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}
