// This file drives StartBuild through the production Git, sandbox and generation owners.
package controlplane

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/testmodule"
	"github.com/goadesign/surla/internal/testwriter"
	"github.com/goadesign/surla/verify/candidate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildProbeDesign imports a real generator dependency. Without its go.sum,
// the module-loading owner returns an authored failure before publication.
const buildProbeDesign = `package design

import . "goa.design/goa/v3/dsl"

var _ = Service("shop", func() {
	Method("read", func() { Result(String) })
})
`

// TestStartBuildUsesTheProductionCopy retains the reviewer's real-owner probe:
// one copy across starts, correct project subfolder, clean original repository,
// and an actionable generation finding. Only the external team is replaced.
func TestStartBuildUsesTheProductionCopy(t *testing.T) {
	if err := candidate.CheckSandbox(); err != nil {
		t.Skipf("production build-start probe requires the candidate sandbox: %v", err)
	}
	for _, subdir := range []string{".", "services/shop"} {
		t.Run(subdir, func(t *testing.T) {
			eval := map[string]string{
				"go.mod":           fmt.Sprintf("module example.com/shop/eval\n\ngo 1.27.3\n\nrequire goa.design/goa/v3 %s\n", testmodule.SurlaVersion(t, "goa.design/goa/v3")),
				"design/design.go": buildProbeDesign,
			}
			repo, project := productionBuildProject(t, subdir, eval)
			f := &teamServiceFixture{}
			var registered []BuildFolders
			f.register = func(_ context.Context, folders BuildFolders) error {
				registered = append(registered, folders)
				return nil
			}
			cp := newControlPlane(t, Options{
				WorkspaceDir: project, Team: &TeamBinding{Owner: "alice@example.com", Service: f},
			})
			card := buildableChange(t, cp, "chg")
			workspace, _, _ := cp.currentWorkspaceTarget()
			builderFolder := filepath.Join(workspace, testwriter.BuilderRole.Folder, subdir)
			folder := filepath.Join(workspace, testwriter.Folder, subdir)
			for i := range 2 {
				if i == 1 {
					// Remove the builder copy before Build again to prove a
					// missing copy is recreated at the saved build's base.
					runGitForTest(t, repo, "worktree", "remove", "--force", filepath.Join(workspace, testwriter.BuilderRole.Folder))
				}
				result, err := startBuildAgain(t, cp, asCaller("alice@example.com", CallerUser), "chg", card.RequestRevision)
				require.NoError(t, err)
				head := productionBuildGit(t, repo, "rev-parse", "HEAD")
				assert.Equal(t, head, productionBuildGit(t, builderFolder, "rev-parse", "HEAD"))
				assert.Equal(t, testwriter.BuilderRole.Branch(1),
					productionBuildGit(t, builderFolder, "symbolic-ref", "--short", "HEAD"))
				assert.Equal(t, head, productionBuildGit(t, folder, "rev-parse", "HEAD"))
				assert.Equal(t, testwriter.TestsRole.Branch(1),
					productionBuildGit(t, folder, "symbolic-ref", "--short", "HEAD"))
				finding, ok := result.Build.TestWriter.Tests.AsNeedsChanges()
				require.True(t, ok, "the real module failure is saved after the build")
				assert.Equal(t, 1, finding.FindingCount)
				assignment, err := cp.GetTestWriterAssignment(asCaller("alice@example.com", CallerTestWriter), &gensurla.GetTestWriterAssignmentPayload{ChangeID: "chg"})
				require.NoError(t, err)
				require.Len(t, assignment.Findings, 1, "the test writer reads the finding's text")
				assert.Contains(t, assignment.Findings[0], "go.sum")
				assert.True(t, strings.HasSuffix(assignment.Findings[0], "Correct the problem in the test module, then report_tests_finished."))
				assert.Equal(t, folder, assignment.WorkingFolder)
				assert.Empty(t, productionBuildGit(t, repo, "status", "--porcelain", "--untracked-files=all"))
			}
			assert.Equal(t, []BuildFolders{
				BuilderAndTestWriter{BuilderFolder: builderFolder, TestWriterFolder: folder},
				BuilderAndTestWriter{BuilderFolder: builderFolder, TestWriterFolder: folder},
			}, registered)
			assert.Equal(t, 3, strings.Count(productionBuildGit(t, repo, "worktree", "list", "--porcelain"), "worktree "))
		})
	}
}

