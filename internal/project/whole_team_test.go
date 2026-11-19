// This file proves the actual team operation preserves every registered trusted role.
package project

import (
	"os"
	"path/filepath"
	"testing"

	genproject "github.com/goadesign/surla/gen/project"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAgentRoleAdmissionIsIdempotent preserves one stable handle and credential per role.
func TestAgentRoleAdmissionIsIdempotent(t *testing.T) {
	for _, role := range []string{"guide", "builder", "test_writer"} {
		t.Run(role, func(t *testing.T) {
			state := testState(t)
			server, err := NewServer(state, func() {}, fixtureTeamDependencies(state))
			require.NoError(t, err)
			first, err := registerTestAgent(server, role+"-first", role, state.Record.Directory)
			require.NoError(t, err)
			before, err := os.ReadFile(first.CredentialFile)
			require.NoError(t, err)
			second, err := registerTestAgent(server, role+"-second", role, state.Record.Directory)
			require.NoError(t, err)
			assert.Equal(t, first, second)
			after, err := os.ReadFile(first.CredentialFile)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			require.Len(t, state.Record.Agents, 1)
		})
	}
}

// TestStoredDuplicateRoleIsRejected validates the same invariant when a project record is loaded.
func TestStoredDuplicateRoleIsRejected(t *testing.T) {
	state := testState(t)
	state.Record.Agents = map[string]agentRecord{"first": {Role: "guide", Folder: state.Record.Directory}, "second": {Role: "guide", Folder: state.Record.Directory}}
	require.NoError(t, state.save())
	_, err := Discover(filepath.Dir(state.Dir), state.Record.Directory)
	require.ErrorContains(t, err, `invalid stored role "guide"`)
}

// TestExistingCustomGuideIsReused keeps its stable actor ID and persistent credential.
func TestExistingCustomGuideIsReused(t *testing.T) {
	state := testState(t)
	server, err := NewServer(state, func() {}, fixtureTeamDependencies(state))
	require.NoError(t, err)
	actor, err := registerTestAgent(server, "long-lived-guide", "guide", server.state.Record.Directory)
	require.NoError(t, err)
	before, err := os.ReadFile(actor.CredentialFile)
	require.NoError(t, err)
	team, err := server.AttachGuide(teamContext(state), &genproject.AttachGuidePayload{})
	require.NoError(t, err)
	assert.Equal(t, actor.ActorID, team.Handle)
	require.Len(t, state.Record.Agents, 1)
	after, err := os.ReadFile(actor.CredentialFile)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	_, err = os.Stat(state.AgentFile(teamName("guide", state.Record.ID)))
	assert.ErrorIs(t, err, os.ErrNotExist)
}
