// This file verifies detached project launch using only test-owned processes.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/project"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// TestDetachedProjectServerStartsAndStops exercises the real serve entrypoint
// and management status without reaching Herdr, desktop opening, or an agent.
func TestDetachedProjectServerStartsAndStops(t *testing.T) {
	isolateServeWorkspace(t)
	isolateProviderFamilies(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("HC_TERMINAL_ID", "fixture-caller-terminal")
	self, err := os.Executable()
	require.NoError(t, err)
	runtime := testProjectRuntime()
	runtime.preflight = func(bool) error { return nil }
	runtime.startTerminal = func(ctx context.Context, state *project.State) (string, error) {
		return startProjectTerminal(ctx, state, func() (string, error) { return "", errProjectHerdrMissing })
	}
	var daemon *projectDaemon
	runtime.startDaemon = func(state *project.State) (*projectDaemon, error) {
		active, err := startProjectDaemon(state, self)
		daemon = active
		return active, err
	}
	t.Cleanup(func() {
		if daemon == nil {
			return
		}
		if err := daemon.process.Kill(); err != nil {
			assert.ErrorIs(t, err, os.ErrProcessDone)
		}
		select {
		case <-daemon.exited:
		case <-time.After(5 * time.Second):
			t.Error("failed to reap the fixture daemon")
		}
	})
	browserLink := ""
	runtime.openViews = func(_ context.Context, link string) error { browserLink = link; return nil }

	var report bytes.Buffer
	require.NoError(t, runWithRuntime(nil, &report, runtime))
	require.NotNil(t, daemon)
	state, err := runtime.discover()
	require.NoError(t, err)
	running, err := state.ReadRunning()
	require.NoError(t, err)
	assert.Equal(t, daemon.process.Pid, running.PID)
	assert.Empty(t, running.Terminal, "the daemon must not publish its caller's terminal")
	sid, err := unix.Getsid(running.PID)
	require.NoError(t, err)
	assert.Equal(t, running.PID, sid, "the server is the leader of a new session")
	assert.Contains(t, browserLink, "/views#link=")
	assert.NotContains(t, report.String(), "#link=")
	logPath := filepath.Join(state.Dir, "serve-startup.log")
	info, err := os.Stat(logPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	raw, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(raw), running.ManagementCredential), "the private startup log contains no management credential")
	assert.False(t, strings.Contains(string(raw), browserLink), "the private startup log contains no browser link")

	report.Reset()
	require.NoError(t, runWithRuntime([]string{"status", "--json"}, &report, runtime))
	var status struct {
		Running   bool              `json:"running"`
		Directory string            `json:"directory"`
		Stage     string            `json:"stage"`
		Roles     []json.RawMessage `json:"roles"`
	}
	require.NoError(t, json.Unmarshal(report.Bytes(), &status))
	assert.True(t, status.Running)
	assert.Equal(t, state.Record.Directory, status.Directory)
	assert.Equal(t, "describe", status.Stage)
	assert.Empty(t, status.Roles, "bare start does not attach an agent")
	assert.False(t, strings.Contains(report.String(), running.ManagementCredential), "status contains no management credential")
	assert.NotContains(t, report.String(), "#link=")

	require.NoError(t, runWithRuntime([]string{"stop"}, io.Discard, runtime))
	select {
	case err := <-daemon.exited:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("detached server did not exit")
	}
	_, err = state.ReadRunning()
	require.ErrorIs(t, err, os.ErrNotExist)
	held, err := controlplane.CardStoreInUse(state.CardsDir())
	require.NoError(t, err)
	assert.False(t, held)
	report.Reset()
	require.NoError(t, runWithRuntime([]string{"status", "--json"}, &report, runtime))
	assert.JSONEq(t, `{"running":false}`, report.String())
}

// TestDetachedStartReportsBoundedEarlyExit proves the watcher detects a child
// exit, captures both streams, and replaces the log without following a link.
func TestDetachedStartReportsBoundedEarlyExit(t *testing.T) {
	isolateServeWorkspace(t)
	state, err := fixtureProjectState("")
	require.NoError(t, err)
	previous := filepath.Join(t.TempDir(), "previous-log")
	require.NoError(t, os.WriteFile(previous, []byte("old data\n"), 0o600))
	log := filepath.Join(state.Dir, "serve-startup.log")
	require.NoError(t, os.Symlink(previous, log))

	child := filepath.Join(t.TempDir(), "serve-fixture")
	script := `#!/bin/sh
printf 'discard this first line\n'
i=0
while [ "$i" -lt 180 ]; do
  printf 'long line from stdout without sensitive data\n'
  i=$((i+1))
done
if read -r line; then
  printf 'unexpected input\n'
else
  printf 'stdin reached EOF\n'
fi
printf '\033[31mconfiguration could not load\033[0m\n' >&2
exit 37
`
	require.NoError(t, os.WriteFile(child, []byte(script), 0o700))
	runtime := testProjectRuntime()
	runtime.startTerminal = func(context.Context, *project.State) (string, error) {
		return "", errProjectHerdrMissing
	}
	runtime.startDaemon = func(state *project.State) (*projectDaemon, error) {
		return startProjectDaemon(state, child)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, _, err = runtime.attachProject(ctx, state)
	require.ErrorContains(t, err, "the project server stopped while starting")
	assert.ErrorContains(t, err, "stdin reached EOF")
	assert.ErrorContains(t, err, "configuration could not load")
	assert.NotContains(t, err.Error(), "discard this first line")
	assert.NotContains(t, err.Error(), "\x1b")
	assert.Less(t, len(err.Error()), startOutputBytes+100)
	raw, err := os.ReadFile(previous)
	require.NoError(t, err)
	assert.Equal(t, "old data\n", string(raw), "an existing link is replaced without truncating its target")
	info, err := os.Lstat(log)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular())
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
