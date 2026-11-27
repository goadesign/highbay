// This file shares real Story refresh production between command journey tests.
package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/controlplane"
)

// mustRefreshStory starts and observes one real owner operation. It retains the
// last state and diagnostic on timeout, stops at either terminal state or read
// error, and returns only the same succeeded operation for adoption.
func mustRefreshStory(
	t *testing.T, cp *controlplane.ControlPlane, caller context.Context,
) *genhighbay.StoryRefreshResult {
	t.Helper()
	started, err := cp.RefreshStory(caller)
	require.NoError(t, err)
	require.NotNil(t, started.OpID)
	var result *genhighbay.StoryRefreshResult
	var readErr error
	var diagnostic string
	// The control plane allows five minutes per real evaluation; tests add
	// thirty seconds for terminal publication/cleanup, without changing that owner.
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
		assert.Contains(collect, []string{"succeeded", "failed"}, result.State,
			"refresh %s last state: %s; diagnostic: %s", *started.OpID, result.State, diagnostic)
	}, 5*time.Minute+30*time.Second, 25*time.Millisecond)
	require.NoError(t, readErr)
	require.Equal(t, "succeeded", result.State, "evaluation failed: %s", diagnostic)
	require.Equal(t, started.OpID, result.OpID)
	return result
}
