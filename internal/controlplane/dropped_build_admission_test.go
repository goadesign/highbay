// This file preserves real post-drop recovery when content keeps the same digest.
package controlplane

import (
	"maps"
	"path/filepath"
	"strings"
	"testing"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDroppedBuildSameRevisionRecovery uses saved compiler results while real
// refresh reuse, revision, adoption, Drop and StartBuild own draft provenance.
func TestDroppedBuildSameRevisionRecovery(t *testing.T) {
	workspace := t.TempDir()
	snapshot := goaGoaAIDSLFixture(t, generationDesign)
	writeGenerationFiles(t, workspace, snapshot)
	evalSnapshot := maps.Clone(snapshot)
	evalSnapshot["go.mod"] = strings.Replace(snapshot["go.mod"], "module example.com/design", "module example.com/design/eval", 1)
	writeGenerationFiles(t, filepath.Join(workspace, "eval"), evalSnapshot)
	commitGenerationProject(t, workspace)
	cp := newControlPlane(t, Options{
		WorkspaceDir: workspace, CardsDir: t.TempDir(),
		Team: &TeamBinding{Owner: "developer@example.com", Service: &teamServiceFixture{}},
	})
	guide := asCaller("developer@example.com", CallerGuide)
	user := asCaller("developer@example.com", CallerUser)
	opID := seedStoryRefreshForTest(t, cp, "developer@example.com", storyRefreshStateSucceeded, nil, "")
	card, _ := generationNativeBuild(t, cp)
	observed, err := cp.GetStoryRefresh(guide)
	require.NoError(t, err)
	require.Equal(t, &opID, observed.OpID, "build setup reuses the saved compiler result")
	cp.mu.RLock()
	original, found, err := cp.cards.loadOpenBuild()
	cp.mu.RUnlock()
	require.NoError(t, err)
	require.True(t, found)
	read := func(want bool) *gensurla.StartedBuild {
		t.Helper()
		public, err := cp.GetBuild(user, &gensurla.GetBuildPayload{ChangeID: card.ChangeID})
		require.NoError(t, err)
		assert.Equal(t, want, public.DraftedBeforeDrop)
		started, ok := public.Build.AsStarted()
		require.True(t, ok)
		return started
	}
	revise := func(op *string) *gensurla.ChangeCardResult {
		t.Helper()
		examples := make([]*gensurla.CP1RevisionAnalysisExample, 0, len(card.Examples))
		for _, example := range card.Examples {
			id := example.ID
			examples = append(examples, &gensurla.CP1RevisionAnalysisExample{
				PriorID: &id, Scenario: example.Scenario, InputSummary: example.InputSummary,
				ExpectedOutcome: example.ExpectedOutcome, VerificationKind: example.VerificationKind,
			})
		}
		revised, err := cp.ReviseChange(guide, &gensurla.ReviseChangePayload{
			ChangeID: card.ChangeID, RequestRevision: card.RequestRevision, Prompt: card.Prompt,
			StoryRefreshOpID: op,
			Analysis: &gensurla.CP1RevisionAnalysis{
				Summary: card.Summary, Ambiguities: []*gensurla.CP1AnalysisAmbiguity{},
				Examples: examples, RemovedExampleIds: []string{}, PreservedBehaviors: card.PreservedBehaviors,
			},
		})
		require.NoError(t, err)
		require.NotNil(t, revised.ProposedRevision)
		return revised
	}
	adopt := func(revised *gensurla.ChangeCardResult) {
		t.Helper()
		adopted, err := cp.ConfirmIntent(user, &gensurla.ConfirmIntentPayload{
			ChangeID: card.ChangeID, RequestRevision: revised.ProposedRevision.RequestRevision,
			Answers: card.Answers, Examples: revised.ProposedRevision.Examples,
		})
		require.NoError(t, err)
		assert.Equal(t, card.RequestRevision, adopted.RequestRevision)
	}
	read(false)
	// A waiting pre-drop proposal and even its adoption cannot repair provenance.
	stale := revise(nil)
	dropped, err := cp.DropBuild(user, &gensurla.DropBuildPayload{
		ChangeID: card.ChangeID, StartedAt: verdictTime(original.StartedAt),
	})
	require.NoError(t, err)
	assert.True(t, dropped.DraftedBeforeDrop)
	read(true)
	adopt(stale)
	staleBuild := read(true)
	assert.True(t, staleBuild.InForce)
	_, err = cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	assertRefused(t, err, "drafted_before_drop")
	// A saved post-drop evaluation carries the real draft's new provenance.
	opID = seedStoryRefreshForTest(t, cp, "developer@example.com", storyRefreshStateSucceeded, nil, "")
	refreshed := generationRefresh(t, cp)
	require.Equal(t, &opID, refreshed.OpID, "post-drop refresh reuses the current saved result")
	revised := revise(refreshed.OpID)
	read(true) // A refreshed proposal still waits beside the stale agreement.
	adopt(revised)
	beforeCard, err := cp.GetChange(user, &gensurla.GetChangePayload{ChangeID: card.ChangeID})
	require.NoError(t, err)
	assert.Equal(t, card.RequestRevision, beforeCard.RequestRevision)
	assert.Nil(t, beforeCard.ProposedRevision)
	before := read(false)
	assert.True(t, before.InForce)
	assert.Equal(t, gensurla.BuildTakingKindDropped, before.Taking.Kind())
	started, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: beforeCard.RequestRevision,
	})
	require.NoError(t, err)
	assert.Equal(t, card.RequestRevision, started.Build.RequestRevision)
	afterCard, err := cp.GetChange(user, &gensurla.GetChangePayload{ChangeID: card.ChangeID})
	require.NoError(t, err)
	assert.Equal(t, beforeCard.RequestRevision, afterCard.RequestRevision)
	assert.Equal(t, beforeCard.Examples, afterCard.Examples)
	assert.Nil(t, afterCard.ProposedRevision)
	after := read(false)
	assert.True(t, after.InForce)
	assert.Equal(t, gensurla.BuildTakingKindBuilding, after.Taking.Kind())
	assert.False(t, after.Builder.Read)
	assert.False(t, after.TestWriter.Read)
	assert.Zero(t, after.TestWriter.Report)
	cp.mu.RLock()
	next, found, err := cp.cards.loadOpenBuild()
	cp.mu.RUnlock()
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, original.Number+1, next.Number)
	assert.Equal(t, productionBuildGit(t, workspace, "rev-parse", "HEAD"), next.Base)
	assert.NotEqual(t, original.StartedAt, next.StartedAt)
}

