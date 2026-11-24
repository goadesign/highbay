// This file verifies live adoption, non-modal launch and exact worker teardown.
package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	again, err := s.AttachGuide(teamContext(state), &genproject.AttachGuidePayload{
		HcTerminalID: &address,
		AncestorPids: []int{4202, 4201, 4200},
	})
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
			host.terminals = []TeamTerminal{{
				Address: "target", PID: 4200, State: "live",
				Foreground: &TeamForeground{PID: 4201, Cwd: state.Record.Directory, Argv: []string{"codex"}},
			}}
			switch scenario {
			case "not live":
				host.terminals[0].State = "finished"
			case "idle shell":
				host.terminals[0].Foreground = &TeamForeground{PID: 4201, Cwd: state.Record.Directory, Argv: []string{"zsh"}}
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
			result, err := s.AttachGuide(teamContext(state), &genproject.AttachGuidePayload{
				HcTerminalID: &address,
				AncestorPids: []int{4202, 4201, 4200},
			})
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
			for _, worker := range workerRoles {
				assert.Contains(t, worker.FirstMessage, "If you cannot join Chorus or its watcher fails, do not stop and do not inspect, start, stop or reconfigure Chorus or any other service or process on this computer")
			}
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

// TestStartAgentsHonorsClientModelChoices verifies valid installed program paths
// and Antigravity's alternate model/effort forms through actual team admission.
func TestStartAgentsHonorsClientModelChoices(t *testing.T) {
	model := "configured-model-" + strings.Repeat("a", 256)
	effort := "high"
	for _, scenario := range []struct {
		name, model string
		effort      *string
		allowed     bool
	}{
		{"plain model and separate effort", model, &effort, true},
		{"suffixed model alone", model + "-high", nil, true},
		{"suffixed model and separate effort", model + "-high", &effort, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			state := testState(t)
			deps := fixtureTeamDependencies(state)
			host := deps.Host.(*teamFixture)
			program := "/installed/" + strings.Repeat("p/", 260) + "agy"
			deps.DiscoverAgents = func(context.Context) ([]*gensurla.InstalledAgentOption, error) {
				return []*gensurla.InstalledAgentOption{{
					Client: "antigravity", Program: program, ModelSupported: true,
					ThinkingSupported: true, Efforts: []string{effort},
				}}, nil
			}
			s, err := NewServer(state, func() {}, deps)
			require.NoError(t, err)
			fixtureAssignments(s, false)
			require.NoError(t, s.RegisterBuildRoles(t.Context(), controlplane.BuilderOnly{BuilderFolder: state.Record.Directory}))
			choice := &gensurla.RoleAgentChoice{
				Client: "antigravity", Program: program, Model: &scenario.model, Thinking: scenario.effort,
			}
			_, err = s.StartAgents(t.Context(), map[string]*gensurla.RoleAgentChoice{"builder": choice})
			if !scenario.allowed {
				require.Error(t, err)
				assert.Empty(t, host.calls)
				return
			}
			require.NoError(t, err)
			require.Len(t, host.choices, 1)
			assert.Equal(t, choice, host.choices[0]["builder"])
		})
	}
}

