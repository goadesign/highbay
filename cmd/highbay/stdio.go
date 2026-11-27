// This file implements "highbay stdio": the MCP server every coding agent on
// this computer starts, registered once by `highbay setup -global`. It speaks
// newline-delimited JSON-RPC 2.0 on stdin and stdout.
//
// The folder the agent runs in decides who it is. Inside a project's
// builder working copy (.highbay-build) it is that project's builder; inside
// the test writer's (.highbay-tests) it is the test writer; anywhere else it
// is a guide. Until the agent calls a tool or reads a resource, stdio
// answers initialize, ping and the tool and resource lists itself from the
// generated MCP catalog, creating no state and starting no process. The
// first tool call binds the session: a guide starts or attaches to the
// folder's project server, hands over this shell's model settings and
// attaches as the project's guide; a worker only reads the credential the
// running server registered for it. Every later request is relayed to the
// server's /mcp with that agent's credential.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	mcpruntime "goa.design/goa-ai/runtime/mcp"
	goahttp "goa.design/goa/v3/http"
	"goa.design/goa/v3/jsonrpc"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genmcpserver "github.com/goadesign/highbay/gen/jsonrpc/mcp_highbay/server"
	genmcp "github.com/goadesign/highbay/gen/mcp_highbay"
	"github.com/goadesign/highbay/internal/abspath"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/modelsettings"
	"github.com/goadesign/highbay/internal/project"
	"github.com/goadesign/highbay/internal/testwriter"
)

type (
	// stdioSession is one coding agent's connection. Its role is fixed when
	// stdio starts; its relay is set by the first tool call or resource read
	// and replaced when the server stops accepting it.
	stdioSession struct {
		// role is guide, builder or test_writer, derived from the folder.
		role controlplane.CallerKind
		// discovery answers initialize, ping and the catalogs for role
		// without a server.
		discovery http.Handler
		// bind returns a relay to the project server for role, given the MCP
		// client name the agent reported in initialize ("" when unknown).
		bind func(ctx context.Context, client string) (*stdioRelay, error)
		// binding serializes binds so concurrent first calls share one start
		// and one guide attachment. It is held while the server starts, so
		// discovery and relayed requests never wait on it.
		binding sync.Mutex
		// mu guards client and relay.
		mu sync.Mutex
		// client is the coding-agent client named by initialize's clientInfo.
		client string
		// relay is the bound server connection, nil until the first bind.
		relay *stdioRelay
	}

	// stdioRelay sends JSON-RPC frames to one project server's /mcp as one
	// registered agent.
	stdioRelay struct {
		// endpoint is the server's MCP URL.
		endpoint string
		// credential is the agent's bearer credential; it never leaves the
		// Authorization header.
		credential string
		// http has no overall timeout: each request carries its own context.
		http *http.Client
	}

	// stdioFrame carries one raw line or read error from the stdin reader.
	stdioFrame struct {
		// payload is one trimmed non-empty JSON-RPC line.
		payload []byte
		// err is a non-EOF read error.
		err error
	}

	// stdioWriter serializes single-line JSON-RPC responses onto out.
	stdioWriter struct {
		// mu guards out and err.
		mu sync.Mutex
		// out receives newline-terminated frames.
		out io.Writer
		// err is the first write failure; later writes are skipped.
		err error
	}

	// stdioCancels maps in-flight request IDs to their cancel functions so
	// notifications/cancelled can stop them.
	stdioCancels struct {
		// mu guards next and active.
		mu sync.Mutex
		// next numbers registrations so release never removes a newer request
		// that reused an ID.
		next uint64
		// active maps a canonical JSON request ID to its registration.
		active map[string]stdioCancelEntry
	}

	// stdioCancelEntry is one registered in-flight request.
	stdioCancelEntry struct {
		// seq is the registration number.
		seq uint64
		// cancel stops the request's context.
		cancel context.CancelFunc
	}

	// memoryResponseWriter captures the in-process discovery handler's reply.
	memoryResponseWriter struct {
		// header holds the handler's response headers.
		header http.Header
		// status is the first status written.
		status int
		// body buffers the response bytes.
		body bytes.Buffer
	}
)

