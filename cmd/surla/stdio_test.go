package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
