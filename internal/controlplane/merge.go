// This file merges the exact approved result into the user's current branch.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/abspath"
	"github.com/goadesign/surla/internal/procgroup"
	"github.com/goadesign/surla/internal/verify"
	"github.com/goadesign/surla/internal/verifyrun"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	goa "goa.design/goa/v3/pkg"
)

const (
	// mergeWriteTimeout bounds the two Git writes while the build and approval
	// are reserved. Rollback gets its own time budget if that limit expires.
	mergeWriteTimeout = 30 * time.Second
	// workerStopTimeout bounds lifecycle cleanup after the durable user action.
	workerStopTimeout = 45 * time.Second
)

// errMergeGitProcessActive means a Git hook may continue to use the working
// tree after the Git command exits. In that case rollback is unconfirmed.
var errMergeGitProcessActive = errors.New("Git still has running child processes")

// MergeChange checks the exact live approval for the open build and merges its
// candidate and any written tests. Both commits must reach the user's branch;
// a failed write rolls back to the original HEAD before the copy turns release.
// A branch that already contains the result needs only worker cleanup.
func (c *ControlPlane) MergeChange(
	ctx context.Context, p *gensurla.MergeChangePayload,
) (*gensurla.MergeChangeResult, error) {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return nil, err
	}
	principal, _ := PrincipalFromContext(ctx) // ownedTeam established the owner.
	result, build, err := c.mergeApproved(ctx, principal, p)
	if err != nil {
		return nil, err
	}
	c.stopFinishedBuildWorkers(ctx, team, build)
	return result, nil
}

// mergeApproved holds the workspace lifetime and both copy turns for the Git
// transaction. The team calls back into the control plane to read assignments,
// so cleanup must run after this returns and releases those locks.
func (c *ControlPlane) mergeApproved(
	ctx context.Context, principal Principal, p *gensurla.MergeChangePayload,
) (*gensurla.MergeChangeResult, BuildIdentity, error) {
	busy := gensurla.MakeWorkingCopyBusy(errors.New("Surla is using the project or its build working copies. Try merging when that operation finishes."))
	if !c.workspaceMu.TryRLock() {
		return nil, BuildIdentity{}, busy
	}
	defer c.workspaceMu.RUnlock()
	if c.serverCtx.Err() != nil {
		return nil, BuildIdentity{}, busy
	}
	select {
	case c.builderCopyTurn <- struct{}{}:
	default:
		return nil, BuildIdentity{}, busy
	}
	defer func() { <-c.builderCopyTurn }()
	select {
	case c.workingCopyTurn <- struct{}{}:
	default:
		return nil, BuildIdentity{}, busy
	}
	defer func() { <-c.workingCopyTurn }()

	work, cancel := context.WithTimeout(ctx, layoutReadTimeout)
	defer cancel()
	c.mu.RLock()
	held, err := c.mergeBuild(principal, p)
	c.mu.RUnlock()
	if err != nil {
		return nil, BuildIdentity{}, err
	}
	workspace := c.activeWorkspaceDir
	card, err := c.currentCardAt(work, held.record, workspace)
	if err != nil {
		return nil, BuildIdentity{}, err
	}
	if card.Approval == nil || card.UnverifiedApproval != nil {
		return nil, BuildIdentity{}, mergeNotApproved()
	}
	_, root, err := openModuleRootWithIdentity(workspace, c.boundWorkspaceDev, c.boundWorkspaceIno)
	if err != nil {
		return nil, BuildIdentity{}, fmt.Errorf("open the project's bound workspace for merge: %w", err)
	}
	if err := root.Close(); err != nil {
		return nil, BuildIdentity{}, fmt.Errorf("close the project's bound workspace: %w", err)
	}
	repo, _, err := resolveStarterRepo(work, workspace)
	if err != nil {
		return nil, BuildIdentity{}, err
	}

	// A drop, confirmation, or approval can publish while reading the live
	// promotion target. Recheck the exact build, approval and test selection
	// and keep publication excluded until the Git transaction ends.
	c.mu.Lock()
	defer c.mu.Unlock()
	now, err := c.mergeBuild(principal, p)
	if err != nil {
		return nil, BuildIdentity{}, err
	}
	selection, err := c.testsChoice(principal, p.ChangeID, p.RequestRevision)
	_, writtenTests := card.Approval.TestSource.AsWrittenTests()
	if err != nil || !now.file.StartedAt.Equal(held.file.StartedAt) ||
		!reflect.DeepEqual(now.record.card.Approval, held.record.card.Approval) ||
		selection.commit != card.Approval.SuiteCommit ||
		(selection.source == verify.TestsWritten) != writtenTests {
		return nil, BuildIdentity{}, mergeNotApproved()
	}
	result, err := c.mergeIntoCurrentBranch(work, repo, verify.Plan{
		CandidateCommit: card.Approval.CandidateCommit,
		SuiteCommit:     selection.commit,
		Tests:           selection.source,
	})
	if err != nil {
		return nil, BuildIdentity{}, err
	}
	return result, BuildIdentity{ChangeID: now.file.ChangeID, StartedAt: now.file.StartedAt}, nil
}

