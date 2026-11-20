// This file exercises the status callback with saved cards, runs and one Git history.
package controlplane

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	genproject "github.com/goadesign/surla/gen/project"
	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/verify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProjectStatusFollowsSavedChange exercises the project management
// callback. No agents, promotion checks or builds run. The tiny Git fixture
// proves that taking the passed commit changes the status on the next read.
func TestProjectStatusFollowsSavedChange(t *testing.T) {
	cards, err := openCardStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, cards.close()) })
	workspace := realTempDir(t)
	const owner = "alice@example.com"
	guide := &gensurla.TeamRoleState{
		Role: "guide", Handle: "guide-test", State: "needs_you", Attention: "question",
		Watcher: "held", Assignment: "nothing_owed",
	}
	team := &teamServiceFixture{snapshot: gensurla.TeamSnapshot{
		Team: "test-project", HerdrMode: "attached", Roles: []*gensurla.TeamRoleState{guide},
	}}
	runsDir := realTempDir(t)
	cp := &ControlPlane{
		cards: cards, initialWorkspaceRoot: workspace, activeWorkspaceDir: workspace,
		team: TeamBinding{Owner: owner, Service: team}, runsDir: runsDir,
		verifier: storedVerification{cfg: &VerificationConfig{
			Plan: verify.Plan{SuiteCommit: strings.Repeat("2", 40)},
		}},
	}

	check := func(stage, attention string, card *gensurla.ChangeCardResult) {
		t.Helper()
		status, err := cp.ProjectStatus(t.Context())
		require.NoError(t, err)
		require.NoError(t, genproject.ValidateProjectStatusValue(status))
		assert.Equal(t, stage, status.Stage)
		assert.Equal(t, attention, status.Attention)
		assert.Equal(t, filepath.Base(workspace), status.ProjectName)
		assert.Equal(t, workspace, status.Directory)
		assert.Equal(t, "test-project", status.Team)
		assert.Equal(t, "attached", status.HerdrMode)
		assert.Equal(t, []*genproject.TeamRoleState{{
			Role: guide.Role, Handle: guide.Handle, State: guide.State,
			Attention: guide.Attention, Watcher: guide.Watcher, Assignment: guide.Assignment,
		}}, status.Roles)
		if card == nil {
			assert.Empty(t, status.ChangeID)
			assert.Empty(t, status.ChangeTitle)
		} else {
			assert.Equal(t, card.ChangeID, status.ChangeID)
			assert.Equal(t, card.Summary, status.ChangeTitle)
		}
	}

	check("describe", "Your coding agent needs your attention.", nil)
	guide.State, guide.Attention = "waiting", "none"
	confirmed := saveCard(t, cp, nil)
	check("build", "Start a build for the current agreement.", confirmed)
	pending := saveStatusProposal(t, cp, owner, "chg-next")
	saveStatusProposal(t, cp, "another@example.com", "chg-other")
	check("agree", "Review and confirm the change.", pending)

	startedAt := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	build := buildFile{
		ChangeID: confirmed.ChangeID, RequestRevision: confirmed.RequestRevision, StartedAt: startedAt,
		Number: 1, Base: strings.Repeat("3", 40), Roles: buildRolesBuilderOnly,
		GoMod:      "module example.com/refunds\n\ngo 1.27.3\n",
		MadeFrom:   madeFromSnapshot{Start: madeFromStart{Write: 1}},
		TestWriter: testWriterAssignment{State: testsAssigned},
	}
	require.NoError(t, cards.saveOpenBuild(build))
	writeListedRun(t, runsDir, owner, confirmed, strings.Repeat("1", 32), startedAt.Add(-time.Second), true)
	check("build", "", confirmed)

	writeListedRun(t, runsDir, owner, confirmed, strings.Repeat("2", 32), startedAt.Add(2*time.Second), false)
	check("check", "Review the latest check.", confirmed)

	ensureBuildFixtureRepository(t, workspace)
	productionBuildGit(t, workspace, "switch", "--quiet", "-c", "candidate")
	productionBuildGit(t, workspace, "commit", "--quiet", "--allow-empty", "-m", "Verified change")
	candidate := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	productionBuildGit(t, workspace, "switch", "--quiet", "-")
	writeListedRunAt(t, runsDir, owner, confirmed, strings.Repeat("3", 32), startedAt, true, candidate)
	check("approve", "Review the passing check for approval.", confirmed)

	productionBuildGit(t, workspace, "merge", "--quiet", "--ff-only", candidate)
	check("merged", "", confirmed)
	require.NoError(t, cards.saveMadeFrom(madeFromRecord{
		Write: 2,
		Start: madeFromStart{Write: 2, Dropped: &droppedBuild{
			ChangeID: confirmed.ChangeID, StartedAt: startedAt, Number: build.Number,
		}},
	}))
	check("agree", "Review and confirm the change.", pending)
	assert.Len(t, team.calls, 8)
	for _, call := range team.calls {
		assert.Equal(t, "read", call)
	}
}

// saveStatusProposal writes an unconfirmed card under the indicated owner so
// the callback's selection can be tested without invoking the guide flow.
func saveStatusProposal(t *testing.T, cp *ControlPlane, owner, id string) *gensurla.ChangeCardResult {
	t.Helper()
	card := &gensurla.ChangeCardResult{
		ChangeID: id, Status: StatusNeedsConfirmation,
		Prompt: "Add a refund receipt.", Summary: "Add a refund receipt.",
	}
	var err error
	card.RequestRevision, err = computeCanonicalRevision(card, owner, "")
	require.NoError(t, err)
	saveMetadataRecord(t, cp, storedChangeCard{card: card, owner: owner})
	return card
}
