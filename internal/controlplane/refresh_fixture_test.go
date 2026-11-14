// This file supplies the real refresh producer for workspace/API journey tests.
// Downstream consumer tests use explicitly authored saved-card fixtures instead.
package controlplane

import (
	"context"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/stretchr/testify/require"
)

// mustProposeFromRefresh evaluates the actual draft through RefreshStory and
// GetStoryRefresh, then proposes exactly that succeeded operation's snapshot.
// It neither seeds an operation nor bypasses evaluation or its failures.
func mustProposeFromRefresh(
	t *testing.T, cp *ControlPlane, caller context.Context,
	changeID, prompt string, analysis *gensurla.CP1Analysis,
) *gensurla.ChangeCardResult {
	t.Helper()
	refreshed := mustRefreshStory(t, cp, caller)
	card, err := cp.ProposeChange(caller, &gensurla.ProposeChangePayload{
		ChangeID: changeID, Prompt: prompt, Analysis: analysis, StoryRefreshOpID: refreshed.OpID,
	})
	require.NoError(t, err)
	return card
}

// mustRefreshStory waits for one real operation, stopping immediately on either
// terminal state and retaining its diagnostic when evaluation cannot succeed.
func mustRefreshStory(
	t *testing.T, cp *ControlPlane, caller context.Context,
) *gensurla.StoryRefreshResult {
	t.Helper()
	started, err := cp.RefreshStory(caller)
	require.NoError(t, err)
	require.NotNil(t, started.OpID)
	var result *gensurla.StoryRefreshResult
	var readErr error
	require.Eventually(t, func() bool {
		result, readErr = cp.GetStoryRefresh(caller)
		return readErr != nil || result.State == storyRefreshStateSucceeded || result.State == storyRefreshStateFailed
	}, 2*time.Minute, 25*time.Millisecond, "refresh %s did not finish", *started.OpID)
	require.NoError(t, readErr)
	require.Equal(t, storyRefreshStateSucceeded, result.State, "evaluation failed: %v", result.Diagnostic)
	require.Equal(t, started.OpID, result.OpID)
	return result
}
