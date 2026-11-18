// This file drives StartBuild through the production Git and generation owners.
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

// reviewEval is a minimal test module used by the production build-start tests.
var reviewEval = map[string]string{
	"go.mod":           "module example.com/shop/eval\n\ngo 1.27.3\n",
	"design/design.go": "package design\n",
}

// TestStartBuildUsesTheProductionCopy retains the reviewer's real-owner probe:
// one copy across starts, correct project subfolder, clean original repository,
// and an actionable generation finding. Only the external team is replaced.
func TestStartBuildUsesTheProductionCopy(t *testing.T) {
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
				assert.Equal(t, gensurla.TestsKindAssigned, result.Build.TestWriter.Tests.Kind(), "StartBuild leaves preparation for the marking read")
				head := productionBuildGit(t, repo, "rev-parse", "HEAD")
				assert.Equal(t, head, productionBuildGit(t, builderFolder, "rev-parse", "HEAD"))
				assert.Equal(t, testwriter.BuilderRole.Branch(1),
					productionBuildGit(t, builderFolder, "symbolic-ref", "--short", "HEAD"))
				assert.Equal(t, head, productionBuildGit(t, folder, "rev-parse", "HEAD"))
				assert.Equal(t, testwriter.TestsRole.Branch(1),
					productionBuildGit(t, folder, "symbolic-ref", "--short", "HEAD"))
				assignment, err := cp.GetTestWriterAssignment(asCaller("alice@example.com", CallerTestWriter), &gensurla.GetTestWriterAssignmentPayload{ChangeID: "chg"})
				require.NoError(t, err)
				finding, ok := assignment.Progress.Tests.AsNeedsChanges()
				require.True(t, ok, "the real module failure is saved on the marking read")
				assert.Equal(t, 1, finding.FindingCount)
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
		t.Skip("runs a real generation to its injected one-second deadline")
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
	_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{
		ChangeID: "chg", RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	assignment, err := cp.GetTestWriterAssignment(asCaller("alice@example.com", CallerTestWriter), &gensurla.GetTestWriterAssignmentPayload{
		ChangeID: "chg",
	})
	require.NoError(t, err, "the limit is reached during the marking read")
	failure, ok := assignment.Progress.Tests.AsCouldNotRun()
	require.True(t, ok)
	assert.Equal(t, "The time limit for reading the test writer's assignment ended before the required tests could be prepared.", failure.Reason)
	assert.Equal(t, "Build this change again on the views page.", failure.Step)
}

// TestStoredEvaluatorMakesTheBuilderCopy verifies that a stored-evaluator build
// makes .surla-build on surla/build-1 through the production path, creates no
// .surla-tests copy, and registers BuilderOnly.
func TestStoredEvaluatorMakesTheBuilderCopy(t *testing.T) {
	repo, project, cp, registered := reviewProject(t, Options{BuildCacheDir: t.TempDir(), Verification: &VerificationConfig{RunsDir: t.TempDir()}})
	card := buildableChange(t, cp, "chg")
	result, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{
		ChangeID: "chg", RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	assert.Equal(t, gensurla.TeamKindReady, result.Team.Kind())
	builder := filepath.Join(project, testwriter.BuilderRole.Folder)
	assert.Equal(t, "surla/build-1", productionBuildGit(t, builder, "symbolic-ref", "--short", "HEAD"))
	assert.NoDirExists(t, filepath.Join(project, testwriter.TestsRole.Folder))
	assert.Equal(t, []BuildFolders{BuilderOnly{BuilderFolder: builder}}, *registered)
	assert.Empty(t, productionBuildGit(t, repo, "status", "--porcelain", "--untracked-files=all"))
}

// TestBuildAgainMakesMissingCopiesAtTheSavedBase verifies that after a stop
// that left no copies and a new project commit, Build again remakes both copies
// at the saved base rather than the project's new HEAD.
func TestBuildAgainMakesMissingCopiesAtTheSavedBase(t *testing.T) {
	repo, project, cp, _ := reviewProject(t, Options{})
	card := buildableChange(t, cp, "chg")
	_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{
		ChangeID: "chg", RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	b := productionBuildGit(t, repo, "rev-parse", "HEAD")
	for _, role := range []testwriter.Role{testwriter.BuilderRole, testwriter.TestsRole} {
		runGitForTest(t, repo, "worktree", "remove", "--force", filepath.Join(project, role.Folder))
		runGitForTest(t, repo, "branch", "-D", role.Branch(1))
	}
	writeWorkspaceFiles(t, project, map[string]string{"later.txt": "later\n"})
	runGitForTest(t, repo, "add", "later.txt")
	runGitForTest(t, repo, "commit", "--quiet", "-m", "later")
	_, err = startBuildAgain(t, cp, asCaller("alice@example.com", CallerUser), "chg", card.RequestRevision)
	require.NoError(t, err)
	for _, role := range []testwriter.Role{testwriter.BuilderRole, testwriter.TestsRole} {
		dir := filepath.Join(project, role.Folder)
		assert.Equal(t, b, productionBuildGit(t, dir, "rev-parse", "HEAD"), "%s at the saved base", role.Folder)
		assert.Equal(t, role.Branch(1), productionBuildGit(t, dir, "symbolic-ref", "--short", "HEAD"))
	}
}

// TestBuildNeverMovesAnExistingCopy verifies that when both copies are on side
// branches with uncommitted work, Build again leaves them and prepares nothing
// in the tests copy.
func TestBuildNeverMovesAnExistingCopy(t *testing.T) {
	_, project, cp, registered := reviewProject(t, Options{})
	card := buildableChange(t, cp, "chg")
	_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{
		ChangeID: "chg", RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	tests := filepath.Join(project, testwriter.TestsRole.Folder)
	require.NoError(t, os.RemoveAll(filepath.Join(tests, "eval", "gen")))
	for _, role := range []testwriter.Role{testwriter.BuilderRole, testwriter.TestsRole} {
		dir := filepath.Join(project, role.Folder)
		runGitForTest(t, dir, "switch", "--quiet", "-c", "mine-"+role.Folder[1:])
		writeWorkspaceFiles(t, dir, map[string]string{"wip.txt": "wip\n"})
	}
	_, err = startBuildAgain(t, cp, asCaller("alice@example.com", CallerUser), "chg", card.RequestRevision)
	require.NoError(t, err)
	for _, role := range []testwriter.Role{testwriter.BuilderRole, testwriter.TestsRole} {
		dir := filepath.Join(project, role.Folder)
		assert.Equal(t, "mine-"+role.Folder[1:], productionBuildGit(t, dir, "symbolic-ref", "--short", "HEAD"))
		assert.FileExists(t, filepath.Join(dir, "wip.txt"))
	}
	assert.NoDirExists(t, filepath.Join(tests, "eval", "gen"), "no Prepare in a tests copy off its build branch")
	assert.Len(t, *registered, 2)
}

// TestMakeCopyFailsAfterTheSave verifies that when MakeCopy fails after the
// build is saved (because a deleted copy's registration is locked), StartBuild
// registers only the role whose copy exists and returns the saved build with
// team.unavailable ("Build this change again on the views page.").
func TestMakeCopyFailsAfterTheSave(t *testing.T) {
	t.Run("builder MakeCopy fails", func(t *testing.T) {
		repo, project, cp, registered := reviewProject(t, Options{})
		card := buildableChange(t, cp, "chg")
		_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{
			ChangeID: "chg", RequestRevision: card.RequestRevision,
		})
		require.NoError(t, err)
		*registered = nil
		builder := filepath.Join(project, testwriter.BuilderRole.Folder)
		runGitForTest(t, repo, "worktree", "lock", builder)
		require.NoError(t, os.RemoveAll(builder))

		result, err := startBuildAgain(t, cp, asCaller("alice@example.com", CallerUser), "chg", card.RequestRevision)
		require.NoError(t, err)
		assert.Equal(t, gensurla.TeamKindUnavailable, result.Team.Kind())
		u, ok := result.Team.AsUnavailable()
		require.True(t, ok)
		assert.Equal(t, gensurla.BuildStartStep("Build this change again on the views page."), u.Step)
		assert.Empty(t, *registered, "nothing is registered when .surla-build does not exist")
	})

	t.Run("tests MakeCopy fails", func(t *testing.T) {
		repo, project, cp, registered := reviewProject(t, Options{})
		card := buildableChange(t, cp, "chg")
		_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{
			ChangeID: "chg", RequestRevision: card.RequestRevision,
		})
		require.NoError(t, err)
		*registered = nil
		builder := filepath.Join(project, testwriter.BuilderRole.Folder)
		tests := filepath.Join(project, testwriter.TestsRole.Folder)
		runGitForTest(t, repo, "worktree", "lock", tests)
		require.NoError(t, os.RemoveAll(tests))

		result, err := startBuildAgain(t, cp, asCaller("alice@example.com", CallerUser), "chg", card.RequestRevision)
		require.NoError(t, err)
		assert.Equal(t, gensurla.TeamKindUnavailable, result.Team.Kind())
		u, ok := result.Team.AsUnavailable()
		require.True(t, ok)
		assert.Equal(t, gensurla.BuildStartStep("Build this change again on the views page."), u.Step)
		assert.Equal(t, []BuildFolders{BuilderOnly{BuilderFolder: builder}}, *registered,
			"only the existing builder copy is registered when .surla-tests is missing")
	})
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

// reviewProject makes a production project and control plane that records
// registrations.
func reviewProject(t *testing.T, opts Options) (string, string, *ControlPlane, *[]BuildFolders) {
	t.Helper()
	repo, project := productionBuildProject(t, ".", reviewEval)
	f := &teamServiceFixture{}
	var registered []BuildFolders
	f.register = func(_ context.Context, folders BuildFolders) error {
		registered = append(registered, folders)
		return nil
	}
	opts.WorkspaceDir = project
	opts.Team = &TeamBinding{Owner: "alice@example.com", Service: f}
	cp := newControlPlane(t, opts)
	return repo, project, cp, &registered
}
