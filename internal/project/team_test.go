// This file holds the stand-ins the team tests share: an in-memory herdr-core
// and Chorus, and the dependencies that bind them.
package project

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	genchorus "github.com/goadesign/chorus/gen/view"
	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/hc"
	"github.com/stretchr/testify/require"
)

type (
	// fakeCore stands in for herdr-core. It keeps terminals in memory,
	// records what the team asked of it, and tells a subscriber about each
	// change the way the core does.
	fakeCore struct {
		// mu guards every field below.
		mu sync.Mutex
		// terminals holds the records by address.
		terminals map[string]hc.Terminal
		// next numbers the next terminal Run creates.
		next int
		// runs, labels, interrupts, kills and removes record the calls.
		runs       []hc.RunRequest
		labels     []string
		interrupts []string
		kills      []string
		removes    []string
		// dials counts connections opened.
		dials int
		// dialErr, when set, fails every dial; callErr fails every call on
		// an open connection.
		dialErr, callErr error
		// updates carries changes to the current subscriber.
		updates chan hc.StreamUpdate
		// done is closed to end the current connection.
		done chan struct{}
		// stream is what Attach returns.
		stream *fakeStream
		// attaches records the attach requests.
		attaches []hc.AttachRequest
		// now stamps new terminals.
		now func() time.Time
	}

	// fakeStream stands in for one viewer's stream to a terminal.
	fakeStream struct {
		// events are what Recv returns, in order.
		events chan hc.AttachEvent
		// mu guards the recorded writes.
		mu sync.Mutex
		// input, sizes, focus and credit record what the viewer sent.
		input  []byte
		sizes  [][2]uint16
		focus  []bool
		credit uint32
		// closed counts Close calls.
		closed int
		// writeErr, when set, fails every write.
		writeErr error
	}

	// fakeChorus stands in for the local Chorus server. It keeps one swarm
	// in memory and records what was posted.
	fakeChorus struct {
		// mu guards every field below.
		mu sync.Mutex
		// view is the swarm; its Etag changes with every change.
		view *genchorus.SwarmView
		// posts records the posted messages.
		posts []*genchorus.PostMessagePayload
		// err, when set, fails every call.
		err error
		// connects counts how often the server was resolved.
		connects int
		// connectErr, when set, fails resolving the server.
		connectErr error
	}

	// fakeViewer stands in for the page's end of the terminal WebSocket.
	fakeViewer struct {
		// frames are what the page sends, in order; closing it ends the read.
		frames chan *genhighbay.TerminalClientFrame
		// mu guards sent and closed.
		mu sync.Mutex
		// sent records what the server sent to the page.
		sent []*genhighbay.TerminalServerFrame
		// closed is closed by Close.
		closed chan struct{}
		// once makes Close safe to call twice.
		once sync.Once
	}

	// teamFakes are the stand-ins behind one server's dependencies.
	teamFakes struct {
		core   *fakeCore
		chorus *fakeChorus
		// trust records the targets worker trust was prepared for.
		trust []WorkerTrustTarget
		// trustErr, when set, is what preparing trust returns.
		trustErr error
	}
)

// newFakeCore returns an empty core stamping terminals with a fixed time.
func newFakeCore() *fakeCore {
	return &fakeCore{
		terminals: map[string]hc.Terminal{},
		updates:   make(chan hc.StreamUpdate, 256),
		done:      make(chan struct{}),
		stream:    &fakeStream{events: make(chan hc.AttachEvent, 16)},
		now:       func() time.Time { return time.Unix(1_700_000_000, 0) },
	}
}

// fixtureTeam returns dependencies bound to fresh stand-ins, with two
// installed coding agents and a command line that names the client and the
// first message.
func fixtureTeam(lifetime context.Context) (*teamFakes, TeamDependencies) {
	fakes := &teamFakes{core: newFakeCore(), chorus: &fakeChorus{view: &genchorus.SwarmView{Etag: "0"}}}
	return fakes, TeamDependencies{
		Lifetime: lifetime,
		Shell:    "/bin/zsh",
		DialTerminals: func(context.Context) (TeamTerminals, error) {
			return fakes.core.dial()
		},
		ConnectChorus: func(context.Context) (TeamChorus, error) {
			return fakes.chorus.connect()
		},
		DiscoverAgents: func(context.Context) ([]*genhighbay.InstalledAgentOption, error) {
			return []*genhighbay.InstalledAgentOption{
				{Client: "claude", Program: "/bin/claude", ModelSupported: true, YoloSupported: true},
				{Client: "codex", Program: "/bin/codex", ModelSupported: true},
			}, nil
		},
		PrepareWorkerTrust: func(_ context.Context, targets []WorkerTrustTarget) error {
			fakes.trust = append(fakes.trust, targets...)
			return fakes.trustErr
		},
		AgentCommand: func(choice *genhighbay.RoleAgentChoice, prompt string) ([]string, error) {
			return []string{choice.Program, "--", prompt}, nil
		},
		AgentEnv: map[string]string{"LOGIN": "yes"},
	}
}