func TestAttachGuideLiveClientFallback(t *testing.T) {
	stale := "t45"
	cases := []struct {
		name         string
		terminalID   *string
		client       string
		ancestorPids []int
		setup        func(t *testing.T, state *State, host *teamFixture)
		wantAdopted  bool
		wantTerminal string
		wantAgent    string
	}{
		{
			name:         "missing HcTerminalID adopts unique Codex pane matching AncestorPids",
			client:       "codex-mcp-client",
			ancestorPids: []int{52099, 52010, 51000},
			setup: func(_ *testing.T, state *State, host *teamFixture) {
				host.spaces = []TeamSpace{{
					ID: "space-live", Tabs: []TeamTab{{ID: "tab-live", Terminals: []TeamPane{{Terminal: "t52", Pane: "p52"}}}},
				}}
				host.terminals = []TeamTerminal{{
					Address: "t52", PID: 51000, State: "live",
					Foreground: &TeamForeground{PID: 52010, Argv: []string{"/opt/homebrew/bin/codex"}, Cwd: state.Record.Directory},
				}}
			},
			wantAdopted:  true,
			wantTerminal: "t52",
			wantAgent:    "codex",
		},
		{
			name:         "missing HcTerminalID adopts node-launched Claude pane in project subdirectory via AncestorPids",
			client:       "claude-code",
			ancestorPids: []int{60199, 60100},
			setup: func(t *testing.T, state *State, host *teamFixture) {
				sub := filepath.Join(state.Record.Directory, "cmd", "api")
				require.NoError(t, os.MkdirAll(sub, 0o755))
				host.spaces = []TeamSpace{{
					ID: "space-claude", Tabs: []TeamTab{{ID: "tab-claude", Terminals: []TeamPane{{Terminal: "t60", Pane: "p60"}}}},
				}}
				host.terminals = []TeamTerminal{{
					Address: "t60", PID: 60000, State: "live",
					Foreground: &TeamForeground{PID: 60100, Argv: []string{"node", "/usr/local/bin/claude"}, Cwd: sub},
				}}
			},
			wantAdopted:  true,
			wantTerminal: "t60",
			wantAgent:    "claude",
		},
		{
			name:         "external session with unrelated AncestorPids never adopts single matching Herdr pane",
			client:       "codex",
			ancestorPids: []int{99200, 99100},
			setup: func(_ *testing.T, state *State, host *teamFixture) {
				host.spaces = []TeamSpace{{
					ID: "space-herdr", Tabs: []TeamTab{{ID: "tab-herdr", Terminals: []TeamPane{{Terminal: "t52", Pane: "p52"}}}},
				}}
				host.terminals = []TeamTerminal{{
					Address: "t52", PID: 51000, State: "live",
					Foreground: &TeamForeground{PID: 52010, Argv: []string{"codex"}, Cwd: state.Record.Directory},
				}}
			},
			wantAdopted: false,
		},
		{
			name:   "shared daemon or external session without lineage evidence never adopts active Herdr pane",
			client: "codex",
			setup: func(_ *testing.T, state *State, host *teamFixture) {
				host.spaces = []TeamSpace{{
					ID: "space-herdr", Tabs: []TeamTab{{ID: "tab-herdr", Terminals: []TeamPane{{Terminal: "t52", Pane: "p52"}}}},
				}}
				host.terminals = []TeamTerminal{{
					Address: "t52", PID: 51000, State: "live",
					Status:     &TeamStatus{State: "working"},
					Foreground: &TeamForeground{PID: 52010, Argv: []string{"codex"}, Cwd: state.Record.Directory},
				}}
			},
			wantAdopted: false,
		},
		{
			name:         "stale live HcTerminalID in another directory is rejected without adopting foreign pane",
			terminalID:   &stale,
			client:       "codex",
			ancestorPids: []int{99200, 99100},
			setup: func(t *testing.T, _ *State, host *teamFixture) {
				otherRepo := t.TempDir()
				host.spaces = []TeamSpace{{
					ID: "space-other", Tabs: []TeamTab{{ID: "tab-other", Terminals: []TeamPane{{Terminal: "t45", Pane: "p45"}}}},
				}}
				host.terminals = []TeamTerminal{{
					Address: "t45", PID: 45000, State: "live",
					Foreground: &TeamForeground{PID: 45010, Argv: []string{"codex"}, Cwd: otherRepo},
				}}
			},
			wantAdopted: false,
		},
		{
			name:         "stale live HcTerminalID running another client is rejected",
			terminalID:   &stale,
			client:       "codex",
			ancestorPids: []int{45099, 45010, 45000},
			setup: func(_ *testing.T, state *State, host *teamFixture) {
				host.spaces = []TeamSpace{{
					ID: "space-other-client", Tabs: []TeamTab{{ID: "tab-other-client", Terminals: []TeamPane{{Terminal: "t45", Pane: "p45"}}}},
				}}
				host.terminals = []TeamTerminal{{
					Address: "t45", PID: 45000, State: "live",
					Foreground: &TeamForeground{PID: 45010, Argv: []string{"claude"}, Cwd: state.Record.Directory},
				}}
			},
			wantAdopted: false,
		},
		{
			name:         "stale live HcTerminalID with zero PIDs fails closed when AncestorPids is required",
			terminalID:   &stale,
			client:       "codex",
			ancestorPids: []int{99200, 99100},
			setup: func(_ *testing.T, state *State, host *teamFixture) {
				host.spaces = []TeamSpace{{
					ID: "space-zero", Tabs: []TeamTab{{ID: "tab-zero", Terminals: []TeamPane{{Terminal: "t45", Pane: "p45"}}}},
				}}
				host.terminals = []TeamTerminal{{
					Address: "t45", PID: 0, State: "live",
					Foreground: &TeamForeground{PID: 0, Argv: []string{"codex"}, Cwd: state.Record.Directory},
				}}
			},
			wantAdopted: false,
		},
		{
			name:       "live direct HcTerminalID without AncestorPids is refused",
			terminalID: &stale,
			client:     "codex",
			setup: func(_ *testing.T, state *State, host *teamFixture) {
				host.spaces = []TeamSpace{{
					ID: "space-direct", Tabs: []TeamTab{{ID: "tab-direct", Terminals: []TeamPane{{Terminal: "t45", Pane: "p45"}}}},
				}}
				host.terminals = []TeamTerminal{{
					Address: "t45", PID: 45000, State: "live",
					Foreground: &TeamForeground{PID: 45010, Argv: []string{"codex"}, Cwd: state.Record.Directory},
				}}
			},
			wantAdopted: false,
		},
		{
			name:         "live direct HcTerminalID with empty Cwd is refused",
			terminalID:   &stale,
			client:       "codex",
			ancestorPids: []int{45099, 45010, 45000},
			setup: func(_ *testing.T, _ *State, host *teamFixture) {
				host.spaces = []TeamSpace{{
					ID: "space-nocwd", Tabs: []TeamTab{{ID: "tab-nocwd", Terminals: []TeamPane{{Terminal: "t45", Pane: "p45"}}}},
				}}
				host.terminals = []TeamTerminal{{
					Address: "t45", PID: 45000, State: "live",
					Foreground: &TeamForeground{PID: 45010, Argv: []string{"codex"}, Cwd: ""},
				}}
			},
			wantAdopted: false,
		},
		{
			name:         "stale live HcTerminalID falls back to actual session pane proven by AncestorPids",
			terminalID:   &stale,
			client:       "codex",
			ancestorPids: []int{52099, 52010, 51000},
			setup: func(t *testing.T, state *State, host *teamFixture) {
				otherRepo := t.TempDir()
				host.spaces = []TeamSpace{
					{ID: "space-stale", Tabs: []TeamTab{{ID: "tab-stale", Terminals: []TeamPane{{Terminal: "t45", Pane: "p45"}}}}},
					{ID: "space-live", Tabs: []TeamTab{{ID: "tab-live", Terminals: []TeamPane{{Terminal: "t52", Pane: "p52"}}}}},
				}
				host.terminals = []TeamTerminal{
					{Address: "t45", PID: 45000, State: "live", Foreground: &TeamForeground{PID: 45010, Argv: []string{"codex"}, Cwd: otherRepo}},
					{Address: "t52", PID: 51000, State: "live", Foreground: &TeamForeground{PID: 52010, Argv: []string{"codex"}, Cwd: state.Record.Directory}},
				}
			},
			wantAdopted:  true,
			wantTerminal: "t52",
			wantAgent:    "codex",
		},
		{
			name:         "refuses adoption when two panes match AncestorPids",
			client:       "codex",
			ancestorPids: []int{52099, 52010},
			setup: func(_ *testing.T, state *State, host *teamFixture) {
				host.spaces = []TeamSpace{{
					ID: "space-ambig",
					Tabs: []TeamTab{
						{ID: "tab-1", Terminals: []TeamPane{{Terminal: "t1", Pane: "p1"}}},
						{ID: "tab-2", Terminals: []TeamPane{{Terminal: "t2", Pane: "p2"}}},
					},
				}}
				host.terminals = []TeamTerminal{
					{Address: "t1", PID: 52010, State: "live", Foreground: &TeamForeground{PID: 52010, Argv: []string{"codex"}, Cwd: state.Record.Directory}},
					{Address: "t2", PID: 52010, State: "live", Foreground: &TeamForeground{PID: 52010, Argv: []string{"codex"}, Cwd: state.Record.Directory}},
				}
			},
			wantAdopted: false,
		},
		{
			name:         "refuses adoption when pane cwd is inside .surla-build",
			client:       "codex",
			ancestorPids: []int{52099, 52010},
			setup: func(_ *testing.T, state *State, host *teamFixture) {
				host.spaces = []TeamSpace{{
					ID: "space-worker", Tabs: []TeamTab{{ID: "tab-worker", Terminals: []TeamPane{{Terminal: "t-build", Pane: "p-build"}}}},
				}}
				host.terminals = []TeamTerminal{{
					Address: "t-build", PID: 52000, State: "live",
					Foreground: &TeamForeground{PID: 52010, Argv: []string{"codex"}, Cwd: filepath.Join(state.Record.Directory, ".surla-build")},
				}}
			},
			wantAdopted: false,
		},
		{
			name:         "refuses adoption when pane cwd is inside .surla-tests",
			client:       "claude",
			ancestorPids: []int{60099, 60010},
			setup: func(_ *testing.T, state *State, host *teamFixture) {
				host.spaces = []TeamSpace{{
					ID: "space-tests", Tabs: []TeamTab{{ID: "tab-tests", Terminals: []TeamPane{{Terminal: "t-tests", Pane: "p-tests"}}}},
				}}
				host.terminals = []TeamTerminal{{
					Address: "t-tests", PID: 60000, State: "live",
					Foreground: &TeamForeground{PID: 60010, Argv: []string{"claude"}, Cwd: filepath.Join(state.Record.Directory, ".surla-tests", "eval")},
				}}
			},
			wantAdopted: false,
		},
		{
			name:         "refuses adoption when pane cwd belongs to another repo",
			client:       "codex",
			ancestorPids: []int{52099, 52010},
			setup: func(t *testing.T, _ *State, host *teamFixture) {
				other := t.TempDir()
				host.spaces = []TeamSpace{{
					ID: "space-other", Tabs: []TeamTab{{ID: "tab-other", Terminals: []TeamPane{{Terminal: "t-other", Pane: "p-other"}}}},
				}}
				host.terminals = []TeamTerminal{{
					Address: "t-other", PID: 52000, State: "live",
					Foreground: &TeamForeground{PID: 52010, Argv: []string{"codex"}, Cwd: other},
				}}
			},
			wantAdopted: false,
		},
		{
			name:         "refuses adoption when live pane client family differs from connecting client",
			client:       "claude",
			ancestorPids: []int{52099, 52010},
			setup: func(_ *testing.T, state *State, host *teamFixture) {
				host.spaces = []TeamSpace{{
					ID: "space-mismatch", Tabs: []TeamTab{{ID: "tab-mismatch", Terminals: []TeamPane{{Terminal: "t-codex", Pane: "p-codex"}}}},
				}}
				host.terminals = []TeamTerminal{{
					Address: "t-codex", PID: 52000, State: "live",
					Foreground: &TeamForeground{PID: 52010, Argv: []string{"codex"}, Cwd: state.Record.Directory},
				}}
			},
			wantAdopted: false,
		},
		{
			name:         "refuses adoption when pane is an idle shell",
			client:       "codex",
			ancestorPids: []int{52099, 52000},
			setup: func(_ *testing.T, state *State, host *teamFixture) {
				host.spaces = []TeamSpace{{
					ID: "space-shell", Tabs: []TeamTab{{ID: "tab-shell", Terminals: []TeamPane{{Terminal: "t-shell", Pane: "p-shell"}}}},
				}}
				host.terminals = []TeamTerminal{{
					Address: "t-shell", PID: 52000, State: "live",
					Foreground: &TeamForeground{PID: 52000, Argv: []string{"zsh"}, Cwd: state.Record.Directory},
				}}
			},
			wantAdopted: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := testState(t)
			deps := fixtureTeamDependencies(state)
			deps.CompanionInstalled = true
			connected := 0
			deps.Connect = func(context.Context) (TeamConnection, error) {
				connected++
				return TeamConnection{Endpoint: "http://chorus.test"}, nil
			}
			host := deps.Host.(*teamFixture)
			tc.setup(t, state, host)
			s, err := NewServer(state, func() {}, deps)
			require.NoError(t, err)

			client := tc.client
			result, err := s.AttachGuide(teamContext(state), &genproject.AttachGuidePayload{
				HcTerminalID: tc.terminalID,
				Client:       &client,
				AncestorPids: tc.ancestorPids,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.wantAdopted, result.AdoptedHerdr)
			if tc.wantAdopted {
				assert.Equal(t, "attached", result.HerdrMode)
				assert.Contains(t, host.calls, "label")
				assert.Contains(t, host.calls, "companion")
				assert.Equal(t, 1, connected)
				for _, term := range host.terminals {
					if term.Address == tc.wantTerminal {
						assert.Equal(t, tc.wantAgent, term.Labels["chorus.agent"])
						assert.Equal(t, "guide", term.Labels["chorus.role"])
					}
				}
			} else {
				assert.Equal(t, "available", result.HerdrMode)
				assert.NotContains(t, host.calls, "label")
				assert.NotContains(t, host.calls, "companion")
				assert.Zero(t, connected)
				assert.Empty(t, state.Record.Space)
				assert.Empty(t, state.Record.Tab)
			}
		})
	}
}

