// These tests cover the page's terminal drawer: the single-use ticket and
// the relay between the page and a role's terminal.
package project

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genhighbayclient "github.com/goadesign/highbay/gen/http/highbay/client"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/hc"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goahttp "goa.design/goa/v3/http"
)

// TestTerminalTickets checks that a ticket is accepted once, in time, and
// only for the role and session it was minted for.
func TestTerminalTickets(t *testing.T) {
	minted := time.Unix(1_700_000_000, 0)
	grant := terminalTicket{role: "builder", caller: "user-1"}
	for _, test := range []struct {
		name   string
		ticket string
		want   terminalTicket
		at     time.Time
		taken  bool
	}{
		{name: "used at once", ticket: "ticket", want: grant, at: minted, taken: true},
		{name: "used just before it expires", ticket: "ticket", want: grant, at: minted.Add(terminalTicketLifetime - time.Nanosecond), taken: true},
		{name: "used as it expires", ticket: "ticket", want: grant, at: minted.Add(terminalTicketLifetime)},
		{name: "another role", ticket: "ticket", want: terminalTicket{role: "guide", caller: "user-1"}, at: minted},
		{name: "another session", ticket: "ticket", want: terminalTicket{role: "builder", caller: "user-2"}, at: minted},
		{name: "a value never minted", ticket: "tickex", want: grant, at: minted},
		{name: "no value", ticket: "", want: grant, at: minted},
	} {
		t.Run(test.name, func(t *testing.T) {
			var tickets terminalTickets
			expires := tickets.add("ticket", grant, minted)
			assert.Equal(t, minted.Add(terminalTicketLifetime), expires)
			assert.Equal(t, test.taken, tickets.take(test.ticket, test.want, test.at))
			assert.Equal(t, test.ticket != "ticket", tickets.take("ticket", grant, minted),
				"a ticket presented once is used up, whatever it was presented for; one never presented is still good")
		})
	}

	t.Run("names its role and session until it is used or expires", func(t *testing.T) {
		var tickets terminalTickets
		tickets.add("ticket", grant, minted)
		found, ok := tickets.grant("ticket", minted.Add(terminalTicketLifetime-time.Nanosecond))
		assert.True(t, ok)
		assert.Equal(t, grant.role, found.role)
		assert.Equal(t, "user-1", found.caller)
		_, ok = tickets.grant("ticket", minted.Add(terminalTicketLifetime))
		assert.False(t, ok, "an expired ticket names nobody")
		_, ok = tickets.grant("other", minted)
		assert.False(t, ok)
		tickets.add("later", grant, minted.Add(terminalTicketLifetime))
		assert.NotContains(t, tickets.open, "ticket", "minting drops expired tickets")
	})
}

// TestAuthenticateTerminalUpgrade checks the one request a browser session
// cannot sign with its credential: the WebSocket upgrade, which the ticket
// authenticates when it comes from the project's own page.
func TestAuthenticateTerminalUpgrade(t *testing.T) {
	server, _ := teamServer(t)
	origin := server.state.Origin()
	const terminal = "/team/roles/guide/terminal"
	for _, test := range []struct {
		name    string
		path    string
		ticket  string
		origin  string
		upgrade bool
		caller  string
	}{
		{name: "the page's upgrade with a fresh ticket", path: terminal, ticket: "ticket", origin: origin, upgrade: true, caller: "user-9"},
		{name: "another origin", path: terminal, ticket: "ticket", origin: "http://127.0.0.1:1", upgrade: true},
		{name: "no origin", path: terminal, ticket: "ticket", upgrade: true},
		{name: "an unknown ticket", path: terminal, ticket: "guess", origin: origin, upgrade: true},
		{name: "a ticket on an ordinary request", path: terminal, ticket: "ticket", origin: origin},
		{name: "another role's terminal", path: "/team/roles/builder/terminal", ticket: "ticket", origin: origin, upgrade: true},
		{name: "an upgrade of another route", path: "/changes", ticket: "ticket", origin: origin, upgrade: true},
		{name: "an upgrade below the terminal's route", path: terminal + "/", ticket: "ticket", origin: origin, upgrade: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server.team.tickets = terminalTickets{}
			server.team.tickets.add("ticket", terminalTicket{role: "guide", caller: "user-9"}, server.now())
			request := httptest.NewRequest(http.MethodGet, origin+test.path+"?ticket="+test.ticket, nil)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.upgrade {
				request.Header.Set("Connection", "Upgrade")
				request.Header.Set("Upgrade", "websocket")
			}
			principal, err := server.Authenticate(request)
			if test.caller == "" {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, controlplane.Principal{ID: server.state.Record.Owner, CallerID: "user-9", Kind: controlplane.CallerUser}, principal)
			_, err = server.Authenticate(request)
			assert.NoError(t, err, "authenticating does not use the ticket up; attaching does")
		})
	}
}