// stdioBindTimeout bounds starting or attaching to the project server. A
// relayed request has no stdio deadline: the server bounds its own operations
// (some run 30 minutes), and the agent stops one with notifications/cancelled.
const stdioBindTimeout = 2 * time.Minute

var (
	// errStdioServerStopped is a worker's answer when its project server is
	// not running: only the user starts it.
	errStdioServerStopped = errors.New("this project's Highbay server is not running; ask the user to run highbay in the project folder, or to reopen Highbay from their project coding agent (/highbay), then call the tool again")
	// stdioClientNames maps a word in the MCP clientInfo name to the client
	// attach_guide accepts.
	stdioClientNames = []string{"claude", "codex", "antigravity", "cursor", "gemini", "opencode"}
)

// runStdio serves MCP on in and out for the coding agent that started
// `highbay stdio` in its working folder, until in ends or the process gets
// SIGINT or SIGTERM. Notices go to diagnostics, never to out.
func runStdio(args []string, in io.Reader, out, diagnostics io.Writer, runtime projectRuntime) error {
	fs := flag.NewFlagSet("stdio", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	session, err := newStdioSession(runtime, cwd, os.Getenv("HC_TERMINAL_ID"), diagnostics)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return serveStdioLoop(ctx, in, out, session.dispatch)
}

// newStdioSession derives the agent's role from the physical folder cwd and
// prepares its offline catalog and binding. It reads nothing else and
// creates nothing.
func newStdioSession(runtime projectRuntime, cwd, terminal string, diagnostics io.Writer) (*stdioSession, error) {
	physical, err := abspath.Eval(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve the working folder: %w", err)
	}
	role, folder := stdioWorkerRole(physical)
	session := &stdioSession{role: role, discovery: newStdioDiscovery(role)}
	if role == controlplane.CallerGuide {
		session.bind = func(ctx context.Context, client string) (*stdioRelay, error) {
			return bindStdioGuide(ctx, runtime, physical, terminal, client, diagnostics)
		}
	} else {
		session.bind = func(ctx context.Context, _ string) (*stdioRelay, error) {
			return bindStdioWorker(ctx, runtime, physical, role, folder)
		}
	}
	return session, nil
}

// stdioWorkerRole reports the worker role whose working copy holds the
// physical folder dir, and that copy's folder name: the innermost
// .highbay-build component makes a builder and .highbay-tests a test writer.
// Any other folder is a guide's. A worker never becomes a guide, even when
// no project owns its copy.
func stdioWorkerRole(dir string) (controlplane.CallerKind, string) {
	parts := strings.Split(filepath.ToSlash(dir), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		switch parts[i] {
		case testwriter.BuilderRole.Folder:
			return controlplane.CallerBuilder, parts[i]
		case testwriter.TestsRole.Folder:
			return controlplane.CallerTestWriter, parts[i]
		}
	}
	return controlplane.CallerGuide, ""
}

// newStdioDiscovery mounts the generated MCP server over a catalog-only
// adapter, keeping the tools and resources role may call, exactly as the
// project server filters them. Only discovery methods reach it; a tool call
// or resource read is always relayed.
func newStdioDiscovery(role controlplane.CallerKind) http.Handler {
	endpoints := genmcp.NewEndpoints(genmcp.NewMCPAdapter(&genhighbay.Endpoints{}, nil))
	tools := endpoints.ToolsList
	endpoints.ToolsList = func(ctx context.Context, req any) (any, error) {
		result, err := tools(ctx, req)
		if err != nil {
			return nil, err
		}
		catalog := result.(*genmcp.ToolsListResult)
		catalog.Tools = slices.DeleteFunc(catalog.Tools, func(tool *genmcp.ToolInfo) bool {
			return !controlplane.OperationAllowed(role, tool.Name)
		})
		return catalog, nil
	}
	resources := endpoints.ResourcesList
	endpoints.ResourcesList = func(ctx context.Context, req any) (any, error) {
		result, err := resources(ctx, req)
		if err != nil {
			return nil, err
		}
		catalog := result.(*genmcp.ResourcesListResult)
		catalog.Resources = slices.DeleteFunc(catalog.Resources, func(resource *genmcp.ResourceInfo) bool {
			return !controlplane.OperationAllowed(role, resource.Name)
		})
		return catalog, nil
	}
	mux := goahttp.NewMuxer()
	genmcpserver.Mount(mux, genmcpserver.New(endpoints, mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, nil))
	return mux
}

// bindStdioGuide starts or attaches to the project server of the physical
// folder dir (the nearest project at or above it, else a new one for dir),
// hands it this process's model provider settings, attaches the agent as the
// project's guide (labeling its herdr-core terminal when it runs in one) and
// returns a relay with the guide's credential.
func bindStdioGuide(
	ctx context.Context, runtime projectRuntime, dir, terminal, client string, diagnostics io.Writer,
) (*stdioRelay, error) {
	if err := runtime.preflight(false); err != nil {
		return nil, err
	}
	root, err := runtime.operatorRoot(dir)
	if err != nil {
		return nil, err
	}
	state, err := project.Discover(root, dir)
	if err != nil {
		return nil, err
	}
	management, created, err := runtime.attachProject(ctx, state)
	if created {
		err = errors.Join(err, writeProjectCreatedNotice(diagnostics))
	}
	if err != nil {
		return nil, err
	}
	if err := handOverModelSettings(ctx, state, modelsettings.Select(os.Environ()), diagnostics); err != nil {
		return nil, err
	}
	payload := buildAttachGuidePayload(ctx, terminal, client)
	attached, err := management.AttachGuide(ctx, payload)
	if err != nil {
		return nil, fmt.Errorf("attach this agent as the project's guide: %w", err)
	}
	credential, err := state.AgentCredential(attached.Handle)
	if err != nil {
		return nil, fmt.Errorf("read the guide's credential: %w", err)
	}
	return newStdioRelay(state, credential), nil
}

// bindStdioWorker finds the project whose working copy named folder holds
// dir and returns a relay with the credential of the agent registered for
// role there: the one whose registered working folder holds dir, or, in the
// project folder's own copy, the only agent with role. A project created in
// a child folder keeps its copies there, which only the registration names.
// It never starts a server, never hands over model settings and never
// attaches a guide: a stopped server or a missing registration is an error
// the agent reports to the user.
func bindStdioWorker(
	ctx context.Context, runtime projectRuntime, dir string, role controlplane.CallerKind, folder string,
) (*stdioRelay, error) {
	noProject := fmt.Errorf("this %s folder belongs to no Highbay project; work only in the folder your assignment names", folder)
	root, err := runtime.operatorRootCandidate(dir)
	if err != nil {
		return nil, err
	}
	if err := validateOperatorPrivateDir(root); errors.Is(err, os.ErrNotExist) {
		return nil, noProject
	} else if err != nil {
		return nil, fmt.Errorf("read operator storage root %s: %w", root, err)
	}
	state, err := project.Discover(root, dir)
	if err != nil {
		return nil, err
	}
	if state.Record.ID == "" {
		return nil, noProject
	}
	inUse, err := controlplane.CardStoreInUse(state.CardsDir())
	if err != nil {
		return nil, err
	}
	if !inUse {
		return nil, errStdioServerStopped
	}
	identity, err := confirmProject(ctx, state, projectClient(state))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", projectHolder(state), err)
	}
	if err := confirmBuild(identity); err != nil {
		return nil, err
	}
	var registered, holding []string
	for handle, agent := range state.Record.Agents {
		if agent.Role != string(role) {
			continue
		}
		registered = append(registered, handle)
		within, err := stdioWithin(agent.Folder, dir)
		if err != nil {
			return nil, err
		}
		if within {
			holding = append(holding, handle)
		}
	}
	handle := ""
	switch {
	case len(holding) == 1:
		handle = holding[0]
	case len(holding) == 0 && len(registered) == 1:
		rootCopy, err := stdioWithin(filepath.Join(state.Record.Directory, folder), dir)
		if err != nil {
			return nil, err
		}
		if !rootCopy {
			return nil, noProject
		}
		handle = registered[0]
	case len(registered) == 0:
		return nil, fmt.Errorf("this project has no registered %s; ask the user to start the build's agents from the views page", role)
	default:
		return nil, fmt.Errorf("this folder is not the working folder of this project's %s; work only in the folder your assignment names", role)
	}
	credential, err := state.AgentCredential(handle)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("the project's %s has no credential yet; ask the user to start the build's agents from the views page", role)
	}
	if err != nil {
		return nil, fmt.Errorf("read the %s credential: %w", role, err)
	}
	return newStdioRelay(state, credential), nil
}

