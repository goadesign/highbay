//go:build herdr

// This file runs the client against a real herdr-core in a session of its
// own. It starts shells, never an agent or a model.
package hc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHCClient takes one shell through its whole life over a single framed
// "hc connect": run, the created event, lookup by label, a typing viewer, an
// observing viewer, labels, kill, removal, and the error kinds for each
// state. It needs the real core because the wire contract with it is what is
// under test; the unit tests cover the client's own logic against a fake.
// Cost: about two seconds: starting and stopping the core, and the quiet
// period the core waits before typing a message.
//
// The name stays short on purpose: it is part of the scratch folder's path,
// and the core's socket path there must fit the operating system's limit
// (103 bytes on macOS).
func TestHCClient(t *testing.T) {
	if testing.Short() {
		t.Skip("real isolated herdr-core lifecycle")
	}
	hc := isolateHerdr(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c, err := Dial(ctx, ExecConnector(hc, nil))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, c.Close()) })
	assert.Contains(t, c.Hello().Features, "automatic_input")
	assert.Contains(t, c.Hello().Features, "resize_notifications")
	status, err := c.ServerStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, c.Hello().Version, status.Version)
	assert.NotZero(t, status.PID)

	// Follow this test's terminals only.
	labels := map[string]string{"highbay.test": "lifecycle"}
	updates := make(chan StreamUpdate, 256)
	subscribed, stop := context.WithCancel(ctx)
	followed := async(func() (struct{}, error) {
		return struct{}{}, c.SubscribeEvents(subscribed, labels, func(update StreamUpdate) { updates <- update })
	})
	assert.Equal(t, []Terminal{}, nextUpdate(t, updates).(Snapshot).Terminals)

	workspace := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.Mkdir(workspace, 0o700))
	shell, err := c.Run(ctx, RunRequest{
		Argv:     []string{"/bin/sh"},
		Cwd:      workspace,
		Env:      map[string]string{"PS1": "ready> "},
		Labels:   labels,
		NoParent: true,
		NoNotify: true,
		Size:     &Size{Cols: 90, Rows: 30},
	})
	require.NoError(t, err)
	t.Cleanup(func() { removeTerminal(t, hc, shell.Address) })
	assert.Equal(t, StateLive, shell.State)
	assert.Equal(t, labels, shell.Labels)
	assert.Equal(t, uint16(90), shell.Cols)
	assert.Equal(t, uint16(30), shell.Rows)

	created := awaitChange[Created](t, updates, shell.Address)
	assert.Equal(t, shell.Address, created.Terminal.Address)

	got, err := c.Get(ctx, shell.Address)
	require.NoError(t, err)
	assert.Equal(t, shell.Address, got.Address)
	listed, err := c.List(ctx, ListRequest{Labels: labels})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, shell.Address, listed[0].Address)
	none, err := c.List(ctx, ListRequest{Labels: map[string]string{"highbay.test": "another"}})
	require.NoError(t, err)
	assert.Empty(t, none)

	// A typing viewer: the first paint, then the shell's answer to a command.
	viewer, err := c.Attach(ctx, AttachRequest{Terminal: shell.Address, Cols: 100, Rows: 32, History: 200})
	require.NoError(t, err)
	assert.Equal(t, shell.Address, viewer.Terminal.Address)
	assert.False(t, viewer.ResizeNotifications, "the core announces sizes only to observers")
	assert.True(t, viewer.AutomaticInput)
	assert.Equal(t, SizeChanged{Cols: 100, Rows: 32}, awaitChange[SizeChanged](t, updates, shell.Address),
		"a typing viewer sets the terminal's size by attaching")
	// Enter is a carriage return, as a terminal sends it: the core treats
	// typed characters as an unsent draft until it sees one, and holds a
	// Send for up to a minute while a draft is open.
	require.NoError(t, viewer.WriteInput([]byte("echo typed-$((40+2))\r")))
	readUntil(t, ctx, viewer, "typed-42")
	require.NoError(t, viewer.WriteAutoInput([]byte("echo auto-$((40+3))\r")))
	readUntil(t, ctx, viewer, "auto-43")
	require.NoError(t, viewer.Focus(true))
	require.NoError(t, viewer.Resize(110, 35))
	assert.Equal(t, SizeChanged{Cols: 110, Rows: 35}, awaitChange[SizeChanged](t, updates, shell.Address))

	// An observing viewer is told the grid before its first paint.
	observer, err := c.Attach(ctx, AttachRequest{Terminal: shell.Address, Cols: 20, Rows: 5, Observe: true})
	require.NoError(t, err)
	assert.True(t, observer.ResizeNotifications)
	first, err := observer.Recv(ctx)
	require.NoError(t, err)
	assert.Equal(t, TerminalResized{Cols: 110, Rows: 35}, first)
	require.NoError(t, observer.Close())
	_, err = observer.Recv(ctx)
	assert.ErrorIs(t, err, ErrStreamEnded)

	labeled, err := c.Label(ctx, shell.Address, map[string]string{"highbay.role": "guide"})
	require.NoError(t, err)
	assert.Equal(t, "guide", labeled.Labels["highbay.role"])
	relabeled := awaitChange[LabelsChanged](t, updates, shell.Address)
	assert.Equal(t, labeled.Labels, relabeled.Labels)

	// A message is typed and submitted; one conditional on a status the
	// shell never reported is refused without typing.
	_, err = c.Send(ctx, SendRequest{Terminal: shell.Address, Text: "echo never-$((40+4))", If: "idle"})
	assert.ErrorIs(t, err, ErrStatusMismatch)
	sent, err := c.Send(ctx, SendRequest{Terminal: shell.Address, Text: "echo sent-$((40+5))"})
	require.NoError(t, err)
	assert.Empty(t, sent.Status, "a shell reports no status")
	assert.Nil(t, sent.Started)
	readUntil(t, ctx, viewer, "sent-45")
	pressed, err := c.Keys(ctx, shell.Address, []string{"enter"})
	require.NoError(t, err)
	assert.Equal(t, sent.Turn, pressed.Turn)

	// A live terminal cannot be removed; kill ends it and closes its viewers.
	var kind *KindError
	require.ErrorAs(t, c.Remove(ctx, shell.Address), &kind)
	assert.Equal(t, "live", kind.Kind)
	killed, err := c.Kill(ctx, shell.Address)
	require.NoError(t, err)
	assert.Equal(t, StateFinished, killed.State)
	finished := awaitChange[Finished](t, updates, shell.Address)
	assert.Equal(t, "killed", finished.End.Reason)
	assert.Equal(t, "killed", readUntilClosed(t, ctx, viewer).Reason.Reason)
	_, err = viewer.Recv(ctx)
	assert.ErrorIs(t, err, io.EOF)

	// A finished terminal takes no viewer and no input.
	_, err = c.Attach(ctx, AttachRequest{Terminal: shell.Address, Cols: 80, Rows: 24})
	assert.ErrorIs(t, err, ErrFinished)
	_, err = c.Keys(ctx, shell.Address, []string{"enter"})
	assert.ErrorIs(t, err, ErrFinished)
	_, err = c.Send(ctx, SendRequest{Terminal: shell.Address, Text: "hello"})
	assert.ErrorIs(t, err, ErrFinished)
	_, err = c.Interrupt(ctx, shell.Address)
	assert.ErrorIs(t, err, ErrFinished)

	require.NoError(t, c.Remove(ctx, shell.Address))
	awaitChange[Removed](t, updates, shell.Address)
	_, err = c.Get(ctx, shell.Address)
	assert.ErrorIs(t, err, ErrNotFound)

	stop()
	_, err = await(t, followed)
	assert.ErrorIs(t, err, context.Canceled)
	assert.NoError(t, c.Err(), "leaving the subscription keeps the connection")
}

