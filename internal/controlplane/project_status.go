// This file summarizes the project owner's saved change and cached team facts.
package controlplane

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/verifyrun"
)

// ProjectStatus supplies the management service with the current project's
// saved stage and observed roles. Its caller authorizes management access
// before invoking the callback. For a passed build it also checks whether
// the current branch contains the verified commits. All reads are side-effect-free.
func (c *ControlPlane) ProjectStatus(ctx context.Context) (*genproject.ProjectStatus, error) {
	record, build, err := c.projectStatusCard()
	if err != nil {
		return nil, err
	}
	stage, attention, err := c.projectChangeStage(ctx, record, build)
	if err != nil {
		return nil, err
	}
	team, err := c.team.Service.ReadTeam(ctx)
	if err != nil {
		return nil, fmt.Errorf("read project team: %w", err)
	}

	status := &genproject.ProjectStatus{
		ProjectName: filepath.Base(c.initialWorkspaceRoot),
		Directory:   c.initialWorkspaceRoot,
		Team:        team.Team,
		HerdrMode:   team.HerdrMode,
		Stage:       stage,
		Attention:   attention,
		Roles:       make([]*genproject.TeamRoleState, 0, len(team.Roles)),
	}
	if record.card != nil {
		status.ChangeID = record.card.ChangeID
		status.ChangeTitle = record.card.Summary
		if record.proposed != nil {
			status.Attention = "Review the proposed revision."
		}
	}
	for _, role := range team.Roles {
		status.Roles = append(status.Roles, &genproject.TeamRoleState{
			Role:       role.Role,
			Handle:     role.Handle,
			State:      role.State,
			Attention:  role.Attention,
			Watcher:    role.Watcher,
			Assignment: role.Assignment,
		})
		if status.Attention == "" && role.State == "needs_you" {
			status.Attention = projectRoleAttention(role.Role)
		}
	}
	return status, nil
}

// projectStatusCard reads the usable open build and its card under the same
// lock. Without an owned open build, it selects the owner's most recently
// saved card, breaking saved-time ties by ascending change ID as ListChanges
// does. A project without owned cards has no selected record.
func (c *ControlPlane) projectStatusCard() (storedChangeCard, *buildFile, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	build, found, err := c.cards.loadOpenBuild()
	if err != nil {
		return storedChangeCard{}, nil, err
	}
	active, err := c.activeBuildFound(build, found)
	if err != nil {
		return storedChangeCard{}, nil, err
	}
	if active {
		record, found, err := c.cards.load(build.ChangeID)
		if err != nil {
			return storedChangeCard{}, nil, err
		}
		if !found {
			return storedChangeCard{}, nil, fmt.Errorf("open build names missing change %q", build.ChangeID)
		}
		if record.owner == c.team.Owner && c.usableBuild(build) {
			return record, &build, nil
		}
	}

	records, err := c.cards.all()
	if err != nil {
		return storedChangeCard{}, nil, err
	}
	var latest storedChangeCard
	for _, record := range records {
		if record.owner != c.team.Owner {
			continue
		}
		if latest.card == nil || record.savedAt.After(latest.savedAt) ||
			record.savedAt.Equal(latest.savedAt) && record.card.ChangeID < latest.card.ChangeID {
			latest = record
		}
	}
	return latest, nil, nil
}

// projectChangeStage follows the agreed revision of the selected card. An
// open build stays active while a proposal waits. Only matching verification
// inputs drive Check and Approve. An eligible pass can establish that the
// branch took the build even if the project has since chosen different tests.
func (c *ControlPlane) projectChangeStage(
	ctx context.Context, record storedChangeCard, build *buildFile,
) (string, string, error) {
	switch {
	case record.card == nil:
		return "describe", "", nil
	case record.agreedRevision() == "":
		return "agree", "Review and confirm the change.", nil
	case build == nil || !buildInForce(*build, record):
		if record.proposed != nil {
			return "agree", "", nil
		}
		return "build", "Start a build for the current agreement.", nil
	case c.runsDir == "":
		return "build", "", nil
	}

	runs, err := verifyrun.List(c.runsDir, record.owner, record.card.ChangeID)
	if err != nil {
		return "", "", fmt.Errorf("read project verification runs: %w", err)
	}
	stage, attention := "build", ""
	var passed []verifyrun.Run
	for _, run := range runs {
		if run.Request.AcceptedAt.Before(build.StartedAt) ||
			run.Request.Plan.RequestRevision != build.RequestRevision {
			continue
		}
		pass := run.State.Kind == verifyrun.StateCompleted && run.State.Report.Passed
		if pass {
			passed = append(passed, run)
		}
		if !c.verificationInputsCurrent(record, *build, true, run.Request.Plan) {
			continue
		}
		switch {
		case pass && stage != "approve":
			stage, attention = "approve", "Review the passing check for approval."
		case !pass && stage == "build":
			stage = "check"
			if run.State.Kind != verifyrun.StateRunning {
				attention = "Review the latest check."
			}
		}
		if approval := record.card.Approval; pass && approval != nil && approval.ReportDigest == run.State.Report.Digest {
			attention = "Review the saved approval before merging."
		}
	}

	if len(passed) > 0 {
		merged, err := c.projectBuildMerged(ctx, passed)
		if err != nil {
			return "", "", err
		}
		if merged {
			return "merged", "", nil
		}
	}
	return stage, attention, nil
}

// projectBuildMerged checks the current branch against the eligible passes
// accepted for the selected start. The workspace lock is released before
// running Git, and one deadline bounds all read-only history queries.
func (c *ControlPlane) projectBuildMerged(ctx context.Context, passed []verifyrun.Run) (bool, error) {
	work, cancel := context.WithTimeout(ctx, layoutReadTimeout)
	defer cancel()
	repo, _, err := resolveStarterRepo(work, c.currentWorkspaceDir())
	if err != nil {
		return false, err
	}
	head, err := runGitRead(work, repo, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return false, err
	}
	project := strings.TrimSpace(string(head))
	for _, run := range passed {
		merged, err := runTaken(work, repo, project, run)
		if err != nil || merged {
			return merged, err
		}
	}
	return false, nil
}

// projectRoleAttention summarizes the first cached role asking the user for
// help. The role list retains the reason and assignment details.
func projectRoleAttention(role string) string {
	switch role {
	case "guide":
		return "Your coding agent needs your attention."
	case "builder":
		return "The builder needs your attention."
	case "test_writer":
		return "The test writer needs your attention."
	default:
		return ""
	}
}