func TestAttachGuideLineageAdoptionReusedByBuildWorkers(t *testing.T) {
	directID := "t-codex"
	for _, mode := range []struct {
		name       string
		terminalID *string
	}{
		{name: "fallback_scan", terminalID: nil},
		{name: "direct_terminal_id", terminalID: &directID},
	} {
		t.Run(mode.name, func(t *testing.T) {
			state := testState(t)
			deps := fixtureTeamDependencies(state)
			host := deps.Host.(*teamFixture)
			host.spaces = []TeamSpace{{
				ID:   "space-adopted",
				Tabs: []TeamTab{{ID: "tab-adopted", Terminals: []TeamPane{{Terminal: "t-codex", Pane: "p-codex"}}}},
			}}
			host.terminals = []TeamTerminal{{
				Address: "t-codex", PID: 41000, State: "live",
				Foreground: &TeamForeground{PID: 41055, Argv: []string{"codex"}, Cwd: state.Record.Directory},
			}}
			s, err := NewServer(state, func() {}, deps)
			require.NoError(t, err)

			client := "codex"
			// Without AncestorPids, even a live direct HcTerminalID must stay unattached.
			unproven, err := s.AttachGuide(teamContext(state), &genproject.AttachGuidePayload{
				HcTerminalID: mode.terminalID,
				Client:       &client,
			})
			require.NoError(t, err)
			require.False(t, unproven.AdoptedHerdr)
			assert.Equal(t, "available", unproven.HerdrMode)
			assert.Empty(t, state.Record.Space)

			attached, err := s.AttachGuide(teamContext(state), &genproject.AttachGuidePayload{
				HcTerminalID: mode.terminalID,
				Client:       &client,
				AncestorPids: []int{41099, 41055, 41000},
			})
			require.NoError(t, err)
			require.True(t, attached.AdoptedHerdr)
			assert.Equal(t, "attached", attached.HerdrMode)

			fixtureAssignments(s, true)
			builderDir := filepath.Join(state.Record.Directory, ".surla-build")
			writerDir := filepath.Join(state.Record.Directory, ".surla-tests")
			require.NoError(t, os.MkdirAll(builderDir, 0o755))
			require.NoError(t, os.MkdirAll(writerDir, 0o755))
			require.NoError(t, s.RegisterBuildRoles(t.Context(), controlplane.BuilderAndTestWriter{
				BuilderFolder: builderDir, TestWriterFolder: writerDir,
			}))

			started, err := s.StartAgents(t.Context(), nil)
			require.NoError(t, err)
			assert.Zero(t, host.creates, "Build must reuse the adopted guide space without creating a second space")
			assert.Equal(t, "space-adopted", started.Space)
			assert.Equal(t, "attached", started.HerdrMode)
			rawRoster, err := os.ReadFile(filepath.Join(deps.RosterDirectory, "space-adopted.json"))
			require.NoError(t, err)
			var roster launcherRoster
			require.NoError(t, json.Unmarshal(rawRoster, &roster))
			require.Len(t, roster.Roles, 3)
			assert.Equal(t, "guide", roster.Roles[0].Role)
			require.NotNil(t, roster.Roles[1].Place)
			assert.Equal(t, attached.Handle, roster.Roles[1].Place.Beside)
			assert.Equal(t, "right", roster.Roles[1].Place.Direction)
		})
	}
}