// stdioWithin reports whether dir is in root's physical subtree; a missing
// root holds nothing.
func stdioWithin(root, dir string) (bool, error) {
	within, err := abspath.Within(root, dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return within, err
}

// newStdioRelay connects to state's server as the agent holding credential,
// with the management transport's proxy and redirect refusal but no overall
// timeout: the request's context ends it.
func newStdioRelay(state *project.State, credential string) *stdioRelay {
	client := projectHTTPClient()
	client.Timeout = 0
	return &stdioRelay{endpoint: state.Origin() + "/mcp", credential: credential, http: client}
}

// dispatch answers one JSON-RPC request. A tool call or resource read binds
// the session first; before that, every other request is answered from the
// offline catalog, and after it every request is relayed. When the server
// certainly did not run the request, because it refused the credential or
// no server accepted the connection, the session binds again and sends it
// once more; any other failure is reported, so a tool that changes state is
// never sent twice.
func (s *stdioSession) dispatch(
	ctx context.Context, payload []byte, raw *jsonrpc.RawRequest, notify func([]byte),
) ([]byte, error) {
	method := ""
	if raw != nil && !raw.Invalid {
		method = raw.Method
	}
	if method == "initialize" {
		s.recordClient(raw.Params)
	}
	relay := s.currentRelay()
	if relay == nil && method != "tools/call" && method != "resources/read" {
		return serveStdioDiscovery(ctx, s.discovery, payload, raw)
	}
	for attempt := 0; ; attempt++ {
		if relay == nil {
			var err error
			if relay, err = s.bindRelay(ctx); err != nil {
				return stdioErrorResponse(raw, err), nil
			}
		}
		status, body, err := relay.send(ctx, payload, raw, notify)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt == 0 && (status == http.StatusUnauthorized || errors.Is(err, syscall.ECONNREFUSED)) {
			s.dropRelay(relay)
			relay = nil
			continue
		}
		if err != nil {
			return nil, err
		}
		return validateStdioHTTPResponse(status, body, relay.endpoint)
	}
}

// recordClient keeps the attach_guide client named by initialize's
// clientInfo, such as "claude" for "claude-code".
func (s *stdioSession) recordClient(params json.RawMessage) {
	var initialize struct {
		// ClientInfo names the connecting MCP client.
		ClientInfo struct {
			// Name is the client's self-reported name.
			Name string `json:"name"`
		} `json:"clientInfo"`
	}
	if json.Unmarshal(params, &initialize) != nil {
		return
	}
	name := strings.ToLower(initialize.ClientInfo.Name)
	for _, client := range stdioClientNames {
		if strings.Contains(name, client) {
			s.mu.Lock()
			s.client = client
			s.mu.Unlock()
			return
		}
	}
}

// currentRelay returns the bound relay, or nil before the first bind.
func (s *stdioSession) currentRelay() *stdioRelay {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.relay
}

// bindRelay binds the session once at a time: concurrent first calls share
// one start and one guide attachment.
func (s *stdioSession) bindRelay(ctx context.Context) (*stdioRelay, error) {
	s.binding.Lock()
	defer s.binding.Unlock()
	s.mu.Lock()
	relay, client := s.relay, s.client
	s.mu.Unlock()
	if relay != nil {
		return relay, nil
	}
	bindCtx, cancel := context.WithTimeout(ctx, stdioBindTimeout)
	defer cancel()
	relay, err := s.bind(bindCtx, client)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.relay = relay
	s.mu.Unlock()
	return relay, nil
}

// dropRelay forgets relay unless another request already replaced it.
func (s *stdioSession) dropRelay(relay *stdioRelay) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.relay == relay {
		s.relay = nil
	}
}

