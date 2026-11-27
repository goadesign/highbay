// These tests cover the server's version reports and the in-place update,
// with stand-ins for the release source, the installer and the replacement.
package project

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/selfupdate"
)

// fakeUpdates stands in for the composition root's release bindings and
// records what the server asked of them.
type fakeUpdates struct {
	// latest and latestErr are what Latest returns.
	latest    selfupdate.Release
	latestErr error
	// manual is the command CanAutoUpdate reports; empty means the
	// installation can be updated in place.
	manual string
	// installErr and prepareErr fail those steps.
	installErr, prepareErr error
	// calls records the steps in order.
	calls []string
	// released counts releases of the installer's lock.
	released int
	// replaced is the executable Replace received.
	replaced string
}

// bind gives server the stand-ins, reporting version.
func (f *fakeUpdates) bind(server *Server, version string) {
	server.BindUpdates(UpdateDependencies{
		Version: version,
		Latest: func(context.Context) (selfupdate.Release, error) {
			f.calls = append(f.calls, "latest")
			return f.latest, f.latestErr
		},
		CanAutoUpdate: func() (bool, string) { return f.manual == "", f.manual },
		Install: func(_ context.Context, version string) (string, func() error, error) {
			f.calls = append(f.calls, "install "+version)
			if f.installErr != nil {
				return "", nil, f.installErr
			}
			return "/opt/highbay/bin/highbay", func() error { f.released++; return nil }, nil
		},
		Prepare: func(_ context.Context, next string) error {
			f.calls = append(f.calls, "prepare "+next)
			return f.prepareErr
		},
		Replace: func(next string, _ func() error) {
			f.calls = append(f.calls, "replace")
			f.replaced = next
		},
	})
}

// callerOf returns a context for one kind of caller of the project's owner.
func callerOf(state *State, kind controlplane.CallerKind) context.Context {
	return controlplane.WithPrincipal(context.Background(), controlplane.Principal{ID: state.Record.Owner, CallerID: string(kind), Kind: kind})
}

// TestServerReportsItsVersion checks where the running version and the page
// build are reported: to the highbay command with the project's identity,
// and to the page with its session.
func TestServerReportsItsVersion(t *testing.T) {
	for _, test := range []struct {
		name  string
		bound string
		want  string
	}{
		{name: "a release", bound: "v0.5.0", want: "v0.5.0"},
		{name: "a build that reports no version", bound: "", want: "(devel)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, _ := teamServer(t)
			(&fakeUpdates{}).bind(server, test.bound)
			identity, err := server.Identity(teamContext(server.state))
			require.NoError(t, err)
			require.NotNil(t, identity.Version)
			assert.Equal(t, test.want, *identity.Version)
			session := browserSession(t, server)
			assert.Equal(t, test.want, session.Version)
			assert.Equal(t, controlplane.ViewsPageScriptSHA256, session.UIBuild)
			assert.NotEmpty(t, session.UIBuild)
			assert.NotContains(t, session.UIBuild, "'", "the digest is reported without its policy quoting")
		})
	}
}

