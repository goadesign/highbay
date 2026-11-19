package controlplane

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/testwriter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goa "goa.design/goa/v3/pkg"
)

type (
	// mergeLifecycleTeam observes the user operation after the Git or card
	// store effect. It does not supervise or launch any external process.
	mergeLifecycleTeam struct {
		*teamServiceFixture
		stopped []BuildIdentity
		onStop  func(context.Context) error
	}

	// mergeFixture binds real Git history to an explicitly saved approval and
	// open build. The transaction runs no build, model, or candidate program.
	mergeFixture struct {
		cp                  *ControlPlane
		team                *mergeLifecycleTeam
		ctx                 context.Context
		payload             gensurla.MergeChangePayload
		build               buildFile
		repo, branch, suite string
	}
)

// StopWorkers records every cleanup request and observes the completed action.
func (t *mergeLifecycleTeam) StopWorkers(ctx context.Context, build BuildIdentity) error {
	t.stopped = append(t.stopped, build)
	if t.onStop != nil {
		return t.onStop(ctx)
	}
	return nil
}

func TestMergeChangeAppliesApprovedCommits(t *testing.T) {
	for _, source := range []string{"evaluator", "written"} {
		t.Run(source, func(t *testing.T) {
			f := newMergeFixture(t, source, "")
			f.branch = "merge=approved"
			productionBuildGit(t, f.repo, "branch", "-m", f.branch)
			f.write(t, ".mcp.json", "user configuration\n")
			f.write(t, ".git/info/exclude", ".claude/\n")
			require.NoError(t, os.Mkdir(filepath.Join(f.repo, ".claude"), 0o700))
			f.write(t, ".claude/settings.json", "ignored user configuration\n")
			f.team.onStop = func(context.Context) error {
				// Reenter the read boundary to ensure the caller released c.mu.
				_, err := f.cp.GetBuild(f.ctx, &gensurla.GetBuildPayload{ChangeID: f.payload.ChangeID})
				require.NoError(t, err)
				assert.Equal(t, f.payload.CandidateCommit, productionBuildGit(t, f.repo, "merge-base", "HEAD", f.payload.CandidateCommit))
				if source == "written" {
					assert.Equal(t, f.suite, productionBuildGit(t, f.repo, "merge-base", "HEAD", f.suite))
				}
				return errors.New("worker teardown failed")
			}
			result, err := f.cp.MergeChange(f.ctx, &f.payload)
			require.NoError(t, err, "a teardown failure cannot undo the completed merge")
			assert.Equal(t, f.branch, result.Branch)
			assert.Equal(t, productionBuildGit(t, f.repo, "rev-parse", "HEAD"), result.HeadCommit)
			assert.Equal(t, []BuildIdentity{f.identity()}, f.team.stopped)
			assert.FileExists(t, filepath.Join(f.repo, "candidate.txt"))
			if source == "written" {
				assert.FileExists(t, filepath.Join(f.repo, "tests.txt"))
			} else {
				assert.Equal(t, f.payload.CandidateCommit, result.HeadCommit, "the evaluator is external")
			}
			assert.Equal(t, "?? .mcp.json", productionBuildGit(t, f.repo, "status", "--porcelain"))
			ignored, err := os.ReadFile(filepath.Join(f.repo, ".claude", "settings.json"))
			require.NoError(t, err)
			assert.Equal(t, "ignored user configuration\n", string(ignored))
			f.write(t, "base.txt", "an edit after the successful merge\n")
			second, err := f.cp.MergeChange(f.ctx, &f.payload)
			require.NoError(t, err)
			assert.Equal(t, result, second)
			assert.Equal(t, []BuildIdentity{f.identity(), f.identity()}, f.team.stopped)
			assert.Contains(t, productionBuildGit(t, f.repo, "status", "--porcelain"), "M base.txt")
		})
	}
}

