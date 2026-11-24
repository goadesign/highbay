package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpruntime "goa.design/goa-ai/runtime/mcp"
	"goa.design/goa/v3/jsonrpc"

	"github.com/goadesign/surla/internal/abspath"
	"github.com/goadesign/surla/internal/controlplane"
	"github.com/goadesign/surla/internal/project"
)

// stdioReply is one JSON-RPC response line read back from stdout.
type stdioReply struct {
	// ID is the request ID the response answers.
	ID json.RawMessage `json:"id"`
	// Result is the successful result.
	Result json.RawMessage `json:"result"`
	// Error is the failure, when there is one.
	Error *struct {
		// Code is the JSON-RPC error code.
		Code int `json:"code"`
		// Message explains the failure.
		Message string `json:"message"`
	} `json:"error"`
}

// runStdioFrames serves lines through session and returns the replies by
// request ID once input ends and every request has finished.
func runStdioFrames(t *testing.T, session *stdioSession, lines ...string) map[string]stdioReply {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, serveStdioLoop(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out, session.dispatch))
	replies := make(map[string]stdioReply)
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var reply stdioReply
		require.NoError(t, json.Unmarshal([]byte(line), &reply), line)
		replies[string(reply.ID)] = reply
	}
	return replies
}

// toolNames lists the tool names of a tools/list result.
func toolNames(t *testing.T, result json.RawMessage) []string {
	t.Helper()
	var list struct {
		// Tools is the catalog.
		Tools []struct {
			// Name is the tool's name.
			Name string `json:"name"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(result, &list))
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// failingBind fails the test when a session binds: discovery must not.
func failingBind(t *testing.T) func(context.Context, string) (*stdioRelay, error) {
	return func(context.Context, string) (*stdioRelay, error) {
		t.Error("discovery bound the session")
		return nil, io.ErrUnexpectedEOF
	}
}

func TestStdioDiscoveryCreatesNothing(t *testing.T) {
	configRoot := t.TempDir()
	dir := t.TempDir()
	session, err := newStdioSession(testProjectRuntimeAt(configRoot), dir, "t42", io.Discard)
	require.NoError(t, err)
	assert.Equal(t, controlplane.CallerGuide, session.role)
	session.bind = failingBind(t)

	replies := runStdioFrames(t, session,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"claude-code","version":"2"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/list"}`,
	)
	require.Len(t, replies, 4)
	for id, reply := range replies {
		assert.Nil(t, reply.Error, id)
	}
	var initialize struct {
		// ProtocolVersion is the negotiated version.
		ProtocolVersion string `json:"protocolVersion"`
		// ServerInfo names the server.
		ServerInfo struct {
			// Name is the server's name.
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	require.NoError(t, json.Unmarshal(replies["1"].Result, &initialize))
	assert.Equal(t, "2025-11-25", initialize.ProtocolVersion)
	assert.Equal(t, "surla", initialize.ServerInfo.Name)
	tools := toolNames(t, replies["3"].Result)
	for _, name := range []string{"propose_change", "revise_change", "init_project", "open_views", "update_draft", "get_build"} {
		assert.Contains(t, tools, name)
	}
	for _, name := range []string{"generate_code", "confirm_intent", "approve_merge", "report_tests_finished"} {
		assert.NotContains(t, tools, name)
	}
	assert.Contains(t, string(replies["4"].Result), "surla://golden-path")
	assert.Equal(t, "claude", session.client)

	for _, folder := range []string{configRoot, dir} {
		entries, err := os.ReadDir(folder)
		require.NoError(t, err)
		assert.Empty(t, entries, "discovery wrote into %s", folder)
	}
}

func TestStdioWorkerFoldersBindOnlyTheirRole(t *testing.T) {
	project := t.TempDir()
	for _, dir := range []string{".surla-build/surla/build-1/sub", ".surla-tests/eval", "nested/.surla-build/x/.surla-tests/y"} {
		require.NoError(t, os.MkdirAll(filepath.Join(project, dir), 0o755))
	}
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(filepath.Join(project, ".surla-build", "surla"), alias))

	cases := []struct {
		name, dir  string
		role       controlplane.CallerKind
		has, lacks string
	}{
		{"project folder", project, controlplane.CallerGuide, "propose_change", "generate_code"},
		{"builder copy with a nested prefix", filepath.Join(project, ".surla-build", "surla", "build-1", "sub"), controlplane.CallerBuilder, "generate_code", "propose_change"},
		{"builder copy through a symlink", filepath.Join(alias, "build-1"), controlplane.CallerBuilder, "generate_code", "init_project"},
		{"test writer's eval module", filepath.Join(project, ".surla-tests", "eval"), controlplane.CallerTestWriter, "report_tests_finished", "propose_change"},
		{"innermost copy decides", filepath.Join(project, "nested", ".surla-build", "x", ".surla-tests", "y"), controlplane.CallerTestWriter, "get_test_writer_assignment", "generate_code"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			session, err := newStdioSession(testProjectRuntimeAt(t.TempDir()), c.dir, "", io.Discard)
			require.NoError(t, err)
			assert.Equal(t, c.role, session.role)
			session.bind = failingBind(t)
			replies := runStdioFrames(t, session, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
			tools := toolNames(t, replies["1"].Result)
			assert.Contains(t, tools, c.has)
			assert.NotContains(t, tools, c.lacks)
		})
	}
}

func TestStdioWorkerWithoutARunningProjectNeverBecomesAGuide(t *testing.T) {
	configRoot := t.TempDir()
	runtime := testProjectRuntimeAt(configRoot)
	workspace, err := abspath.Eval(t.TempDir())
	require.NoError(t, err)
	worker := filepath.Join(workspace, ".surla-tests", "eval")
	require.NoError(t, os.MkdirAll(worker, 0o755))
	call := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"list_test_writer_assignments","arguments":{}}}`

	// No operator storage: the copy belongs to no project.
	session, err := newStdioSession(runtime, worker, "t42", io.Discard)
	require.NoError(t, err)
	reply := runStdioFrames(t, session, call)["7"]
	require.NotNil(t, reply.Error)
	assert.Contains(t, reply.Error.Message, "belongs to no Surla project")
	entries, err := os.ReadDir(configRoot)
	require.NoError(t, err)
	assert.Empty(t, entries, "a worker bind created operator storage")

	// A project whose server is stopped: the worker is told to ask the user.
	root, err := runtime.operatorRoot(workspace)
	require.NoError(t, err)
	initializeTestProject(t, root, workspace)
	session, err = newStdioSession(runtime, worker, "t42", io.Discard)
	require.NoError(t, err)
	reply = runStdioFrames(t, session, call)["7"]
	require.NotNil(t, reply.Error)
	assert.Contains(t, reply.Error.Message, errStdioServerStopped.Error())
}

// initializeTestProject saves a project record for workspace under the
// operator root, as the first `surla` start does, without starting a server.
func initializeTestProject(t *testing.T, root, workspace string) {
	t.Helper()
	state, err := project.Discover(root, workspace)
	require.NoError(t, err)
	lock, err := state.StartLock(context.Background())
	require.NoError(t, err)
	created, err := state.Initialize("uid:test")
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, lock.Close())
}

func TestStdioRelaysCallsWithTheBoundCredential(t *testing.T) {
	var (
		mu       sync.Mutex
		received []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			// ID is the relayed request's ID.
			ID json.RawMessage `json:"id"`
			// Method is the relayed method.
			Method string `json:"method"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		mu.Lock()
		received = append(received, request.Method+" "+r.Header.Get("Authorization")+" "+r.Header.Get("MCP-Protocol-Version"))
		mu.Unlock()
		if r.Header.Get("Authorization") == "Bearer stale" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(request.ID)+`,"result":{"method":"`+request.Method+`"}}`)
		assert.NoError(t, err)
	}))
	defer server.Close()

	session, err := newStdioSession(testProjectRuntimeAt(t.TempDir()), t.TempDir(), "", io.Discard)
	require.NoError(t, err)
	var binds []string
	credentials := []string{"stale", "fresh"}
	session.bind = func(_ context.Context, client string) (*stdioRelay, error) {
		binds = append(binds, client)
		credential := credentials[len(binds)-1]
		return &stdioRelay{endpoint: server.URL + "/mcp", credential: credential, http: server.Client()}, nil
	}

	replies := runStdioFrames(t, session,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"codex-mcp-client","version":"1"}}}`,
	)
	assert.Nil(t, replies["1"].Error)
	replies = runStdioFrames(t, session,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_changes","arguments":{}}}`,
	)
	assert.JSONEq(t, `{"method":"tools/call"}`, string(replies["2"].Result))
	replies = runStdioFrames(t, session, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	assert.JSONEq(t, `{"method":"tools/list"}`, string(replies["3"].Result))

	assert.Equal(t, []string{"codex", "codex"}, binds, "a refused credential binds once more")
	assert.Equal(t, []string{
		"tools/call Bearer stale 2025-11-25",
		"tools/call Bearer fresh 2025-11-25",
		"tools/list Bearer fresh 2025-11-25",
	}, received)
}