// TestDraftedBeforeDropMatchesOnlyAgreedProvenance covers the strict generation
// boundary and the separate missing-agreement/design admission conditions.
func TestDraftedBeforeDropMatchesOnlyAgreedProvenance(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		agreed, design, dropped bool
		draft                   uint64
		want                    bool
	}{
		{name: "before actual drop", agreed: true, design: true, dropped: true, draft: 1, want: true},
		{name: "at actual drop", agreed: true, design: true, dropped: true, draft: 2},
		{name: "after actual drop", agreed: true, design: true, dropped: true, draft: 3},
		{name: "no actual drop", agreed: true, design: true, draft: 1},
		{name: "no agreement", design: true, dropped: true, draft: 1},
		{name: "no saved provenance", agreed: true, dropped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := storedChangeCard{card: &gensurla.ChangeCardResult{
				Status: StatusNeedsConfirmation, RequestRevision: "revision",
			}}
			if tc.agreed {
				record.card.Status = StatusConfirmed
			}
			if tc.design {
				record.madeFrom = &madeFromSnapshot{Start: madeFromStart{Write: tc.draft}}
			}
			provenance := madeFromRecord{Start: madeFromStart{Write: 2}}
			if tc.dropped {
				provenance.Start.Dropped = &droppedBuild{}
			}
			assert.Equal(t, tc.want, draftedBeforeDrop(record, provenance))
		})
	}
}