// fixtureTeamDependencies is fixtureTeam for tests that never look at the
// stand-ins.
func fixtureTeamDependencies(*State) TeamDependencies {
	_, dependencies := fixtureTeam(context.Background())
	return dependencies
}

// teamContext grants management through the same trusted principal boundary used by HTTP.
func teamContext(state *State) context.Context {
	return controlplane.WithPrincipal(context.Background(), controlplane.Principal{ID: state.Record.Owner, CallerID: "management", Kind: controlplane.CallerManagement})
}

// userContext is a browser session's context.
func userContext(state *State) context.Context {
	return controlplane.WithPrincipal(context.Background(), controlplane.Principal{ID: state.Record.Owner, CallerID: "user-1", Kind: controlplane.CallerUser})
}

// fixtureAssignments selects one stable active build without starting a control plane.
func fixtureAssignments(server *Server, writer bool) {
	server.BindAssignments(func(context.Context) (controlplane.TeamAssignments, error) {
		return controlplane.TeamAssignments{
			BuildIdentity:  &controlplane.BuildIdentity{ChangeID: "change", StartedAt: time.Unix(10, 0)},
			BuilderHasWork: true, TestWriterHasWork: writer,
		}, nil
	})
}

// testAgent records the immutable handle and local credential path.
type testAgent struct {
	ActorID        string
	CredentialFile string
}

// registerTestAgent saves a role under handle, working in folder.
func registerTestAgent(server *Server, handle, role, folder string) (*testAgent, error) {
	actual, err := server.registerAgent(handle, agentRecord{Role: role, Folder: folder})
	if err != nil {
		return nil, err
	}
	return &testAgent{ActorID: actual, CredentialFile: server.state.AgentFile(actual)}, nil
}

// teamServer opens a project in a scratch folder and returns its server with
// the stand-ins behind it.
func teamServer(t *testing.T) (*Server, *teamFakes) {
	t.Helper()
	state := testState(t)
	fakes, dependencies := fixtureTeam(context.Background())
	server, err := NewServer(state, func() {}, dependencies)
	require.NoError(t, err)
	server.now = fakes.core.now
	return server, fakes
}

// dial opens a connection, counting it.
func (f *fakeCore) dial() (TeamTerminals, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	f.dials++
	select {
	case <-f.done:
		f.done = make(chan struct{})
	default:
	}
	return f, nil
}

// drop ends the current connection, as a restarted core does.
func (f *fakeCore) drop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	close(f.done)
}

// add puts a terminal in the core and tells the subscriber.
func (f *fakeCore) add(terminal hc.Terminal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminals[terminal.Address] = terminal
	f.updates <- hc.Event{Terminal: terminal.Address, Change: hc.Created{Terminal: terminal}}
}

// change replaces a terminal's record and tells the subscriber as change.
func (f *fakeCore) change(terminal hc.Terminal, change hc.Change) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminals[terminal.Address] = terminal
	f.updates <- hc.Event{Terminal: terminal.Address, Change: change}
}

// record returns one terminal's record.
func (f *fakeCore) record(address string) (hc.Terminal, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	terminal, found := f.terminals[address]
	return terminal, found
}

func (f *fakeCore) Run(_ context.Context, req hc.RunRequest) (hc.Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.callErr != nil {
		return hc.Terminal{}, f.callErr
	}
	f.next++
	terminal := hc.Terminal{
		Address: "t" + strconv.Itoa(f.next), Labels: maps.Clone(req.Labels), Argv: req.Argv, Cwd: req.Cwd,
		State: hc.StateLive, PID: uint32(1000 + f.next), CreatedMS: uint64(f.now().UnixMilli()),
	}
	f.runs = append(f.runs, req)
	f.terminals[terminal.Address] = terminal
	f.updates <- hc.Event{Terminal: terminal.Address, Change: hc.Created{Terminal: terminal}}
	return terminal, nil
}