func TestStdioReportsABindFailureToTheAgent(t *testing.T) {
	session, err := newStdioSession(testProjectRuntimeAt(t.TempDir()), t.TempDir(), "", io.Discard)
	require.NoError(t, err)
	session.bind = func(context.Context, string) (*stdioRelay, error) {
		return nil, errStdioServerStopped
	}
	reply := runStdioFrames(t, session, `{"jsonrpc":"2.0","id":"a","method":"resources/read","params":{"uri":"surla://golden-path"}}`)[`"a"`]
	require.NotNil(t, reply.Error)
	assert.Equal(t, -32603, reply.Error.Code)
	assert.Equal(t, "surla: "+errStdioServerStopped.Error(), reply.Error.Message)
	assert.Nil(t, session.currentRelay(), "a failed bind binds nothing")
}

func TestStdioCancelledRequestGetsNoReply(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The server notices a closed connection only after reading the body.
		_, err := io.Copy(io.Discard, r.Body)
		assert.NoError(t, err)
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	session, err := newStdioSession(testProjectRuntimeAt(t.TempDir()), t.TempDir(), "", io.Discard)
	require.NoError(t, err)
	session.bind = func(context.Context, string) (*stdioRelay, error) {
		return &stdioRelay{endpoint: server.URL + "/mcp", credential: "c", http: server.Client()}, nil
	}

	in, input := io.Pipe()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- serveStdioLoop(context.Background(), in, &out, session.dispatch) }()
	_, err = io.WriteString(input, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"wait_for_changes","arguments":{}}}`+"\n")
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the call never reached the server")
	}
	_, err = io.WriteString(input, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":9}}`+"\n")
	require.NoError(t, err)
	require.NoError(t, input.Close())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled call kept the loop running")
	}
	assert.Empty(t, out.String())
}

