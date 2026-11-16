// This file tests the persisted distinction between missing and empty Story evidence.
package verifyrun

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRequestStoryContextPersistence checks the owner boundary, including old
// records, malformed explicit contexts and newly written standalone requests.
func TestRequestStoryContextPersistence(t *testing.T) {
	runID := NewRunID()
	req := Request{RunID: runID, Owner: "cli", AcceptedAt: time.Now().UTC(), Plan: evaluatorPlan}
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"story_context":{"state":"not_recorded"}`)
	var original map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &original))
	for _, tc := range []struct {
		name    string
		context string
		state   StoryContextState
		wantErr string
	}{
		{name: "old request without field", state: StoryNotRecorded},
		{name: "explicit not recorded", context: `{"state":"not_recorded"}`, state: StoryNotRecorded},
		{name: "recorded empty Story", context: `{"state":"recorded","build_number":1,"build_started_at":"2026-11-01T12:00:00Z","stories":[]}`, state: StoryRecorded},
		{name: "missing state", context: `{}`, wantErr: "state is required"},
		{name: "null context", context: `null`, wantErr: "state is required"},
		{name: "unknown state", context: `{"state":"guessed"}`, wantErr: "unknown Story context state"},
		{name: "unknown field", context: `{"state":"not_recorded","guess":true}`, wantErr: "unknown field"},
		{name: "recorded without binding", context: `{"state":"recorded"}`, wantErr: "requires its build"},
		{name: "unrecorded with evidence", context: `{"state":"not_recorded","build_number":1}`, wantErr: "must not carry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := make(map[string]json.RawMessage, len(original))
			for key, value := range original {
				wire[key] = value
			}
			if tc.context == "" {
				delete(wire, "story_context")
			} else {
				wire["story_context"] = json.RawMessage(tc.context)
			}
			input, err := json.Marshal(wire)
			require.NoError(t, err)
			got, err := decodeRequest(runID, input)
			if tc.wantErr != "" {
				assert.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.state, got.StoryContext.State)
			assert.Empty(t, got.StoryContext.Stories)
			assert.Equal(t, "cli", got.Owner)
		})
	}
}
