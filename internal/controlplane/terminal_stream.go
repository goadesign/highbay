// This file bounds the WebSocket of a role's terminal stream. The generated
// stream upgrades the connection itself and offers no way to limit what it
// reads, to bound a write, or to close the connection when its closing
// message cannot be sent, so the upgrade is observed here and the connection
// is kept for the one request it belongs to.
package controlplane

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
)

type (
	// terminalConnKey is the context key of a terminal request's
	// *terminalConn.
	terminalConnKey struct{}

	// terminalConn holds the WebSocket of one terminal request once the
	// generated stream has upgraded it.
	terminalConn struct {
		// mu guards conn and closed.
		mu sync.Mutex
		// conn is nil until the upgrade.
		conn *websocket.Conn
		// closed is true once close ran, so a second close does nothing.
		closed bool
	}

	// terminalUpgrader upgrades terminal requests from the server's own
	// page, limits what each connection reads, and records the connection
	// for its request.
	terminalUpgrader struct {
		// upgrader does the WebSocket handshake.
		upgrader websocket.Upgrader
	}

	// boundedTerminalStream is the generated terminal stream with a bound
	// on every write and a Close that always closes the connection.
	boundedTerminalStream struct {
		genhighbay.AttachRoleTerminalServerStream
		// conn is the request's connection holder.
		conn *terminalConn
	}
)

const (
	// terminalFrameLimit is the largest frame a viewer may send: typed or
	// pasted input, encoded. A larger frame closes the connection.
	terminalFrameLimit = 1 << 20
	// terminalWriteWait bounds one write to a viewer. A page that stopped
	// reading, such as a suspended tab, ends its stream instead of holding
	// the terminal's relay.
	terminalWriteWait = 10 * time.Second
)

// Upgrade performs the handshake for a request terminalUpgradeOnly admitted
// and records the connection in the request's holder. The stream's first
// write follows the handshake at once, before the stream's wrapper can see
// the connection, so its bound is set here. A request whose holder was
// already closed gets no connection: its relay has returned, and nothing
// would ever close one opened now.
func (u *terminalUpgrader) Upgrade(w http.ResponseWriter, r *http.Request, header http.Header) (*websocket.Conn, error) {
	conn, err := u.upgrader.Upgrade(w, r, header)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(terminalFrameLimit)
	if err := conn.SetWriteDeadline(time.Now().Add(terminalWriteWait)); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	holder, ok := r.Context().Value(terminalConnKey{}).(*terminalConn)
	if !ok {
		return conn, nil
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()
	if holder.closed {
		return nil, errors.Join(errors.New("the terminal stream has ended"), conn.Close())
	}
	holder.conn = conn
	return conn, nil
}

// Send writes frame within terminalWriteWait.
func (s *boundedTerminalStream) Send(frame *genhighbay.TerminalServerFrame) error {
	return s.SendWithContext(context.Background(), frame)
}

// SendWithContext writes frame within terminalWriteWait. The first write
// upgrades the connection, whose upgrader sets that write's bound.
func (s *boundedTerminalStream) SendWithContext(ctx context.Context, frame *genhighbay.TerminalServerFrame) error {
	if err := s.conn.boundWrite(); err != nil {
		return err
	}
	return s.AttachRoleTerminalServerStream.SendWithContext(ctx, frame)
}

// Close sends the closing message when it still can and closes the
// connection in every case. The generated Close leaves the connection open
// when the closing message cannot be written, which is the ordinary case
// after the viewer closed first.
func (s *boundedTerminalStream) Close() error {
	err := s.conn.boundWrite()
	if err == nil {
		err = s.AttachRoleTerminalServerStream.Close()
	}
	return errors.Join(err, s.conn.close())
}

// boundWrite gives the connection's next write terminalWriteWait. Before
// the upgrade there is nothing to bound.
func (c *terminalConn) boundWrite() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.closed {
		return nil
	}
	return c.conn.SetWriteDeadline(time.Now().Add(terminalWriteWait))
}

// close ends the request's stream once: it closes the connection if the
// request was upgraded, which ends a read that is waiting for the viewer's
// next frame, and refuses an upgrade that comes later.
func (c *terminalConn) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