// send posts payload to the server as the relay's agent and returns the
// HTTP status and the response body. A request that asks for progress may be
// answered as an event stream: send passes each notification of the stream
// to notify as it arrives, such as the phases of init_project, and returns
// the stream's one response.
func (r *stdioRelay) send(
	ctx context.Context, payload []byte, raw *jsonrpc.RawRequest, notify func([]byte),
) (status int, body []byte, err error) {
	req, err := newStdioMCPRequest(ctx, r.endpoint, payload, raw)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.credential)
	if stdioAsksProgress(raw) {
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); media == "text/event-stream" {
		body, err = readStdioEventStream(resp.Body, notify)
		return resp.StatusCode, body, err
	}
	body, err = io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}

// stdioAsksProgress reports whether the frame's _meta carries a progress
// token, the client's request for progress notifications.
func stdioAsksProgress(raw *jsonrpc.RawRequest) bool {
	if raw == nil || raw.Invalid || len(raw.Params) == 0 {
		return false
	}
	var params struct {
		// Meta carries the client's progress token.
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(raw.Params, &params) != nil {
		return false
	}
	token, asks := params.Meta["progressToken"]
	return asks && !bytes.Equal(bytes.TrimSpace(token), []byte("null"))
}

// readStdioEventStream reads a server-sent event stream whose events each
// hold one JSON-RPC message in their data. It passes every message without
// an ID, a notification, to notify and returns the one message with an ID,
// the response; data that is no JSON object is skipped. Comment lines, which the server sends to keep a long call
// alive, and other fields are skipped. Only an event ended by its blank line
// counts: data cut off by the end of the stream is dropped, as the server's
// own reader drops it. The response ends the read at once, without waiting
// for the stream to close, so a connection that fails afterwards cannot
// discard it. A stream that ends without a complete response is an error:
// the call's outcome is unknown.
func readStdioEventStream(stream io.Reader, notify func([]byte)) ([]byte, error) {
	reader := bufio.NewReader(stream)
	var data, response []byte
	flush := func() {
		message := bytes.TrimSpace(data)
		data = nil
		if len(message) == 0 {
			return
		}
		var envelope struct {
			// ID is present on a response and absent on a notification.
			ID json.RawMessage `json:"id"`
		}
		switch {
		case json.Unmarshal(message, &envelope) != nil:
			// Not a JSON-RPC message: neither forwarded nor a response.
		case len(envelope.ID) == 0:
			notify(message)
		default:
			response = message
		}
	}
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			// The stream ended: a line without its newline and an event
			// without its blank line were both cut off, and are dropped.
			if !errors.Is(err, io.EOF) {
				return nil, err
			}
			return nil, errors.New("the server's event stream ended without a response")
		}
		line = bytes.TrimRight(line, "\r\n")
		switch {
		case len(line) == 0:
			if flush(); response != nil {
				return response, nil
			}
		case bytes.HasPrefix(line, []byte("data:")):
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" "))...)
		}
	}
}