func TestMergeChangeRollsBackSecondGitFailure(t *testing.T) {
	for _, tc := range []struct {
		name, message, untracked string
	}{
		{name: "conflict", message: "base.txt"},
		{name: "untracked collision", message: "would be overwritten", untracked: "tests.txt"},
		{name: "ignored collision", message: "would be overwritten", untracked: "tests.txt"},
		{name: "ignored ancestor collision", message: "would be overwritten", untracked: "blocked"},
		{name: "ignored directory collision", message: "would be overwritten", untracked: "tests.txt/private"},
		{name: "commit hook", message: "fixture commit hook refused"},
		{name: "commit signing", message: "failed to write commit object"},
		{name: "rollback failure", message: "could not confirm restoration"},
		{name: "reset leaves a tracked edit", message: "could not confirm restoration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMergeFixture(t, "written", tc.name)
			f.write(t, ".mcp.json", "keep this untracked\n")
			if tc.untracked != "" {
				require.NoError(t, os.MkdirAll(filepath.Join(f.repo, filepath.Dir(tc.untracked)), 0o700))
				f.write(t, tc.untracked, "untracked work at the suite path\n")
			}
			switch tc.name {
			case "commit signing":
				productionBuildGit(t, f.repo, "config", "commit.gpgsign", "true")
				productionBuildGit(t, f.repo, "config", "gpg.format", "openpgp")
				productionBuildGit(t, f.repo, "config", "gpg.program", filepath.Join(t.TempDir(), "missing-signer"))
			case "commit hook", "rollback failure", "reset leaves a tracked edit":
				// The candidate fast-forwards. The test commit needs a merge
				// commit, which is when Git calls this local hook.
				require.NoError(t, os.MkdirAll(filepath.Join(f.repo, ".git", "hooks"), 0o700))
				productionBuildGit(t, f.repo, "config", "--unset", "core.hooksPath")
				script := "#!/bin/sh\n"
				if tc.name == "commit hook" {
					t.Setenv("OPENAI_API_KEY", "fixture-provider-value")
					script += "if [ -n \"${OPENAI_API_KEY:-}\" ]; then echo 'fixture hook received provider environment' >&2; fi\n"
				}
				if tc.name == "rollback failure" {
					script += "touch .git/index.lock\n"
				}
				if tc.name == "reset leaves a tracked edit" {
					script += "echo 'hook edit' > base.txt\n"
				}
				script += "echo 'fixture commit hook refused' >&2\nexit 1\n"
				require.NoError(t, os.WriteFile(filepath.Join(f.repo, ".git", "hooks", "commit-msg"), []byte(script), 0o700))
			}
			before := productionBuildGit(t, f.repo, "status", "--porcelain", "--ignored")
			result, err := f.cp.MergeChange(f.ctx, &f.payload)
			requireMergeRefusal(t, err, "merge_conflict", tc.message)
			assert.Nil(t, result)
			assert.Empty(t, f.team.stopped)
			if tc.name == "rollback failure" {
				assert.Contains(t, err.Error(), "fixture commit hook refused")
				assert.Contains(t, err.Error(), "index.lock")
				assert.Equal(t, f.payload.CandidateCommit, productionBuildGit(t, f.repo, "rev-parse", "HEAD"))
				return
			}
			if tc.name == "reset leaves a tracked edit" {
				assert.Contains(t, err.Error(), "fixture commit hook refused")
				assert.Contains(t, err.Error(), "base.txt")
				assert.Equal(t, f.build.Base, productionBuildGit(t, f.repo, "rev-parse", "HEAD"))
				return
			}
			if tc.name == "commit hook" {
				assert.NotContains(t, err.Error(), "fixture hook received provider environment")
			}
			assert.Contains(t, err.Error(), "restored the branch")
			assert.Equal(t, f.build.Base, productionBuildGit(t, f.repo, "rev-parse", "HEAD"))
			assert.Equal(t, before, productionBuildGit(t, f.repo, "status", "--porcelain", "--ignored"))
			assert.NoFileExists(t, filepath.Join(f.repo, ".git", "MERGE_HEAD"))
			assert.NoFileExists(t, filepath.Join(f.repo, "candidate.txt"))
			if tc.untracked != "" {
				raw, readErr := os.ReadFile(filepath.Join(f.repo, tc.untracked))
				require.NoError(t, readErr)
				assert.Equal(t, "untracked work at the suite path\n", string(raw))
			}
			raw, readErr := os.ReadFile(filepath.Join(f.repo, "base.txt"))
			require.NoError(t, readErr)
			assert.Equal(t, "base contents\n", string(raw))
		})
	}
}

