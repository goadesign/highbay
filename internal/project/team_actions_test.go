// This file verifies user actions with fresh recorded ownership and no real window effects.
package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	genproject "github.com/goadesign/surla/gen/project"
	"github.com/goadesign/surla/internal/controlplane"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goa "goa.design/goa/v3/pkg"
)

// TestTeamReadCreatesNothing returns a stable expected name and registered roles only.
func TestTeamReadCreatesNothing(t *testing.T) {
	state := testState(t)
	deps := fixtureTeamDependencies(state)
	host := deps.Host.(*teamFixture)
	server, err := NewServer(state, func() {}, deps)
	require.NoError(t, err)
	before, err := os.ReadDir(state.Dir)
	require.NoError(t, err)
	first, err := server.ReadTeam(t.Context())
	require.NoError(t, err)
	second, err := server.ReadTeam(t.Context())
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.NotEmpty(t, first.Team)
	assert.Empty(t, first.Roles)
	assert.Empty(t, host.calls)
	assert.Empty(t, state.Record.Team)
	assert.Empty(t, state.Record.Agents)
	after, err := os.ReadDir(state.Dir)
	require.NoError(t, err)
	assert.Equal(t, len(before), len(after))
}

// TestTeamShowPaneOverlayAndOwnership refuses all open overlays before any
// discovery/window operation, and never focuses a differently owned fresh pane.
func TestTeamShowPaneOverlayAndOwnership(t *testing.T) {
	for _, scenario := range []string{"owned", "companion", "launcher", "other overlay", "wrong project", "wrong space", "wrong handle", "ended", "unregistered", "space folder changed"} {
		t.Run(scenario, func(t *testing.T) {
			o, f, _ := newSupervisionFixture(t)
			host := o.server.team.dependencies.Host.(*teamFixture)
			host.spaces = []TeamSpace{{ID: "space", Folder: o.selection.Root, Tabs: []TeamTab{{ID: "tab", Terminals: []TeamPane{{Terminal: "other-pane"}, {Terminal: "terminal"}}}}}}
			role := "builder"
			switch scenario {
			case "companion":
				host.overlays = []string{"surla"}
			case "launcher":
				host.overlays = []string{"chorus-launcher"}
			case "other overlay":
				host.overlays = []string{"frame-palette"}
			case "wrong project":
				f.terminal.Labels["chorus.project"] = "/other"
			case "wrong space":
				f.terminal.Labels["herdr.space"] = "other"
			case "wrong handle":
				f.terminal.Labels["chorus.handle"] = "other"
			case "ended":
				f.terminal.State = "finished"
			case "unregistered":
				role = "guide"
			case "space folder changed":
				host.spaces[0].Folder = "/other"
				host.spaces[0].Name = "renamed by user"
			}
			host.terminals = []TeamTerminal{f.terminal}
			err := o.server.ShowPane(t.Context(), role)
			if scenario == "owned" || scenario == "space folder changed" || scenario == "companion" {
				require.NoError(t, err)
				assert.Equal(t, TeamSpace{ID: "space", Name: host.spaces[0].Name, Folder: host.spaces[0].Folder, Tabs: []TeamTab{{ID: "tab", Terminals: []TeamPane{{Terminal: "terminal"}}}}}, host.focused)
				assert.Equal(t, []string{"overlays", "terminals", "spaces", "read:terminal", "focus"}, host.calls)
			} else {
				var refusal *goa.ServiceError
				require.ErrorAs(t, err, &refusal)
				want := "pane_unavailable"
				if scenario == "launcher" || scenario == "other overlay" {
					want = "window_busy"
				}
				assert.Equal(t, want, refusal.Name)
				assert.NotContains(t, host.calls, "focus")
				assert.Empty(t, host.focused.ID)
			}
			if scenario == "launcher" || scenario == "other overlay" {
				assert.Equal(t, []string{"overlays"}, host.calls)
				assert.Empty(t, f.reads)
			}
		})
	}
}

// TestShowPaneHerdrFailureNamesRetry gives a failed Show pane the Herdr step without
// sending the user to start agents that are already running: the step retries the
// action the user took.
func TestShowPaneHerdrFailureNamesRetry(t *testing.T) {
	o, _, _ := newSupervisionFixture(t)
	host := o.server.team.dependencies.Host.(*teamFixture)
	o.server.team.dependencies.Host = failingOverlays{host}
	err := o.server.ShowPane(t.Context(), "builder")
	var page *goa.ServiceError
	require.ErrorAs(t, err, &page)
	assert.Equal(t, "team_unavailable", page.Name)
	assert.Contains(t, page.Message, "open the Herdr app, then try again")
	assert.NotContains(t, page.Message, "start the agents")
}

func TestUserTeamErrorBoundary(t *testing.T) {
	conflict := &controlplane.TeamSpaceConflict{Count: 2}
	wrapped := fmt.Errorf("private peer output: %w", conflict)
	assert.Same(t, conflict, userTeamError(conflict))
	assert.Same(t, wrapped, userTeamError(wrapped))
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"window_busy", &goa.ServiceError{Name: "window_busy", Message: "private peer output"}},
		{"team_unavailable", &genproject.TeamUnavailable{Name: "team_unavailable", Cause: "herdr"}},
		{"team_unavailable", fmt.Errorf("private peer output: %w", &genproject.TeamUnavailable{Name: "team_unavailable", Cause: "herdr"})},
		{"team_unavailable", errors.New("private peer output")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var failure *goa.ServiceError
			require.ErrorAs(t, userTeamError(tc.err), &failure)
			assert.Equal(t, tc.name, failure.Name)
			assert.NotContains(t, failure.Message, "private peer output")
			assert.NotContains(t, failure.Message, "check Herdr")
			var typed *genproject.TeamUnavailable
			if errors.As(tc.err, &typed) {
				assert.Same(t, typed, errors.Unwrap(failure), "only the typed failure is kept")
			}
		})
	}
}

// TestUserTeamStepNamesARealRetry gives every team failure cause, and an
// unknown one, a step whose retry the user can take from the page, a coding
// agent or the Herdr companion. None tells the user to type surla: since
// agents attach through surla stdio, surla neither prints a team step nor
// starts agents.
func TestUserTeamStepNamesARealRetry(t *testing.T) {
	for _, tc := range []struct{ cause, want string }{
		{"stopping", "ask your coding agent to open Surla views, or choose Start Surla in the Herdr companion, which starts it again and opens a fresh page, then use Team → Start workers there"},
		{"chorus", "Run chorus up (or chorus status to see the error), then try again."},
		{"herdr", "open the Herdr app, then try again"},
		{"saved_space", "Close this project's obsolete Herdr space, then start the agents again."},
		{"roster_folder", "then start the agents again"},
		{"project_state", "this project's private state could not be read or written; try again, and if it keeps failing, restore this project's Surla state from a backup"},
		{"role_folder", "then start the agents again"},
		{"", "project team could not be prepared; try again"},
	} {
		t.Run(tc.cause, func(t *testing.T) {
			step := userTeamStep(tc.cause)
			assert.Contains(t, step, tc.want)
			assert.NotContains(t, strings.ToLower(step), "run surla")
		})
	}
}

type failingOverlays struct{ *teamFixture }

func (failingOverlays) Overlays(context.Context) ([]string, error) {
	return nil, errors.New("private host error")
}
