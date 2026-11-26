// This file checks Chorus's status result before it becomes a team connection.
package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChorusManagedAddressRefusals keeps malformed output and command failures
// out of both adapters; valid binding and its bounds live in the reminder test.
func TestChorusManagedAddressRefusals(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		err       error
	}{
		{name: "command failure", err: assert.AnError},
		{name: "invalid JSON", raw: "private invalid reply"},
		{name: "null", raw: "null"},
		{name: "missing origin", raw: `{}`},
		{name: "unknown field", raw: `{"base_url":"http://localhost:9550","private":"value"}`},
		{name: "second value", raw: `{"base_url":"http://localhost:9550"} {}`},
		{name: "invalid host", raw: `{"base_url":"http://["}`},
		{name: "credential", raw: `{"base_url":"http://private:secret@localhost:9550"}`},
		{name: "path", raw: `{"base_url":"http://localhost:9550/mcp"}`},
		{name: "empty query", raw: `{"base_url":"http://localhost:9550?"}`},
		{name: "empty fragment", raw: `{"base_url":"http://localhost:9550#"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			commands := teamCommands{run: func(context.Context, []string, string, ...string) ([]byte, error) {
				return []byte(test.raw), test.err
			}}
			connection, err := commands.connectChorus(t.Context())
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private")
			assert.Empty(t, connection.Endpoint)
			assert.Nil(t, connection.Observer)
			assert.Nil(t, connection.Sender)
		})
	}
}

// TestChorusManagedAddressAutoStartsWhenStopped starts Chorus with `chorus up`
// when the initial `status -json` fails so `/highbay` works without running
// `hbay up` or `chorus up` ahead of each session.
func TestChorusManagedAddressAutoStartsWhenStopped(t *testing.T) {
	var seen [][]string
	running := false
	commands := teamCommands{
		chorus:    "chorus",
		chorusEnv: []string{"PATH=/usr/bin"},
		run: func(_ context.Context, env []string, program string, args ...string) ([]byte, error) {
			assert.Equal(t, []string{"PATH=/usr/bin"}, env)
			seen = append(seen, append([]string{program}, args...))
			if len(args) == 1 && args[0] == "up" {
				running = true
				return []byte("ready\n"), nil
			}
			if !running {
				return nil, assert.AnError
			}
			return []byte(`{"base_url":"http://localhost:9550"}`), nil
		},
	}
	connection, err := commands.connectChorus(t.Context())
	require.NoError(t, err)
	assert.Equal(t, [][]string{
		{"chorus", "status", "-json"},
		{"chorus", "up"},
		{"chorus", "status", "-json"},
	}, seen)
	assert.Equal(t, "http://localhost:9550", connection.Endpoint)
	assert.NotNil(t, connection.Observer)
	assert.NotNil(t, connection.Sender)
}