func TestMergeChangeRefusesUnsafeOrStaleInputs(t *testing.T) {
	for _, tc := range []struct {
		name, refusal, message string
		change                 func(*testing.T, *mergeFixture)
	}{
		{name: "unknown change", refusal: "not_found", change: func(_ *testing.T, f *mergeFixture) {
			f.payload.ChangeID = "unknown"
		}},
		{name: "revision", refusal: "not_approved", change: func(_ *testing.T, f *mergeFixture) {
			f.payload.RequestRevision = "earlier revision"
		}},
		{name: "candidate", refusal: "not_approved", change: func(_ *testing.T, f *mergeFixture) {
			f.payload.CandidateCommit = f.build.Base
		}},
		{name: "digest", refusal: "not_approved", change: func(_ *testing.T, f *mergeFixture) {
			f.payload.ReportDigest = "earlier digest"
		}},
		{name: "detached", refusal: "detached_head", change: func(t *testing.T, f *mergeFixture) {
			productionBuildGit(t, f.repo, "switch", "--detach")
		}},
		{name: "dirty tracked", refusal: "dirty_worktree", message: "base.txt", change: func(t *testing.T, f *mergeFixture) {
			f.write(t, "base.txt", "user edit\n")
		}},
		{name: "dirty index", refusal: "dirty_worktree", message: "staged.txt", change: func(t *testing.T, f *mergeFixture) {
			f.write(t, "staged.txt", "user edit\n")
			productionBuildGit(t, f.repo, "add", "staged.txt")
		}},
		{name: "active Git operation", refusal: "working_copy_busy", change: func(t *testing.T, f *mergeFixture) {
			f.write(t, filepath.Join(".git", "MERGE_HEAD"), f.payload.CandidateCommit+"\n")
		}},
		{name: "builder copy busy", refusal: "working_copy_busy", change: func(t *testing.T, f *mergeFixture) {
			f.cp.builderCopyTurn <- struct{}{}
			t.Cleanup(func() { <-f.cp.builderCopyTurn })
		}},
		{name: "test copy busy", refusal: "working_copy_busy", change: func(t *testing.T, f *mergeFixture) {
			f.cp.workingCopyTurn <- struct{}{}
			t.Cleanup(func() { <-f.cp.workingCopyTurn })
		}},
		{name: "workspace busy", refusal: "working_copy_busy", change: func(t *testing.T, f *mergeFixture) {
			f.cp.workspaceMu.Lock()
			t.Cleanup(f.cp.workspaceMu.Unlock)
		}},
		{name: "worktree hooks", refusal: "merge_conflict", message: "tracked working tree", change: func(t *testing.T, f *mergeFixture) {
			productionBuildGit(t, f.repo, "config", "core.hooksPath", ".githooks")
		}},
		{name: "hook symlink into worktree", refusal: "merge_conflict", message: "tracked working tree", change: func(t *testing.T, f *mergeFixture) {
			hooks := t.TempDir()
			productionBuildGit(t, f.repo, "config", "core.hooksPath", hooks)
			require.NoError(t, os.Symlink(filepath.Join(f.repo, "candidate.txt"), filepath.Join(hooks, "post-merge")))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMergeFixture(t, "evaluator", "")
			tc.change(t, f)
			before := productionBuildGit(t, f.repo, "status", "--porcelain")
			result, err := f.cp.MergeChange(f.ctx, &f.payload)
			requireMergeRefusal(t, err, tc.refusal, tc.message)
			assert.Nil(t, result)
			assert.Equal(t, f.build.Base, productionBuildGit(t, f.repo, "rev-parse", "HEAD"))
			assert.Equal(t, before, productionBuildGit(t, f.repo, "status", "--porcelain"))
			assert.Empty(t, f.team.stopped)
		})
	}
}

func TestMergeChangeChecksLiveApprovalAndPublication(t *testing.T) {
	for _, name := range []string{"tests changed", "binaries changed", "promotion unavailable", "approval changed during lookup", "drop during lookup"} {
		t.Run(name, func(t *testing.T) {
			f := newMergeFixture(t, "written", "")
			target := &promotionTargetStub{artifacts: map[string]string{}}
			f.cp.resolvePromotionTarget = func(_ context.Context, workspace string) (PromotionTarget, error) {
				assert.Equal(t, f.repo, workspace)
				switch name {
				case "approval changed during lookup":
					record := loadMetadataRecord(t, f.cp, f.payload.ChangeID)
					record.card.Approval.ReportDigest = strings.Repeat("0", 64)
					saveMetadataRecord(t, f.cp, record)
				case "drop during lookup":
					_, err := f.cp.DropBuild(f.ctx, f.dropPayload())
					require.NoError(t, err)
				}
				return target, nil
			}
			switch name {
			case "tests changed":
				saveListedTestState(t, f.cp, f.payload.ChangeID, testsAccepted, f.build.Base)
			case "binaries changed":
				target.artifacts["candidate"] = "a different binary"
			case "promotion unavailable":
				target.artifactsErr = errors.New("release is unavailable")
			}
			_, err := f.cp.MergeChange(f.ctx, &f.payload)
			requireMergeRefusal(t, err, "not_approved", "current approval")
			assert.Equal(t, f.build.Base, productionBuildGit(t, f.repo, "rev-parse", "HEAD"))
			if name == "drop during lookup" {
				assert.Equal(t, []BuildIdentity{f.identity()}, f.team.stopped)
			} else {
				assert.Empty(t, f.team.stopped)
			}
		})
	}
}

