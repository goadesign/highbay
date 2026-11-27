// This file exercises project-wide admission and exact replacement consent.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/scaffold"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOneBuildAdmissionHasNoEffectsWithoutCurrentConsent exercises the public
// start and read consumers, including consent from an obsolete page.
func TestOneBuildAdmissionHasNoEffectsWithoutCurrentConsent(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	x := buildableChange(t, cp, "x")
	y := buildableChange(t, cp, "y")
	user := asCaller("alice@example.com", CallerUser)
	effects := 0
	factory := cp.buildCopyFactory
	cp.buildCopyFactory = func(ctx context.Context, folder string, layout scaffold.Layout) (buildCopy, error) {
		effects++
		return factory(ctx, folder, layout)
	}
	first, err := cp.StartBuild(user, &genhighbay.StartBuildPayload{ChangeID: "x", RequestRevision: x.RequestRevision})
	require.NoError(t, err)
	before := readBuildFile(t, cp, "x")
	effectsBefore := effects
	stale, err := time.Parse(time.RFC3339Nano, first.Build.StartedAt)
	require.NoError(t, err)
	cases := []struct {
		name, changeID, revision, refusal string
		replaces                          *genhighbay.ReplacedBuild
	}{
		{name: "same change without consent", changeID: "x", revision: x.RequestRevision, refusal: "unfinished_build_open"},
		{name: "same change with obsolete start", changeID: "x", revision: x.RequestRevision, refusal: "unfinished_build_open", replaces: &genhighbay.ReplacedBuild{ChangeID: "x", StartedAt: stale.Add(-time.Second).Format(time.RFC3339Nano)}},
		{name: "same change with another identity", changeID: "x", revision: x.RequestRevision, refusal: "unfinished_build_open", replaces: &genhighbay.ReplacedBuild{ChangeID: "y", StartedAt: first.Build.StartedAt}},
		{name: "other change without consent", changeID: "y", revision: y.RequestRevision, refusal: "previous_change_open"},
		{name: "other change with consent to replace x", changeID: "y", revision: y.RequestRevision, refusal: "previous_change_open", replaces: &genhighbay.ReplacedBuild{ChangeID: "x", StartedAt: first.Build.StartedAt}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cp.StartBuild(user, &genhighbay.StartBuildPayload{ChangeID: tc.changeID, RequestRevision: tc.revision, Replaces: tc.replaces})
			var refusal *genhighbay.OpenBuildRefusal
			require.True(t, errors.As(err, &refusal), "%v", err)
			assert.Equal(t, tc.refusal, refusal.Name)
			assert.Equal(t, "x", refusal.ChangeID)
			require.NotNil(t, refusal.Build)
			assert.Equal(t, first.Build.StartedAt, refusal.Build.StartedAt)
			assert.Equal(t, genhighbay.BuildTakingKindBuilding, refusal.Build.Taking.Kind())
			assert.Equal(t, before, readBuildFile(t, cp, "x"))
			assert.Equal(t, effectsBefore, effects, "refusal must precede working-copy effects")
		})
	}
	other, err := cp.GetBuild(user, &genhighbay.GetBuildPayload{ChangeID: "y"})
	require.NoError(t, err)
	assert.Equal(t, genhighbay.BuildKindNotStarted, other.Build.Kind())
	makeBuildFixtureBuilderCopy(t, cp)
	assignments, err := cp.ListBuilderAssignments(asCaller("alice@example.com", CallerBuilder))
	require.NoError(t, err)
	require.Len(t, assignments.Assignments, 1)
	assert.Equal(t, "x", assignments.Assignments[0].ChangeID)
}

// TestOneBuildRestartKeepsItsNumberAndBase proves a fresh start does not reuse
// an old read or renumber the same open build, including without run storage.
func TestOneBuildRestartKeepsItsNumberAndBase(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	cp.runsDir = ""
	card := buildableChange(t, cp, "x")
	user := asCaller("alice@example.com", CallerUser)
	first, err := cp.StartBuild(user, &genhighbay.StartBuildPayload{ChangeID: "x", RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	cp.mu.RLock()
	old, found, err := cp.cards.loadOpenBuild()
	cp.mu.RUnlock()
	require.NoError(t, err)
	require.True(t, found)
	makeBuildFixtureBuilderCopy(t, cp)
	_, err = cp.GetBuilderAssignment(asCaller("alice@example.com", CallerBuilder), &genhighbay.GetBuilderAssignmentPayload{ChangeID: "x"})
	require.NoError(t, err)
	restarted, err := cp.StartBuild(user, &genhighbay.StartBuildPayload{
		ChangeID: "x", RequestRevision: card.RequestRevision,
		Replaces: &genhighbay.ReplacedBuild{ChangeID: "x", StartedAt: first.Build.StartedAt},
	})
	require.NoError(t, err)
	assert.False(t, restarted.Build.Builder.Read)
	assert.False(t, restarted.Build.TestWriter.Read)
	assert.NotEqual(t, first.Build.StartedAt, restarted.Build.StartedAt)
	cp.mu.RLock()
	current, found, err := cp.cards.loadOpenBuild()
	cp.mu.RUnlock()
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, old.Number, current.Number)
	assert.Equal(t, old.Base, current.Base)
	assert.Equal(t, old.MadeFrom, current.MadeFrom)
	_, err = cp.StartBuild(user, &genhighbay.StartBuildPayload{ChangeID: "x", RequestRevision: card.RequestRevision})
	var refusal *genhighbay.OpenBuildRefusal
	require.True(t, errors.As(err, &refusal))
	assert.Equal(t, "unfinished_build_open", refusal.Name)
	assert.Equal(t, restarted.Build.StartedAt, refusal.Build.StartedAt)
}

// TestOneBuildRefusesLegacyMetadataBeforeEffects keeps old cards readable but
// requires a real refreshed revision before creating their first new build.
func TestOneBuildRefusesLegacyMetadataBeforeEffects(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	card := confirmedChange(t, cp, "old")
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		t.Fatal("legacy metadata refusal must precede copy creation")
		return nil, nil
	}
	_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &genhighbay.StartBuildPayload{ChangeID: "old", RequestRevision: card.RequestRevision})
	assertRefused(t, err, "conflict")
	assert.ErrorContains(t, err, "The agreed revision records no design")
	assert.ErrorContains(t, err, "Ask your coding agent to propose a revision from its draft, adopt it, then build.")
	_, statErr := os.Stat(filepath.Join(cp.cards.dir, openBuildRecordFile))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
	read, err := cp.GetChange(asCaller("alice@example.com", CallerGuide), &genhighbay.GetChangePayload{ChangeID: "old"})
	require.NoError(t, err)
	assert.Equal(t, card.RequestRevision, read.RequestRevision)
}