// isolateHerdr points this test at a herdr-core session of its own inside
// the test's scratch folder, starts that core, and stops it at cleanup. It
// returns the hc executable, or skips when there is none.
func isolateHerdr(t *testing.T) string {
	t.Helper()
	hc, err := exec.LookPath("hc")
	if err != nil {
		home, homeErr := os.UserHomeDir()
		require.NoError(t, homeErr)
		hc = filepath.Join(home, ".local", "bin", "hc")
		if _, err := os.Stat(hc); err != nil {
			t.Skip("herdr-core hc executable unavailable")
		}
	}
	scratch := t.TempDir()
	t.Setenv("HC_SESSION", fmt.Sprintf("highbay-hc-%d-%d", os.Getpid(), time.Now().UnixNano()))
	t.Setenv("HC_SOCKET", filepath.Join(scratch, "s"))
	t.Setenv("HC_TERMINAL_ID", "")
	// The core waits this long after a person's last key before it types a
	// Send or Keys: three seconds by default, shortened for this session.
	t.Setenv("HC_QUIET_MS", "100")
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		dir := filepath.Join(scratch, key)
		require.NoError(t, os.Mkdir(dir, 0o700))
		t.Setenv(key, dir)
	}
	raw, err := isolatedHerdr(hc, "server", "start", "--json")
	require.NoError(t, err, string(raw))
	t.Cleanup(func() {
		raw, err := isolatedHerdr(hc, "ls", "--json", "--all")
		assert.NoError(t, err, string(raw))
		assert.NotContains(t, string(raw), `"live"`, "the isolated session must have no live terminal")
		raw, err = isolatedHerdr(hc, "server", "stop", "--json")
		assert.NoError(t, err, string(raw))
		assert.Eventually(t, func() bool {
			_, err := os.Stat(os.Getenv("HC_SOCKET"))
			return errors.Is(err, os.ErrNotExist)
		}, 5*time.Second, 25*time.Millisecond, "the isolated core's socket must be removed")
	})
	return hc
}

