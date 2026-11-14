// This file derives whether a build's passed commits have reached the user's branch.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/goadesign/surla/internal/verify"
	"github.com/goadesign/surla/internal/verifyrun"
)

// eligibleBuildRuns returns every pass belonging to this particular start, newest first.
func (c *ControlPlane) eligibleBuildRuns(owner string, saved buildFile) ([]verifyrun.Run, error) {
	if c.runsDir == "" {
		return nil, nil
	}
	runs, err := verifyrun.List(c.runsDir, owner, saved.ChangeID)
	if err != nil {
		return nil, fmt.Errorf("read passed runs for the open build: %w", err)
	}
	var passed []verifyrun.Run
	for _, run := range runs {
		if run.Request.Plan.RequestRevision == saved.RequestRevision && !run.Request.AcceptedAt.Before(saved.StartedAt) && run.State.Kind == verifyrun.StateCompleted && run.State.Report.Passed {
			passed = append(passed, run)
		}
	}
	return passed, nil
}

// commitTaken accepts ancestry or a commit in the project's history holding all
// files changed by the source since its common ancestor with the project.
func commitTaken(ctx context.Context, repo, source, project string) (bool, error) {
	ancestor, err := buildCommitInHistory(ctx, repo, source, project)
	if err != nil || ancestor {
		return ancestor, err
	}
	base, found, err := mergeBaseCommit(ctx, repo, source, project)
	if err != nil || !found {
		return false, err
	}
	paths, err := runGitRead(ctx, repo, "diff-tree", "-r", "--no-renames", "--name-only", "-z", base, source)
	if err != nil {
		return false, err
	}
	names := strings.Split(strings.TrimSuffix(string(paths), "\x00"), "\x00")
	if len(names) == 1 && names[0] == "" {
		return false, nil
	}
	history, err := runGitRead(ctx, repo, "rev-list", project, "^"+base)
	if err != nil {
		return false, err
	}
	for _, commit := range strings.Fields(string(history)) {
		args := append([]string{"diff", "--quiet", "--no-renames", source, commit, "--"}, names...)
		_, err = runGitRead(ctx, repo, args...)
		if err == nil {
			return true, nil
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return false, err
		}
	}
	return false, nil
}

// runTaken requires the candidate and, for written tests, the same run's suite.
func runTaken(ctx context.Context, repo, project string, run verifyrun.Run) (bool, error) {
	ok, err := commitTaken(ctx, repo, run.Request.Plan.CandidateCommit, project)
	if err != nil || !ok || run.Request.Plan.Tests != verify.TestsWritten {
		return ok, err
	}
	return commitTaken(ctx, repo, run.Request.Plan.SuiteCommit, project)
}

// dropSafeGuidance reports the two cases where the project may already contain
// the desired work despite not holding the exact passed commits.
func dropSafeGuidance(ctx context.Context, repo, base, project, candidate string) (bool, error) {
	ancestor, err := buildCommitInHistory(ctx, repo, base, project)
	if err != nil || !ancestor {
		return !ancestor, err
	}
	common, found, err := mergeBaseCommit(ctx, repo, candidate, project)
	if err != nil || !found {
		return false, err
	}
	paths, err := runGitRead(ctx, repo, "diff-tree", "-r", "--no-renames", "--name-only", "-z", common, candidate)
	if err != nil {
		return false, err
	}
	names := strings.Split(strings.TrimSuffix(string(paths), "\x00"), "\x00")
	if len(names) == 1 && names[0] == "" {
		return false, nil
	}
	args := append([]string{"log", "--format=", "--name-only", "-z", common + ".." + project, "--"}, names...)
	changed, err := runGitRead(ctx, repo, args...)
	return len(changed) != 0, err
}
