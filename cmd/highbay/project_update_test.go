// This test follows a project server through an accepted update: the real
// serve entry, with stand-ins only for the release installer and for the
// replacement of the process.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/modelsettings"
	"github.com/goadesign/highbay/internal/project"
)

// pageRequest sends one request the way the project's page does, with its
// session credential, and returns the status and body.
func pageRequest(t *testing.T, state *project.State, method, path, session, body string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, state.Origin()+path, strings.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("Origin", state.Origin())
	if session != "" {
		request.Header.Set("Authorization", "Bearer "+session)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := projectHTTPClient().Do(request)
	require.NoError(t, err)
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	return response.StatusCode, raw
}

// TestServeReplacesItselfAndKeepsSessionsAndModelSettings runs the real serve
// entry twice in this process. The first server holds a browser session and
// handed-over model settings, accepts an update from the highbay command,
// shuts down in order and asks to be replaced by the installed executable
// with its start environment and a handoff. The second server starts from
// that handoff, as the replaced process does, and the page's stored
// credential and the model choice are still valid on it.
//
// It starts two servers on a loopback port and no other process (under a
// second).
func TestServeReplacesItselfAndKeepsSessionsAndModelSettings(t *testing.T) {
	isolateServeWorkspace(t)
	isolateProviderFamilies(t)
	t.Setenv("ANTHROPIC_API_KEY", "sentinel-core-key")
	state, err := fixtureProjectState("")
	require.NoError(t, err)

	const next = "/opt/highbay/bin/highbay-next"
	errNotReplaced := errors.New("the test does not replace its own process")
	var (
		installed, prepared []string
		replaced            struct {
			program    string
			argv, env  []string
			handoff    serverHandoff
			handedOver bool
		}
	)
	runtime := testProjectRuntime()
	runtime.teamDependencies = func(ctx context.Context, _ []string) (project.TeamDependencies, error) {
		return fixtureTeam(ctx), nil
	}
	runtime.updates = func() (project.UpdateDependencies, error) {
		return project.UpdateDependencies{
			Version:       "v0.5.0",
			CanAutoUpdate: func() (bool, string) { return true, "" },
			Install: func(_ context.Context, version string) (string, func() error, error) {
				installed = append(installed, version)
				return next, func() error { return nil }, nil
			},
			Prepare: func(_ context.Context, executable string) error {
				prepared = append(prepared, executable)
				return nil
			},
		}, nil
	}
	runtime.replaceProcess = func(program string, argv, env []string) error {
		replaced.program, replaced.argv, replaced.env = program, argv, env
		// Read the handoff as the next process would, before this one
		// gives the pipe up. The reader owns the descriptor it is given,
		// so it gets a duplicate of the one this process still holds.
		for _, kv := range env {
			value, named := strings.CutPrefix(kv, handoffVariable+"=")
			if !named {
				continue
			}
			offered, err := strconv.Atoi(value)
			if err != nil {
				return err
			}
			inherited, err := unix.Dup(offered)
			if err != nil {
				return err
			}
			if err := os.Setenv(handoffVariable, strconv.Itoa(inherited)); err != nil {
				return err
			}
		}
		var err error
		replaced.handoff, replaced.handedOver, err = takeServerHandoff()
		return errors.Join(errNotReplaced, err)
	}
	// serve runs the serve entry until it returns.
	serve := func() chan error {
		done := make(chan error, 1)
		go func() { done <- runWithRuntime([]string{"serve", "-state", state.Dir}, io.Discard, runtime) }()
		return done
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	first := serve()
	_, err = waitForRunning(ctx, state)
	require.NoError(t, err)
	var line bytes.Buffer
	require.NoError(t, handOverModelSettings(ctx, state, modelsettings.Select([]string{"ANTHROPIC_API_KEY=sentinel-handed-key"}), &line))
	link, err := projectClient(state).BrowserLink(ctx)
	require.NoError(t, err)
	status, raw := pageRequest(t, state, http.MethodPost, "/project/browser-session", "",
		`{"link":"`+strings.TrimPrefix(link.URL, state.Origin()+"/views#link=")+`"}`)
	require.Equal(t, http.StatusCreated, status, string(raw))
	var session struct {
		Session string `json:"session"`
		Version string `json:"version"`
	}
	require.NoError(t, json.Unmarshal(raw, &session))
	assert.Equal(t, "v0.5.0", session.Version)
	identity, err := projectClient(state).Identity(ctx)
	require.NoError(t, err)
	require.NotNil(t, identity.Version)
	assert.Equal(t, "v0.5.0", *identity.Version)

	// The page keeps one waiting read open, as it always does. It must not
	// hold the update: the server answers it as soon as the stop begins.
	status, raw = pageRequest(t, state, http.MethodGet, "/changes", session.Session, "")
	require.Equal(t, http.StatusOK, status, string(raw))
	var list struct {
		Version string `json:"version"`
	}
	require.NoError(t, json.Unmarshal(raw, &list))
	type waited struct {
		status int
		err    error
	}
	waiting := make(chan waited, 1)
	waitRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, state.Origin()+"/change-updates?since="+url.QueryEscape(list.Version), nil)
	require.NoError(t, err)
	waitRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	waitRequest.Header.Set("Authorization", "Bearer "+session.Session)
	go func() {
		client := projectHTTPClient()
		client.Timeout = 0
		response, err := client.Do(waitRequest)
		if err != nil {
			waiting <- waited{err: err}
			return
		}
		waiting <- waited{status: response.StatusCode, err: response.Body.Close()}
	}()

	result, err := projectClient(state).ApplyUpdate(ctx, &genproject.ApplyUpdatePayload{Version: "v0.5.1"})
	require.NoError(t, err)
	assert.Equal(t, &genproject.ApplyUpdateResult{Status: "restarting", Version: "v0.5.1"}, result)
	select {
	case err := <-first:
		require.ErrorIs(t, err, errNotReplaced, "the server ended without asking to be replaced")
	case <-ctx.Done():
		t.Fatal("the updated server did not shut down")
	}
	select {
	case got := <-waiting:
		require.NoError(t, got.err, "the page's open waiting read was cut off instead of answered")
		assert.Equal(t, http.StatusOK, got.status)
	case <-ctx.Done():
		t.Fatal("the page's open waiting read never ended")
	}
	assert.Equal(t, []string{"0.5.1"}, installed)
	assert.Equal(t, []string{next}, prepared)
	assert.Equal(t, next, replaced.program)
	assert.Equal(t, []string{next, "serve", "-state", state.Dir}, replaced.argv)
	assert.Contains(t, replaced.env, "ANTHROPIC_API_KEY=sentinel-core-key", "the next server starts with the environment this one started with")
	require.True(t, replaced.handedOver, "the next server was offered no handoff")
	assert.Equal(t, 1, len(replaced.handoff.Sessions))
	assert.Contains(t, replaced.handoff.Sessions, session.Session)
	assert.Equal(t, map[string]map[string]string{"anthropic": {"ANTHROPIC_API_KEY": "sentinel-handed-key"}}, replaced.handoff.ModelSettings)
	_, err = state.ReadRunning()
	assert.Error(t, err, "the replaced server withdrew its management record, as on stop")

	// The next process: the same state folder, the start environment and
	// the handoff.
	read, _, err := replaced.handoff.offer()
	require.NoError(t, err)
	// The next server owns the descriptor it is given, so it gets one that
	// no file of this test also owns.
	inherited, err := unix.Dup(int(read.Fd()))
	require.NoError(t, err)
	require.NoError(t, read.Close())
	t.Setenv(handoffVariable, strconv.Itoa(inherited))
	second := serve()
	_, err = waitForRunning(ctx, state)
	require.NoError(t, err)
	_, declared := os.LookupEnv(handoffVariable)
	assert.False(t, declared, "the next server left the handoff's name in its environment")

	status, raw = pageRequest(t, state, http.MethodGet, "/project/browser-session", session.Session, "")
	require.Equal(t, http.StatusOK, status, "the page's stored credential: %s", raw)
	var restored struct {
		Session string `json:"session"`
	}
	require.NoError(t, json.Unmarshal(raw, &restored))
	assert.Equal(t, session.Session, restored.Session)
	status, raw = pageRequest(t, state, http.MethodGet, "/cp1/model", session.Session, "")
	require.Equal(t, http.StatusOK, status, string(raw))
	assert.Contains(t, string(raw), `"provider":"anthropic"`, "the model choice survived: %s", raw)
	assert.NotContains(t, string(raw), "sentinel")

	require.NoError(t, projectClient(state).Stop(ctx))
	select {
	case err := <-second:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("the second server did not stop")
	}
}

// TestServeCutsOffARequestThatOutlivesTheDrain checks the end of the HTTP
// server: a request still open when the drain time is over has its
// connection closed, and the stop succeeds. A slow request must not fail a
// stop, or keep a server that already answered an update from being
// replaced.
func TestServeCutsOffARequestThatOutlivesTheDrain(t *testing.T) {
	isolateServeWorkspace(t)
	state, err := fixtureProjectState("")
	require.NoError(t, err)
	management, err := project.NewServer(state, func() {}, fixtureTeamDependencies(state))
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	})}
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	served := make(chan error, 1)
	go func() {
		served <- serveProjectHTTPWithin(ctx, server, &ownedProjectListener{Listener: listener, management: management}, 50*time.Millisecond)
	}()
	requested := make(chan error, 1)
	go func() {
		response, err := (&http.Client{}).Get("http://" + listener.Addr().String() + "/slow")
		if err == nil {
			err = errors.Join(errors.New("the slow request was answered"), response.Body.Close())
		}
		requested <- err
	}()
	<-entered
	stop()
	select {
	case err := <-served:
		require.NoError(t, err, "a request that outlived the drain failed the stop")
	case <-time.After(5 * time.Second):
		t.Fatal("the server waited for the slow request")
	}
	select {
	case err := <-requested:
		assert.NotContains(t, err.Error(), "was answered")
	case <-time.After(5 * time.Second):
		t.Fatal("the slow request's connection was left open")
	}
}
