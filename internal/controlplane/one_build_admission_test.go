// This file exercises project-wide admission and exact replacement consent.
package controlplane

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/scaffold"
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
	first, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "x", RequestRevision: x.RequestRevision})
	require.NoError(t, err)
	before := readBuildFile(t, cp, "x")
	effectsBefore := effects
	stale, err := time.Parse(time.RFC3339Nano, first.Build.StartedAt)
	require.NoError(t, err)
	cases := []struct {
		name, changeID, revision, refusal string
		replaces                          *gensurla.ReplacedBuild
	}{
		{name: "same change without consent", changeID: "x", revision: x.RequestRevision, refusal: "unfinished_build_open"},
		{name: "same change with obsolete start", changeID: "x", revision: x.RequestRevision, refusal: "unfinished_build_open", replaces: &gensurla.ReplacedBuild{ChangeID: "x", StartedAt: stale.Add(-time.Second).Format(time.RFC3339Nano)}},
		{name: "same change with another identity", changeID: "x", revision: x.RequestRevision, refusal: "unfinished_build_open", replaces: &gensurla.ReplacedBuild{ChangeID: "y", StartedAt: first.Build.StartedAt}},
		{name: "other change without consent", changeID: "y", revision: y.RequestRevision, refusal: "previous_change_open"},
		{name: "other change with consent to replace x", changeID: "y", revision: y.RequestRevision, refusal: "previous_change_open", replaces: &gensurla.ReplacedBuild{ChangeID: "x", StartedAt: first.Build.StartedAt}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: tc.changeID, RequestRevision: tc.revision, Replaces: tc.replaces})
			var refusal *gensurla.OpenBuildRefusal
			require.True(t, errors.As(err, &refusal), "%v", err)
			assert.Equal(t, tc.refusal, refusal.Name)
			assert.Equal(t, "x", refusal.ChangeID)
			require.NotNil(t, refusal.Build)
			assert.Equal(t, first.Build.StartedAt, refusal.Build.StartedAt)
			assert.Equal(t, gensurla.BuildTakingKindBuilding, refusal.Build.Taking.Kind())
			assert.Equal(t, before, readBuildFile(t, cp, "x"))
			assert.Equal(t, effectsBefore, effects, "refusal must precede working-copy effects")
		})
	}
	other, err := cp.GetBuild(user, &gensurla.GetBuildPayload{ChangeID: "y"})
	require.NoError(t, err)
	assert.Equal(t, gensurla.BuildKindNotStarted, other.Build.Kind())
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
	first, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "x", RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	cp.mu.RLock()
	old, found, err := cp.cards.loadOpenBuild()
	cp.mu.RUnlock()
	require.NoError(t, err)
	require.True(t, found)
	makeBuildFixtureBuilderCopy(t, cp)
	_, err = cp.GetBuilderAssignment(asCaller("alice@example.com", CallerBuilder), &gensurla.GetBuilderAssignmentPayload{ChangeID: "x"})
	require.NoError(t, err)
	restarted, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: "x", RequestRevision: card.RequestRevision,
		Replaces: &gensurla.ReplacedBuild{ChangeID: "x", StartedAt: first.Build.StartedAt},
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
	_, err = cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "x", RequestRevision: card.RequestRevision})
	var refusal *gensurla.OpenBuildRefusal
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
	_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{ChangeID: "old", RequestRevision: card.RequestRevision})
	assertRefused(t, err, "conflict")
	assert.ErrorContains(t, err, "The agreed revision records no design")
	assert.ErrorContains(t, err, "Ask your coding agent to propose a revision from its draft, adopt it, then build.")
	_, statErr := os.Stat(filepath.Join(cp.cards.dir, openBuildRecordFile))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
	read, err := cp.GetChange(asCaller("alice@example.com", CallerGuide), &gensurla.GetChangePayload{ChangeID: "old"})
	require.NoError(t, err)
	assert.Equal(t, card.RequestRevision, read.RequestRevision)
}