// isolatedHerdr runs one bounded hc command in the test's isolated session.
func isolatedHerdr(hc string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, hc, args...)
	var out bytes.Buffer
	command.Stdout = &out
	command.Stderr = &out
	err := command.Run()
	return out.Bytes(), err
}

// removeTerminal ends and removes a terminal the test started, if the test
// did not get that far itself.
func removeTerminal(t *testing.T, hc, address string) {
	t.Helper()
	raw, err := isolatedHerdr(hc, "get", "--json", address)
	if err != nil {
		return // already removed
	}
	if strings.Contains(string(raw), `"live"`) {
		raw, err = isolatedHerdr(hc, "kill", "--json", address)
		assert.NoError(t, err, string(raw))
	}
	raw, err = isolatedHerdr(hc, "rm", "--json", address)
	assert.NoError(t, err, string(raw))
}

// awaitChange returns the next change of type C to the terminal at address,
// skipping other updates.
func awaitChange[C Change](t *testing.T, updates <-chan StreamUpdate, address string) C {
	t.Helper()
	for {
		event, isEvent := nextUpdate(t, updates).(Event)
		if !isEvent || event.Terminal != address {
			continue
		}
		if change, matches := event.Change.(C); matches {
			return change
		}
	}
}

// readUntil reads a viewer's output until it contains want, acknowledging
// every byte so the core keeps sending.
func readUntil(t *testing.T, ctx context.Context, viewer *AttachStream, want string) {
	t.Helper()
	var seen []byte
	for !bytes.Contains(seen, []byte(want)) {
		event, err := viewer.Recv(ctx)
		require.NoError(t, err, "output so far: %q", seen)
		output, isOutput := event.(TerminalOutput)
		require.True(t, isOutput, "unexpected %T before %q", event, want)
		seen = append(seen, output.Data...)
		require.NoError(t, viewer.GrantCredit(uint32(len(output.Data))))
	}
}

// readUntilClosed reads a viewer's stream to its closing event.
func readUntilClosed(t *testing.T, ctx context.Context, viewer *AttachStream) TerminalClosed {
	t.Helper()
	for {
		event, err := viewer.Recv(ctx)
		require.NoError(t, err)
		switch event := event.(type) {
		case TerminalClosed:
			return event
		case TerminalOutput:
			require.NoError(t, viewer.GrantCredit(uint32(len(event.Data))))
		}
	}
}