type mutatingReadTerminalHost struct {
	*teamFixture
	reads  int
	onRead func(readCount int)
}

func (h *mutatingReadTerminalHost) ReadTerminal(ctx context.Context, address string) (TeamTerminal, error) {
	h.reads++
	if h.onRead != nil {
		h.onRead(h.reads)
	}
	return h.teamFixture.ReadTerminal(ctx, address)
}

func TestAttachGuideRefusesPaneChangedBetweenScanAndFinalValidation(t *testing.T) {
	for _, mutation := range []string{"lineage_pid", "cwd"} {
		t.Run(mutation, func(t *testing.T) {
			state := testState(t)
			deps := fixtureTeamDependencies(state)
			baseHost := deps.Host.(*teamFixture)
			baseHost.spaces = []TeamSpace{{
				ID:   "space-race",
				Tabs: []TeamTab{{ID: "tab-race", Terminals: []TeamPane{{Terminal: "t-race", Pane: "p-race"}}}},
			}}
			baseHost.terminals = []TeamTerminal{{
				Address: "t-race", PID: 41000, State: "live",
				Foreground: &TeamForeground{PID: 41055, Argv: []string{"codex"}, Cwd: state.Record.Directory},
			}}
			otherDir := t.TempDir()
			wrapper := &mutatingReadTerminalHost{
				teamFixture: baseHost,
				onRead: func(readCount int) {
					if readCount == 2 {
						switch mutation {
						case "lineage_pid":
							baseHost.terminals[0].PID = 88000
							baseHost.terminals[0].Foreground.PID = 88055
						case "cwd":
							baseHost.terminals[0].Foreground.Cwd = otherDir
						}
					}
				},
			}
			deps.Host = wrapper
			s, err := NewServer(state, func() {}, deps)
			require.NoError(t, err)

			client := "codex"
			res, err := s.AttachGuide(teamContext(state), &genproject.AttachGuidePayload{
				Client:       &client,
				AncestorPids: []int{41099, 41055, 41000},
			})
			require.NoError(t, err)
			assert.Equal(t, 2, wrapper.reads, "scan read + final validation read")
			assert.False(t, res.AdoptedHerdr)
			assert.Equal(t, "available", res.HerdrMode)
			assert.NotContains(t, baseHost.calls, "label")
			assert.Empty(t, state.Record.Space)
			assert.Empty(t, state.Record.Tab)
		})
	}
}