// mergeBuild reads the undropped current build and its saved approval. The
// caller holds c.mu and still has to verify the approval's live inputs.
func (c *ControlPlane) mergeBuild(principal Principal, p *gensurla.MergeChangePayload) (heldBuild, error) {
	held, err := c.workerOwnedBuild(principal, p.ChangeID)
	var refusal *goa.ServiceError
	if errors.As(err, &refusal) {
		switch refusal.Name {
		case "not_found":
			// The owner uses not_found for an unknown card and for an existing
			// card with no open build. Preserve the resource lookup error.
			if _, cardErr := c.ownedCard(principal, p.ChangeID); cardErr != nil {
				return heldBuild{}, cardErr
			}
			return heldBuild{}, mergeNotApproved()
		case "no_build":
			return heldBuild{}, mergeNotApproved()
		}
	}
	if err != nil {
		return heldBuild{}, err
	}
	saved := held.record.card.Approval
	if !held.inForce || held.file.RequestRevision != p.RequestRevision || saved == nil ||
		saved.CandidateCommit != p.CandidateCommit || saved.ReportDigest != p.ReportDigest {
		return heldBuild{}, mergeNotApproved()
	}
	return held, nil
}

// mergeNotApproved gives every stale approval or build the same recovery.
func mergeNotApproved() error {
	return gensurla.MakeNotApproved(errors.New("This result has no current approval for the open build. Review the current result and approve it before merging."))
}

// mergeIntoCurrentBranch completes the Git transaction while the approved
// inputs are held. An already-taken result needs a branch and head to return,
// and does not need a clean worktree because the call makes no Git writes.
func (c *ControlPlane) mergeIntoCurrentBranch(
	ctx context.Context, repo string, plan verify.Plan,
) (*gensurla.MergeChangeResult, error) {
	refOut, err := runGitRead(ctx, repo, "symbolic-ref", "-q", "HEAD")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil, gensurla.MakeDetachedHead(errors.New("The project has a detached HEAD. Check out the branch that should receive the approved result."))
	}
	if err != nil {
		return nil, err
	}
	branch, local := strings.CutPrefix(strings.TrimSuffix(string(refOut), "\n"), "refs/heads/")
	if !local || branch == "" {
		return nil, gensurla.MakeDetachedHead(errors.New("HEAD must point to a local branch before merging."))
	}
	headOut, err := runGitRead(ctx, repo, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil, err
	}
	origHEAD := strings.TrimSpace(string(headOut))
	taken, err := runTaken(ctx, repo, origHEAD, verifyrun.Run{Request: verifyrun.Request{Plan: plan}})
	if err != nil {
		return nil, err
	}
	if taken {
		return &gensurla.MergeChangeResult{Branch: branch, HeadCommit: origHEAD}, nil
	}
	if err := checkMergeWorktree(ctx, repo); err != nil {
		return nil, err
	}
	hooks, err := mergeHooksPath(ctx, repo)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Once Git can change the project, a disconnected browser cannot interrupt
	// the transaction. Both commands and rollback remain bounded.
	ctx, span := c.tracer("surla/controlplane").Start(ctx, "surla.controlplane.merge_change")
	defer span.End()
	write, cancel := context.WithTimeout(context.WithoutCancel(ctx), mergeWriteTimeout)
	defer cancel()
	commits := []string{plan.CandidateCommit}
	if plan.Tests == verify.TestsWritten {
		commits = append(commits, plan.SuiteCommit)
	}
	for _, commit := range commits {
		if err := runMergeGit(write, repo, hooks, "merge", "--no-edit", "--no-autostash", "--no-overwrite-ignore",
			"--commit", "--no-squash", "--", commit); err != nil {
			return nil, rollbackMerge(ctx, span, repo, hooks, origHEAD, err)
		}
	}
	headOut, err = runGitRead(write, repo, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil, rollbackMerge(ctx, span, repo, hooks, origHEAD, err)
	}
	return &gensurla.MergeChangeResult{Branch: branch, HeadCommit: strings.TrimSpace(string(headOut))}, nil
}