func TestStdioDiscoveryAnswersWhileTheFirstCallBinds(t *testing.T) {
	session, err := newStdioSession(testProjectRuntimeAt(t.TempDir()), t.TempDir(), "", io.Discard)
	require.NoError(t, err)
	binding := make(chan struct{})
	release := make(chan struct{})
	session.bind = func(context.Context, string) (*stdioRelay, error) {
		close(binding)
		<-release
		return nil, errStdioServerStopped
	}
	in, input := io.Pipe()
	out := &stdioLockedWriter{w: &bytes.Buffer{}}
	done := make(chan error, 1)
	go func() { done <- serveStdioLoop(context.Background(), in, out, session.dispatch) }()
	_, err = io.WriteString(input, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_changes","arguments":{}}}`+"\n")
	require.NoError(t, err)
	<-binding
	_, err = io.WriteString(input, `{"jsonrpc":"2.0","id":2,"method":"ping"}`+"\n")
	require.NoError(t, err)
	assert.Eventually(t, func() bool { return strings.Contains(out.String(), `"id":2`) }, 10*time.Second, 10*time.Millisecond)
	close(release)
	require.NoError(t, input.Close())
	require.NoError(t, <-done)
}

// stdioLockedWriter lets a test read output while the loop writes it.
type stdioLockedWriter struct {
	// mu guards w.
	mu sync.Mutex
	// w receives the loop's output.
	w *bytes.Buffer
}

// Write appends p under the lock.
func (l *stdioLockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// String returns what was written so far.
func (l *stdioLockedWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.String()
}

// startStdioProjectServer serves state's project on its own address with the
// real project server and control plane, as `surla serve` does, and
// publishes its management credential.
func startStdioProjectServer(t *testing.T, state *project.State) *project.Server {
	t.Helper()
	management, err := project.NewServer(state, func() {}, fixtureTeamDependencies(state))
	require.NoError(t, err)
	cp, err := controlplane.New(fixtureProjectOptions(state, management))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, cp.Close()) })
	api := httptest.NewUnstartedServer(management.Handler(controlplane.NewHTTPHandler(cp, management.Authenticate)))
	require.NoError(t, api.Listener.Close())
	api.Listener, err = net.Listen("tcp", state.Address())
	require.NoError(t, err)
	api.Start()
	t.Cleanup(api.Close)
	require.NoError(t, management.PublishRunning(os.Getpid(), "fixture"))
	return management
}

