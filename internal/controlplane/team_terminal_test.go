// This file opens a role's terminal stream over a real WebSocket through the
// generated HTTP server, as the views page's terminal drawer does.
package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	goahttp "goa.design/goa/v3/http"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genhighbayclient "github.com/goadesign/highbay/gen/http/highbay/client"
)

// terminalDialer opens the stream's WebSocket the way a browser page does: it
// names the page's origin on the upgrade, or none when origin is empty, and
// keeps the status of the server's answer.
type terminalDialer struct {
	// origin is the Origin header sent with the upgrade.
	origin string
	// status is the HTTP status the server answered the upgrade with.
	status int
}

// DialContext sends the upgrade with the dialer's origin.
func (d *terminalDialer) DialContext(
	ctx context.Context, target string, header http.Header,
) (*websocket.Conn, *http.Response, error) {
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

// TestRoleTerminalStream sends attach_role_terminal's WebSocket upgrade to
// NewHTTPHandler on a listening server and uses the generated client's stream.
//
// An upgrade naming the server's own origin reaches the team owner with the
// request's role, ticket and size, and frames cross in both directions: the
// fixture answers each input frame with an output frame of the same bytes.
// An upgrade naming no origin, another host, another port or the https scheme,
// and a request to the route that is not an upgrade, are answered 403 before
// the team owner is asked, so the request's single-use ticket is not used up.
//
// The diagnostics history then holds the accepted upgrade, although it was
// sent with GET: it opens a stream that can type into a terminal, so it is
// recorded like the user's other changes. The user's ordinary GET read of the
// team, sent to the same server, is still left out.
//
// Each case listens on a loopback port for a few milliseconds: an upgrade
// takes over a real connection, which a recorded response cannot provide.
func TestRoleTerminalStream(t *testing.T) {
	const owner = "alice@example.com"
	// serve starts one project's handler on a listening server.
	serve := func(t *testing.T) (*ControlPlane, *teamServiceFixture, *httptest.Server) {
		provider := sdktrace.NewTracerProvider()
		t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
		team := &teamServiceFixture{}
		cp := newControlPlane(t, Options{TracerProvider: provider, Team: &TeamBinding{Owner: owner, Service: team}})
		srv := httptest.NewServer(NewHTTPHandler(cp, func(*http.Request) (Principal, error) {
			return Principal{ID: owner, CallerID: "session", Kind: CallerUser}, nil
		}))
		t.Cleanup(srv.Close)
		return cp, team, srv
	}
	payload := genhighbay.AttachRoleTerminalPayload{Role: "builder", Ticket: fixtureTerminalTicket, Cols: 132, Rows: 43}
	// attach opens the builder's terminal stream on srv, naming origin, and
	// also returns the status the server answered the upgrade with.
	attach := func(t *testing.T, srv *httptest.Server, origin string) (genhighbay.AttachRoleTerminalClientStream, int, error) {
		address, err := url.Parse(srv.URL)
		require.NoError(t, err)
		dialer := &terminalDialer{origin: origin}
		transport := genhighbayclient.NewClient(address.Scheme, address.Host, srv.Client(),
			goahttp.RequestEncoder, goahttp.ResponseDecoder, false, dialer, nil)
		client := &genhighbay.Client{AttachRoleTerminalEndpoint: transport.AttachRoleTerminal()}
		request := payload
		stream, err := client.AttachRoleTerminal(t.Context(), &request)
		return stream, dialer.status, err
	}

	for _, refused := range []struct {
		name string
		// origin returns the Origin header sent to a server listening at host.
		origin func(host string) string
	}{
		{name: "no origin", origin: func(string) string { return "" }},
		{name: "another host", origin: func(string) string { return "http://example.com" }},
		{name: "another port", origin: func(string) string { return "http://127.0.0.1:1" }},
		{name: "https scheme", origin: func(host string) string { return "https://" + host }},
	} {
		t.Run("refused/"+refused.name, func(t *testing.T) {
			_, team, srv := serve(t)
			_, status, err := attach(t, srv, refused.origin(srv.Listener.Addr().String()))
			assert.Error(t, err)
			assert.Equal(t, http.StatusForbidden, status)
			// Close returns once the refused request's handler has ended.
			srv.Close()
			assert.Empty(t, team.recorded(), "a refused upgrade reached the team owner, which would use its ticket up")
		})
	}

	t.Run("refused/not an upgrade", func(t *testing.T) {
		_, team, srv := serve(t)
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
			srv.URL+"/team/roles/builder/terminal?ticket="+fixtureTerminalTicket, nil)
		require.NoError(t, err)
		request.Header.Set("Origin", srv.URL)
		response, err := srv.Client().Do(request)
		require.NoError(t, err)
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		assert.Equal(t, http.StatusForbidden, response.StatusCode)
		var refusal struct {
			Name string `json:"name"`
		}
		require.NoError(t, json.Unmarshal(body, &refusal))
		assert.Equal(t, "forbidden", refusal.Name)
		assert.Empty(t, team.recorded(), "a plain request reached the team owner")
	})

	t.Run("own origin", func(t *testing.T) {
		cp, team, srv := serve(t)
		response, err := srv.Client().Get(srv.URL + "/team")
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusOK, response.StatusCode)

		stream, status, err := attach(t, srv, srv.URL)
		require.NoError(t, err)
		assert.Equal(t, http.StatusSwitchingProtocols, status)
		for _, typed := range []string{"go test ./...\r", "\x03"} {
			input := genhighbay.NewClientFrameInput(&genhighbay.TerminalInputFrame{Data: []byte(typed)})
			require.NoError(t, stream.Send(&genhighbay.TerminalClientFrame{ClientFrame: input}))
			frame, err := stream.Recv()
			require.NoError(t, err)
			output, ok := frame.ServerFrame.AsOutput()
			require.True(t, ok, "the server sent a %s frame", frame.ServerFrame.Kind())
			assert.Equal(t, typed, string(output.Data))
		}
		require.NoError(t, stream.Close())

		// history returns the operations saved for the owner, with their caller.
		history := func() ([]string, error) {
			page, err := cp.traces.List(owner, "all", 100, nil)
			if err != nil {
				return nil, err
			}
			names := make([]string, 0, len(page.Spans))
			for _, span := range page.Spans {
				names = append(names, span.Name+" "+span.Attributes["highbay.caller_kind"])
			}
			return names, nil
		}
		// The server notices the client's close on its own goroutine, so the
		// upgrade's record arrives shortly after Close returns here.
		require.Eventually(t, func() bool {
			names, err := history()
			return err != nil || len(names) > 0
		}, 10*time.Second, 10*time.Millisecond, "the history never recorded the upgrade")
		saved, err := history()
		require.NoError(t, err)
		assert.Equal(t, []string{"highbay.request.attach_role_terminal user"}, saved,
			"the history holds something else than the one upgrade")
		assert.Equal(t, []string{"read", "attach:builder"}, team.recorded())
		team.mu.Lock()
		defer team.mu.Unlock()
		assert.Equal(t, &payload, team.attached)
		assert.Equal(t, 2, team.typed)
	})
}

