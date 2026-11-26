// This file supplies the real refresh producer for workspace/API journey tests.
// Downstream consumer tests use explicitly authored saved-card fixtures instead.
package controlplane

import (
	"context"
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustProposeFromRefresh evaluates the actual draft through RefreshStory and
// GetStoryRefresh, then proposes exactly that succeeded operation's snapshot.
// It neither seeds an operation nor bypasses evaluation or its failures.
func mustProposeFromRefresh(
	t *testing.T, cp *ControlPlane, caller context.Context,
	changeID, prompt string, analysis *genhighbay.CP1Analysis,
) *genhighbay.ChangeCardResult {
	t.Helper()
	refreshed := mustRefreshStory(t, cp, caller)
	card, err := cp.ProposeChange(caller, &genhighbay.ProposeChangePayload{
		ChangeID: changeID, Prompt: prompt, Analysis: analysis, StoryRefreshOpID: refreshed.OpID,
	})
	require.NoError(t, err)
	return card
}

// mustRefreshStory waits for one real operation, stopping immediately on either
// terminal state and retaining its diagnostic when evaluation cannot succeed.
func mustRefreshStory(
	t *testing.T, cp *ControlPlane, caller context.Context,
) *genhighbay.StoryRefreshResult {
	t.Helper()
	started, err := cp.RefreshStory(caller)
	require.NoError(t, err)
	require.NotNil(t, started.OpID)
	var result *genhighbay.StoryRefreshResult
	var readErr error
	var diagnostic string
	// Observe the owner's five-minute evaluation deadline and bounded cleanup.
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		result, readErr = cp.GetStoryRefresh(caller)
		if readErr != nil {
			return // Stop immediately; the owning read error is asserted below.
		}
		if !assert.NotNil(collect, result, "refresh %s returned no state", *started.OpID) {
			return
		}
		diagnostic = ""
		if result.Diagnostic != nil {
			diagnostic = *result.Diagnostic
		}
		assert.Contains(collect, []string{storyRefreshStateSucceeded, storyRefreshStateFailed},
			result.State, "refresh %s last state: %s; diagnostic: %s", *started.OpID, result.State, diagnostic)
	}, defaultStoryRefreshTimeout+30*time.Second, 25*time.Millisecond)
	require.NoError(t, readErr)
	require.Equal(t, storyRefreshStateSucceeded, result.State, "evaluation failed: %s", diagnostic)
	require.Equal(t, started.OpID, result.OpID)
	return result
}
