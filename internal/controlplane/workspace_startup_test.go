// This file checks that control-plane construction uses the same workspace Git
// allowance as the CLI and refuses failed reads before creating private state.
package controlplane

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

	"github.com/goadesign/highbay/internal/verify"
)

// TestNewWorkspaceGitAdmission exercises the complete constructor with a private
// factory that observes the production policy before shortening the real timer.
func TestNewWorkspaceGitAdmission(t *testing.T) {
	for _, outcome := range []struct {
		name    string
		script  string
		message string
		timeout bool
	}{
		{name: "timeout", script: "exec /bin/sleep 60\n", message: "timed out", timeout: true},
		{name: "execution error", script: "printf 'fatal: repository unavailable\\n' >&2\nexit 7\n", message: "repository unavailable"},
	} {
		t.Run(outcome.name, func(t *testing.T) {
			root := t.TempDir()
			workspace := filepath.Join(root, "workspace")
			require.NoError(t, os.Mkdir(workspace, 0o700))
			bin := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+outcome.script), 0o700))
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			called := false
			withTimeout := func(ctx context.Context, allowance time.Duration) (context.Context, context.CancelFunc) {
				called = true
				assert.Equal(t, 30*time.Second, allowance)
				assert.Equal(t, verify.WorkspaceGitTimeout, allowance)
				if outcome.timeout {
					allowance = 100 * time.Millisecond
				}
				return context.WithTimeout(ctx, allowance)
			}
			cp, err := newWithWorkspaceGitTimeout(Options{
				Team:         testTeamBinding(),
				WorkspaceDir: workspace,
				CardsDir:     filepath.Join(root, "cards"),
				RunsDir:      filepath.Join(root, "runs"),
				InitStageDir: filepath.Join(root, "init-stage"),
			}, withTimeout)
			require.Error(t, err)
			assert.Nil(t, cp)
			assert.True(t, called)
			assert.Contains(t, err.Error(), "git rev-parse")
			assert.Contains(t, err.Error(), outcome.message)
			assert.Contains(t, err.Error(), "start Highbay again")
			assert.Equal(t, outcome.timeout, errors.Is(err, context.DeadlineExceeded))
			var exitErr *exec.ExitError
			assert.ErrorAs(t, err, &exitErr)
			entries, readErr := os.ReadDir(root)
			require.NoError(t, readErr)
			require.Len(t, entries, 1, "failed admission creates no cards, runs, or staging directory")
			assert.Equal(t, "workspace", entries[0].Name())
			entries, readErr = os.ReadDir(workspace)
			require.NoError(t, readErr)
			assert.Empty(t, entries)
		})
	}
	assert.Equal(t, 10*time.Second, workspaceGitTimeout, "init_project HEAD checks keep their own allowance")
}