// TestStartBuildPreSaveInspectRefusals verifies that InspectCopy refuses
// folder-in-use and branch-in-use for both .surla-build and .surla-tests
// before saving the build or registering any team roles.
func TestStartBuildPreSaveInspectRefusals(t *testing.T) {
	cases := []struct {
		name           string
		startFirst     bool
		setup          func(t *testing.T, repo string)
		wantError      string
		wantWords      string
		wantWorktrees  int
		wantRegistered int
	}{
		{
			name: "builder folder in use",
			setup: func(t *testing.T, repo string) {
				require.NoError(t, os.WriteFile(filepath.Join(repo, testwriter.BuilderRole.Folder), []byte("file"), 0o600))
			},
			wantError:     "copy_folder_in_use",
			wantWords:     "the project's .surla-build entry is not Surla's working copy",
			wantWorktrees: 1,
		},
		{
			name: "tests folder in use",
			setup: func(t *testing.T, repo string) {
				require.NoError(t, os.WriteFile(filepath.Join(repo, testwriter.TestsRole.Folder), []byte("file"), 0o600))
			},
			wantError:     "copy_folder_in_use",
			wantWords:     "the project's .surla-tests entry is not Surla's working copy",
			wantWorktrees: 1,
		},
		{
			name:       "builder branch in use on build again",
			startFirst: true,
			setup: func(t *testing.T, repo string) {
				runGitForTest(t, repo, "worktree", "remove", "--force", filepath.Join(repo, testwriter.BuilderRole.Folder))
				runGitForTest(t, repo, "switch", "--quiet", testwriter.BuilderRole.Branch(1))
			},
			wantError:      "tests_branch_in_use",
			wantWords:      "branch surla/build-1, which Surla reserves for the builder's working copy, is held by another working copy",
			wantWorktrees:  2,
			wantRegistered: 2,
		},
		{
			name:       "tests branch in use on build again",
			startFirst: true,
			setup: func(t *testing.T, repo string) {
				runGitForTest(t, repo, "worktree", "remove", "--force", filepath.Join(repo, testwriter.TestsRole.Folder))
				runGitForTest(t, repo, "switch", "--quiet", testwriter.TestsRole.Branch(1))
			},
			wantError:      "tests_branch_in_use",
			wantWords:      "branch surla/tests-1, which Surla reserves for the test writer's working copy, is held by another working copy",
			wantWorktrees:  2,
			wantRegistered: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eval := map[string]string{
				"go.mod":           "module example.com/shop/eval\n\ngo 1.27.3\n",
				"design/design.go": "package design\n",
			}
			repo, project := productionBuildProject(t, ".", eval)
			f := &teamServiceFixture{}
			cp := newControlPlane(t, Options{
				WorkspaceDir: project, Team: &TeamBinding{Owner: "alice@example.com", Service: f},
			})
			cp.buildSandboxCheck = func() error { return nil }
			card := buildableChange(t, cp, "chg")
			var firstStartedAt string
			if tc.startFirst {
				first, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{
					ChangeID: "chg", RequestRevision: card.RequestRevision,
				})
				require.NoError(t, err)
				firstStartedAt = first.Build.StartedAt
			}
			tc.setup(t, repo)

			_, err := startBuildAgain(t, cp, asCaller("alice@example.com", CallerUser), "chg", card.RequestRevision)
			assertRefused(t, err, tc.wantError)
			assert.ErrorContains(t, err, tc.wantWords)
			if tc.startFirst {
				assert.Equal(t, firstStartedAt, mustStartedBuild(t, cp, asCaller("alice@example.com", CallerUser), "chg").StartedAt,
					"the refused Build again left the earlier build untouched")
			} else {
				assertNoBuildSaved(t, cp, "chg")
			}
			assert.Len(t, f.calls, tc.wantRegistered)
			assert.Equal(t, tc.wantWorktrees, strings.Count(productionBuildGit(t, repo, "worktree", "list", "--porcelain"), "worktree "))
		})
	}
}

// TestStartBuildNamesARealGenerationLimit cancels the real Prepare owner while
// its design cannot finish. The phase context supplies the reason even if the
// command's exit error does not wrap DeadlineExceeded.
func TestStartBuildNamesARealGenerationLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real sandboxed generation to its injected one-second deadline")
	}
	if err := candidate.CheckSandbox(); err != nil {
		t.Skipf("real generation-limit proof requires the candidate sandbox: %v", err)
	}
	// The design imports Goa-AI's evaluation DSL, as a test module's does, so
	// its module keeps the Goa-AI requirement the Story table's imports need.
	design := strings.Replace(buildProbeDesign, `import . "goa.design/goa/v3/dsl"`, `import (
	. "goa.design/goa/v3/dsl"
	"time"

	_ "goa.design/goa-ai/eval/dsl"
)

func init() { time.Sleep(30 * time.Second) }`, 1)
	eval := testmodule.Module(t, "example.com/shop/eval", "", map[string]string{"design/design.go": design})
	_, project := productionBuildProject(t, ".", eval)
	cp := newControlPlane(t, Options{
		WorkspaceDir: project, Team: &TeamBinding{Owner: "alice@example.com", Service: &teamServiceFixture{}},
	})
	cp.buildStartWithTimeout = func(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
		assert.Equal(t, 5*time.Minute, limit)
		return context.WithTimeout(ctx, time.Second)
	}
	card := buildableChange(t, cp, "chg")
	result, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{
		ChangeID: "chg", RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err, "the limit is reached after saving")
	failure, ok := result.Build.TestWriter.Tests.AsCouldNotRun()
	require.True(t, ok)
	assert.Equal(t, "The build-start time limit ended before the required tests could be prepared.", failure.Reason)
	assert.Equal(t, "Build this change again on the views page.", failure.Step)
}

// productionBuildProject creates and commits only trusted fixture files, with
// explicit local identity and no hooks/signing, for the real production owners.
func productionBuildProject(
	t *testing.T, subdir string, eval map[string]string,
) (string, string) {
	t.Helper()
	repo := realTempDir(t)
	project := filepath.Join(repo, subdir)
	files := map[string]string{"go.mod": "module example.com/shop\n\ngo 1.27.3\n"}
	for name, content := range eval {
		files["eval/"+name] = content
	}
	writeWorkspaceFiles(t, project, files)
	initGitWorktreeForBoundaryTest(t, repo)
	for _, setting := range []string{"user.name=Build Tester", "user.email=build@example.com", "commit.gpgsign=false", "core.hooksPath=/dev/null", "core.fsmonitor=false"} {
		key, value, _ := strings.Cut(setting, "=")
		runGitForTest(t, repo, "config", key, value)
	}
	runGitForTest(t, repo, "add", "--all")
	runGitForTest(t, repo, "commit", "--quiet", "-m", "Create the build fixture")
	return repo, project
}

// productionBuildGit reads the real owner's Git postconditions without shell
// interpretation. It starts no authored program and receives no standard input.
func productionBuildGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}