// stdioCall is a tools/call frame for name with the 2026-07-28 protocol
// version in _meta, as current clients send it.
func stdioCall(id int, name string) string {
	return `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"tools/call","params":{"name":"` + name +
		`","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"` + mcpruntime.ProtocolVersion +
		`","io.modelcontextprotocol/clientCapabilities":{}}}}`
}

// stdioRead is a resources/read frame for uri with the 2026-07-28 protocol
// version in _meta, as current clients send it.
func stdioRead(id int, uri string) string {
	return `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"resources/read","params":{"uri":"` + uri +
		`","_meta":{"io.modelcontextprotocol/protocolVersion":"` + mcpruntime.ProtocolVersion +
		`","io.modelcontextprotocol/clientCapabilities":{}}}}`
}

func TestStdioBindsToARealProjectServer(t *testing.T) {
	isolateServeWorkspace(t)
	workspace, err := abspath.Eval(t.TempDir())
	require.NoError(t, err)
	state, err := fixtureProjectState(workspace)
	require.NoError(t, err)
	management := startStdioProjectServer(t, state)
	runtime := testProjectRuntime()
	runtime.preflight = func(bool) error { return nil }

	// The guide binds from its own folder, not the process's.
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NotEqual(t, workspace, cwd)
	guide, err := newStdioSession(runtime, workspace, "", io.Discard)
	require.NoError(t, err)
	reply := runStdioFrames(t, guide, stdioCall(1, "list_changes"))["1"]
	require.Nil(t, reply.Error, string(reply.Result))
	first := guide.currentRelay()
	require.NotNil(t, first)

	// The playbook resource is read under the same protocol version, which
	// requires the resource's URI in the name header.
	reply = runStdioFrames(t, guide, stdioRead(5, "surla://golden-path"))["5"]
	require.Nil(t, reply.Error, string(reply.Result))
	assert.Contains(t, string(reply.Result), "# Golden Path")

	// A call that asks for progress is answered whether or not the server
	// has any to report: the relay accepts a stream and plain JSON alike.
	progress := strings.Replace(stdioCall(6, "list_changes"), `"_meta":{`, `"_meta":{"progressToken":"p-6",`, 1)
	reply = runStdioFrames(t, guide, progress)["6"]
	require.Nil(t, reply.Error, string(reply.Result))
	assert.Contains(t, string(reply.Result), "changes")

	// A rotated credential is refused once, then the guide binds again.
	var guideHandle string
	reloaded, err := project.Discover(filepath.Dir(state.Dir), workspace)
	require.NoError(t, err)
	for handle, agent := range reloaded.Record.Agents {
		if agent.Role == "guide" {
			guideHandle = handle
		}
	}
	require.NotEmpty(t, guideHandle)
	require.NoError(t, os.WriteFile(state.AgentFile(guideHandle), []byte(strings.Repeat("ab", 32)), 0o600))
	reply = runStdioFrames(t, guide, stdioCall(2, "list_changes"))["2"]
	require.Nil(t, reply.Error, string(reply.Result))
	assert.Equal(t, strings.Repeat("ab", 32), guide.currentRelay().credential)

	// Workers bind to the registered folder that holds them, in the project
	// folder's own copy and in a child project folder's copy.
	for _, builder := range []string{
		filepath.Join(workspace, ".surla-build", "app"),
		filepath.Join(workspace, "shop", ".surla-build", "app"),
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(builder, "sub"), 0o755))
		require.NoError(t, management.RegisterBuildRoles(t.Context(), controlplane.BuilderOnly{BuilderFolder: builder}))
		worker, err := newStdioSession(runtime, filepath.Join(builder, "sub"), "", io.Discard)
		require.NoError(t, err)
		require.Equal(t, controlplane.CallerBuilder, worker.role)
		reply := runStdioFrames(t, worker, stdioCall(3, "list_builder_assignments"))["3"]
		require.Nil(t, reply.Error, string(reply.Result))
	}

	// A test writer's copy left from an earlier build has no registered agent
	// on a builder-only build, and never becomes a guide.
	leftover := filepath.Join(workspace, "shop", ".surla-tests", "eval")
	require.NoError(t, os.MkdirAll(leftover, 0o755))
	worker, err := newStdioSession(runtime, leftover, "", io.Discard)
	require.NoError(t, err)
	reply = runStdioFrames(t, worker, stdioCall(4, "list_test_writer_assignments"))["4"]
	require.NotNil(t, reply.Error)
	assert.Contains(t, reply.Error.Message, "this project has no registered test_writer")
	assert.Nil(t, worker.currentRelay())
}