func TestStartAgentsPreparesWorkerTrustAndRefusesExplicitPolicyBeforeEffects(t *testing.T) {
	state := testState(t)
	deps := fixtureTeamDependencies(state)
	host := deps.Host.(*teamFixture)
	host.spaces = nil
	var prepared [][]WorkerTrustTarget
	var trustErr error
	deps.PrepareWorkerTrust = func(_ context.Context, targets []WorkerTrustTarget) error {
		prepared = append(prepared, targets)
		return trustErr
	}
	s, err := NewServer(state, func() {}, deps)
	require.NoError(t, err)
	fixtureAssignments(s, true)
	builderDir := filepath.Join(state.Record.Directory, ".surla-build")
	writerDir := filepath.Join(state.Record.Directory, ".surla-tests")
	require.NoError(t, os.MkdirAll(builderDir, 0o755))
	require.NoError(t, os.MkdirAll(writerDir, 0o755))
	require.NoError(t, s.RegisterBuildRoles(t.Context(), controlplane.BuilderAndTestWriter{
		BuilderFolder: builderDir, TestWriterFolder: writerDir,
	}))

	// Refused choice must not prepare trust.
	_, err = s.StartAgents(t.Context(), map[string]*gensurla.RoleAgentChoice{
		"builder": {Client: "codex", Program: "/missing"},
	})
	require.Error(t, err)
	assert.Empty(t, prepared)

	// Explicit TeamPolicyRefusal returns before creating a space, saving choices, or writing a roster.
	trustErr = &controlplane.TeamPolicyRefusal{
		Kind:   controlplane.PolicyCodexProjectUntrusted,
		Client: "codex",
		Detail: "untrusted",
	}
	_, err = s.StartAgents(t.Context(), nil)
	require.Error(t, err)
	var policyRefusal *controlplane.TeamPolicyRefusal
	require.ErrorAs(t, err, &policyRefusal)
	assert.Contains(t, policyRefusal.Error(), `trust_level = "trusted"`)
	assert.Zero(t, host.creates, "policy refusal must not create a Herdr space")
	assert.NotContains(t, host.calls, "launch")
	assert.NoDirExists(t, deps.RosterDirectory)
	assert.Empty(t, state.Record.SavedChoices)

	// Unresolved preflight config errors are also surfaced as actionable refusals before launch.
	prepared = nil
	trustErr = assert.AnError
	_, err = s.StartAgents(t.Context(), nil)
	require.Error(t, err)
	require.ErrorAs(t, err, &policyRefusal)
	assert.Equal(t, controlplane.PolicyConfigUnusable, policyRefusal.Kind)
	assert.Zero(t, host.creates, "unusable config refusal must not create a Herdr space")
	assert.NotContains(t, host.calls, "launch")

	// Clean preflight launches workers with exact role-scoped targets.
	prepared = nil
	trustErr = nil
	_, err = s.StartAgents(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, prepared, 1)
	assert.Equal(t, []WorkerTrustTarget{
		{Role: "builder", ProjectDir: state.Record.Directory, Folder: builderDir, Client: "codex", Yolo: false, RequireChorus: true},
		{Role: "test_writer", ProjectDir: state.Record.Directory, Folder: writerDir, Client: "claude", Yolo: false, RequireChorus: true},
	}, prepared[0])
	assert.Equal(t, 1, host.creates)
	assert.Contains(t, host.calls, "launch")
}