// serveStdioDiscovery answers one discovery request with the in-process
// catalog handler.
func serveStdioDiscovery(ctx context.Context, handler http.Handler, payload []byte, raw *jsonrpc.RawRequest) ([]byte, error) {
	req, err := newStdioMCPRequest(ctx, "http://highbay-stdio/mcp", payload, raw)
	if err != nil {
		return nil, err
	}
	w := &memoryResponseWriter{header: make(http.Header)}
	handler.ServeHTTP(w, req)
	return validateStdioHTTPResponse(w.status, w.body.Bytes(), "the offline catalog")
}

// stdioErrorResponse reports a failed bind to the agent as a JSON-RPC error
// with the request's ID, so the agent can tell the user what to do.
func stdioErrorResponse(raw *jsonrpc.RawRequest, err error) []byte {
	var id any
	if raw != nil && raw.HasID {
		id = raw.ID
	}
	out, marshalErr := json.Marshal(jsonrpc.MakeErrorResponse(id, jsonrpc.InternalError, "highbay: "+err.Error(), nil))
	if marshalErr != nil {
		return nil
	}
	return out
}

// serveStdioLoop reads frames from in and dispatches each request on its own
// goroutine, so a long tool call does not hold up pings or discovery, until
// in ends or ctx is done. It then waits for the requests in flight.
func serveStdioLoop(
	ctx context.Context,
	in io.Reader,
	out io.Writer,
	dispatch func(context.Context, []byte, *jsonrpc.RawRequest, func([]byte)) ([]byte, error),
) error {
	frames := make(chan stdioFrame)
	go readStdioFrames(ctx, in, frames)
	writer := &stdioWriter{out: out}
	cancels := &stdioCancels{active: make(map[string]stdioCancelEntry)}
	var (
		wg      sync.WaitGroup
		readErr error
	)
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case frame, ok := <-frames:
			if !ok {
				break loop
			}
			if frame.err != nil {
				readErr = frame.err
				break loop
			}
			var raw jsonrpc.RawRequest
			decoded := raw.UnmarshalJSON(frame.payload) == nil
			if decoded && !raw.Invalid && raw.JSONRPC == "2.0" && raw.HasMethod && raw.Method != "" && !raw.HasID {
				handleStdioNotification(&raw, cancels)
				continue
			}
			var rawPtr *jsonrpc.RawRequest
			if decoded {
				rawPtr = &raw
			}
			reqCtx, cancel := context.WithCancel(ctx)
			idKey, seq := "", uint64(0)
			if rawPtr != nil && !rawPtr.Invalid && rawPtr.HasID {
				// Register before starting the request so a cancellation that
				// follows immediately finds it.
				if idKey = canonicalStdioIDKey(rawPtr.ID); idKey != "" {
					seq = cancels.track(idKey, cancel)
				}
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer cancel()
				if idKey != "" {
					defer cancels.release(idKey, seq)
				}
				// A notification the server sends while it works, such as
				// progress, goes out at once, ahead of the response.
				notify := func(message []byte) {
					if line := compactStdioJSON(message); len(line) > 0 && reqCtx.Err() == nil {
						writer.writeLine(line)
					}
				}
				resp, err := dispatch(reqCtx, frame.payload, rawPtr, notify)
				if reqCtx.Err() != nil {
					return
				}
				if err != nil {
					resp = stdioErrorResponse(rawPtr, err)
				}
				if line := compactStdioJSON(resp); len(line) > 0 {
					writer.writeLine(line)
				}
			}()
		}
	}
	wg.Wait()
	if readErr != nil {
		return fmt.Errorf("read stdio input: %w", readErr)
	}
	return writer.firstErr()
}