// TestAttachTerminal follows both interactive and passive viewers: the ticket,
// the attach request herdr-core receives, frames in both directions, and the
// end of the stream.
func TestAttachTerminal(t *testing.T) {
	t.Run("interactive attach applies input, resize, focus, and credit", func(t *testing.T) {
		server, fakes := teamServer(t)
		buildTeam(t, server)
		ctx := userContext(server.state)
		_, err := server.StartAgents(ctx, nil)
		require.NoError(t, err)

		ticket, err := server.CreateTerminalTicket(ctx, "builder")
		require.NoError(t, err)
		assert.Len(t, ticket.Ticket, 64)
		assert.Equal(t, server.now().Add(terminalTicketLifetime).UTC().Format(time.RFC3339Nano), ticket.ExpiresAt)

		exit := int32(0)
		stream, viewer := fakes.core.stream, newFakeViewer()
		viewer.frames <- &genhighbay.TerminalClientFrame{ClientFrame: genhighbay.NewClientFrameInput(&genhighbay.TerminalInputFrame{Data: []byte("ls\r")})}
		viewer.frames <- &genhighbay.TerminalClientFrame{ClientFrame: genhighbay.NewClientFrameResize(&genhighbay.TerminalResize{Cols: 132, Rows: 43})}
		viewer.frames <- &genhighbay.TerminalClientFrame{ClientFrame: genhighbay.NewClientFrameFocus(&genhighbay.TerminalFocusFrame{Focused: true})}
		viewer.frames <- &genhighbay.TerminalClientFrame{ClientFrame: genhighbay.NewClientFrameCredit(&genhighbay.TerminalCreditFrame{Bytes: 5})}
		stream.events <- hc.TerminalResized{Cols: 100, Rows: 30}
		stream.events <- hc.TerminalOutput{Data: []byte("paint")}

		done := make(chan error, 1)
		go func() {
			done <- server.AttachTerminal(ctx, &genhighbay.AttachRoleTerminalPayload{Role: "builder", Ticket: ticket.Ticket, Cols: 120, Rows: 40, Observe: false}, viewer)
		}()
		require.Eventually(t, func() bool {
			stream.mu.Lock()
			defer stream.mu.Unlock()
			return stream.credit == 5
		}, 5*time.Second, time.Millisecond)
		stream.events <- hc.TerminalClosed{Reason: hc.CloseReason{Reason: "exited", ExitCode: &exit}}
		require.NoError(t, <-done)

		require.Len(t, fakes.core.attaches, 1)
		assert.Equal(t, hc.AttachRequest{Terminal: "t1", Cols: 120, Rows: 40, History: terminalHistory, Observe: false}, fakes.core.attaches[0])
		stream.mu.Lock()
		assert.Equal(t, []byte("ls\r"), stream.input)
		assert.Equal(t, [][2]uint16{{132, 43}}, stream.sizes)
		assert.Equal(t, []bool{true}, stream.focus)
		assert.Equal(t, 1, stream.closed, "the stream to herdr-core is closed when the relay ends")
		stream.mu.Unlock()
		code := 0
		assert.Equal(t, []*genhighbay.TerminalServerFrame{
			{ServerFrame: genhighbay.NewServerFrameResize(&genhighbay.TerminalResize{Cols: 100, Rows: 30})},
			{ServerFrame: genhighbay.NewServerFrameOutput(&genhighbay.TerminalOutputFrame{Data: []byte("paint")})},
			{ServerFrame: genhighbay.NewServerFrameClosed(&genhighbay.TerminalClosed{Reason: "exited", ExitCode: &code})},
		}, viewer.received())

		err = server.AttachTerminal(ctx, &genhighbay.AttachRoleTerminalPayload{Role: "builder", Ticket: ticket.Ticket}, newFakeViewer())
		assert.Equal(t, "unprocessable", errorName(err), "a ticket opens one stream")
	})

	t.Run("passive observe attach grants credit and ignores input, resize, and focus", func(t *testing.T) {
		server, fakes := teamServer(t)
		buildTeam(t, server)
		ctx := userContext(server.state)
		_, err := server.StartAgents(ctx, nil)
		require.NoError(t, err)

		ticket, err := server.CreateTerminalTicket(ctx, "builder")
		require.NoError(t, err)

		exit := int32(0)
		stream, viewer := fakes.core.stream, newFakeViewer()
		viewer.frames <- &genhighbay.TerminalClientFrame{ClientFrame: genhighbay.NewClientFrameInput(&genhighbay.TerminalInputFrame{Data: []byte("rm -rf /\r")})}
		viewer.frames <- &genhighbay.TerminalClientFrame{ClientFrame: genhighbay.NewClientFrameResize(&genhighbay.TerminalResize{Cols: 80, Rows: 20})}
		viewer.frames <- &genhighbay.TerminalClientFrame{ClientFrame: genhighbay.NewClientFrameFocus(&genhighbay.TerminalFocusFrame{Focused: true})}
		viewer.frames <- &genhighbay.TerminalClientFrame{ClientFrame: genhighbay.NewClientFrameCredit(&genhighbay.TerminalCreditFrame{Bytes: 7})}
		stream.events <- hc.TerminalResized{Cols: 140, Rows: 45}
		stream.events <- hc.TerminalOutput{Data: []byte("observe")}

		done := make(chan error, 1)
		go func() {
			done <- server.AttachTerminal(ctx, &genhighbay.AttachRoleTerminalPayload{Role: "builder", Ticket: ticket.Ticket, Cols: 120, Rows: 40, Observe: true}, viewer)
		}()
		require.Eventually(t, func() bool {
			stream.mu.Lock()
			defer stream.mu.Unlock()
			return stream.credit == 7
		}, 5*time.Second, time.Millisecond)
		stream.events <- hc.TerminalClosed{Reason: hc.CloseReason{Reason: "exited", ExitCode: &exit}}
		require.NoError(t, <-done)

		require.Len(t, fakes.core.attaches, 1)
		assert.Equal(t, hc.AttachRequest{Terminal: "t1", Cols: 120, Rows: 40, History: terminalHistory, Observe: true}, fakes.core.attaches[0])
		stream.mu.Lock()
		assert.Empty(t, stream.input, "passive observe stream forwarded input bytes")
		assert.Empty(t, stream.sizes, "passive observe stream resized the PTY")
		assert.Empty(t, stream.focus, "passive observe stream forwarded focus changes")
		assert.Equal(t, uint32(7), stream.credit)
		stream.mu.Unlock()
	})
}