func TestMergeChangeRefusesGitAttributeCommandsBeforeReadingTheWorktree(t *testing.T) {
	for _, tc := range []struct {
		name, detail string
	}{
		{name: "info attributes", detail: "local info/attributes"},
		{name: "ignored live attributes", detail: "nested/.gitattributes"},
		{name: "candidate attributes", detail: "nested/.gitattributes"},
		{name: "suite attributes", detail: "nested/.gitattributes"},
		{name: "global attributes", detail: "global Git attributes file"},
		{name: "default merge driver", detail: "merge.fixture.driver"},
		{name: "command hook", detail: "hook.fixture.command"},
		{name: "worktree config include", detail: "configuration from the project working tree"},
		{name: "worktree global config", detail: "configuration from the project working tree"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMergeFixture(t, "written", "conflict")
			const marker = "printf invoked > .git/fixture-command-ran"
			const attrs = "* filter=fixture merge=fixture\n"
			switch tc.name {
			case "info attributes":
				f.write(t, ".git/info/attributes", attrs)
			case "ignored live attributes":
				f.write(t, ".git/info/exclude", "nested/\n")
				require.NoError(t, os.Mkdir(filepath.Join(f.repo, "nested"), 0o700))
				f.write(t, "nested/.gitattributes", attrs)
			case "candidate attributes", "suite attributes":
				addMergeFixtureAttributes(t, f, tc.name == "suite attributes", attrs)
			case "global attributes":
				path := filepath.Join(t.TempDir(), "attributes")
				require.NoError(t, os.WriteFile(path, []byte(attrs), 0o600))
				productionBuildGit(t, f.repo, "config", "core.attributesFile", path)
			case "default merge driver":
				productionBuildGit(t, f.repo, "config", "merge.default", "fixture")
			case "command hook":
				productionBuildGit(t, f.repo, "config", "hook.fixture.command", marker)
				productionBuildGit(t, f.repo, "config", "hook.fixture.event", "post-merge")
			case "worktree config include":
				productionBuildGit(t, f.repo, "config", "include.path", "../future-worktree-config")
			case "worktree global config":
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.repo, "future-config"))
			}
			before := productionBuildGit(t, f.repo, "ls-files", "--stage")
			productionBuildGit(t, f.repo, "config", "filter.fixture.clean", marker+" && cat")
			productionBuildGit(t, f.repo, "config", "merge.fixture.driver", marker)
			// A changed timestamp makes Git status check the file's content,
			// which could invoke a clean filter even when the bytes still match.
			stamp := time.Unix(1, 0)
			require.NoError(t, os.Chtimes(filepath.Join(f.repo, "base.txt"), stamp, stamp))

			result, err := f.cp.MergeChange(f.ctx, &f.payload)
			requireMergeRefusal(t, err, "merge_conflict", tc.detail)
			assert.Contains(t, err.Error(), "merge the approved commits manually")
			assert.NotContains(t, err.Error(), marker)
			assert.Nil(t, result)
			assert.Empty(t, f.team.stopped)
			assert.NoFileExists(t, filepath.Join(f.repo, ".git", "fixture-command-ran"))
			assert.NoFileExists(t, filepath.Join(f.repo, ".git", "MERGE_HEAD"))
			assert.NoFileExists(t, filepath.Join(f.repo, "candidate.txt"))
			assert.Equal(t, f.build.Base, productionBuildGit(t, f.repo, "rev-parse", "HEAD"))
			assert.Equal(t, before, productionBuildGit(t, f.repo, "ls-files", "--stage"))
			base, err := os.ReadFile(filepath.Join(f.repo, "base.txt"))
			require.NoError(t, err)
			assert.Equal(t, "base contents\n", string(base))
		})
	}
}

