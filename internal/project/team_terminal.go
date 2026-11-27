// This file lets the page's terminal drawer attach to a role's terminal: it
// mints the single-use ticket that authorizes one WebSocket, and relays that
// connection's frames to and from herdr-core.
package project

import (
	"context"
	"errors"
	"sync"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/hc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

type (
	// terminalTickets holds the tickets that were minted and not yet used.
	// A ticket stands in for the session credential on the one request that
	// cannot carry it: a browser sets no header on a WebSocket upgrade.
	terminalTickets struct {
		// mu guards open.
		mu sync.Mutex
		// open maps each unused ticket to what it authorizes.
		open map[string]terminalTicket
	}

	// terminalTicket authorizes one attach to one role's terminal by the
	// session that asked for it.
	terminalTicket struct {
		// role is the role whose terminal may be attached.
		role string
		// caller is the browser session's caller identity.
		caller string
		// expires is when the ticket stops being accepted.
		expires time.Time
	}
)

const (
	// terminalTicketLifetime is how long a ticket is accepted. The page uses
	// it in the request that immediately follows minting it.
	terminalTicketLifetime = 30 * time.Second
	// terminalHistory is how many scrollback lines a viewer's first paint
	// includes.
	terminalHistory = 200
)

// CreateTerminalTicket mints a ticket for the caller's session to attach to
// role's terminal once within terminalTicketLifetime. It returns
// terminal_unavailable when the role has no live terminal.
func (s *Server) CreateTerminalTicket(ctx context.Context, role string) (*genhighbay.TerminalTicketResult, error) {
	principal, ok := controlplane.PrincipalFromContext(ctx)
	if !ok {
		return nil, genhighbay.MakeForbidden(errors.New("caller may not access this project team"))
	}
	if _, err := s.liveRoleTerminal(ctx, role); err != nil {
		return nil, err
	}
	ticket, err := randomCredential()
	if err != nil {
		return nil, err
	}
	expires := s.team.tickets.add(ticket, terminalTicket{role: role, caller: principal.CallerID}, s.now())
	return &genhighbay.TerminalTicketResult{Ticket: ticket, ExpiresAt: expires.UTC().Format(time.RFC3339Nano)}, nil
}

// AttachTerminal consumes the request's ticket and relays one viewer's
// frames between the page and the role's terminal until the page disconnects
// or the terminal's stream closes. An unusable ticket is unprocessable; a
// role without a live terminal is terminal_unavailable. Both are answered
// before the connection is upgraded.
func (s *Server) AttachTerminal(
	ctx context.Context, p *genhighbay.AttachRoleTerminalPayload, viewer genhighbay.AttachRoleTerminalServerStream,
) error {
	ctx, span := otel.Tracer("highbay/project").Start(ctx, "highbay.project.attach_terminal")
	defer span.End()
	span.SetAttributes(attribute.String("team.role", p.Role), attribute.Bool("terminal.observe", p.Observe))
	principal, ok := controlplane.PrincipalFromContext(ctx)
	if !ok || !s.team.tickets.take(p.Ticket, terminalTicket{role: p.Role, caller: principal.CallerID}, s.now()) {
		return genhighbay.MakeUnprocessable(errors.New("the terminal ticket is used, expired or for another role; open the terminal again"))
	}
	terminal, err := s.liveRoleTerminal(ctx, p.Role)
	if err != nil {
		return err
	}
	client, err := s.team.terminals.client(ctx, s.team.dependencies.DialTerminals)
	if err != nil {
		return terminalUnavailable()
	}
	stream, err := client.Attach(ctx, hc.AttachRequest{
		Terminal: terminal.Address,
		Cols:     uint16(p.Cols),
		Rows:     uint16(p.Rows),
		History:  terminalHistory,
		Observe:  p.Observe,
	})
	if err != nil {
		return terminalUnavailable()
	}
	err = errors.Join(relayTerminal(ctx, stream, viewer), stream.Close())
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "terminal stream failed")
	}
	// The connection is already upgraded: there is no response left to carry
	// an error, and the page sees the stream end.
	return nil
}

// relayTerminal copies the terminal's events to the viewer and, on its own
// goroutine, the viewer's frames to the terminal. It returns when the
// terminal's stream closes, after telling the viewer why, or when the viewer
// disconnects. A viewer that simply leaves is not an error.
func relayTerminal(ctx context.Context, stream TerminalStream, viewer genhighbay.AttachRoleTerminalServerStream) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	viewerLeft := make(chan error, 1)
	go relayViewerFrames(ctx, stream, viewer, cancel, viewerLeft)
	err := relayTerminalEvents(ctx, stream, viewer)
	// Closing the viewer's connection ends relayViewerFrames' read.
	closeErr := viewer.Close()
	inputErr := <-viewerLeft
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		// The viewer left first; its own error, if any, is the cause.
		return inputErr
	}
	return errors.Join(err, closeErr)
}