func TestStdioRequestHeadersNameTheFrameTarget(t *testing.T) {
	modern := `"_meta":{"io.modelcontextprotocol/protocolVersion":"` + mcpruntime.ProtocolVersion + `"}`
	tests := []struct {
		// name describes the frame.
		name string
		// method and params form the frame the client sent.
		method, params string
		// wantVersion is the protocol version header.
		wantVersion string
		// wantName is the Mcp-Name header values, nil when the header is absent.
		wantName []string
	}{
		{"tool call", "tools/call", `{"name":"get_change",` + modern + `}`, mcpruntime.ProtocolVersion, []string{"get_change"}},
		{"resource read", "resources/read", `{"uri":"surla://golden-path",` + modern + `}`, mcpruntime.ProtocolVersion, []string{"surla://golden-path"}},
		{"prompt", "prompts/get", `{"name":"p",` + modern + `}`, mcpruntime.ProtocolVersion, []string{"p"}},
		{"task", "tasks/get", `{"taskId":"t-1",` + modern + `}`, mcpruntime.ProtocolVersion, []string{"t-1"}},
		{"encoded name", "resources/read", `{"uri":"surla://é",` + modern + `}`, mcpruntime.ProtocolVersion, []string{"=?base64?c3VybGE6Ly/DqQ==?="}},
		{"field in another case", "resources/read", `{"URI":"surla://golden-path","_META":{"io.modelcontextprotocol/protocolVersion":"` +
			mcpruntime.ProtocolVersion + `"}}`, mcpruntime.ProtocolVersion, []string{"surla://golden-path"}},
		{"target not a string", "resources/read", `{"uri":7,` + modern + `}`, mcpruntime.ProtocolVersion, nil},
		{"no target", "tools/list", `{"name":"ignored",` + modern + `}`, mcpruntime.ProtocolVersion, nil},
		{"legacy frame", "resources/read", `{"uri":"surla://golden-path"}`, mcpruntime.LegacyProtocolVersion, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"` + tt.method + `","params":` + tt.params + `}`)
			var raw jsonrpc.RawRequest
			require.NoError(t, raw.UnmarshalJSON(payload))
			req, err := newStdioMCPRequest(t.Context(), "http://127.0.0.1:1/mcp", payload, &raw)
			require.NoError(t, err)
			assert.Equal(t, tt.wantVersion, req.Header.Get("MCP-Protocol-Version"))
			assert.Equal(t, tt.wantName, req.Header.Values("Mcp-Name"))
			if tt.wantVersion == mcpruntime.ProtocolVersion {
				assert.Equal(t, tt.method, req.Header.Get("Mcp-Method"))
			}
		})
	}
}

// TestStdioForwardsProgressAheadOfTheResponse relays a call whose client
// asked for progress to a server that answers with an event stream: the
// keep-alive comment is dropped, each notification reaches the client at once
// and in order, and the response follows. A call without a progress token
// asks for plain JSON, and a stream that ends without a response is an error.
func TestStdioForwardsProgressAheadOfTheResponse(t *testing.T) {
	var accepts []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		accepts = append(accepts, r.Header.Get("Accept"))
		mu.Unlock()
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			_, err = io.WriteString(w, `{"jsonrpc":"2.0","id":2,"result":{"plain":true}}`)
			assert.NoError(t, err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		events := []string{
			":\n\n",
			"event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":\"p\",\"progress\":0,\"total\":8,\"message\":\"Resolving the service's Go modules\"}}\n\n",
			"data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\n" +
				"data: \"params\":{\"progressToken\":\"p\",\"progress\":1,\"total\":8,\"message\":\"Generating the service's code\"}}\r\n\r\n",
		}
		if !bytes.Contains(body, []byte(`"lost"`)) {
			events = append(events, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"streamed\":true}}\n\n")
		}
		for _, event := range events {
			_, err = io.WriteString(w, event)
			assert.NoError(t, err)
			w.(http.Flusher).Flush()
		}
	}))
	defer server.Close()
	session, err := newStdioSession(testProjectRuntimeAt(t.TempDir()), t.TempDir(), "", io.Discard)
	require.NoError(t, err)
	session.bind = func(context.Context, string) (*stdioRelay, error) {
		return &stdioRelay{endpoint: server.URL + "/mcp", credential: "c", http: &http.Client{}}, nil
	}
	serve := func(frame string) []string {
		var out bytes.Buffer
		require.NoError(t, serveStdioLoop(context.Background(), strings.NewReader(frame+"\n"), &out, session.dispatch))
		return strings.Split(strings.TrimSpace(out.String()), "\n")
	}

	lines := serve(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"init_project","arguments":{},"_meta":{"progressToken":"p"}}}`)
	require.Len(t, lines, 3)
	assert.JSONEq(t, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"p","progress":0,"total":8,"message":"Resolving the service's Go modules"}}`, lines[0])
	assert.JSONEq(t, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"p","progress":1,"total":8,"message":"Generating the service's code"}}`, lines[1])
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":{"streamed":true}}`, lines[2])

	lines = serve(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_changes","arguments":{},"_meta":{"progressToken":null}}}`)
	require.Len(t, lines, 1)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":2,"result":{"plain":true}}`, lines[0])

	lines = serve(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"lost","arguments":{},"_meta":{"progressToken":"p"}}}`)
	require.Len(t, lines, 3, "the notifications arrived before the stream ended")
	var failed stdioReply
	require.NoError(t, json.Unmarshal([]byte(lines[2]), &failed))
	require.NotNil(t, failed.Error)
	assert.Contains(t, failed.Error.Message, "event stream ended without a response")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"application/json, text/event-stream", "application/json", "application/json, text/event-stream"}, accepts)
}

func TestStdioRetriesOnlyRequestsTheServerNeverRan(t *testing.T) {
	var calls int
	var mu sync.Mutex
	dropped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		// The server received the call, then the connection ends without a reply.
		conn, _, err := w.(http.Hijacker).Hijack()
		if assert.NoError(t, err) {
			assert.NoError(t, conn.Close())
		}
	}))
	defer dropped.Close()
	answering := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
		assert.NoError(t, err)
	}))
	defer answering.Close()
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	refused := "http://" + closed.Addr().String() + "/mcp"
	require.NoError(t, closed.Close())

	cases := []struct {
		name      string
		endpoints []string
		binds     int
		wantError bool
	}{
		{name: "refused connection binds again", endpoints: []string{refused, answering.URL + "/mcp"}, binds: 2},
		{name: "dropped call is not sent again", endpoints: []string{dropped.URL + "/mcp", answering.URL + "/mcp"}, binds: 1, wantError: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			session, err := newStdioSession(testProjectRuntimeAt(t.TempDir()), t.TempDir(), "", io.Discard)
			require.NoError(t, err)
			binds := 0
			session.bind = func(context.Context, string) (*stdioRelay, error) {
				binds++
				return &stdioRelay{endpoint: c.endpoints[binds-1], credential: "c", http: &http.Client{}}, nil
			}
			reply := runStdioFrames(t, session, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"verify_change","arguments":{}}}`)["1"]
			assert.Equal(t, c.binds, binds)
			assert.Equal(t, c.wantError, reply.Error != nil)
		})
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, calls, "the dropped call reached the server once")
}