func (f *fakeCore) List(_ context.Context, req hc.ListRequest) ([]hc.Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.callErr != nil {
		return nil, f.callErr
	}
	return f.matching(req.Labels, req.All), nil
}

func (f *fakeCore) Get(_ context.Context, address string) (hc.Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	terminal, found := f.terminals[address]
	if !found {
		return hc.Terminal{}, &hc.KindError{Kind: "not_found", Message: "no terminal " + address}
	}
	return terminal, f.callErr
}

func (f *fakeCore) Label(_ context.Context, address string, labels map[string]string) (hc.Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	terminal, found := f.terminals[address]
	if !found {
		return hc.Terminal{}, &hc.KindError{Kind: "not_found", Message: "no terminal " + address}
	}
	if f.callErr != nil {
		return hc.Terminal{}, f.callErr
	}
	terminal.Labels = maps.Clone(terminal.Labels)
	if terminal.Labels == nil {
		terminal.Labels = map[string]string{}
	}
	for key, value := range labels {
		if value == "" {
			delete(terminal.Labels, key)
		} else {
			terminal.Labels[key] = value
		}
	}
	f.labels = append(f.labels, address)
	f.terminals[address] = terminal
	f.updates <- hc.Event{Terminal: address, Change: hc.LabelsChanged{Labels: terminal.Labels, Record: &terminal}}
	return terminal, nil
}

func (f *fakeCore) Interrupt(_ context.Context, address string) (hc.InterruptResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.callErr != nil {
		return hc.InterruptResult{}, f.callErr
	}
	f.interrupts = append(f.interrupts, address)
	return hc.InterruptResult{Outcome: "stopped", Key: "esc"}, nil
}

func (f *fakeCore) Kill(_ context.Context, address string) (hc.Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	terminal, found := f.terminals[address]
	if !found {
		return hc.Terminal{}, &hc.KindError{Kind: "not_found", Message: "no terminal " + address}
	}
	if f.callErr != nil {
		return hc.Terminal{}, f.callErr
	}
	terminal.State, terminal.Reason, terminal.PID, terminal.Foreground = hc.StateFinished, "killed", 0, nil
	f.kills = append(f.kills, address)
	f.terminals[address] = terminal
	f.updates <- hc.Event{Terminal: address, Change: hc.Finished{End: hc.CloseReason{Reason: "killed"}}}
	return terminal, nil
}

func (f *fakeCore) Remove(_ context.Context, address string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	terminal, found := f.terminals[address]
	switch {
	case !found:
		return &hc.KindError{Kind: "not_found", Message: "no terminal " + address}
	case f.callErr != nil:
		return f.callErr
	case terminal.State == hc.StateLive:
		return &hc.KindError{Kind: "live", Message: address + " is live"}
	}
	f.removes = append(f.removes, address)
	delete(f.terminals, address)
	f.updates <- hc.Event{Terminal: address, Change: hc.Removed{}}
	return nil
}

func (f *fakeCore) SubscribeEvents(ctx context.Context, labels map[string]string, emit func(hc.StreamUpdate)) error {
	f.mu.Lock()
	if f.callErr != nil {
		f.mu.Unlock()
		return f.callErr
	}
	snapshot, done := f.matching(labels, true), f.done
	f.mu.Unlock()
	emit(hc.Snapshot{Terminals: snapshot})
	for {
		select {
		case update := <-f.updates:
			emit(update)
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return hc.ErrClosed
		}
	}
}

func (f *fakeCore) Attach(_ context.Context, req hc.AttachRequest) (TerminalStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.callErr != nil {
		return nil, f.callErr
	}
	f.attaches = append(f.attaches, req)
	return f.stream, nil
}

func (f *fakeCore) Done() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.done
}

func (f *fakeCore) Close() error { return nil }

// matching returns the terminals carrying every label, finished ones only
// with all. f.mu is held.
func (f *fakeCore) matching(labels map[string]string, all bool) []hc.Terminal {
	var found []hc.Terminal
	for _, terminal := range f.terminals {
		if terminal.State != hc.StateLive && !all {
			continue
		}
		matches := true
		for key, value := range labels {
			matches = matches && terminal.Labels[key] == value
		}
		if matches {
			found = append(found, terminal)
		}
	}
	slices.SortFunc(found, func(a, b hc.Terminal) int { return cmpString(a.Address, b.Address) })
	return found
}

