//go:build e2e

// This file exercises real committed-workspace verification preparation only
// in the hosted native profile; local focused runs use -short.
package verify

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPrepareUsesCommittedNestedWorkspace builds the real app/lib checkout
// with its committed workspace. Removing that workspace from the commit fails
// dependency preparation even when an enclosing untracked go.work exists.
func TestPrepareUsesCommittedNestedWorkspace(t *testing.T) {
	if testing.Short() {
		t.Skip("native verification preparation runs in hosted e2e")
	}
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	for _, internal := range []bool{true, false} {
		t.Run(fmt.Sprintf("internal=%t", internal), func(t *testing.T) {
			repo, commit := goWorkVerificationFixture(t, internal)
			outer := t.TempDir()
			const outside = "go 1.27.3\n\nuse ./untracked-app\n"
			file := filepath.Join(outer, "go.work")
			require.NoError(t, os.WriteFile(file, []byte(outside), 0o600))
			prepared, err := Prepare(t.Context(), goWorkVerificationPlan(repo, commit, filepath.Join(outer, "run")), t.TempDir())
			if internal {
				require.NoError(t, err)
				assert.Len(t, prepared.Binding.Artifacts, 1)
				assert.FileExists(t, prepared.SuiteBin)
			} else {
				var failure *StageError
				require.ErrorAs(t, err, &failure)
				assert.Equal(t, StageBuild, failure.Stage)
				assert.Equal(t, summaryCandidateBuild, failure.Summary)
				assert.ErrorContains(t, failure.Err, "download candidate modules")
			}
			after, readErr := os.ReadFile(file)
			require.NoError(t, readErr)
			assert.Equal(t, outside, string(after))
		})
	}
}

// TestPrepareWrittenSuiteIgnoresBothWorkspaces builds the same committed
// app/lib checkout with a written suite: the app needs its workspace, while
// eval ignores both workspaces and resolves its local sibling replacement.
func TestPrepareWrittenSuiteIgnoresBothWorkspaces(t *testing.T) {
	if testing.Short() {
		t.Skip("native written-suite preparation runs in hosted e2e")
	}
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	repo, commit := writtenWorkspaceVerificationFixture(t)
	plan := goWorkVerificationPlan(repo, commit, filepath.Join(t.TempDir(), "run"))
	plan.Tests = TestsWritten
	prepared, err := Prepare(t.Context(), plan, t.TempDir())
	require.NoError(t, err)
	assert.Len(t, prepared.Binding.Artifacts, 1)
	assert.FileExists(t, prepared.SuiteBin)
}