// checkMergeWorktree names modified and staged tracked paths and leaves an
// existing Git operation alone even when it has not written tracked changes.
func checkMergeWorktree(ctx context.Context, repo string) error {
	status, err := runGitRead(ctx, repo, "status", "--porcelain", "--untracked-files=no", "--ignore-submodules=untracked")
	if err != nil {
		return err
	}
	if len(status) != 0 {
		return gensurla.MakeDirtyWorktree(fmt.Errorf("Commit or stash these tracked changes before merging:\n%s", strings.TrimRight(string(status), "\n")))
	}
	paths, err := runGitRead(ctx, repo, "rev-parse", "--path-format=absolute",
		"--git-path", "MERGE_HEAD", "--git-path", "CHERRY_PICK_HEAD", "--git-path", "REVERT_HEAD",
		"--git-path", "sequencer", "--git-path", "rebase-merge", "--git-path", "rebase-apply")
	if err != nil {
		return err
	}
	for _, path := range strings.Split(strings.TrimSuffix(string(paths), "\n"), "\n") {
		if _, err := os.Lstat(path); err == nil {
			return gensurla.MakeWorkingCopyBusy(errors.New("Finish or abort the Git operation already in progress before merging."))
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// mergeHooksPath permits hooks owned by the local Git installation and refuses
// paths the merge itself could replace with worker-authored hook code.
func mergeHooksPath(ctx context.Context, repo string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "--no-optional-locks", "rev-parse",
		"--path-format=absolute", "--git-common-dir", "--git-path", "hooks")
	cmd.Env = cleanGitEnv()
	cmd.WaitDelay = time.Second
	stdout := &cappedBuffer{limit: gitReadMaxStderr}
	cmd.Stdout = stdout
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("resolve the project's Git hooks: %w", err)
	}
	commonDir, hooks, ok := strings.Cut(strings.TrimSuffix(stdout.buf.String(), "\n"), "\n")
	if !ok || !filepath.IsAbs(commonDir) || !filepath.IsAbs(hooks) || strings.Contains(hooks, "\n") {
		return "", errors.New("Git did not return absolute paths for its common directory and hooks")
	}
	if err := checkMergeHookPath(repo, commonDir, hooks); err != nil {
		return "", err
	}
	resolved, err := abspath.EvalMissing(hooks)
	if err != nil {
		return "", err
	}
	// A locally installed hook may itself be a symlink into the worktree.
	for _, name := range []string{"pre-merge-commit", "pre-commit", "prepare-commit-msg", "commit-msg",
		"post-merge", "reference-transaction", "post-index-change"} {
		if err := checkMergeHookPath(repo, commonDir, filepath.Join(resolved, name)); err != nil {
			// Git's standard disabled hook path is /dev/null, a regular file.
			if !errors.Is(err, syscall.ENOTDIR) {
				return "", err
			}
		}
	}
	return resolved, nil
}

// checkMergeHookPath distinguishes Git's administrative directory from paths
// its worktree checkout owns, checking both the path and any symlink target.
func checkMergeHookPath(repo, commonDir, path string) error {
	parent, err := filepath.Rel(repo, path)
	if err != nil {
		return err
	}
	insideWorktree := parent != ".." && !strings.HasPrefix(parent, ".."+string(filepath.Separator))
	adminParent, err := filepath.Rel(commonDir, path)
	if err != nil {
		return err
	}
	insideAdmin := adminParent != ".." && !strings.HasPrefix(adminParent, ".."+string(filepath.Separator))
	pointsIntoWorktree, err := abspath.Within(repo, path)
	if err != nil {
		return err
	}
	pointsIntoAdmin, err := abspath.Within(commonDir, path)
	if err != nil {
		return err
	}
	if insideWorktree && !insideAdmin || pointsIntoWorktree && !pointsIntoAdmin {
		return gensurla.MakeMergeConflict(errors.New("Git hooks for this merge are in the tracked working tree. Move the hooks outside the working tree or merge manually."))
	}
	return nil
}

// runMergeGit performs a bounded local write, honoring the project's commit
// hooks and signing configuration. It keeps conflict details Git writes to
// stdout as well as stderr and cannot read a different repository from env.
func runMergeGit(ctx context.Context, repo, hooks string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo, "--no-replace-objects", "--no-lazy-fetch",
		"-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + hooks}, args...)...)
	cmd.Env = cleanGitEnv()
	stdout := &cappedBuffer{limit: gitReadMaxOutput}
	stderr := &cappedBuffer{limit: gitReadMaxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr, stopErr := procgroup.Run(cmd, syscall.SIGTERM, time.Second, 2*time.Second)
	if stopErr != nil {
		stopErr = fmt.Errorf("%w: %w", errMergeGitProcessActive, stopErr)
	}
	if err := errors.Join(runErr, stopErr); err != nil {
		details := strings.TrimSpace(stderr.buf.String() + "\n" + stdout.buf.String())
		return fmt.Errorf("git %s failed: %w\n%s", args[0], err, details)
	}
	return nil
}

// rollbackMerge restores the original branch tip and tracked files, and
// distinguishes a Git refusal from one whose recovery also failed.
func rollbackMerge(ctx context.Context, span trace.Span, repo, hooks, origHEAD string, mergeErr error) error {
	rollback, cancel := context.WithTimeout(context.WithoutCancel(ctx), mergeWriteTimeout)
	defer cancel()
	if err := runMergeGit(rollback, repo, hooks, "reset", "--merge", "--quiet", origHEAD); err != nil ||
		errors.Is(mergeErr, errMergeGitProcessActive) {
		span.RecordError(errors.New("git merge rollback failed"))
		span.SetStatus(codes.Error, "original Git state could not be restored")
		return gensurla.MakeMergeConflict(fmt.Errorf("Merge failed and Surla could not confirm restoration to %s. Inspect the branch, index and working tree before retrying.\n%w", origHEAD, errors.Join(mergeErr, err)))
	}
	span.AddEvent("merge_rolled_back")
	return gensurla.MakeMergeConflict(fmt.Errorf("Git restored the branch and tracked files to %s after the merge failed. Inspect the Git output before retrying.\n%w", origHEAD, mergeErr))
}

// stopFinishedBuildWorkers asks the team to clean up the exact completed build.
// A team failure belongs to this span because the merge or drop already took
// effect, and the project supervisor can retry the teardown.
func (c *ControlPlane) stopFinishedBuildWorkers(ctx context.Context, team Team, build BuildIdentity) {
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), workerStopTimeout)
	defer cancel()
	work, span := c.tracer("surla/controlplane").Start(work, "surla.controlplane.stop_finished_build_workers",
		trace.WithAttributes(attribute.String("surla.change_id", build.ChangeID)))
	defer span.End()
	if err := team.StopWorkers(work, build); err != nil {
		span.RecordError(errors.New("project team could not stop build workers"))
		span.SetStatus(codes.Error, "build worker teardown failed")
	}
}