func TestMergeChangeAllowsUnusedGitHelpersInAnExternalConfig(t *testing.T) {
	f := newMergeFixture(t, "written", "")
	config := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(config, []byte("[filter \"lfs\"]\nprocess = printf invoked > .git/fixture-command-ran\n"), 0o600))
	productionBuildGit(t, f.repo, "config", "include.path", config)
	productionBuildGit(t, f.repo, "config", "merge.fixture.driver", "printf invoked > .git/fixture-command-ran")
	result, err := f.cp.MergeChange(f.ctx, &f.payload)
	require.NoError(t, err)
	assert.Equal(t, f.branch, result.Branch)
	assert.Equal(t, []BuildIdentity{f.identity()}, f.team.stopped)
	assert.NoFileExists(t, filepath.Join(f.repo, ".git", "fixture-command-ran"))
}

// addMergeFixtureAttributes places rules in only one approved input tree.
func addMergeFixtureAttributes(t *testing.T, f *mergeFixture, suite bool, attributes string) {
	t.Helper()
	role := testwriter.BuilderRole
	if suite {
		role = testwriter.TestsRole
	}
	productionBuildGit(t, f.repo, "switch", role.Branch(f.build.Number))
	require.NoError(t, os.Mkdir(filepath.Join(f.repo, "nested"), 0o700))
	f.write(t, "nested/.gitattributes", attributes)
	productionBuildGit(t, f.repo, "add", "nested/.gitattributes")
	productionBuildGit(t, f.repo, "commit", "-m", "Add fixture attributes")
	commit := productionBuildGit(t, f.repo, "rev-parse", "HEAD")
	productionBuildGit(t, f.repo, "switch", f.branch)
	record := loadMetadataRecord(t, f.cp, f.payload.ChangeID)
	if suite {
		f.build.TestWriter.Commit = commit
		record.card.Approval.SuiteCommit = commit
		f.cp.mu.Lock()
		err := f.cp.cards.saveOpenBuild(f.build)
		f.cp.mu.Unlock()
		require.NoError(t, err)
	} else {
		f.payload.CandidateCommit = commit
		record.card.Approval.CandidateCommit = commit
	}
	saveMetadataRecord(t, f.cp, record)
}

func TestDropBuildStopsWorkersOnlyAfterSave(t *testing.T) {
	f := newMergeFixture(t, "evaluator", "")
	f.team.onStop = func(context.Context) error {
		build, err := f.cp.GetBuild(f.ctx, &gensurla.GetBuildPayload{ChangeID: f.payload.ChangeID})
		require.NoError(t, err)
		started, ok := build.Build.AsStarted()
		require.True(t, ok)
		assert.Equal(t, gensurla.BuildTakingKindDropped, started.Taking.Kind())
		return errors.New("worker teardown failed")
	}
	_, err := f.cp.GetBuild(f.ctx, &gensurla.GetBuildPayload{ChangeID: f.payload.ChangeID})
	require.NoError(t, err)
	assert.Empty(t, f.team.stopped)
	staging := filepath.Join(f.cp.cards.dir, madeFromStagingFile)
	require.NoError(t, os.Mkdir(staging, 0o700))
	_, err = f.cp.DropBuild(f.ctx, f.dropPayload())
	assert.ErrorContains(t, err, "save dropped build")
	assert.Empty(t, f.team.stopped)
	require.NoError(t, os.Remove(staging))
	_, err = f.cp.DropBuild(f.ctx, f.dropPayload())
	require.NoError(t, err, "a teardown failure cannot undo the durable drop")
	assert.Equal(t, []BuildIdentity{f.identity()}, f.team.stopped)
	_, err = f.cp.GetBuild(f.ctx, &gensurla.GetBuildPayload{ChangeID: f.payload.ChangeID})
	require.NoError(t, err)
	_, err = f.cp.MergeChange(f.ctx, &f.payload)
	requireMergeRefusal(t, err, "not_approved", "open build")
	assert.Equal(t, []BuildIdentity{f.identity()}, f.team.stopped)
	assert.Equal(t, f.build.Base, productionBuildGit(t, f.repo, "rev-parse", "HEAD"))
}

// dropPayload names the open start the user saw.
func (f *mergeFixture) dropPayload() *gensurla.DropBuildPayload {
	return &gensurla.DropBuildPayload{
		ChangeID: f.payload.ChangeID, StartedAt: f.build.StartedAt.Format(time.RFC3339Nano),
	}
}