// TestBuildAndTeamStartSurfaceBoundedPolicyRefusal gives every operation that
// can start an agent the team owner's refusal naming the coding client's
// setting that blocks it. start_build keeps the saved build and returns the
// setting's repair as the team step; start_team_agents and send_team_message,
// which starts a missing guide, answer unprocessable with the same repair,
// each ending with its own retry.
func TestBuildAndTeamStartSurfaceBoundedPolicyRefusal(t *testing.T) {
	refusals := []struct {
		name       string
		refusal    *TeamPolicyRefusal
		wantSubstr string
	}{
		{
			name: "codex untrusted project",
			refusal: &TeamPolicyRefusal{
				Kind:   PolicyCodexProjectUntrusted,
				Client: "codex",
				Detail: "untrusted",
			},
			wantSubstr: `Set trust_level = "trusted" for it in your Codex config.toml`,
		},
		{
			name: "cursor wildcard permission with long detail clamped within BuildStartStep",
			refusal: &TeamPolicyRefusal{
				Kind:   PolicyCursorPermission,
				Client: "cursor",
				Server: "highbay",
				List:   "deny",
				Detail: "Mcp(*:get_builder_assignment_" + string(make([]byte, 300)) + ")",
			},
			wantSubstr: "Remove or narrow that entry in permissions.deny in ~/.cursor/cli-config.json",
		},
		{
			name: "gemini excluded tool",
			refusal: &TeamPolicyRefusal{
				Kind:   PolicyGeminiToolFiltered,
				Client: "gemini",
				Server: "highbay",
				List:   "excludeTools",
				Detail: "get_builder_assignment",
			},
			wantSubstr: "excludeTools in your Gemini settings.json",
		},
		{
			name: "unusable client configuration file",
			refusal: &TeamPolicyRefusal{
				Kind:   PolicyConfigUnusable,
				Client: "claude",
				Detail: "settings.json",
			},
			wantSubstr: "Claude Code configuration file settings.json could not be read or safely used.",
		},
	}

	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			team := &teamServiceFixture{
				start: func(context.Context) (*genhighbay.ProjectTeam, error) {
					return nil, tc.refusal
				},
				send: func(context.Context, *genhighbay.SendTeamMessagePayload) (*genhighbay.TeamMessage, error) {
					return nil, fmt.Errorf("start the guide: %w", tc.refusal)
				},
				restartErr: fmt.Errorf("restart the role: %w", tc.refusal),
			}
			cp := newBuildControlPlane(t, Options{
				Team: &TeamBinding{Owner: "alice@example.com", Service: team},
			})
			card := buildableChange(t, cp, "x")
			user := asCaller("alice@example.com", CallerUser)

			started, err := cp.StartBuild(user, &genhighbay.StartBuildPayload{
				ChangeID:        "x",
				RequestRevision: card.RequestRevision,
			})
			require.NoError(t, err)
			unavailable, ok := started.Team.AsUnavailable()
			require.True(t, ok, "expected BuildTeamUnavailable outcome")
			assert.Contains(t, string(unavailable.Step), tc.wantSubstr)
			assert.Contains(t, string(unavailable.Step), "Team → Start workers on the views page to start the team agents for this saved build")
			assert.NotContains(t, string(unavailable.Step), "The project team could not be prepared.")
			_, encodeErr := genhighbay.EncodeStartBuildResult(started)
			require.NoError(t, encodeErr, "BuildStartStep must satisfy 1..512 UTF-8 schema bounds")

			_, teamErr := cp.StartTeamAgents(user, &genhighbay.StartTeamAgentsPayload{})
			assertRefused(t, teamErr, "unprocessable")
			assert.ErrorContains(t, teamErr, tc.wantSubstr)
			assert.ErrorContains(t, teamErr, "then choose Start workers again.")

			_, sendErr := cp.SendTeamMessage(user, &genhighbay.SendTeamMessagePayload{To: "guide", Text: "Add a refund receipt."})
			assertRefused(t, sendErr, "unprocessable")
			assert.ErrorContains(t, sendErr, tc.wantSubstr)
			if assert.Error(t, sendErr) {
				assert.True(t, strings.HasSuffix(sendErr.Error(), "then send your message again."), sendErr.Error())
				assert.NotContains(t, sendErr.Error(), "Start workers")
			}

			restartErr := cp.RestartTeamRole(user, &genhighbay.RestartTeamRolePayload{Role: "builder"})
			assertRefused(t, restartErr, "unprocessable")
			assert.ErrorContains(t, restartErr, tc.wantSubstr)
			assert.ErrorContains(t, restartErr, "then choose Restart again.")
		})
	}
}
