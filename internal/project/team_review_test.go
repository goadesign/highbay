// This file verifies live adoption, non-modal launch and exact worker teardown.
package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	genproject "github.com/goadesign/surla/gen/project"
	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/controlplane"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAttachGuideAdoptsExistingPaneAndKeepsItsCredential(t *testing.T) {
	state := testState(t)
	deps := fixtureTeamDependencies(state)
	deps.CompanionInstalled = true
	host := deps.Host.(*teamFixture)
	host.overlays = []string{"surla"}
	s, err := NewServer(state, func() {}, deps)
	require.NoError(t, err)
	result := attachFixtureGuide(t, s, host)
	assert.Equal(t, "attached", result.HerdrMode)
	assert.Equal(t, "antigravity", host.terminals[0].Labels["chorus.agent"])
	assert.Equal(t, "role:"+result.Handle, host.terminals[0].Labels["chorus.row"])
	assert.Empty(t, host.choices)
	assert.NotContains(t, host.calls, "companion")
	assert.NotContains(t, host.calls, "focus")
	assert.Zero(t, host.creates)
	assert.NoDirExists(t, deps.RosterDirectory)
	auth, err := os.ReadFile(state.AgentFile(result.Handle))
	require.NoError(t, err)

	host.overlays, host.calls = nil, nil
	address := "t-guide"
	again, err := s.AttachGuide(teamContext(state), &genproject.AttachGuidePayload{HcTerminalID: &address})
	require.NoError(t, err)
	assert.Equal(t, result, again)
	assert.NotContains(t, host.calls, "companion", "an overlay closed between attaches stays closed")
	current, err := os.ReadFile(state.AgentFile(result.Handle))
	require.NoError(t, err)
	assert.Equal(t, auth, current)
	loaded, err := Discover(filepath.Dir(state.Dir), state.Record.Directory)
	require.NoError(t, err)
	assert.Equal(t, "space-guide", loaded.Record.Space)
	assert.Equal(t, "tab-guide", loaded.Record.Tab)
	assert.True(t, loaded.Record.CompanionShown)
}

func TestAttachGuideRefusesConflictedPanes(t *testing.T) {
	for _, scenario := range []string{"not live", "idle shell", "no model pane", "remote", "other tab", "foreign root", "foreign swarm"} {
		t.Run(scenario, func(t *testing.T) {
			state := testState(t)
			state.Record.Team = "home"
			deps := fixtureTeamDependencies(state)
			host := deps.Host.(*teamFixture)
			host.spaces = []TeamSpace{{
				ID: "space", Tabs: []TeamTab{{ID: "tab", Terminals: []TeamPane{{Terminal: "target", Pane: "pane"}}}},
			}}
			host.terminals = []TeamTerminal{{Address: "target", State: "live", Foreground: &TeamForeground{Argv: []string{"codex"}}}}
			switch scenario {
			case "not live":
				host.terminals[0].State = "finished"
			case "idle shell":
				host.terminals[0].Foreground = &TeamForeground{Argv: []string{"zsh"}}
			case "no model pane":
				host.spaces[0].Tabs[0].Terminals[0].Pane = ""
			case "remote":
				machine := "other"
				host.spaces[0].Machine = &machine
			case "other tab":
				host.spaces[0].Tabs = append(host.spaces[0].Tabs, TeamTab{ID: "duplicate", Terminals: []TeamPane{{Terminal: "other", Pane: "p-other"}}})
				host.terminals = append(host.terminals, TeamTerminal{Address: "other", State: "live", Labels: map[string]string{"chorus.project": state.Record.Directory}})
			case "foreign root", "foreign swarm":
				host.spaces[0].Tabs[0].Terminals = append(host.spaces[0].Tabs[0].Terminals, TeamPane{Terminal: "other", Pane: "p-other"})
				labels := map[string]string{"chorus.project": "/other"}
				if scenario == "foreign swarm" {
					labels = map[string]string{"chorus.swarm": "other"}
				}
				host.terminals = append(host.terminals, TeamTerminal{Address: "other", State: "live", Labels: labels})
			}
			s, err := NewServer(state, func() {}, deps)
			require.NoError(t, err)
			address := "target"
			result, err := s.AttachGuide(teamContext(state), &genproject.AttachGuidePayload{HcTerminalID: &address})
			require.NoError(t, err)
			assert.False(t, result.AdoptedHerdr)
			assert.Equal(t, "available", result.HerdrMode)
			assert.Len(t, state.Record.Agents, 1, "the guide still authenticates outside a team pane")
			assert.Empty(t, state.Record.Space)
			assert.NotContains(t, host.calls, "label")
			assert.Zero(t, host.creates)
		})
	}
}