func TestAttachGuideRefusesConflictingStaleAgentLabel(t *testing.T) {
	direct := "t-pane"
	codexClient := "codex"
	claudeClient := "claude"

	t.Run("positive direct session with consistent label and foreground adopts pane", func(t *testing.T) {
		state := testState(t)
		deps := fixtureTeamDependencies(state)
		host := deps.Host.(*teamFixture)
		host.spaces = []TeamSpace{{
			ID: "space-ok", Tabs: []TeamTab{{ID: "tab-ok", Terminals: []TeamPane{{Terminal: direct, Pane: "p-ok"}}}},
		}}
		host.terminals = []TeamTerminal{{
			Address: direct, PID: 51000, State: "live",
			Labels:     map[string]string{"chorus.agent": "codex"},
			Foreground: &TeamForeground{PID: 52010, Argv: []string{"codex"}, Cwd: state.Record.Directory},
		}}
		s, err := NewServer(state, func() {}, deps)
		require.NoError(t, err)

		res, err := s.AttachGuide(teamContext(state), &genproject.AttachGuidePayload{
			HcTerminalID: &direct,
			Client:       &codexClient,
			AncestorPids: []int{52099, 52010, 51000},
		})
		require.NoError(t, err)
		assert.True(t, res.AdoptedHerdr)
		assert.Equal(t, "attached", res.HerdrMode)
		assert.Contains(t, host.calls, "label")
	})

	for _, tc := range []struct {
		name       string
		terminalID *string
		client     *string
		labelAgent string
		fgArgv     []string
	}{
		{
			name:       "direct HcTerminalID matching shell PID with stale codex label and claude foreground (codex caller)",
			terminalID: &direct,
			client:     &codexClient,
			labelAgent: "codex",
			fgArgv:     []string{"claude"},
		},
		{
			name:       "direct HcTerminalID matching shell PID with stale codex label and claude foreground (claude caller)",
			terminalID: &direct,
			client:     &claudeClient,
			labelAgent: "codex",
			fgArgv:     []string{"claude"},
		},
		{
			name:       "direct HcTerminalID matching shell PID with stale codex label and claude foreground (omitted client)",
			terminalID: &direct,
			client:     nil,
			labelAgent: "codex",
			fgArgv:     []string{"claude"},
		},
		{
			name:       "fallback scan matching shell PID with stale codex label and claude foreground",
			terminalID: nil,
			client:     &codexClient,
			labelAgent: "codex",
			fgArgv:     []string{"claude"},
		},
		{
			name:       "unrecognized explicit chorus.agent label conflicts even when foreground matches caller",
			terminalID: &direct,
			client:     &codexClient,
			labelAgent: "custom-foreign-agent",
			fgArgv:     []string{"codex"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := testState(t)
			deps := fixtureTeamDependencies(state)
			host := deps.Host.(*teamFixture)
			host.spaces = []TeamSpace{{
				ID: "space-conflict", Tabs: []TeamTab{{ID: "tab-conflict", Terminals: []TeamPane{{Terminal: direct, Pane: "p-conflict"}}}},
			}}
			host.terminals = []TeamTerminal{{
				Address: direct, PID: 51000, State: "live",
				Labels:     map[string]string{"chorus.agent": tc.labelAgent},
				Foreground: &TeamForeground{PID: 60010, Argv: tc.fgArgv, Cwd: state.Record.Directory},
			}}
			s, err := NewServer(state, func() {}, deps)
			require.NoError(t, err)

			res, err := s.AttachGuide(teamContext(state), &genproject.AttachGuidePayload{
				HcTerminalID: tc.terminalID,
				Client:       tc.client,
				AncestorPids: []int{52099, 51000},
			})
			require.NoError(t, err)
			assert.False(t, res.AdoptedHerdr)
			assert.NotContains(t, host.calls, "label")
			assert.Empty(t, state.Record.Space)
			assert.Empty(t, state.Record.Tab)
		})
	}
}