// readStdioFrames sends each non-empty trimmed line of in to frames until
// EOF, a read error or ctx's end, then closes frames.
func readStdioFrames(ctx context.Context, in io.Reader, frames chan<- stdioFrame) {
	defer close(frames)
	reader := bufio.NewReader(in)
	for {
		line, err := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			select {
			case frames <- stdioFrame{payload: bytes.Clone(trimmed)}:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				select {
				case frames <- stdioFrame{err: err}:
				case <-ctx.Done():
				}
			}
			return
		}
	}
}

// handleStdioNotification stops the request a notifications/cancelled
// names. Other notifications need no reply and are not forwarded.
func handleStdioNotification(raw *jsonrpc.RawRequest, cancels *stdioCancels) {
	if raw.Method != "notifications/cancelled" || len(raw.Params) == 0 {
		return
	}
	var params struct {
		// RequestID is the ID of the request to stop.
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(raw.Params, &params) != nil || len(params.RequestID) == 0 {
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(params.RequestID))
	decoder.UseNumber()
	var id any
	if decoder.Decode(&id) != nil {
		return
	}
	if key := canonicalStdioIDKey(id); key != "" {
		cancels.cancel(key)
	}
}

// validateStdioHTTPResponse requires a JSON body: every dispatched frame is a
// request, so an empty or 202 reply means the server failed it.
func validateStdioHTTPResponse(status int, body []byte, target string) ([]byte, error) {
	trimmed := bytes.TrimSpace(body)
	if status == http.StatusAccepted || len(trimmed) == 0 {
		return nil, fmt.Errorf("unexpected empty HTTP %d response from %s", status, target)
	}
	if !json.Valid(trimmed) {
		return nil, fmt.Errorf("unexpected HTTP %d response from %s that is not JSON", status, target)
	}
	return trimmed, nil
}

// newStdioMCPRequest builds the HTTP POST for one frame. A frame whose params
// carry the 2026-07-28 protocol version in _meta gets that version, the
// method header and, for a method that names its target, the name header;
// any other request after initialize gets the legacy protocol version, which
// the generated server serves statelessly.
func newStdioMCPRequest(ctx context.Context, target string, payload []byte, raw *jsonrpc.RawRequest) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if raw != nil && !raw.Invalid {
		// Typed fields keep the lookup the same as the generated decoder's,
		// so the header names the target the server dispatches.
		var params struct {
			// Name is a tools/call tool name or a prompts/get prompt name.
			Name json.RawMessage `json:"name"`
			// URI is a resources/read resource.
			URI json.RawMessage `json:"uri"`
			// TaskID is a task method's task.
			TaskID json.RawMessage `json:"taskId"`
			// Meta carries the modern protocol version.
			Meta map[string]any `json:"_meta"`
		}
		if len(raw.Params) > 0 && json.Unmarshal(raw.Params, &params) == nil {
			if version, ok := params.Meta["io.modelcontextprotocol/protocolVersion"].(string); ok && version != "" {
				req.Header.Set("MCP-Protocol-Version", version)
				req.Header.Set("Mcp-Method", raw.Method)
				// The protocol mirrors one params field per method in the
				// name header; the server refuses a missing or different one.
				var field json.RawMessage
				switch raw.Method {
				case "tools/call", "prompts/get":
					field = params.Name
				case "resources/read":
					field = params.URI
				case "tasks/get", "tasks/update", "tasks/cancel":
					field = params.TaskID
				}
				var name string
				if len(field) > 0 && json.Unmarshal(field, &name) == nil {
					req.Header.Set("Mcp-Name", encodeMCPHeaderValue(name))
				}
				return req, nil
			}
		}
		if raw.Method == "initialize" {
			return req, nil
		}
	}
	req.Header.Set("MCP-Protocol-Version", mcpruntime.LegacyProtocolVersion)
	return req, nil
}

