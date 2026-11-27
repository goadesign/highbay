package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/project"
	"github.com/goadesign/highbay/internal/selfupdate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateOptionsPinApproval(t *testing.T) {
	opts, err := parseUpdateOptions([]string{"-y", "-version", "1.2.3"})
	require.NoError(t, err)
	assert.True(t, opts.yes)
	assert.Equal(t, "1.2.3", opts.version)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-check", "-version", "1.2.3"}, "-check cannot be combined with -version"},
		{[]string{"-version="}, "is not MAJOR.MINOR.PATCH"},
		{[]string{"-version", "v1.2.3"}, "is not MAJOR.MINOR.PATCH"},
		{[]string{"1.2.3"}, "unexpected arguments"},
	} {
		_, err := parseUpdateOptions(tc.args)
		assert.ErrorContains(t, err, tc.want)
	}
}

func TestUpdateReportsInstalledAndRunningVersions(t *testing.T) {
	for _, tc := range []struct {
		name, current, running, release, target, message string
		exact                                            bool
		refused                                          string
	}{
		{
			name: "available", current: "1.0.0", running: "v1.0.0", release: "1.1.0", target: "1.1.0",
			message: "Update available: highbay v1.0.0 -> v1.1.0 (https://example.test/release)\n",
		},
		{
			name: "restart", current: "1.1.0", running: "v1.0.0", release: "1.1.0", target: "1.1.0",
			message: "Restart needed: highbay binary is v1.1.0, running server is v1.0.0\n",
		},
		{
			name: "running ahead of both", current: "1.1.0", running: "v1.2.0", release: "1.0.0",
			message: "Highbay v1.2.0 is running, newer than the latest release v1.0.0\n",
		},
		{
			name: "installed ahead of both", current: "1.2.0", running: "v1.1.0", release: "1.0.0", target: "1.2.0",
			message: "Restart needed: highbay binary is v1.2.0, running server is v1.1.0\n",
		},
		{
			name: "current without project", current: "1.1.0", release: "1.1.0",
			message: "Highbay v1.1.0 is up to date\n",
		},
		{
			name: "exact release downgrades CLI", current: "1.2.0", release: "1.1.0", exact: true,
			refused: "v1.1.0 is older than this Highbay binary v1.2.0",
		},
		{
			name: "exact release downgrades running server", current: "1.0.0", running: "v1.2.0",
			release: "1.1.0", exact: true, refused: "v1.1.0 is older than the running project server v1.2.0",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var identity *genproject.ProjectIdentity
			if tc.running != "" {
				identity = &genproject.ProjectIdentity{Version: &tc.running}
			}
			rel := selfupdate.Release{Version: tc.release, URL: "https://example.test/release"}
			var out bytes.Buffer
			target, err := reportUpdate(&out, tc.current, identity, rel, tc.exact)
			if tc.refused != "" {
				assert.EqualError(t, err, tc.refused)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tc.target, target)
			assert.Equal(t, tc.message, out.String())
		})
	}
}

func TestUpdateOutsideProjectDoesNotCreateOperatorStorage(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	config := filepath.Join(home, "config")
	runtime := projectRuntime{configRoot: func() (string, error) { return config, nil }}
	state, err := runtime.updateProject()
	require.NoError(t, err)
	assert.Nil(t, state)
	_, err = os.Stat(config)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestUpdateServerAppliesPinnedReleaseAndWaitsThroughRestart(t *testing.T) {
	state := &project.State{Record: project.Record{
		ID: "project", Directory: "/project", Owner: "uid:501",
	}}
	calls := 0
	identities := 0
	old, target := "v1.0.0", "v1.1.0"
	client := &genproject.Client{
		ApplyUpdateEndpoint: func(_ context.Context, payload any) (any, error) {
			calls++
			assert.Equal(t, &genproject.ApplyUpdatePayload{Version: "1.1.0"}, payload)
			return &genproject.ApplyUpdateResult{Status: "restarting", Version: target}, nil
		},
		IdentityEndpoint: func(context.Context, any) (any, error) {
			identities++
			identity := &genproject.ProjectIdentity{
				ProjectID: state.Record.ID, Directory: state.Record.Directory, Owner: state.Record.Owner,
				Version: &target,
			}
			switch identities {
			case 1:
				identity.Version = &old
			case 2:
				return nil, errors.New("management credential is rotating")
			}
			return identity, nil
		},
	}
	var out bytes.Buffer
	installLocally, err := updateProjectServer(t.Context(), state, client, "1.1.0", &out)
	require.NoError(t, err)
	assert.False(t, installLocally)
	assert.Equal(t, 1, calls)
	assert.Equal(t, 3, identities)
	assert.Equal(t, "Highbay server is running v1.1.0\n", out.String())
}

func TestUpdateServerAlreadyOnTargetRequiresLocalInstall(t *testing.T) {
	state := &project.State{Record: project.Record{
		ID: "project", Directory: "/project", Owner: "uid:501",
	}}
	target := "v1.1.0"
	client := &genproject.Client{
		ApplyUpdateEndpoint: func(context.Context, any) (any, error) {
			return &genproject.ApplyUpdateResult{Status: "up_to_date", Version: target}, nil
		},
		IdentityEndpoint: func(context.Context, any) (any, error) {
			return &genproject.ProjectIdentity{
				ProjectID: state.Record.ID, Directory: state.Record.Directory, Owner: state.Record.Owner,
				Version: &target,
			}, nil
		},
	}
	var out bytes.Buffer
	installLocally, err := updateProjectServer(t.Context(), state, client, "1.1.0", &out)
	require.NoError(t, err)
	assert.True(t, installLocally)
	assert.Empty(t, out.String(), "updating the CLI still has to succeed")
}

func TestUpdateServerStopsWhenSavedPortAnswersForAnotherProject(t *testing.T) {
	state := &project.State{Record: project.Record{ID: "project"}}
	client := &genproject.Client{
		ApplyUpdateEndpoint: func(context.Context, any) (any, error) {
			return &genproject.ApplyUpdateResult{Status: "restarting", Version: "v1.1.0"}, nil
		},
		IdentityEndpoint: func(context.Context, any) (any, error) {
			return &genproject.ProjectIdentity{ProjectID: "other"}, nil
		},
	}
	installLocally, err := updateProjectServer(t.Context(), state, client, "1.1.0", io.Discard)
	assert.ErrorIs(t, err, errWrongProject)
	assert.False(t, installLocally)
}

func TestUpdateUsesInstalledExecutableForSetup(t *testing.T) {
	next := filepath.Join(t.TempDir(), "highbay")
	script := "#!/bin/sh\nprintf '%s no_start=%s\\n' \"$*\" \"$HIGHBAY_NO_START\"\n"
	require.NoError(t, os.WriteFile(next, []byte(script), 0o755))
	var out bytes.Buffer
	require.NoError(t, setupUpdatedHighbay(t.Context(), next, cliUpdateTimeout, &out))
	assert.Equal(t, "up --yes no_start=1\nsetup -global no_start=1\n", out.String())
}
