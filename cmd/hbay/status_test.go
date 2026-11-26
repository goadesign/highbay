// This file checks the read-only boundary of the local status command.
package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	genprojectserver "github.com/goadesign/highbay/gen/http/project/server"
	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/project"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStatusWithoutAProjectLeavesNoState covers the companion's idle poll
// in a directory that has neither a Highbay project nor operator storage.
func TestStatusWithoutAProjectLeavesNoState(t *testing.T) {
	isolateServeWorkspace(t)
	runtime := testProjectRuntime()
	var output bytes.Buffer
	require.NoError(t, runWithRuntime([]string{"status", "--json"}, &output, runtime))
	assert.JSONEq(t, `{"running":false}`, output.String())
	root, err := runtime.configRoot()
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, "highbay"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// TestStatusVerifiesServerOwnership covers a stale port, another project's
// identity, and a current project serving the typed status from another build.
func TestStatusVerifiesServerOwnership(t *testing.T) {
	for _, tc := range []struct {
		name     string
		held     bool
		mismatch bool
	}{
		{name: "stale port"},
		{name: "identity mismatch", held: true, mismatch: true},
		{name: "compatible server from another build", held: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateServeWorkspace(t)
			state, err := fixtureProjectState("")
			require.NoError(t, err)
			if tc.held {
				owner, err := openFixtureProjectCards(state)
				require.NoError(t, err)
				t.Cleanup(func() { assert.NoError(t, owner.Close()) })
			}
			var calls atomic.Int32
			management := strings.Repeat("a", 64)
			require.NoError(t, state.SaveRunning(project.Running{ManagementCredential: management}))
			api := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				assert.Equal(t, http.MethodGet, r.Method)
				assert.True(t, r.Header.Get("Authorization") == "Bearer "+management, "the holder receives its management credential")
				w.Header().Set("Content-Type", "application/json")
				if call == 2 {
					assert.Equal(t, "/project/status", r.URL.Path)
					assert.NoError(t, json.NewEncoder(w).Encode(genprojectserver.NewStatusResponseBody(&genproject.ProjectStatus{
						ProjectName: filepath.Base(state.Record.Directory),
						Directory:   state.Record.Directory,
						HerdrMode:   "unavailable",
						Stage:       "describe",
					})))
					return
				}
				assert.Equal(t, "/project/identity", r.URL.Path)
				id := state.Record.ID
				if tc.mismatch {
					id = "different-project"
				}
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]string{
					"project_id": id,
					"directory":  state.Record.Directory,
					"owner":      state.Record.Owner,
					"build":      strings.Repeat("b", 64),
				}))
			}))
			require.NoError(t, api.Listener.Close())
			api.Listener, err = net.Listen("tcp", state.Address())
			require.NoError(t, err)
			api.Start()
			t.Cleanup(api.Close)

			var output bytes.Buffer
			err = runWithRuntime([]string{"status", "--json"}, &output, testProjectRuntime())
			if tc.mismatch {
				require.ErrorIs(t, err, errWrongProject)
				assert.Empty(t, output.String())
				assert.False(t, strings.Contains(err.Error(), management), "errors contain no management credential")
				assert.EqualValues(t, 1, calls.Load(), "status is never fetched for a mismatched server")
				return
			}
			require.NoError(t, err)
			if tc.held {
				assert.Contains(t, output.String(), `"running":true`)
				assert.EqualValues(t, 2, calls.Load(), "status follows the authenticated identity")
				return
			}
			assert.JSONEq(t, `{"running":false}`, output.String())
			assert.Zero(t, calls.Load(), "no credential goes to a listener without the store writer lock")
			_, err = os.Stat(state.CardsDir())
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}