// newMergeFixture creates the project's divergent approved commits and saves
// the exact build and approval that a consumer of Approve would send back.
func newMergeFixture(t *testing.T, source, failure string) *mergeFixture {
	t.Helper()
	team := &mergeLifecycleTeam{teamServiceFixture: &teamServiceFixture{}}
	opts := Options{Team: &TeamBinding{Owner: "alice@example.com", Service: team}}
	roles := buildRolesBuilderOnly
	if source == "written" {
		roles = buildRolesBuilderAndTestWriter
		opts.RunsDir = t.TempDir()
		opts.WrittenTestRuns = &WrittenTestRuns{ReportDir: t.TempDir(), Timeout: time.Minute}
	} else {
		opts.Verification = verificationConfig(t)
	}
	cp := newControlPlane(t, buildFixtureOptions(t, opts))
	f := &mergeFixture{cp: cp, team: team, repo: cp.activeWorkspaceDir, ctx: asCaller("alice@example.com", CallerUser)}
	f.branch = productionBuildGit(t, f.repo, "branch", "--show-current")
	f.write(t, "base.txt", "base contents\n")
	if strings.HasPrefix(failure, "ignored ") {
		f.write(t, ".gitignore", "tests.txt\nblocked\n")
		productionBuildGit(t, f.repo, "add", ".gitignore")
	}
	productionBuildGit(t, f.repo, "add", "base.txt")
	productionBuildGit(t, f.repo, "commit", "-m", "Record merge base")
	saveCard(t, cp, nil)
	card := attachBuildSnapshot(t, cp, "chg")
	f.build = fixtureOpenBuild(t, cp, card, roles)
	productionBuildGit(t, f.repo, "switch", "-c", testwriter.BuilderRole.Branch(f.build.Number))
	f.write(t, "candidate.txt", "approved change\n")
	if failure == "conflict" {
		f.write(t, "base.txt", "builder content\n")
	}
	productionBuildGit(t, f.repo, "add", "candidate.txt", "base.txt")
	productionBuildGit(t, f.repo, "commit", "-m", "Build candidate")
	f.payload = gensurla.MergeChangePayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
		CandidateCommit: productionBuildGit(t, f.repo, "rev-parse", "HEAD"), ReportDigest: strings.Repeat("a", 64),
	}
	if source == "written" {
		productionBuildGit(t, f.repo, "switch", "-c", testwriter.TestsRole.Branch(f.build.Number), f.build.Base)
		testsPath := "tests.txt"
		if failure == "ignored ancestor collision" {
			require.NoError(t, os.Mkdir(filepath.Join(f.repo, "blocked"), 0o700))
			testsPath = "blocked/tests.txt"
		}
		f.write(t, testsPath, "accepted tests\n")
		if failure == "conflict" {
			f.write(t, "base.txt", "test writer content\n")
		}
		productionBuildGit(t, f.repo, "add", "-f", testsPath, "base.txt")
		productionBuildGit(t, f.repo, "commit", "-m", "Accept written tests")
		f.suite = productionBuildGit(t, f.repo, "rev-parse", "HEAD")
		f.build.TestWriter = testWriterAssignment{State: testsAccepted, Commit: f.suite, CheckedAt: time.Now().UTC()}
	} else {
		f.suite = opts.Verification.Plan.SuiteCommit
	}
	productionBuildGit(t, f.repo, "switch", f.branch)
	record := loadMetadataRecord(t, cp, card.ChangeID)
	record.card.Approval = &gensurla.ApprovalItem{
		ApprovedBy: opts.Team.Owner, CandidateCommit: f.payload.CandidateCommit, ReportDigest: f.payload.ReportDigest,
		SuiteCommit: f.suite, SuiteID: "fixture", Environment: "sim", ScenarioCount: 1, Artifacts: map[string]string{},
	}
	saveMetadataRecord(t, cp, record)
	cp.mu.Lock()
	err := cp.cards.saveOpenBuild(f.build)
	cp.mu.Unlock()
	require.NoError(t, err)
	return f
}

// write creates a plain fixture file under its temporary project.
func (f *mergeFixture) write(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.repo, path), []byte(content), 0o600))
}

// identity names the exact start to which worker teardown must apply.
func (f *mergeFixture) identity() BuildIdentity {
	return BuildIdentity{ChangeID: f.build.ChangeID, StartedAt: f.build.StartedAt}
}

// requireMergeRefusal checks the user-facing service error and any expected detail.
func requireMergeRefusal(t *testing.T, err error, name, detail string) {
	t.Helper()
	var refusal *goa.ServiceError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, name, refusal.Name)
	assert.Contains(t, refusal.Message, detail)
}
