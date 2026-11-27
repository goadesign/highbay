// This file checks that the user's conversation and terminal operations reach
// the project's team owner, and only for the user who owns the project.
package controlplane

import (
	"context"
	"testing"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTeamOperationsForwardToTheOwner calls each conversation and terminal
// operation twice. Called by the project's owner, the team owner receives the
// request's own argument and its result comes back unchanged. Called by
// another user, the operation is refused as forbidden before the team owner
// sees a call. The terminal stream's forwarded case needs an upgraded
// connection, so TestRoleTerminalStream owns it.
func TestTeamOperationsForwardToTheOwner(t *testing.T) {
	message := &genhighbay.SendTeamMessagePayload{To: "builder", Text: "Please rerun the checks."}
	conversation := genhighbay.TeamConversation{
		Team: "forward-team", Channel: "#forward-team-8f3a", ChorusStatus: "unavailable",
		Pin:      fixtureTeamMessage("Team manifest"),
		Messages: []*genhighbay.TeamMessage{fixtureTeamMessage("The tests are accepted.")},
	}
	for _, tc := range []struct {
		name string
		// call runs the operation and returns its result, nil when it has none.
		call func(context.Context, *ControlPlane) (any, error)
		// calls is what the team owner records for the project's owner.
		calls []string
		// result is what the project's owner gets back.
		result any
		// streams marks the operation whose forwarded case another test owns.
		streams bool
	}{
		{
			name: "get_team_conversation",
			call: func(ctx context.Context, cp *ControlPlane) (any, error) {
				return cp.GetTeamConversation(ctx)
			},
			calls: []string{"conversation"}, result: &conversation,
		},
		{
			name: "send_team_message",
			call: func(ctx context.Context, cp *ControlPlane) (any, error) {
				return cp.SendTeamMessage(ctx, message)
			},
			calls: []string{"send:builder"}, result: fixtureTeamMessage(message.Text),
		},
		{
			name: "interrupt_team_role",
			call: func(ctx context.Context, cp *ControlPlane) (any, error) {
				return nil, cp.InterruptTeamRole(ctx, &genhighbay.InterruptTeamRolePayload{Role: "builder"})
			},
			calls: []string{"interrupt:builder"},
		},
		{
			name: "stop_team_role",
			call: func(ctx context.Context, cp *ControlPlane) (any, error) {
				return nil, cp.StopTeamRole(ctx, &genhighbay.StopTeamRolePayload{Role: "test_writer"})
			},
			calls: []string{"stop:test_writer"},
		},
		{
			name: "restart_team_role with the saved coding agent",
			call: func(ctx context.Context, cp *ControlPlane) (any, error) {
				return nil, cp.RestartTeamRole(ctx, &genhighbay.RestartTeamRolePayload{Role: "builder"})
			},
			calls: []string{"restart:builder:saved"},
		},
		{
			name: "restart_team_role with another coding agent",
			call: func(ctx context.Context, cp *ControlPlane) (any, error) {
				return nil, cp.RestartTeamRole(ctx, &genhighbay.RestartTeamRolePayload{
					Role: "guide", Choice: &genhighbay.RoleAgentChoice{Client: "codex", Program: "/bin/codex"},
				})
			},
			calls: []string{"restart:guide:codex"},
		},
		{
			name: "create_terminal_ticket",
			call: func(ctx context.Context, cp *ControlPlane) (any, error) {
				return cp.CreateTerminalTicket(ctx, &genhighbay.CreateTerminalTicketPayload{Role: "guide"})
			},
			calls:  []string{"ticket:guide"},
			result: &genhighbay.TerminalTicketResult{Ticket: fixtureTerminalTicket, ExpiresAt: "2026-11-26T12:00:30Z"},
		},
		{
			name: "attach_role_terminal", streams: true,
			call: func(ctx context.Context, cp *ControlPlane) (any, error) {
				return nil, cp.AttachRoleTerminal(ctx, &genhighbay.AttachRoleTerminalPayload{
					Role: "builder", Ticket: fixtureTerminalTicket, Cols: 100, Rows: 30,
				}, nil)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			team := &teamServiceFixture{conversation: conversation}
			cp := newControlPlane(t, Options{Team: &TeamBinding{Owner: "alice@example.com", Service: team}})

			_, err := tc.call(asCaller("bob@example.com", CallerUser), cp)
			assertRefused(t, err, "forbidden")
			assert.Empty(t, team.recorded(), "another user's call reached the team owner")
			if tc.streams {
				return
			}

			result, err := tc.call(asCaller("alice@example.com", CallerUser), cp)
			require.NoError(t, err)
			assert.Equal(t, tc.calls, team.recorded())
			assert.Equal(t, tc.result, result)
			if tc.name == "send_team_message" {
				assert.Same(t, message, team.sent, "the team owner received another message than the user's")
			}
		})
	}
}