// TestAttachTerminalEnds covers the other ways a stream ends.
func TestAttachTerminalEnds(t *testing.T) {
	attach := func(t *testing.T) (*Server, *teamFakes, *fakeViewer, chan error) {
		server, fakes := teamServer(t)
		buildTeam(t, server)
		ctx := userContext(server.state)
		_, err := server.StartAgents(ctx, nil)
		require.NoError(t, err)
		ticket, err := server.CreateTerminalTicket(ctx, "builder")
		require.NoError(t, err)
		viewer := newFakeViewer()
		done := make(chan error, 1)
		go func() {
			done <- server.AttachTerminal(ctx, &genhighbay.AttachRoleTerminalPayload{Role: "builder", Ticket: ticket.Ticket, Cols: 80, Rows: 24}, viewer)
		}()
		return server, fakes, viewer, done
	}

	t.Run("the page closes the drawer", func(t *testing.T) {
		_, fakes, viewer, done := attach(t)
		close(viewer.frames)
		require.NoError(t, <-done)
		fakes.core.stream.mu.Lock()
		defer fakes.core.stream.mu.Unlock()
		assert.Equal(t, 1, fakes.core.stream.closed, "the viewer is detached from the terminal")
	})

	t.Run("the stream closes without a reason", func(t *testing.T) {
		_, fakes, viewer, done := attach(t)
		fakes.core.stream.events <- hc.TerminalClosed{}
		require.NoError(t, <-done)
		assert.Equal(t, []*genhighbay.TerminalServerFrame{
			{ServerFrame: genhighbay.NewServerFrameClosed(&genhighbay.TerminalClosed{Reason: "detached"})},
		}, viewer.received())
	})

	t.Run("the terminal takes no more input", func(t *testing.T) {
		_, fakes, viewer, done := attach(t)
		fakes.core.stream.mu.Lock()
		fakes.core.stream.writeErr = hc.ErrStreamEnded
		fakes.core.stream.mu.Unlock()
		viewer.frames <- &genhighbay.TerminalClientFrame{ClientFrame: genhighbay.NewClientFrameInput(&genhighbay.TerminalInputFrame{Data: []byte("x")})}
		require.NoError(t, <-done, "the page sees the stream end; the failure is recorded on the span")
	})
}