// encodeMCPHeaderValue wraps a header value in MCP's base64 envelope when it
// has surrounding spaces, bytes outside printable ASCII or the envelope's
// own prefix.
func encodeMCPHeaderValue(value string) string {
	unsafe := strings.HasPrefix(value, "=?base64?") || strings.TrimSpace(value) != value
	for i := range len(value) {
		if value[i] < 0x20 || value[i] > 0x7e {
			unsafe = true
			break
		}
	}
	if unsafe {
		return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(value)) + "?="
	}
	return value
}

// canonicalStdioIDKey encodes a decoded request ID as a map key that keeps
// numbers and strings distinct, or "" for a null ID.
func canonicalStdioIDKey(id any) string {
	if id == nil {
		return ""
	}
	encoded, err := json.Marshal(id)
	if err != nil || bytes.Equal(encoded, []byte("null")) {
		return ""
	}
	return string(encoded)
}

// compactStdioJSON removes newlines so each response is one stdout line.
func compactStdioJSON(raw []byte) []byte {
	var compact bytes.Buffer
	if err := json.Compact(&compact, bytes.TrimSpace(raw)); err != nil {
		return nil
	}
	return compact.Bytes()
}

// Header returns the buffered response headers.
func (w *memoryResponseWriter) Header() http.Header { return w.header }

// Write appends response bytes, recording 200 when no status was written.
func (w *memoryResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

// WriteHeader records the first status written.
func (w *memoryResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

// writeLine writes one newline-terminated frame, keeping the first error.
func (w *stdioWriter) writeLine(line []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return
	}
	if _, err := w.out.Write(append(line, '\n')); err != nil {
		w.err = err
	}
}

// firstErr returns the first write error.
func (w *stdioWriter) firstErr() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// track registers cancel for the request idKey and returns its number.
func (c *stdioCancels) track(idKey string, cancel context.CancelFunc) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	c.active[idKey] = stdioCancelEntry{seq: c.next, cancel: cancel}
	return c.next
}

// cancel stops the request idKey when it is in flight.
func (c *stdioCancels) cancel(idKey string) {
	c.mu.Lock()
	entry, ok := c.active[idKey]
	delete(c.active, idKey)
	c.mu.Unlock()
	if ok {
		entry.cancel()
	}
}

// release removes the registration seq of idKey when it is still current.
func (c *stdioCancels) release(idKey string, seq uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.active[idKey]; ok && entry.seq == seq {
		delete(c.active, idKey)
	}
}
