// These tests start short shell scripts in place of "hc connect" to exercise
// starting, closing and reaping the child process.
package hc

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// script writes an executable shell script standing in for "hc" and returns
// its path. The script receives "connect" as its only argument.
func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hc")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700))
	return path
}

// TestExecConnector runs real child processes, because starting one, closing
// its input and reaping it is the contract under test. Each case starts one
// short shell script; the whole test takes well under a second.
func TestExecConnector(t *testing.T) {
	t.Run("forwards both directions and exits when its input closes", func(t *testing.T) {
		program := script(t, `test "$1" = connect || exit 9
test "$HC_SESSION" = isolated || exit 8
exec cat`)
		stream, release, err := ExecConnector(program, []string{"HC_SESSION=isolated"})(context.Background())
		require.NoError(t, err)
		_, err = io.WriteString(stream, "ping\n")
		require.NoError(t, err)
		line, err := bufio.NewReader(stream).ReadString('\n')
		require.NoError(t, err)
		assert.Equal(t, "ping\n", line)
		require.NoError(t, stream.Close())
		assert.NoError(t, stream.Close(), "closing twice is safe")
		assert.NoError(t, release())
	})

	t.Run("reports a failed exit with what the program said", func(t *testing.T) {
		program := script(t, `echo "no core is running" >&2
exit 5`)
		stream, release, err := ExecConnector(program, nil)(context.Background())
		require.NoError(t, err)
		_, err = io.ReadAll(stream)
		require.NoError(t, err, "the output ends when the program exits")
		require.NoError(t, stream.Close())
		err = release()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exit status 5")
		assert.Contains(t, err.Error(), "no core is running")
	})

	t.Run("kills a program that ignores its input closing", func(t *testing.T) {
		connect := execConnector{
			program: script(t, `exec sleep 30`),
			grace:   50 * time.Millisecond,
			drain:   stderrDrain,
		}.connect
		stream, release, err := connect(context.Background())
		require.NoError(t, err)
		require.NoError(t, stream.Close())
		started := time.Now()
		err = release()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "was killed")
		assert.Less(t, time.Since(started), 5*time.Second)
		_, err = stream.Read(make([]byte, 1))
		assert.Error(t, err, "release unblocks readers")
	})

	// A real "hc connect" may start the core, which outlives it and can keep
	// its error output open. Each case leaves a 0.2s sleep behind holding
	// that output, and waits for the program's own exit (its standard output
	// closing) before the grace period starts.
	for _, test := range []struct {
		name         string
		exit         string
		grace, drain time.Duration
		check        func(t *testing.T, err error)
	}{
		{
			name:  "reports the exit of a program whose error output stays open",
			exit:  "echo \"core refused\" >&2\nexit 5",
			grace: 5 * time.Second,
			drain: 20 * time.Millisecond,
			check: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "exit status 5")
				assert.Contains(t, err.Error(), "core refused")
				assert.NotContains(t, err.Error(), "killed")
			},
		},
		{
			// The grace period ends while the error output is still being
			// collected: the program had already exited by itself.
			name:  "does not call a program killed that exited before the grace period ended",
			exit:  "exit 0",
			grace: 50 * time.Millisecond,
			drain: 5 * time.Second,
			check: func(t *testing.T, err error) { assert.NoError(t, err) },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			program := script(t, "sleep 0.2 >/dev/null &\ncat >/dev/null\n"+test.exit)
			connect := execConnector{program: program, grace: test.grace, drain: test.drain}.connect
			stream, release, err := connect(context.Background())
			require.NoError(t, err)
			require.NoError(t, stream.Close())
			_, err = io.ReadAll(stream)
			require.NoError(t, err, "the program exits when its input closes")
			started := time.Now()
			test.check(t, release())
			assert.Less(t, time.Since(started), time.Second, "release does not wait out the grace period or the drain")
		})
	}

	t.Run("refuses a program that cannot start", func(t *testing.T) {
		stream, release, err := ExecConnector(filepath.Join(t.TempDir(), "missing"), nil)(context.Background())
		assert.Nil(t, stream)
		assert.Nil(t, release)
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("refuses a context that is already over", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		stream, release, err := ExecConnector(script(t, `exec cat`), nil)(ctx)
		assert.Nil(t, stream)
		assert.Nil(t, release)
		assert.ErrorIs(t, err, context.Canceled)
	})
}

// TestDialExecConnector checks a Client's whole lifetime over a child
// process: the handshake, then Close ending and reaping the child.
func TestDialExecConnector(t *testing.T) {
	answer := filepath.Join(t.TempDir(), "answer")
	require.NoError(t, os.WriteFile(answer, []byte(helloAnswer()), 0o600))
	program := script(t, `read hello
cat "`+answer+`"
exec cat >/dev/null`)
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	c, err := Dial(ctx, ExecConnector(program, nil))
	require.NoError(t, err)
	assert.Equal(t, "0.1.22", c.Hello().Version)
	assert.NoError(t, c.Close())
	waitDone(t, c)
	assert.ErrorIs(t, c.Err(), ErrClosed)

	t.Run("a child that exits during the handshake", func(t *testing.T) {
		program := script(t, `echo "cannot reach the core" >&2
exit 5`)
		c, err := Dial(ctx, ExecConnector(program, nil))
		assert.Nil(t, c)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot reach the core", "the child's own words explain the failure")
	})
}