// TestTerminalRefusals covers every refusal of a ticket or an attach, each
// answered before any stream opens.
func TestTerminalRefusals(t *testing.T) {
	t.Run("a ticket for a role without a live terminal", func(t *testing.T) {
		server, _ := teamServer(t)
		_, err := server.CreateTerminalTicket(userContext(server.state), "builder")
		assert.Equal(t, "terminal_unavailable", errorName(err))
	})

	t.Run("a ticket when herdr-core cannot be reached", func(t *testing.T) {
		server, fakes := teamServer(t)
		fakes.core.dialErr = errors.New("hc is not installed")
		_, err := server.CreateTerminalTicket(userContext(server.state), "builder")
		assert.Equal(t, "terminal_unavailable", errorName(err))
	})

	started := func(t *testing.T) (*Server, *teamFakes, string) {
		server, fakes := teamServer(t)
		buildTeam(t, server)
		_, err := server.StartAgents(userContext(server.state), nil)
		require.NoError(t, err)
		ticket, err := server.CreateTerminalTicket(userContext(server.state), "builder")
		require.NoError(t, err)
		return server, fakes, ticket.Ticket
	}
	for _, test := range []struct {
		name    string
		payload func(ticket string) *genhighbay.AttachRoleTerminalPayload
		arrange func(server *Server, fakes *teamFakes)
		want    string
	}{
		{
			name: "a ticket never minted",
			payload: func(string) *genhighbay.AttachRoleTerminalPayload {
				return &genhighbay.AttachRoleTerminalPayload{Role: "builder", Ticket: "0123456789abcdef"}
			},
			want: "unprocessable",
		},
		{
			name: "a ticket minted for another role",
			payload: func(ticket string) *genhighbay.AttachRoleTerminalPayload {
				return &genhighbay.AttachRoleTerminalPayload{Role: "test_writer", Ticket: ticket}
			},
			want: "unprocessable",
		},
		{
			name: "an expired ticket",
			payload: func(ticket string) *genhighbay.AttachRoleTerminalPayload {
				return &genhighbay.AttachRoleTerminalPayload{Role: "builder", Ticket: ticket}
			},
			arrange: func(server *Server, _ *teamFakes) {
				minted := server.now()
				server.now = func() time.Time { return minted.Add(terminalTicketLifetime) }
			},
			want: "unprocessable",
		},
		{
			name: "the terminal ended after the ticket was minted",
			payload: func(ticket string) *genhighbay.AttachRoleTerminalPayload {
				return &genhighbay.AttachRoleTerminalPayload{Role: "builder", Ticket: ticket}
			},
			arrange: func(server *Server, _ *teamFakes) {
				if err := server.StopRole(userContext(server.state), "builder"); err != nil {
					panic(err)
				}
			},
			want: "terminal_unavailable",
		},
		{
			name: "herdr-core refuses the attach",
			payload: func(ticket string) *genhighbay.AttachRoleTerminalPayload {
				return &genhighbay.AttachRoleTerminalPayload{Role: "builder", Ticket: ticket}
			},
			arrange: func(_ *Server, fakes *teamFakes) { fakes.core.callErr = hc.ErrFinished },
			want:    "terminal_unavailable",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, fakes, ticket := started(t)
			if test.arrange != nil {
				test.arrange(server, fakes)
			}
			viewer := newFakeViewer()
			err := server.AttachTerminal(userContext(server.state), test.payload(ticket), viewer)
			assert.Equal(t, test.want, errorName(err))
			assert.Empty(t, fakes.core.attaches)
			assert.Empty(t, viewer.received())
		})
	}

	t.Run("a ticket minted for another session", func(t *testing.T) {
		server, _, ticket := started(t)
		other := controlplane.WithPrincipal(t.Context(), controlplane.Principal{ID: server.state.Record.Owner, CallerID: "user-2", Kind: controlplane.CallerUser})
		err := server.AttachTerminal(other, &genhighbay.AttachRoleTerminalPayload{Role: "builder", Ticket: ticket}, newFakeViewer())
		assert.Equal(t, "unprocessable", errorName(err))
	})
}