type (
	// closeCountingListener counts how many accepted connections were
	// closed by the server.
	closeCountingListener struct {
		net.Listener
		// closed counts Close calls on accepted connections.
		closed *atomic.Int32
	}

	// closeCountingConn reports its first Close to its listener's counter.
	closeCountingConn struct {
		net.Conn
		once   sync.Once
		closed *atomic.Int32
	}
)

// Accept wraps the next connection.
func (l closeCountingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &closeCountingConn{Conn: conn, closed: l.closed}, nil
}

// Close counts the first close and closes the connection.
func (c *closeCountingConn) Close() error {
	c.once.Do(func() { c.closed.Add(1) })
	return c.Conn.Close()
}

// TestRoleTerminalStreamClosesItsConnection checks what the control plane
// adds around the generated stream: the server closes the viewer's
// connection when the page closes it first, which the generated Close alone
// does not, and when the page sends a frame over the limit.
//
// Each case listens on a loopback port for a few milliseconds, as an upgrade
// needs a real connection.
func TestRoleTerminalStreamClosesItsConnection(t *testing.T) {
	const owner = "alice@example.com"
	// dial opens the builder's terminal on a new server and returns the
	// viewer's connection and the server's count of closed connections.
	dial := func(t *testing.T) (*websocket.Conn, *atomic.Int32) {
		t.Helper()
		team := &teamServiceFixture{}
		cp := newControlPlane(t, Options{Team: &TeamBinding{Owner: owner, Service: team}})
		srv := httptest.NewUnstartedServer(NewHTTPHandler(cp, func(*http.Request) (Principal, error) {
			return Principal{ID: owner, CallerID: "session", Kind: CallerUser}, nil
		}))
		closed := &atomic.Int32{}
		srv.Listener = closeCountingListener{Listener: srv.Listener, closed: closed}
		srv.Start()
		t.Cleanup(srv.Close)
		target := "ws" + strings.TrimPrefix(srv.URL, "http") + "/team/roles/builder/terminal?ticket=" + fixtureTerminalTicket
		conn, response, err := websocket.DefaultDialer.DialContext(t.Context(), target, http.Header{"Origin": {srv.URL}})
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		t.Cleanup(func() {
			if closeErr := conn.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
				t.Logf("close test websocket: %v", closeErr)
			}
		})
		// One frame each way proves the stream is up before it is ended.
		input := genhighbay.NewClientFrameInput(&genhighbay.TerminalInputFrame{Data: []byte("ls")})
		require.NoError(t, conn.WriteJSON(genhighbayclient.NewAttachRoleTerminalStreamingBody(&genhighbay.TerminalClientFrame{ClientFrame: input})))
		_, _, err = conn.ReadMessage()
		require.NoError(t, err)
		return conn, closed
	}

	t.Run("the page closes first", func(t *testing.T) {
		conn, closed := dial(t)
		require.NoError(t, conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)))
		assert.Eventually(t, func() bool { return closed.Load() == 1 }, 5*time.Second, 5*time.Millisecond,
			"the server left the viewer's connection open")
	})

	t.Run("a frame over the limit", func(t *testing.T) {
		conn, closed := dial(t)
		// The write may fail once the server has stopped reading.
		if writeErr := conn.WriteMessage(websocket.TextMessage, bytes.Repeat([]byte("a"), terminalFrameLimit+1)); writeErr != nil {
			t.Logf("oversize frame write ended early as expected: %v", writeErr)
		}
		assert.Eventually(t, func() bool { return closed.Load() == 1 }, 5*time.Second, 5*time.Millisecond,
			"the server kept a connection that sent more than a frame may hold")
	})
}