// TestStdioGuideBindErrorsRetryTheTool binds a guide to a real project server
// whose published management credential no longer matches, so the first tool
// call fails inside the bind. The agent sees the shared management error,
// which names calling again, never typing surla.
func TestStdioGuideBindErrorsRetryTheTool(t *testing.T) {
	isolateServeWorkspace(t)
	workspace, err := abspath.Eval(t.TempDir())
	require.NoError(t, err)
	state, err := fixtureProjectState(workspace)
	require.NoError(t, err)
	startStdioProjectServer(t, state)
	require.NoError(t, state.SaveRunning(project.Running{PID: os.Getpid(), Terminal: "", ManagementCredential: strings.Repeat("b", 64)}))
	runtime := testProjectRuntime()
	runtime.preflight = func(bool) error { return nil }

	guide, err := newStdioSession(runtime, workspace, "", io.Discard)
	require.NoError(t, err)
	reply := runStdioFrames(t, guide, stdioCall(1, "list_changes"))["1"]
	require.NotNil(t, reply.Error)
	assert.Contains(t, reply.Error.Message, "try again")
	assert.NotContains(t, strings.ToLower(reply.Error.Message), "run surla")
	assert.NotContains(t, reply.Error.Message, strings.Repeat("b", 64))
	assert.Nil(t, guide.currentRelay())
}