// originDialer sends a WebSocket upgrade as a page at origin would, and
// keeps the status the server answered it with.
type originDialer struct {
	// origin is the Origin header sent with the upgrade; empty sends none.
	origin string
	// status is the HTTP status the server answered the upgrade with.
	status int
}

// DialContext sends the upgrade with the dialer's origin.
func (d *originDialer) DialContext(ctx context.Context, target string, header http.Header) (*websocket.Conn, *http.Response, error) {
	sent := header.Clone()
	if d.origin != "" {
		sent.Set("Origin", d.origin)
	}
	conn, response, err := websocket.DefaultDialer.DialContext(ctx, target, sent)
	if response != nil {
		d.status = response.StatusCode
	}
	return conn, response, err
}

// TestTerminalOverTheProjectServer follows the page's requests through the
// listening project server, where the session, the ticket, the page's origin
// and the terminal's route meet: a browser session mints a ticket, the
// WebSocket upgrade carries only that ticket and the page's origin, and
// frames cross in both directions. The same ticket then opens nothing, and
// neither does a fresh one sent from another origin or to another role's
// terminal. It starts one loopback listener and no process.
func TestTerminalOverTheProjectServer(t *testing.T) {
	server, fakes := teamServer(t)
	state := server.state
	cp, err := controlplane.New(controlplane.Options{
		CardsDir:     state.CardsDir(),
		WorkspaceDir: state.Record.Directory,
		Team:         &controlplane.TeamBinding{Owner: state.Record.Owner, Service: server},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cp.Close()) })
	api := httptest.NewUnstartedServer(server.Handler(controlplane.NewHTTPHandler(cp, server.Authenticate)))
	require.NoError(t, api.Listener.Close())
	api.Listener, err = net.Listen("tcp", state.Address())
	require.NoError(t, err)
	api.Start()
	t.Cleanup(api.Close)
	buildTeam(t, server)
	_, err = server.StartAgents(userContext(state), nil)
	require.NoError(t, err)

	session := browserSession(t, server)
	// page returns the generated client as the project's page uses it: the
	// session's credential on ordinary requests, origin on the upgrade.
	page := func(origin string) (*genhighbay.Client, *originDialer) {
		dialer := &originDialer{origin: origin}
		transport := genhighbayclient.NewClient("http", state.Address(),
			headerDoer{client: api.Client(), token: session.Session, browser: true},
			goahttp.RequestEncoder, goahttp.ResponseDecoder, false, dialer, nil)
		return &genhighbay.Client{
			CreateTerminalTicketEndpoint: transport.CreateTerminalTicket(),
			AttachRoleTerminalEndpoint:   transport.AttachRoleTerminal(),
		}, dialer
	}
	client, dialer := page(state.Origin())
	ticket, err := client.CreateTerminalTicket(t.Context(), &genhighbay.CreateTerminalTicketPayload{Role: "builder"})
	require.NoError(t, err)

	fakes.core.stream.events <- hc.TerminalOutput{Data: []byte("paint")}
	stream, err := client.AttachRoleTerminal(t.Context(), &genhighbay.AttachRoleTerminalPayload{Role: "builder", Ticket: ticket.Ticket, Cols: 120, Rows: 40})
	require.NoError(t, err)
	assert.Equal(t, http.StatusSwitchingProtocols, dialer.status)
	input := genhighbay.NewClientFrameInput(&genhighbay.TerminalInputFrame{Data: []byte("ls\r")})
	require.NoError(t, stream.Send(&genhighbay.TerminalClientFrame{ClientFrame: input}))
	frame, err := stream.Recv()
	require.NoError(t, err)
	output, ok := frame.ServerFrame.AsOutput()
	require.True(t, ok, "the server sent a %s frame", frame.ServerFrame.Kind())
	assert.Equal(t, "paint", string(output.Data))
	require.Eventually(t, func() bool {
		fakes.core.stream.mu.Lock()
		defer fakes.core.stream.mu.Unlock()
		return string(fakes.core.stream.input) == "ls\r"
	}, 5*time.Second, time.Millisecond, "the viewer's input never reached the terminal")
	require.NoError(t, stream.Close())

	for _, refused := range []struct {
		name string
		// fresh mints a new ticket for the builder's terminal first.
		fresh  bool
		origin string
		role   string
	}{
		{name: "the ticket a stream already used", origin: state.Origin(), role: "builder"},
		{name: "a fresh ticket from another origin", fresh: true, origin: "http://127.0.0.1:1", role: "builder"},
		{name: "a fresh ticket without an origin", fresh: true, role: "builder"},
		{name: "a fresh ticket for another role's terminal", fresh: true, origin: state.Origin(), role: "test_writer"},
	} {
		t.Run(refused.name, func(t *testing.T) {
			presented := ticket.Ticket
			if refused.fresh {
				minted, err := client.CreateTerminalTicket(t.Context(), &genhighbay.CreateTerminalTicketPayload{Role: "builder"})
				require.NoError(t, err)
				presented = minted.Ticket
			}
			other, dialer := page(refused.origin)
			_, err := other.AttachRoleTerminal(t.Context(), &genhighbay.AttachRoleTerminalPayload{Role: refused.role, Ticket: presented, Cols: 120, Rows: 40})
			assert.Error(t, err)
			assert.NotEqual(t, http.StatusSwitchingProtocols, dialer.status)
			assert.Len(t, fakes.core.attaches, 1, "a refused upgrade reached herdr-core")
		})
	}
}