// TestGetUpdateStatus checks what the page learns about releases.
func TestGetUpdateStatus(t *testing.T) {
	release := selfupdate.Release{Version: "0.5.1", URL: "https://github.com/goadesign/highbay/releases/tag/v0.5.1"}
	manual := "npm install -g @goadesign/highbay@latest"
	for _, test := range []struct {
		name    string
		version string
		updates fakeUpdates
		want    *genproject.UpdateStatusResult
	}{
		{
			name: "a newer release", version: "v0.5.0", updates: fakeUpdates{latest: release},
			want: &genproject.UpdateStatusResult{CurrentVersion: "v0.5.0", LatestVersion: "v0.5.1", UpdateAvailable: true, ReleaseURL: release.URL, CanAutoUpdate: true},
		},
		{
			name: "the same release", version: "v0.5.1", updates: fakeUpdates{latest: release},
			want: &genproject.UpdateStatusResult{CurrentVersion: "v0.5.1", LatestVersion: "v0.5.1", ReleaseURL: release.URL, CanAutoUpdate: true},
		},
		{
			name: "a release older than the running one", version: "v0.6.0", updates: fakeUpdates{latest: release},
			want: &genproject.UpdateStatusResult{CurrentVersion: "v0.6.0", LatestVersion: "v0.5.1", ReleaseURL: release.URL, CanAutoUpdate: true},
		},
		{
			name: "an installation that updates another way", version: "v0.5.0", updates: fakeUpdates{latest: release, manual: manual},
			want: &genproject.UpdateStatusResult{CurrentVersion: "v0.5.0", LatestVersion: "v0.5.1", UpdateAvailable: true, ReleaseURL: release.URL, ManualCommand: &manual},
		},
		{
			name: "a development build", version: "", updates: fakeUpdates{latest: release, manual: "go install github.com/goadesign/highbay/cmd/highbay@latest"},
			want: &genproject.UpdateStatusResult{
				CurrentVersion: "(devel)", LatestVersion: "v0.5.1", ReleaseURL: release.URL,
				ManualCommand: func() *string {
					command := "go install github.com/goadesign/highbay/cmd/highbay@latest"
					return &command
				}(),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, _ := teamServer(t)
			test.updates.bind(server, test.version)
			status, err := server.GetUpdateStatus(userContext(server.state))
			require.NoError(t, err)
			assert.Equal(t, test.want, status)
		})
	}

	t.Run("the release cannot be read", func(t *testing.T) {
		server, _ := teamServer(t)
		(&fakeUpdates{latestErr: errors.New("GET https://api.invalid/private-path: 503")}).bind(server, "v0.5.0")
		_, err := server.GetUpdateStatus(userContext(server.state))
		assert.Equal(t, "release_unavailable", errorName(err))
		assert.NotContains(t, err.Error(), "private-path", "what the release server answered stays out of the refusal")
	})

	t.Run("a server without release bindings", func(t *testing.T) {
		server, _ := teamServer(t)
		_, err := server.GetUpdateStatus(userContext(server.state))
		assert.Equal(t, "release_unavailable", errorName(err))
	})

	for _, kind := range []controlplane.CallerKind{controlplane.CallerManagement, controlplane.CallerGuide, controlplane.CallerBuilder, controlplane.CallerTestWriter} {
		t.Run("refused for "+string(kind), func(t *testing.T) {
			server, _ := teamServer(t)
			updates := &fakeUpdates{}
			updates.bind(server, "v0.5.0")
			_, err := server.GetUpdateStatus(callerOf(server.state, kind))
			assert.Equal(t, "forbidden", errorName(err))
			assert.Empty(t, updates.calls)
		})
	}
}