// TestTerminalUpgradeAfterTheStreamEnded checks that a request whose stream
// already ended gets no WebSocket: the upgrader refuses it and closes the
// connection it had just upgraded, since nothing else would. An upgrade
// before that is recorded for the request and closed with it, once.
func TestTerminalUpgradeAfterTheStreamEnded(t *testing.T) {
	upgrader := &terminalUpgrader{upgrader: websocket.Upgrader{CheckOrigin: sameOriginUpgrade, HandshakeTimeout: terminalWriteWait}}
	// serve upgrades one request with holder, after prepare ran on it, and
	// reports the upgrader's error.
	serve := func(t *testing.T, holder *terminalConn, prepare func(*terminalConn)) (*websocket.Conn, chan error) {
		t.Helper()
		upgraded := make(chan error, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			prepare(holder)
			_, err := upgrader.Upgrade(w, r.WithContext(context.WithValue(r.Context(), terminalConnKey{}, holder)), nil)
			upgraded <- err
		}))
		t.Cleanup(srv.Close)
		conn, response, err := websocket.DefaultDialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(srv.URL, "http"), http.Header{"Origin": {srv.URL}})
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		t.Cleanup(func() {
			if closeErr := conn.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
				t.Logf("close test websocket: %v", closeErr)
			}
		})
		return conn, upgraded
	}

	t.Run("the stream ended before the upgrade", func(t *testing.T) {
		holder := &terminalConn{}
		conn, upgraded := serve(t, holder, func(holder *terminalConn) { require.NoError(t, holder.close()) })
		require.ErrorContains(t, <-upgraded, "the terminal stream has ended")
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
		_, _, err := conn.ReadMessage()
		assert.Error(t, err, "the refused connection was left open")
		holder.mu.Lock()
		defer holder.mu.Unlock()
		assert.Nil(t, holder.conn, "a refused upgrade was recorded")
	})

	t.Run("the stream ends after the upgrade", func(t *testing.T) {
		holder := &terminalConn{}
		conn, upgraded := serve(t, holder, func(*terminalConn) {})
		require.NoError(t, <-upgraded)
		require.NoError(t, holder.boundWrite())
		require.NoError(t, holder.close())
		require.NoError(t, holder.close(), "closing twice is not an error")
		require.NoError(t, holder.boundWrite(), "a closed stream has no write left to bound")
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
		_, _, err := conn.ReadMessage()
		assert.Error(t, err, "closing the stream left the viewer's connection open")
	})
}