func TestStartAgentsUsesWorkerRosterAndPreservesLiveGuide(t *testing.T) {
	for _, mode := range []string{"attached", "available", "idle guide"} {
		t.Run(mode, func(t *testing.T) {
			state := testState(t)
			deps := fixtureTeamDependencies(state)
			host := deps.Host.(*teamFixture)
			s, err := NewServer(state, func() {}, deps)
			require.NoError(t, err)
			if mode != "available" {
				attachFixtureGuide(t, s, host)
			}
			if mode == "idle guide" {
				host.terminals[0].Foreground = &TeamForeground{Argv: []string{"zsh"}}
			}
			fixtureAssignments(s, true)
			builder := state.Record.Directory
			writer := t.TempDir()
			require.NoError(t, s.RegisterBuildRoles(t.Context(), controlplane.BuilderAndTestWriter{
				BuilderFolder: builder, TestWriterFolder: writer,
			}))
			host.calls = nil
			options, err := s.StartAgents(t.Context(), nil)
			require.NoError(t, err)
			assert.NotContains(t, host.calls, "focus")
			assert.NotContains(t, host.calls, "companion")
			require.Len(t, host.choices, 1)
			assert.ElementsMatch(t, []string{"builder", "test_writer"}, keys(host.choices[0]))
			assert.Equal(t, "codex", host.choices[0]["builder"].Client)
			assert.Equal(t, "claude", host.choices[0]["test_writer"].Client)
			raw, err := os.ReadFile(filepath.Join(deps.RosterDirectory, options.Space+".json"))
			require.NoError(t, err)
			var roster launcherRoster
			require.NoError(t, json.Unmarshal(raw, &roster))
			workerRoles := roster.Roles
			if mode == "attached" {
				require.Len(t, workerRoles, 3)
				assert.Equal(t, "guide", workerRoles[0].Role)
				assert.Nil(t, workerRoles[0].Place)
				assert.Equal(t, &launcherPlace{Beside: workerRoles[0].Handle, Direction: "right"}, workerRoles[1].Place)
				workerRoles = workerRoles[1:]
			} else {
				require.Len(t, workerRoles, 2)
				assert.Nil(t, workerRoles[0].Place)
			}
			require.Len(t, workerRoles, 2)
			assert.Equal(t, builder, workerRoles[0].Folder)
			assert.Equal(t, writer, workerRoles[1].Folder)
			assert.Equal(t, &launcherPlace{Beside: workerRoles[0].Handle, Direction: "down"}, workerRoles[1].Place)
			assert.Contains(t, workerRoles[0].FirstMessage, "Keep your Chorus watcher running.")
			assert.Contains(t, workerRoles[1].FirstMessage, "When its verdict is in")
			assert.Equal(t, len(roster.Roles), countEmptyConnections(t, raw))
			assert.NotContains(t, string(raw), "credential_file")
		})
	}
}

func TestStartAgentsValidatesAndPersistsPreferences(t *testing.T) {
	state := testState(t)
	deps := fixtureTeamDependencies(state)
	host := deps.Host.(*teamFixture)
	discover := 0
	deps.DiscoverAgents = func(context.Context) ([]*gensurla.InstalledAgentOption, error) {
		discover++
		return []*gensurla.InstalledAgentOption{{Client: "claude", Program: "claude", ModelSupported: true}}, nil
	}
	s, err := NewServer(state, func() {}, deps)
	require.NoError(t, err)
	fixtureAssignments(s, true)
	require.NoError(t, s.RegisterBuildRoles(t.Context(), controlplane.BuilderAndTestWriter{
		BuilderFolder: state.Record.Directory, TestWriterFolder: t.TempDir(),
	}))
	model := "sonnet"
	_, err = s.StartAgents(t.Context(), map[string]*gensurla.RoleAgentChoice{
		"builder": {Client: "claude", Program: "/uninstalled", Model: &model},
	})
	require.Error(t, err)
	assert.Empty(t, host.calls)
	assert.Empty(t, state.Record.SavedChoices)
	_, err = s.StartAgents(t.Context(), map[string]*gensurla.RoleAgentChoice{
		"builder": {Client: "claude", Program: "claude", Model: &model},
	})
	require.NoError(t, err)
	assert.Equal(t, "claude", host.choices[0]["test_writer"].Client)
	require.NoError(t, s.DismissNotice(t.Context(), "single_agent_family"))
	require.NoError(t, s.DismissNotice(t.Context(), "single_agent_family"))
	require.Error(t, s.DismissNotice(t.Context(), "unknown"))
	loaded, err := Discover(filepath.Dir(state.Dir), state.Record.Directory)
	require.NoError(t, err)
	fresh, err := NewServer(loaded, func() {}, deps)
	require.NoError(t, err)
	snapshot, err := fresh.ReadTeam(t.Context())
	require.NoError(t, err)
	require.NotNil(t, snapshot.SavedChoices["builder"].Model)
	assert.Equal(t, "sonnet", *snapshot.SavedChoices["builder"].Model)
	assert.Equal(t, []string{"single_agent_family"}, snapshot.DismissedNotices)
	assert.Equal(t, 2, discover, "reading the persisted snapshot needs no CLI")
	fixtureAssignments(fresh, true)
	_, err = fresh.StartAgents(t.Context(), nil)
	require.NoError(t, err)
	assert.Equal(t, snapshot.SavedChoices, host.choices[1])
}