// TestApplyUpdate checks each outcome of installing a release in place.
func TestApplyUpdate(t *testing.T) {
	apply := func(t *testing.T, updates *fakeUpdates, running, version string, kind controlplane.CallerKind) (*genproject.ApplyUpdateResult, error) {
		t.Helper()
		server, _ := teamServer(t)
		updates.bind(server, running)
		return server.ApplyUpdate(callerOf(server.state, kind), &genproject.ApplyUpdatePayload{Version: version})
	}

	for _, kind := range []controlplane.CallerKind{controlplane.CallerUser, controlplane.CallerManagement} {
		t.Run("installs, prepares and replaces for "+string(kind), func(t *testing.T) {
			updates := &fakeUpdates{}
			result, err := apply(t, updates, "v0.5.0", "v0.5.1", kind)
			require.NoError(t, err)
			assert.Equal(t, &genproject.ApplyUpdateResult{Status: "restarting", Version: "v0.5.1"}, result)
			assert.Equal(t, []string{"install 0.5.1", "prepare /opt/highbay/bin/highbay", "replace"}, updates.calls)
			assert.Equal(t, "/opt/highbay/bin/highbay", updates.replaced)
			assert.Zero(t, updates.released, "the installer's lock is kept for the replacement")
		})
	}

	t.Run("accepts the version without its prefix", func(t *testing.T) {
		updates := &fakeUpdates{}
		result, err := apply(t, updates, "v0.5.0", "0.5.1", controlplane.CallerUser)
		require.NoError(t, err)
		assert.Equal(t, "v0.5.1", result.Version)
	})

	t.Run("the running server already is that version", func(t *testing.T) {
		updates := &fakeUpdates{}
		result, err := apply(t, updates, "v0.5.1", "v0.5.1", controlplane.CallerUser)
		require.NoError(t, err)
		assert.Equal(t, &genproject.ApplyUpdateResult{Status: "up_to_date", Version: "v0.5.1"}, result)
		assert.Empty(t, updates.calls, "nothing is installed")
	})

	t.Run("a development build installs the release it is asked for", func(t *testing.T) {
		updates := &fakeUpdates{}
		result, err := apply(t, updates, "", "v0.5.1", controlplane.CallerUser)
		require.NoError(t, err)
		assert.Equal(t, "restarting", result.Status)
	})

	for _, refused := range []struct {
		name     string
		updates  fakeUpdates
		version  string
		want     string
		calls    []string
		released int
		says     string
	}{
		{name: "a version that is not three numbers", version: "latest", want: "unprocessable", says: "three numbers"},
		{name: "a pre-release", version: "v0.5.1-rc1", want: "unprocessable"},
		{
			name: "an installation that updates another way", updates: fakeUpdates{manual: "npx @goadesign/highbay@latest"},
			version: "v0.5.1", want: "conflict", says: "npx @goadesign/highbay@latest",
		},
		{
			name:    "the release cannot be fetched",
			updates: fakeUpdates{installErr: fmt.Errorf("%w: GET /private-path: 404", selfupdate.ErrReleaseUnavailable)},
			version: "v0.5.1", want: "release_unavailable", calls: []string{"install 0.5.1"},
		},
		{
			name:    "the installed executable is already newer",
			updates: fakeUpdates{installErr: fmt.Errorf("%w: /private-path reports v0.6.0", selfupdate.ErrWouldDowngrade)},
			version: "v0.5.1", want: "conflict", calls: []string{"install 0.5.1"}, says: "already newer",
		},
		{
			name:    "the installation fails",
			updates: fakeUpdates{installErr: errors.New("rename /private-path: permission denied")},
			version: "v0.5.1", want: "conflict", calls: []string{"install 0.5.1"}, says: "highbay update",
		},
		{
			name:    "the new build's setup fails",
			updates: fakeUpdates{prepareErr: errors.New("/private-path up --yes did not finish")},
			version: "v0.5.1", want: "conflict", calls: []string{"install 0.5.1", "prepare /opt/highbay/bin/highbay"}, released: 1, says: "highbay up",
		},
	} {
		t.Run("refused: "+refused.name, func(t *testing.T) {
			_, err := apply(t, &refused.updates, "v0.5.0", refused.version, controlplane.CallerUser)
			assert.Equal(t, refused.want, errorName(err))
			require.Error(t, err)
			assert.Contains(t, err.Error(), refused.says)
			assert.NotContains(t, err.Error(), "private-path", "a dependency's own text stays out of the refusal")
			assert.Equal(t, refused.calls, refused.updates.calls)
			assert.Equal(t, refused.released, refused.updates.released)
			assert.Empty(t, refused.updates.replaced, "a refused update does not replace the server")
		})
	}

	for _, kind := range []controlplane.CallerKind{controlplane.CallerGuide, controlplane.CallerBuilder, controlplane.CallerTestWriter} {
		t.Run("refused for "+string(kind), func(t *testing.T) {
			updates := &fakeUpdates{}
			_, err := apply(t, updates, "v0.5.0", "v0.5.1", kind)
			assert.Equal(t, "forbidden", errorName(err))
			assert.Empty(t, updates.calls)
		})
	}

	t.Run("one update at a time, and none once the server is being replaced", func(t *testing.T) {
		server, _ := teamServer(t)
		updates := &fakeUpdates{}
		updates.bind(server, "v0.5.0")
		user := userContext(server.state)
		// A refused update leaves the next one free to run.
		updates.prepareErr = errors.New("setup failed")
		_, err := server.ApplyUpdate(user, &genproject.ApplyUpdatePayload{Version: "0.5.1"})
		require.Equal(t, "conflict", errorName(err))
		updates.prepareErr = nil
		_, err = server.ApplyUpdate(user, &genproject.ApplyUpdatePayload{Version: "0.5.1"})
		require.NoError(t, err)
		_, err = server.ApplyUpdate(user, &genproject.ApplyUpdatePayload{Version: "0.5.2"})
		assert.Equal(t, "conflict", errorName(err))
		assert.ErrorContains(t, err, "already in progress")
	})
}