func (s *fakeStream) Recv(ctx context.Context) (hc.AttachEvent, error) {
	select {
	case event, open := <-s.events:
		if !open {
			return nil, hc.ErrStreamEnded
		}
		return event, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *fakeStream) WriteInput(p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.input = append(s.input, p...)
	return s.writeErr
}

func (s *fakeStream) Resize(cols, rows uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sizes = append(s.sizes, [2]uint16{cols, rows})
	return s.writeErr
}

func (s *fakeStream) Focus(focused bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.focus = append(s.focus, focused)
	return s.writeErr
}

func (s *fakeStream) GrantCredit(bytes uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credit += bytes
	return s.writeErr
}

func (s *fakeStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed++
	return nil
}

// connect resolves the stand-in server, counting it.
func (c *fakeChorus) connect() (TeamChorus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connects++
	if c.connectErr != nil {
		return nil, c.connectErr
	}
	return c, nil
}

// set replaces the swarm and gives it a new ETag.
func (c *fakeChorus) set(change func(view *genchorus.SwarmView)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	change(c.view)
	c.bump()
}

// fail makes every call fail with err; nil makes them succeed again.
func (c *fakeChorus) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

// posted returns the messages posted so far.
func (c *fakeChorus) posted() []*genchorus.PostMessagePayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.posts)
}

// View answers the swarm when it differs from the caller's ETag, and
// Chorus's "not_modified" otherwise, after a quarter of a second instead of
// the real minute-long wait. Only the supervisor tests wait that out, on a
// virtual clock.
func (c *fakeChorus) View(ctx context.Context, p *genchorus.ViewPayload) (*genchorus.SwarmView, error) {
	c.mu.Lock()
	err, view := c.err, *c.view
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if p.IfNoneMatch != nil && *p.IfNoneMatch == view.Etag {
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
		}
		return nil, genchorus.MakeNotModified(errors.New("not modified"))
	}
	view.Swarm = p.Swarm
	return &view, nil
}

// PostMessage stores the message in its channel and returns it.
func (c *fakeChorus) PostMessage(_ context.Context, p *genchorus.PostMessagePayload) (*genchorus.PostMessageResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	c.posts = append(c.posts, p)
	var channel *genchorus.Channel
	for _, existing := range c.view.Channels {
		if existing.Name == p.To {
			channel = existing
		}
	}
	if channel == nil {
		channel = &genchorus.Channel{Name: p.To}
		c.view.Channels = append(c.view.Channels, channel)
	}
	channel.HeadSeq++
	human := p.Kind == "human"
	message := &genchorus.Message{
		Seq: channel.HeadSeq, Sender: p.Sender, Text: p.Text, ReplyTo: p.ReplyTo,
		SentAtMs: int64(1_700_000_000_000 + len(c.posts)), Human: &human, Model: &p.Kind,
	}
	channel.Messages = append(channel.Messages, message)
	c.bump()
	return &genchorus.PostMessageResult{Target: p.To, Message: message}, nil
}

// bump changes the swarm's ETag. c.mu is held.
func (c *fakeChorus) bump() {
	current, err := strconv.Atoi(c.view.Etag)
	if err != nil {
		panic(fmt.Sprintf("fake Chorus ETag %q is not a number", c.view.Etag))
	}
	c.view.Etag = strconv.Itoa(current + 1)
}

// newFakeViewer returns a page end with room for frames in both directions.
func newFakeViewer() *fakeViewer {
	return &fakeViewer{frames: make(chan *genhighbay.TerminalClientFrame, 16), closed: make(chan struct{})}
}

func (v *fakeViewer) Send(frame *genhighbay.TerminalServerFrame) error {
	return v.SendWithContext(context.Background(), frame)
}

func (v *fakeViewer) SendWithContext(_ context.Context, frame *genhighbay.TerminalServerFrame) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.sent = append(v.sent, frame)
	return nil
}

func (v *fakeViewer) Recv() (*genhighbay.TerminalClientFrame, error) {
	return v.RecvWithContext(context.Background())
}

func (v *fakeViewer) RecvWithContext(context.Context) (*genhighbay.TerminalClientFrame, error) {
	select {
	case frame, open := <-v.frames:
		if !open {
			return nil, errors.New("the page closed the connection")
		}
		return frame, nil
	case <-v.closed:
		return nil, errors.New("the connection is closed")
	}
}

func (v *fakeViewer) Close() error {
	v.once.Do(func() { close(v.closed) })
	return nil
}

// frames returns what the server sent to the page so far.
func (v *fakeViewer) received() []*genhighbay.TerminalServerFrame {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.sent)
}

// cmpString orders two strings.
func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
