// This file checks workspace Git admission through startup and settings callers
// with a real stand-in process and a shortened private deadline factory.
package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goadesign/surla/internal/abspath"
	"github.com/goadesign/surla/internal/project"
	"github.com/goadesign/surla/internal/verify"
)

// TestWorkspaceGitAdmission checks the actual startup and settings boundaries:
// both request the shared 30s policy, refuse failed Git reads before writes, and
// give a recovery step for the command being attempted.
func TestWorkspaceGitAdmission(t *testing.T) {
	for _, caller := range []string{"startup", "verification", "promotion"} {
		for _, outcome := range []struct {
			name    string
			script  string
			message string
			timeout bool
		}{
			{name: "timeout", script: "exec /bin/sleep 60\n", message: "timed out", timeout: true},
			{name: "execution error", script: "printf 'fatal: repository unavailable\\n' >&2\nexit 7\n", message: "repository unavailable"},
		} {
			t.Run(caller+"/"+outcome.name, func(t *testing.T) {
				workspace, err := abspath.Eval(t.TempDir())
				require.NoError(t, err)
				state, err := project.Discover(t.TempDir(), workspace)
				require.NoError(t, err)
				bin := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+outcome.script), 0o700))
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				called := false
				withTimeout := func(ctx context.Context, allowance time.Duration) (context.Context, context.CancelFunc) {
					called = true
					assert.Equal(t, 30*time.Second, allowance)
					assert.Equal(t, verify.WorkspaceGitTimeout, allowance)
					return context.WithTimeout(ctx, 100*time.Millisecond)
				}
				step := "run surla again"
				if caller == "startup" {
					_, err = configureProjectWorkspaceWithTimeout(state, withTimeout)
				} else {
					var snapshot []byte
					snapshot, err = validatedSettingsWithTimeout(caller, filepath.Join(t.TempDir(), "settings.json"), state, withTimeout)
					assert.Nil(t, snapshot)
					step = "run surla " + caller + " set <file> again"
				}
				require.Error(t, err)
				assert.True(t, called)
				assert.Contains(t, err.Error(), "git rev-parse")
				assert.Contains(t, err.Error(), outcome.message)
				assert.Contains(t, err.Error(), step)
				assert.Equal(t, outcome.timeout, errors.Is(err, context.DeadlineExceeded))
				var exitErr *exec.ExitError
				assert.ErrorAs(t, err, &exitErr)
				_, statErr := os.Stat(state.Dir)
				assert.ErrorIs(t, statErr, os.ErrNotExist, "failed admission must not create private project state")
				entries, readErr := os.ReadDir(workspace)
				require.NoError(t, readErr)
				assert.Empty(t, entries, "failed admission must not change the workspace")
			})
		}
	}
}
