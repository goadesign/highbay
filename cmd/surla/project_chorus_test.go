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
