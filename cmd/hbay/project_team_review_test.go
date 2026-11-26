// This file verifies detached startup and the actual CLI/runtime boundaries without real dependencies.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/project"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServerTeamDependenciesResolveOnOperation keeps management available without machine programs.
func TestServerTeamDependenciesResolveOnOperation(t *testing.T) {
	isolateServeWorkspace(t)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HERDR_APP_HOME", t.TempDir())
	state, err := fixtureProjectState(t.TempDir())
	require.NoError(t, err)
	deps, err := projectTeamDependencies(t.Context(), os.Environ())
	require.NoError(t, err, "server construction must not resolve Herdr or Chorus")
	server, err := project.NewServer(state, func() {}, deps)
	require.NoError(t, err)
	ctx := controlplane.WithPrincipal(t.Context(), controlplane.Principal{ID: state.Record.Owner, CallerID: "management", Kind: controlplane.CallerManagement})
	_, err = server.Identity(ctx)
	require.NoError(t, err)
	team, err := server.EnsureTeam(ctx)
	require.NoError(t, err)
	assert.Equal(t, "unavailable", team.HerdrMode)
	assert.Empty(t, team.Roles)
	connection, err := deps.Connect(ctx)
	require.NoError(t, err)
	assert.Equal(t, project.TeamConnection{}, connection)
}

// testBuildIdentity hashes this test binary's executable once per test process, as
// project.BuildIdentity reads a running hbay's once. Hashing the whole binary takes
// long enough under the race detector to miss a test's bounded wait, so no request
// handler or deadline may include it.
var testBuildIdentity = sync.OnceValues(func() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(self)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
})

// testBuild is the build identity of this test binary: the SHA-256 of its executable.
// Call it on the test's goroutine before starting a fake server or a deadline, and
// give handlers the returned value: require must not run on a handler's goroutine.
func testBuild(t *testing.T) string {
	t.Helper()
	build, err := testBuildIdentity()
	require.NoError(t, err)
	return build
}

// serveFixtureIdentity answers the project's identity at its saved address while the card
// store is held, as a running server of the given build would; an empty build omits the
// field, as servers built before it did. Stop requests release the store.
func serveFixtureIdentity(t *testing.T, state *project.State, build string) {
	t.Helper()
	cp, err := openFixtureProjectCards(state)
	require.NoError(t, err)
	var closeOnce sync.Once
	closeStore := func() { closeOnce.Do(func() { assert.NoError(t, cp.Close()) }) }
	t.Cleanup(closeStore)
	api := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/project/identity":
			identity := map[string]string{"project_id": state.Record.ID, "directory": state.Record.Directory, "owner": state.Record.Owner}
			if build != "" {
				identity["build"] = build
			}
			assert.NoError(t, json.NewEncoder(w).Encode(identity))
		case "/project/stop":
			w.WriteHeader(http.StatusAccepted)
			go closeStore()
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	require.NoError(t, api.Listener.Close())
	api.Listener, err = net.Listen("tcp", state.Address())
	require.NoError(t, err)
	api.Start()
	t.Cleanup(api.Close)
	require.NoError(t, state.SaveRunning(project.Running{ManagementCredential: strings.Repeat("b", 64)}))
}

// TestServerOfAnotherBuildNamesRestartStep refuses to attach to a running server that
// another build of hbay started, such as the one running before an upgrade, and names
// the restart step; the same build attaches.
func TestServerOfAnotherBuildNamesRestartStep(t *testing.T) {
	for _, tc := range []struct{ name, build string }{
		{"same build", ""},
		{"another build", strings.Repeat("0", 64)},
		{"build not reported", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateServeWorkspace(t)
			state, err := fixtureProjectState("")
			require.NoError(t, err)
			build := tc.build
			if tc.name == "same build" {
				build = testBuild(t)
			}
			serveFixtureIdentity(t, state, build)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, _, err = attachOrStartProject(ctx, state, func(context.Context, *project.State) (string, error) {
				t.Error("a running server is never started again")
				return "", nil
			})
			if tc.name == "same build" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, "this project's server was started by another build of hbay; run hbay stop, then hbay")
		})
	}
}

// TestStopAcceptsServerOfAnyBuild keeps hbay stop working against a server that another
// build started, including one built before servers reported their build.
func TestStopAcceptsServerOfAnyBuild(t *testing.T) {
	for _, tc := range []struct{ name, build string }{
		{"another build", strings.Repeat("0", 64)},
		{"build not reported", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateServeWorkspace(t)
			state, err := fixtureProjectState("")
			require.NoError(t, err)
			serveFixtureIdentity(t, state, tc.build)
			var output strings.Builder
			require.NoError(t, testProjectRuntime().stop(nil, &output))
			assert.Equal(t, "Highbay stopped.\n", output.String())
		})
	}
}

// TestCommandErrorsNeverIncludeArguments verifies server adapters do not export selected paths or argv.
func TestCommandErrorsNeverIncludeArguments(t *testing.T) {
	raw, err := runTeamCommand(t.Context(), nil, "/no-such-program", "private-project-path", "private-argument")
	assert.Nil(t, raw)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "private-project-path")
	assert.NotContains(t, err.Error(), "private-argument")
	assert.Equal(t, "no-such-program command failed", err.Error())
}

// TestManagementTimeoutHasRecoveryStep checks that a management request that
// does not finish names its own recovery and no Herdr step: no management
// read waits on Herdr.
func TestManagementTimeoutHasRecoveryStep(t *testing.T) {
	isolateServeWorkspace(t)
	state, err := fixtureProjectState(t.TempDir())
	require.NoError(t, err)
	release := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer api.Close()
	defer close(release)
	state.Record.Port = api.Listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, state.SaveRunning(project.Running{ManagementCredential: strings.Repeat("a", 64)}))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, err = projectClient(state).Identity(ctx)
	require.ErrorContains(t, err, "project server request did not finish")
	assert.NotContains(t, err.Error(), "check Herdr")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}