func TestStdioLineageInspectorAttachPayload(t *testing.T) {
	t.Run("direct CLI session keeps HcTerminalID and AncestorPids", func(t *testing.T) {
		tree := map[int]struct {
			ppid int
			args string
		}{
			900: {800, "/opt/surla/bin/surla stdio"},
			800: {700, "node /usr/local/bin/claude"},
			700: {1, "-zsh"},
		}
		inspector := stdioLineageInspector{
			selfPID:  func() int { return 900 },
			selfPPID: func() int { return 800 },
			parentProcess: func(_ context.Context, pid int) (int, string, error) {
				entry, ok := tree[pid]
				if !ok {
					return 0, "", os.ErrNotExist
				}
				return entry.ppid, entry.args, nil
			},
		}

		payload := inspector.attachPayload(t.Context(), "t-herdr", "claude")
		require.NotNil(t, payload.HcTerminalID)
		assert.Equal(t, "t-herdr", *payload.HcTerminalID)
		require.NotNil(t, payload.Client)
		assert.Equal(t, "claude", *payload.Client)
		assert.Equal(t, []int{900, 800, 700}, payload.AncestorPids)
	})

	t.Run("shared codex app-server omits HcTerminalID and AncestorPids to stay external", func(t *testing.T) {
		tree := map[int]struct {
			ppid int
			args string
		}{
			73900: {73230, "/opt/surla/bin/surla stdio"},
			73230: {2239, "/Users/dev/.codex/bin/codex app-server --listen unix:// --managed-daemon"},
			2239:  {1, "/Users/dev/.codex/bin/codex app-server daemon pid-update-loop"},
		}
		inspector := stdioLineageInspector{
			selfPID:  func() int { return 73900 },
			selfPPID: func() int { return 73230 },
			parentProcess: func(_ context.Context, pid int) (int, string, error) {
				entry, ok := tree[pid]
				if !ok {
					return 0, "", os.ErrNotExist
				}
				return entry.ppid, entry.args, nil
			},
		}

		payload := inspector.attachPayload(t.Context(), "t-stale-daemon-launch", "codex")
		assert.Nil(t, payload.HcTerminalID, "daemon's launch HC_TERMINAL_ID must be dropped")
		assert.Nil(t, payload.AncestorPids, "daemon's launch ancestors must be dropped")
		require.NotNil(t, payload.Client)
		assert.Equal(t, "codex", *payload.Client)
	})
}
