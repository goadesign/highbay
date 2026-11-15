// This file drives native generation fixtures through the real draft and build
// producers. It never seeds an evaluation, saved revision, or open build.
package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/stretchr/testify/require"
)

// commitGenerationProject gives the real working-copy owner a committed base
// with local identity, without changing the original fixture's files.
func commitGenerationProject(t *testing.T, project string) {
	t.Helper()
	initGitWorktreeForBoundaryTest(t, project)
	for key, value := range map[string]string{
		"user.name": "Generation Tester", "user.email": "generation@example.com",
		"commit.gpgsign": "false", "core.hooksPath": "/dev/null", "core.fsmonitor": "false",
	} {
		runGitForTest(t, project, "config", key, value)
	}
	paths := []string{"add", "go.mod", "go.sum", "design", "eval"}
	if _, err := os.Stat(filepath.Join(project, "gen")); err == nil {
		paths = append(paths, "gen")
	} else {
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	runGitForTest(t, project, paths...)
	runGitForTest(t, project, "commit", "--quiet", "-m", "Save generation fixture")
}

// generationNativeBuild refreshes the actual draft, proposes and confirms its
// snapshot, starts a build, and reads the resulting builder working folder.
func generationNativeBuild(
	t *testing.T, cp *ControlPlane,
) (*gensurla.ChangeCardResult, *gensurla.BuilderAssignment) {
	t.Helper()
	refreshed := generationRefresh(t, cp)
	const prompt = "Generate the order lookup service."
	card, err := cp.ProposeChange(asCaller("developer@example.com", CallerGuide),
		&gensurla.ProposeChangePayload{
			ChangeID: "generation", Prompt: prompt, StoryRefreshOpID: refreshed.OpID,
			Analysis: &gensurla.CP1Analysis{
				Summary: prompt, Ambiguities: []*gensurla.CP1AnalysisAmbiguity{},
				Examples: []*gensurla.CP1AnalysisExample{{
					Scenario: "Read an order", InputSummary: "Order ID 42",
					ExpectedOutcome: "Returns the order", VerificationKind: "deterministic",
				}},
				PreservedBehaviors: []string{},
			},
		})
	require.NoError(t, err)
	return generationConfirmBuild(t, cp, card, card.RequestRevision, card.Examples)
}

// generationNativeRevision edits only the guide's returned draft and adopts a
// real evaluated revision before restarting and reading the same build.
func generationNativeRevision(
	t *testing.T, cp *ControlPlane, card *gensurla.ChangeCardResult, design string,
) *gensurla.BuilderAssignment {
	t.Helper()
	guide := asCaller("developer@example.com", CallerGuide)
	previous, err := cp.GetStoryRefresh(guide)
	require.NoError(t, err)
	writeGenerationFiles(t, previous.DraftFolder, map[string]string{"design/design.go": design})
	refreshed := generationRefresh(t, cp)
	examples := make([]*gensurla.CP1RevisionAnalysisExample, 0, len(card.Examples))
	for _, example := range card.Examples {
		id := example.ID
		examples = append(examples, &gensurla.CP1RevisionAnalysisExample{
			PriorID: &id, Scenario: example.Scenario, InputSummary: example.InputSummary,
			ExpectedOutcome: example.ExpectedOutcome, VerificationKind: example.VerificationKind,
		})
	}
	revised, err := cp.ReviseChange(guide, &gensurla.ReviseChangePayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
		Prompt:           "Generate the order lookup service with its revised description.",
		StoryRefreshOpID: refreshed.OpID,
		Analysis: &gensurla.CP1RevisionAnalysis{
			Summary:     "Keep order lookup and update its description.",
			Ambiguities: []*gensurla.CP1AnalysisAmbiguity{}, Examples: examples,
			RemovedExampleIds: []string{}, PreservedBehaviors: []string{},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, revised.ProposedRevision)
	_, assignment := generationConfirmBuild(t, cp, revised,
		revised.ProposedRevision.RequestRevision, revised.ProposedRevision.Examples)
	return assignment
}

// generationConfirmBuild uses the same user's explicit replacement identity
// when a build is already open; reading the assignment then joins that build.
func generationConfirmBuild(
	t *testing.T, cp *ControlPlane, card *gensurla.ChangeCardResult,
	revision string, examples []*gensurla.ExampleItem,
) (*gensurla.ChangeCardResult, *gensurla.BuilderAssignment) {
	t.Helper()
	user := asCaller("developer@example.com", CallerUser)
	confirmed, err := cp.ConfirmIntent(user, &gensurla.ConfirmIntentPayload{
		ChangeID: card.ChangeID, RequestRevision: revision,
		Answers: map[string]string{}, Examples: examples,
	})
	require.NoError(t, err)
	_, err = startBuildAgain(t, cp, user, confirmed.ChangeID, confirmed.RequestRevision)
	require.NoError(t, err)
	assignment, err := cp.GetBuilderAssignment(asCaller("developer@example.com", CallerBuilder),
		&gensurla.GetBuilderAssignmentPayload{ChangeID: confirmed.ChangeID})
	require.NoError(t, err)
	return confirmed, assignment
}

// generationRefresh waits for the actual owner's terminal result, preserving
// its diagnostic and allowing cleanup beyond the owner's five-minute deadline.
func generationRefresh(t *testing.T, cp *ControlPlane) *gensurla.StoryRefreshResult {
	t.Helper()
	guide := asCaller("developer@example.com", CallerGuide)
	started, err := cp.RefreshStory(guide)
	require.NoError(t, err)
	require.NotNil(t, started.OpID)
	var result *gensurla.StoryRefreshResult
	var readErr error
	require.Eventually(t, func() bool {
		result, readErr = cp.GetStoryRefresh(guide)
		return readErr != nil || result.State == storyRefreshStateSucceeded || result.State == storyRefreshStateFailed
	}, defaultStoryRefreshTimeout+30*time.Second, 25*time.Millisecond,
		"refresh %s did not finish", *started.OpID)
	require.NoError(t, readErr)
	require.Equal(t, storyRefreshStateSucceeded, result.State, "evaluation failed: %v", result.Diagnostic)
	require.Equal(t, started.OpID, result.OpID)
	return result
}

// generationRequest bounds one generation after its fixture and build are
// ready; earlier generation calls do not consume this request's budget.
func generationRequest(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)
	return ctx
}