// relayTerminalEvents sends each event of the terminal's stream to the
// viewer until the stream closes or ctx ends.
func relayTerminalEvents(ctx context.Context, stream TerminalStream, viewer genhighbay.AttachRoleTerminalServerStream) error {
	for {
		event, err := stream.Recv(ctx)
		if err != nil {
			return err
		}
		var frame genhighbay.ServerFrame
		switch event := event.(type) {
		case hc.TerminalOutput:
			frame = genhighbay.NewServerFrameOutput(&genhighbay.TerminalOutputFrame{Data: event.Data})
		case hc.TerminalResized:
			frame = genhighbay.NewServerFrameResize(&genhighbay.TerminalResize{Cols: int(event.Cols), Rows: int(event.Rows)})
		case hc.TerminalClosed:
			closed := &genhighbay.TerminalClosed{Reason: event.Reason.Reason}
			if closed.Reason == "" {
				closed.Reason = "detached"
			}
			if code := event.Reason.ExitCode; code != nil {
				value := int(*code)
				closed.ExitCode = &value
			}
			if signal := event.Reason.Signal; signal != nil {
				value := int(*signal)
				closed.Signal = &value
			}
			return viewer.SendWithContext(ctx, &genhighbay.TerminalServerFrame{ServerFrame: genhighbay.NewServerFrameClosed(closed)})
		}
		if err := viewer.SendWithContext(ctx, &genhighbay.TerminalServerFrame{ServerFrame: frame}); err != nil {
			return err
		}
	}
}

// relayViewerFrames applies each frame the viewer sends to the terminal:
// typed bytes, a new size, a focus change, or credit for output the page has
// drawn. When the viewer's connection ends it cancels the relay and reports
// on left why, or nil for a viewer that closed its connection.
func relayViewerFrames(
	ctx context.Context, stream TerminalStream, viewer genhighbay.AttachRoleTerminalServerStream,
	cancel context.CancelFunc, left chan<- error,
) {
	defer cancel()
	for {
		frame, err := viewer.RecvWithContext(ctx)
		if err != nil {
			// The read fails when the page closes the connection and when
			// relayTerminal does; neither is a failure of the stream.
			left <- nil
			return
		}
		if input, ok := frame.ClientFrame.AsInput(); ok {
			err = stream.WriteInput(input.Data)
		} else if resize, ok := frame.ClientFrame.AsResize(); ok {
			err = stream.Resize(uint16(resize.Cols), uint16(resize.Rows))
		} else if focus, ok := frame.ClientFrame.AsFocus(); ok {
			err = stream.Focus(focus.Focused)
		} else if credit, ok := frame.ClientFrame.AsCredit(); ok {
			err = stream.GrantCredit(uint32(credit.Bytes))
		}
		if err != nil {
			left <- err
			return
		}
	}
}

// terminalUnavailable is the page's answer when a role's terminal cannot be
// attached.
func terminalUnavailable() error {
	return genhighbay.MakeTerminalUnavailable(errors.New("that role has no live terminal to open; start the role, then open its terminal again"))
}

// liveRoleTerminal asks herdr-core for role's live terminal. It returns
// terminal_unavailable when there is none or the core cannot be asked.
func (s *Server) liveRoleTerminal(ctx context.Context, role string) (hc.Terminal, error) {
	client, err := s.team.terminals.client(ctx, s.team.dependencies.DialTerminals)
	if err != nil {
		return hc.Terminal{}, terminalUnavailable()
	}
	terminals, err := s.listRoleTerminals(ctx, client, role)
	if err != nil {
		return hc.Terminal{}, terminalUnavailable()
	}
	for _, terminal := range terminals {
		if terminal.State == hc.StateLive {
			return terminal, nil
		}
	}
	return hc.Terminal{}, terminalUnavailable()
}

// add records an unused ticket and returns when it expires. Expired tickets
// are dropped on the way.
func (t *terminalTickets) add(ticket string, grant terminalTicket, now time.Time) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.open == nil {
		t.open = make(map[string]terminalTicket)
	}
	for value, open := range t.open {
		if !now.Before(open.expires) {
			delete(t.open, value)
		}
	}
	grant.expires = now.Add(terminalTicketLifetime)
	t.open[ticket] = grant
	return grant.expires
}

// grant returns the role and session an unused, unexpired ticket was minted
// for, without using the ticket up.
func (t *terminalTickets) grant(ticket string, now time.Time) (terminalTicket, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	grant, found := t.find(ticket)
	if !found || !now.Before(grant.expires) {
		return terminalTicket{}, false
	}
	return grant, true
}

// take uses a ticket up. It reports whether the ticket was unused, unexpired
// and minted for want's role and session. A ticket presented for anything
// else is used up too.
func (t *terminalTickets) take(ticket string, want terminalTicket, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	grant, found := t.find(ticket)
	if !found {
		return false
	}
	delete(t.open, ticket)
	return now.Before(grant.expires) && grant.role == want.role && grant.caller == want.caller
}

// find looks a ticket up without leaking how much of it matched. t.mu is held.
func (t *terminalTickets) find(ticket string) (terminalTicket, bool) {
	var grant terminalTicket
	found := false
	for value, open := range t.open {
		if credentialEqual(ticket, value) {
			grant, found = open, true
		}
	}
	return grant, found
}