func TestStopWorkersChecksBuildIdentityAndOwnedPanes(t *testing.T) {
	state := testState(t)
	deps := fixtureTeamDependencies(state)
	host := deps.Host.(*teamFixture)
	s, err := NewServer(state, func() {}, deps)
	require.NoError(t, err)
	attachFixtureGuide(t, s, host)
	require.NoError(t, s.RegisterBuildRoles(t.Context(), controlplane.BuilderAndTestWriter{
		BuilderFolder: state.Record.Directory, TestWriterFolder: t.TempDir(),
	}))
	for _, role := range []string{"builder", "test_writer", "unrelated"} {
		host.spaces[0].Tabs[0].Terminals = append(host.spaces[0].Tabs[0].Terminals, TeamPane{Terminal: "t-" + role, Pane: "p-" + role})
		host.terminals = append(host.terminals, TeamTerminal{
			Address: "t-" + role, State: "live", Labels: map[string]string{
				"chorus.project": state.Record.Directory, "chorus.swarm": state.Record.Team,
				"herdr.space": state.Record.Space, "herdr.tab": state.Record.Tab,
				"chorus.role": role, "chorus.agent": "codex", "chorus.handle": teamName(role, state.Record.ID),
			},
		})
	}
	var assignments controlplane.TeamAssignments
	s.BindAssignments(func(context.Context) (controlplane.TeamAssignments, error) { return assignments, nil })
	build := controlplane.BuildIdentity{ChangeID: "change", StartedAt: time.Unix(20, 0)}
	newer := controlplane.BuildIdentity{ChangeID: "change", StartedAt: time.Unix(21, 0)}
	assignments = controlplane.TeamAssignments{BuildIdentity: &newer, Merged: true}
	require.NoError(t, s.StopWorkers(t.Context(), build))
	assignments = controlplane.TeamAssignments{BuildIdentity: &build}
	require.NoError(t, s.StopWorkers(t.Context(), build))
	assert.Empty(t, host.closed, "no work alone does not prove a build was merged or dropped")
	assignments.Dropped = true
	host.closeErr = assert.AnError
	require.Error(t, s.StopWorkers(t.Context(), build))
	host.closeErr = nil
	host.closed = nil
	require.NoError(t, s.StopWorkers(t.Context(), build))
	require.NoError(t, s.StopWorkers(t.Context(), build))
	assert.ElementsMatch(t, []string{"p-builder", "p-test_writer"}, host.closed)
}

func countEmptyConnections(t *testing.T, raw []byte) int {
	t.Helper()
	var roster struct {
		Roles []struct {
			Connections json.RawMessage `json:"connections"`
		} `json:"roles"`
	}
	require.NoError(t, json.Unmarshal(raw, &roster))
	for _, role := range roster.Roles {
		assert.JSONEq(t, "[]", string(role.Connections))
	}
	return len(roster.Roles)
}

func TestStartAgentsExplicitGuideRequiresNoLiveSession(t *testing.T) {
	for _, scenario := range []string{"missing", "active pane", "external watcher"} {
		t.Run(scenario, func(t *testing.T) {
			state := testState(t)
			deps := fixtureTeamDependencies(state)
			guideHandle := teamName("guide", state.Record.ID)
			observer := &supervisionFixture{watch: func(context.Context, string) (TeamWatcherView, error) {
				return TeamWatcherView{Held: map[string]bool{guideHandle: scenario == "external watcher"}}, nil
			}}
			deps.Connect = func(context.Context) (TeamConnection, error) {
				return TeamConnection{Endpoint: "http://chorus.test", Observer: observer}, nil
			}
			host := deps.Host.(*teamFixture)
			s, err := NewServer(state, func() {}, deps)
			require.NoError(t, err)
			s.BindAssignments(func(context.Context) (controlplane.TeamAssignments, error) {
				return controlplane.TeamAssignments{}, nil
			})
			if scenario == "active pane" {
				attachFixtureGuide(t, s, host)
			}
			choices := map[string]*gensurla.RoleAgentChoice{"guide": {Client: "codex", Program: "codex"}}
			result, err := s.StartAgents(t.Context(), choices)
			if scenario != "missing" {
				require.ErrorContains(t, err, "current guide session is already running")
				assert.Empty(t, host.choices)
				return
			}
			require.NoError(t, err)
			require.Len(t, host.choices, 1)
			assert.Equal(t, choices, host.choices[0])
			assert.Equal(t, "guide", state.Record.Agents[guideHandle].Role)
			var roster launcherRoster
			require.NoError(t, readJSON(filepath.Join(deps.RosterDirectory, result.Space+".json"), &roster))
			require.Len(t, roster.Roles, 1)
			assert.Equal(t, guideHandle, roster.Roles[0].Handle)
			assert.Nil(t, roster.Roles[0].Place)
		})
	}
}
