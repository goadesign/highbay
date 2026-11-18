// This file exercises team ownership through recorded Herdr effects and temporary rosters.
package project

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	genproject "github.com/goadesign/surla/gen/project"
	"github.com/goadesign/surla/internal/controlplane"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// teamFixture records all host effects and supplies deterministic space/terminal observations.
type teamFixture struct {
	spaces    []TeamSpace
	terminals []TeamTerminal
	overlays  []string
	calls     []string
	creates   int
	focused   TeamSpace
}

// Spaces returns the fixture listing without reading Herdr.
func (f *teamFixture) Spaces(context.Context) ([]TeamSpace, error) {
	f.calls = append(f.calls, "spaces")
	return f.spaces, nil
}

// CreateSpace records the requested local space and returns its terminal mapping.
func (f *teamFixture) CreateSpace(_ context.Context, name, folder string) (TeamSpace, error) {
	f.creates++
	s := TeamSpace{ID: fmt.Sprintf("space-%d", f.creates), Name: name, Folder: folder, Tabs: []TeamTab{{ID: "tab", Terminals: []TeamPane{{Terminal: "terminal"}}}}}
	f.spaces = append(f.spaces, s)
	f.calls = append(f.calls, "create")
	return s, nil
}

// Terminals returns only fixture foreground and label observations.
func (f *teamFixture) Terminals(context.Context, string, string, []string) ([]TeamTerminal, error) {
	f.calls = append(f.calls, "terminals")
	return f.terminals, nil
}

// Focus records the requested fixture focus without touching a window.
func (f *teamFixture) Focus(_ context.Context, space TeamSpace) error {
	f.focused = space
	f.calls = append(f.calls, "focus")
	return nil
}

// Overlays returns the fixture dialog names.
func (f *teamFixture) Overlays(context.Context) ([]string, error) {
	f.calls = append(f.calls, "overlays")
	return f.overlays, nil
}

// OpenLauncher records the request without opening a real dialog.
func (f *teamFixture) OpenLauncher(context.Context) error {
	f.calls = append(f.calls, "open")
	return nil
}

// fixtureTeamDependencies prevents all external effects in server authentication/lifecycle tests.
func fixtureTeamDependencies(state *State) TeamDependencies {
	return TeamDependencies{Lifetime: context.Background(), Host: &teamFixture{}, RosterDirectory: filepath.Join(state.Dir, "test-rosters"), Shell: "/bin/zsh", Connect: func(context.Context) (TeamConnection, error) {
		return TeamConnection{Endpoint: "http://chorus.test"}, nil
	}}
}

// teamContext grants management through the same trusted principal boundary used by HTTP.
func teamContext(state *State) context.Context {
	return controlplane.WithPrincipal(context.Background(), controlplane.Principal{ID: state.Record.Owner, CallerID: "management", Kind: controlplane.CallerManagement})
}

// TestEnsureTeamLifecycle verifies the server or command contract using only temporary state and stand-ins.
func TestEnsureTeamLifecycle(t *testing.T) {
	state := testState(t)
	dependencies := fixtureTeamDependencies(state)
	host := dependencies.Host.(*teamFixture)
	server, err := NewServer(state, func() {}, dependencies)
	require.NoError(t, err)
	first, err := server.EnsureTeam(teamContext(state))
	require.NoError(t, err)
	assert.Equal(t, "opened", first.Dialog)
	assert.Equal(t, 1, host.creates)
	assert.Len(t, state.Record.Agents, 1)
	path := filepath.Join(dependencies.RosterDirectory, first.Space+".json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var roster map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &roster))
	assert.ElementsMatch(t, []string{"root", "swarm", "roles"}, keys(roster))
	var roles []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(roster["roles"], &roles))
	require.Len(t, roles, 1)
	assert.ElementsMatch(t, []string{"role", "title", "handle", "folder", "first_message", "connections", "place"}, keys(roles[0]))
	secret, err := os.ReadFile(state.AgentFile(first.Roles[0].Handle))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), string(secret))
	host.calls = nil
	host.overlays = []string{"chorus-launcher"}
	second, err := server.EnsureTeam(teamContext(state))
	require.NoError(t, err)
	assert.Equal(t, "already_open", second.Dialog)
	assert.NotContains(t, host.calls, "open")
	assert.Equal(t, 1, host.creates)
	assert.Len(t, state.Record.Agents, 1)
	host.terminals = []TeamTerminal{{State: "live", Labels: map[string]string{"chorus.handle": first.Roles[0].Handle, "chorus.project": state.Record.Directory, "herdr.space": first.Space, "herdr.tab": "tab"}, Foreground: &TeamForeground{Argv: []string{"codex"}}}}
	host.calls = nil
	host.overlays = nil
	running, err := server.EnsureTeam(teamContext(state))
	require.NoError(t, err)
	assert.Equal(t, "not_needed", running.Dialog)
	assert.True(t, running.Roles[0].Running)
	assert.NotContains(t, host.calls, "focus")
	host.spaces = nil
	host.terminals = nil
	host.overlays = nil
	replacement, err := server.EnsureTeam(teamContext(state))
	require.NoError(t, err)
	assert.Equal(t, first.Team, replacement.Team)
	assert.NotEqual(t, first.Space, replacement.Space)
	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// TestEnsureTeamAdoptionConflictAndAccess verifies the server or command contract using only temporary state and stand-ins.
func TestEnsureTeamAdoptionConflictAndAccess(t *testing.T) {
	for _, kind := range []controlplane.CallerKind{controlplane.CallerUser, controlplane.CallerGuide, controlplane.CallerBuilder, controlplane.CallerTestWriter} {
		t.Run(string(kind), func(t *testing.T) {
			state := testState(t)
			deps := fixtureTeamDependencies(state)
			s, err := NewServer(state, func() {}, deps)
			require.NoError(t, err)
			ctx := controlplane.WithPrincipal(t.Context(), controlplane.Principal{ID: state.Record.Owner, Kind: kind})
			_, err = s.EnsureTeam(ctx)
			require.Error(t, err)
			assert.Empty(t, deps.Host.(*teamFixture).calls)
			assert.Empty(t, state.Record.Agents)
			assert.Empty(t, state.Record.Team)
		})
	}
	for _, count := range []int{1, 2} {
		t.Run(string(rune('0'+count)), func(t *testing.T) {
			state := testState(t)
			deps := fixtureTeamDependencies(state)
			host := deps.Host.(*teamFixture)
			for i := 0; i < count; i++ {
				host.spaces = append(host.spaces, TeamSpace{ID: string(rune('a' + i)), Name: teamName(filepath.Base(state.Record.Directory), state.Record.ID), Folder: state.Record.Directory, Tabs: []TeamTab{{ID: "tab", Terminals: []TeamPane{{Terminal: "terminal"}}}}})
			}
			s, err := NewServer(state, func() {}, deps)
			require.NoError(t, err)
			result, err := s.EnsureTeam(teamContext(state))
			if count == 2 {
				var conflict *genproject.TeamSpaceConflict
				require.ErrorAs(t, err, &conflict)
				assert.Equal(t, 2, conflict.Count)
				assert.Empty(t, state.Record.Agents)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "a", result.Space)
			}
			assert.Zero(t, host.creates)
		})
	}
	t.Run("skip_non_conforming", func(t *testing.T) {
		state := testState(t)
		deps := fixtureTeamDependencies(state)
		host := deps.Host.(*teamFixture)
		name := teamName(filepath.Base(state.Record.Directory), state.Record.ID)
		state.Record.Team = name
		state.Record.Space = "sp'quoted"
		require.NoError(t, state.save())
		require.NoError(t, os.MkdirAll(deps.RosterDirectory, 0o700))
		oldRoster := filepath.Join(deps.RosterDirectory, "sp'quoted.json")
		require.NoError(t, os.WriteFile(oldRoster, []byte("{}"), 0o600))

		loaded, err := Discover(filepath.Dir(state.Dir), state.Record.Directory)
		require.NoError(t, err)
		assert.Equal(t, "sp'quoted", loaded.Record.Space)
		opened, err := Open(state.Dir)
		require.NoError(t, err)
		assert.Equal(t, "sp'quoted", opened.Record.Space)

		for _, id := range []string{"sp'quoted", ".", "..", strings.Repeat("a", 65), "remote/sp"} {
			host.spaces = append(host.spaces, TeamSpace{ID: id, Name: name, Folder: state.Record.Directory, Tabs: []TeamTab{{ID: "tab", Terminals: []TeamPane{{Terminal: "terminal"}}}}})
		}
		s, err := NewServer(opened, func() {}, deps)
		require.NoError(t, err)
		_, err = s.EnsureTeam(teamContext(opened))
		var unavailable *genproject.TeamUnavailable
		require.ErrorAs(t, err, &unavailable)
		assert.Equal(t, "saved_space", unavailable.Cause)
		assert.Empty(t, s.teamSelection().connection.Endpoint, "supervision sees no new connection on open legacy-space refusal")
		assert.Zero(t, host.creates)
		assert.Equal(t, "sp'quoted", opened.Record.Space)
		assert.FileExists(t, oldRoster)

		host.spaces = host.spaces[1:]
		result, err := s.EnsureTeam(teamContext(opened))
		require.NoError(t, err)
		assert.Equal(t, "space-1", result.Space)
		assert.Equal(t, "space-1", opened.Record.Space)
		assert.Equal(t, "http://chorus.test", s.teamSelection().connection.Endpoint)
		assert.Equal(t, 1, host.creates)
		_, err = os.Stat(oldRoster)
		assert.ErrorIs(t, err, os.ErrNotExist)

		opened.Record.Space = "free'form-2"
		require.NoError(t, opened.save())
		host.spaces = append(host.spaces, TeamSpace{ID: "adopted-1", Name: name, Folder: state.Record.Directory, Tabs: []TeamTab{{ID: "tab", Terminals: []TeamPane{{Terminal: "terminal"}}}}})
		result, err = s.EnsureTeam(teamContext(opened))
		require.NoError(t, err)
		assert.Equal(t, "adopted-1", result.Space)
		assert.Equal(t, "adopted-1", opened.Record.Space)
		assert.Equal(t, 1, host.creates)
	})
}

// TestEnsureTeamOtherDialog verifies the server or command contract using only temporary state and stand-ins.
func TestEnsureTeamOtherDialog(t *testing.T) {
	state := testState(t)
	deps := fixtureTeamDependencies(state)
	host := deps.Host.(*teamFixture)
	host.overlays = []string{"frame-palette"}
	s, err := NewServer(state, func() {}, deps)
	require.NoError(t, err)
	_, err = s.EnsureTeam(teamContext(state))
	require.Error(t, err)
	assert.NotContains(t, host.calls, "open")
}

// TestTeamNamesAndThreeRoleRoster verifies the server or command contract using only temporary state and stand-ins.
func TestTeamNamesAndThreeRoleRoster(t *testing.T) {
	state := testState(t)
	state.Record.Team = teamName("— 123 ! a very long project folder name", state.Record.ID)
	deps := fixtureTeamDependencies(state)
	s, err := NewServer(state, func() {}, deps)
	require.NoError(t, err)
	var roles []launcherRole
	for _, role := range []string{"guide", "builder", "test_writer"} {
		handle := teamName(role, state.Record.ID)
		assert.LessOrEqual(t, len(handle), 32)
		assert.Regexp(t, `^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`, handle)
		_, err := registerTestAgent(s, handle, role, s.state.Record.Directory)
		require.NoError(t, err)
		roles = append(roles, launcherRole{Role: role, Title: role, Handle: handle, Folder: state.Record.Directory, FirstMessage: "Use your Surla connection."})
	}
	roster, err := s.teamRoster(roles)
	require.NoError(t, err)
	assert.Nil(t, roster.Roles[0].Place)
	assert.Equal(t, &launcherPlace{Beside: roles[0].Handle, Direction: "right"}, roster.Roles[1].Place)
	assert.Equal(t, &launcherPlace{Beside: roles[1].Handle, Direction: "down"}, roster.Roles[2].Place)
	other := testState(t)
	assert.NotEqual(t, state.Record.Team, teamName("— 123 ! a very long project folder name", other.Record.ID))
	assert.LessOrEqual(t, len(state.Record.Team), 32)
	assert.Regexp(t, `^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`, state.Record.Team)
}

// TestRoleRunningSharedContract verifies the server or command contract using only temporary state and stand-ins.
func TestRoleRunningSharedContract(t *testing.T) {
	for _, test := range []struct {
		name, state string
		argv        []string
		want        bool
	}{{"no foreground", "live", nil, true}, {"empty", "live", []string{}, true}, {"finished", "finished", []string{"codex"}, false}, {"agent", "live", []string{"/usr/bin/claude"}, true}, {"shell", "live", []string{"/bin/zsh", "-l"}, false}, {"login", "live", []string{"-zsh", "-li"}, false}, {"custom", "live", []string{"/bin/custom"}, false}, {"wrapper", "live", []string{"zsh", "-lic", "exec codex"}, true}, {"command", "live", []string{"bash", "-c", "codex"}, true}, {"second flag", "live", []string{"sh", "-l", "-c"}, true}, {"long flag", "live", []string{"sh", "--command"}, false}, {"nonflag", "live", []string{"sh", "abc"}, false}, {"xonsh", "live", []string{"xonsh"}, false}, {"elvish", "live", []string{"elvish"}, false}} {
		t.Run(test.name, func(t *testing.T) {
			terminal := TeamTerminal{State: test.state}
			if test.argv != nil {
				terminal.Foreground = &TeamForeground{Argv: test.argv}
			}
			assert.Equal(t, test.want, roleRunning(terminal, "/bin/custom"))
		})
	}
}

// keys supports strict schema comparisons without depending on JSON key ordering.
func keys[T any](values map[string]T) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	return result
}

// TestEnsureTeamCleanupRetry retains the old space until its stale roster is removed.
func TestEnsureTeamCleanupRetry(t *testing.T) {
	state := testState(t)
	state.Record.Team = teamName(filepath.Base(state.Record.Directory), state.Record.ID)
	state.Record.Space = "old"
	require.NoError(t, state.save())
	deps := fixtureTeamDependencies(state)
	old := filepath.Join(deps.RosterDirectory, "old.json")
	require.NoError(t, os.MkdirAll(old, 0o700))
	blocker := filepath.Join(old, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("retry fixture"), 0o600))
	s, err := NewServer(state, func() {}, deps)
	require.NoError(t, err)
	_, err = s.EnsureTeam(teamContext(state))
	require.Error(t, err)
	assert.Equal(t, "old", state.Record.Space)
	loaded, err := Discover(filepath.Dir(state.Dir), state.Record.Directory)
	require.NoError(t, err)
	assert.Equal(t, "old", loaded.Record.Space)
	require.NoError(t, os.Remove(blocker))
	result, err := s.EnsureTeam(teamContext(state))
	require.NoError(t, err)
	assert.Equal(t, "space-1", result.Space)
	assert.Equal(t, 1, deps.Host.(*teamFixture).creates)
	_, err = os.Stat(old)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// TestEnsureTeamOwnedLifetime distinguishes caller disconnection from server shutdown.
func TestEnsureTeamOwnedLifetime(t *testing.T) {
	state := testState(t)
	deps := fixtureTeamDependencies(state)
	s, err := NewServer(state, func() {}, deps)
	require.NoError(t, err)
	caller, cancel := context.WithCancel(teamContext(state))
	cancel()
	_, err = s.EnsureTeam(caller)
	require.NoError(t, err)
	state = testState(t)
	deps = fixtureTeamDependencies(state)
	owner, cancel := context.WithCancel(context.Background())
	deps.Lifetime = owner
	cancel()
	s, err = NewServer(state, func() {}, deps)
	require.NoError(t, err)
	_, err = s.EnsureTeam(teamContext(state))
	require.Error(t, err)
	assert.Empty(t, deps.Host.(*teamFixture).calls)
}

// TestEnsureTeamGuideInvariant rejects mismatched roles and missing credentials without a roster.
func TestEnsureTeamGuideInvariant(t *testing.T) {
	for _, scenario := range []string{"role", "credential"} {
		t.Run(scenario, func(t *testing.T) {
			state := testState(t)
			deps := fixtureTeamDependencies(state)
			s, err := NewServer(state, func() {}, deps)
			require.NoError(t, err)
			handle := teamName("guide", state.Record.ID)
			role := "guide"
			if scenario == "role" {
				role = "builder"
			}
			_, err = registerTestAgent(s, handle, role, s.state.Record.Directory)
			require.NoError(t, err)
			if scenario == "credential" {
				require.NoError(t, os.Remove(state.AgentFile(handle)))
			}
			_, err = s.EnsureTeam(teamContext(state))
			require.Error(t, err)
			_, err = os.Stat(deps.RosterDirectory)
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

// TestEnsureTeamPreservesTraceParent verifies accepted work remains a child of the request.
func TestEnsureTeamPreservesTraceParent(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	defer func() { assert.NoError(t, provider.Shutdown(context.Background())) }()
	state := testState(t)
	deps := fixtureTeamDependencies(state)
	s, err := NewServer(state, func() {}, deps)
	require.NoError(t, err)
	parent := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled})
	ctx, cancel := context.WithCancel(trace.ContextWithSpanContext(teamContext(state), parent))
	cancel()
	_, err = s.EnsureTeam(ctx)
	require.NoError(t, err)
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, parent.TraceID(), spans[0].SpanContext().TraceID())
	assert.Equal(t, parent.SpanID(), spans[0].Parent().SpanID())
}

// TestTwoProjectTeams keeps spaces, handles and roster paths distinct under one Herdr owner.
func TestTwoProjectTeams(t *testing.T) {
	host := &teamFixture{}
	rosters := t.TempDir()
	var results []*genproject.ProjectTeam
	for i := 0; i < 2; i++ {
		state := testState(t)
		deps := fixtureTeamDependencies(state)
		deps.Host = host
		deps.RosterDirectory = rosters
		s, err := NewServer(state, func() {}, deps)
		require.NoError(t, err)
		result, err := s.EnsureTeam(teamContext(state))
		require.NoError(t, err)
		results = append(results, result)
		_, err = os.Stat(filepath.Join(rosters, result.Space+".json"))
		require.NoError(t, err)
	}
	assert.NotEqual(t, results[0].Team, results[1].Team)
	assert.NotEqual(t, results[0].Space, results[1].Space)
	assert.NotEqual(t, results[0].Roles[0].Handle, results[1].Roles[0].Handle)
}

// TestMissingForegroundPreservesLiveGuide mirrors older Herdr's absent/null foreground replies.
func TestMissingForegroundPreservesLiveGuide(t *testing.T) {
	for _, foreground := range []string{"", `,"foreground":null`, `,"foreground":{}`, `,"foreground":{"argv":[]}`, `,"foreground":{"argv":[""]}`} {
		t.Run(foreground, func(t *testing.T) {
			state := testState(t)
			deps := fixtureTeamDependencies(state)
			host := deps.Host.(*teamFixture)
			s, err := NewServer(state, func() {}, deps)
			require.NoError(t, err)
			team, err := s.EnsureTeam(teamContext(state))
			require.NoError(t, err)
			raw := fmt.Sprintf(`{"state":"live","labels":{"chorus.handle":%q,"chorus.project":%q,"herdr.space":%q,"herdr.tab":"tab"}%s}`, team.Roles[0].Handle, state.Record.Directory, team.Space, foreground)
			var terminal TeamTerminal
			require.NoError(t, json.Unmarshal([]byte(raw), &terminal))
			assert.True(t, roleRunning(terminal, deps.Shell))
			host.terminals = []TeamTerminal{terminal}
			host.calls = nil
			result, err := s.EnsureTeam(teamContext(state))
			require.NoError(t, err)
			assert.True(t, result.Roles[0].Running)
			assert.Equal(t, "not_needed", result.Dialog)
			assert.NotContains(t, host.calls, "focus")
			assert.NotContains(t, host.calls, "open")
		})
	}
}

// testAgent exposes credential paths to package tests without a management API.
type testAgent struct {
	ActorID        string
	CredentialFile string
}

// registerTestAgent exercises the owning private admission with explicit saved folders.
func registerTestAgent(server *Server, handle, role, folder string) (*testAgent, error) {
	actual, err := server.registerAgent(handle, agentRecord{Role: role, Folder: folder})
	if err != nil {
		return nil, err
	}
	return &testAgent{ActorID: actual, CredentialFile: server.state.AgentFile(actual)}, nil
}

// TestTeamResultNextStep covers each real owner outcome and the user-facing conversion.
func TestTeamResultNextStep(t *testing.T) {
	for _, tc := range []struct{ dialog, step string }{
		{"opened", "Choose an agent for each role in the open Chorus dialog in Herdr."},
		{"already_open", "If this is your project's dialog, click Reload team and choose agents. Otherwise close it; use Start agents on the page or run surla."},
		{"not_needed", "Every listed role is running."},
	} {
		t.Run(tc.dialog, func(t *testing.T) {
			state := testState(t)
			deps := fixtureTeamDependencies(state)
			host := deps.Host.(*teamFixture)
			server, err := NewServer(state, func() {}, deps)
			require.NoError(t, err)
			first, err := server.EnsureTeam(teamContext(state))
			require.NoError(t, err)
			switch tc.dialog {
			case "already_open":
				host.overlays = []string{"chorus-launcher"}
			case "not_needed":
				host.terminals = []TeamTerminal{{State: "live", Labels: map[string]string{
					"chorus.project": state.Record.Directory,
					"herdr.space":    first.Space,
					"herdr.tab":      "tab",
					"chorus.handle":  first.Roles[0].Handle,
				}}}
			}
			team, err := server.EnsureTeam(teamContext(state))
			require.NoError(t, err)
			assert.Equal(t, tc.dialog, team.Dialog)
			assert.Contains(t, team.Step, tc.step)
			if tc.dialog != "not_needed" {
				assert.Contains(t, team.Step, "From a terminal, run chorus team inspect --space '"+first.Space+"' to see the roles")
				assert.Contains(t, team.Step, "add --choices choices.json for models and permission effects")
				assert.Contains(t, team.Step, "then chorus team launch with the same --space and --choices choices.json")
				assert.Contains(t, team.Step, "--choices - reads stdin; see chorus team launch --help")
				assert.NotContains(t, team.Step, "<id>")
			} else {
				assert.Equal(t, tc.step, team.Step)
			}
			// The generated response bound must hold up to the 64-character space ID
			// limit and reject non-conforming space IDs.
			for _, id := range []string{"12345678-1234-1234-1234-123456789012",
				"123456781234567812345678123456781234567812345678",
				strings.Repeat("a", 64), ".a", "..a"} {
				response := *team
				response.Space, response.Step = id, teamNextStep(tc.dialog, id)
				_, err := genproject.EncodeProjectTeam(&response)
				assert.NoError(t, err, "dialog %s with space %q must encode", tc.dialog, id)
			}
			for _, invalid := range []string{"sp'quoted", ".", "..", strings.Repeat("a", 65), "remote/sp"} {
				response := *team
				response.Space, response.Step = invalid, teamNextStep(tc.dialog, invalid)
				_, err := genproject.EncodeProjectTeam(&response)
				assert.Error(t, err, "dialog %s with invalid space %q must be refused", tc.dialog, invalid)
			}
			userTeam, err := server.StartAgents(t.Context())
			require.NoError(t, err)
			assert.Equal(t, team.Step, userTeam.Step, "the user operation must carry the owner's step unchanged")
		})
	}
}

// TestEnsureTeamCountsOnlyGuideAndRolesWithWork checks that EnsureTeam counts
// only the guide and roles with work toward missing, while an unbound or
// failing assignment read records a bounded span error and counts every listed
// role when not running. It also checks that controlplane.New binds the
// project server's reader synchronously before EnsureTeam runs.
func TestEnsureTeamCountsOnlyGuideAndRolesWithWork(t *testing.T) {
	state := testState(t)
	deps := fixtureTeamDependencies(state)
	host := deps.Host.(*teamFixture)
	server, err := NewServer(state, func() {}, deps)
	require.NoError(t, err)

	builderDir := filepath.Join(state.Record.Directory, ".surla-builder")
	writerDir := filepath.Join(state.Record.Directory, ".surla-tests")
	require.NoError(t, os.MkdirAll(builderDir, 0o755))
	require.NoError(t, os.MkdirAll(writerDir, 0o755))
	require.NoError(t, server.RegisterBuildRoles(t.Context(), controlplane.BuilderAndTestWriter{
		BuilderFolder: builderDir, TestWriterFolder: writerDir,
	}))

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	defer func() { assert.NoError(t, provider.Shutdown(context.Background())) }()

	ensureSpans := func() []sdktrace.ReadOnlySpan {
		var out []sdktrace.ReadOnlySpan
		for _, s := range recorder.Ended() {
			if s.Name() == "surla.project.ensure_team" {
				out = append(out, s)
			}
		}
		return out
	}

	// 1. Unbound reader: counts every listed role when not running and records
	// errAssignmentsUnavailable on the span.
	first, err := server.EnsureTeam(teamContext(state))
	require.NoError(t, err)
	require.Len(t, first.Roles, 3)
	assert.Equal(t, "opened", first.Dialog)
	spans := ensureSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status().Code)
	assert.Equal(t, "team assignments could not be read", spans[0].Status().Description)

	liveTerminal := func(handle string) TeamTerminal {
		return TeamTerminal{
			State: "live",
			Labels: map[string]string{
				"chorus.project": state.Record.Directory,
				"herdr.space":    first.Space,
				"herdr.tab":      "tab",
				"chorus.handle":  handle,
			},
			Foreground: &TeamForeground{Argv: []string{"codex"}},
		}
	}
	guideHandle := first.Roles[0].Handle
	builderHandle := first.Roles[1].Handle
	writerHandle := first.Roles[2].Handle

	// 2. Guide running, neither worker role has work (e.g. a dropped or passed
	// build): no dialog is opened even though builder and test_writer are not
	// running.
	var current controlplane.TeamAssignments
	var readErr error
	server.BindAssignments(func(context.Context) (controlplane.TeamAssignments, error) {
		return current, readErr
	})
	host.terminals = []TeamTerminal{liveTerminal(guideHandle)}
	host.calls = nil
	res, err := server.EnsureTeam(teamContext(state))
	require.NoError(t, err)
	assert.Equal(t, "not_needed", res.Dialog)
	assert.NotContains(t, host.calls, "open")
	spans = ensureSpans()
	require.Len(t, spans, 2)
	assert.Equal(t, codes.Unset, spans[1].Status().Code)

	// 3. Guide not running while neither worker role has work: the guide alone
	// still counts as missing and opens the dialog.
	host.terminals = nil
	res, err = server.EnsureTeam(teamContext(state))
	require.NoError(t, err)
	assert.Equal(t, "opened", res.Dialog)

	// 4. Builder has work, test_writer does not: missing while builder is not
	// running; not_needed once guide and builder are running (with test_writer
	// still stopped).
	current = controlplane.TeamAssignments{BuilderHasWork: true, TestWriterHasWork: false}
	host.terminals = []TeamTerminal{liveTerminal(guideHandle)}
	res, err = server.EnsureTeam(teamContext(state))
	require.NoError(t, err)
	assert.Equal(t, "opened", res.Dialog)

	host.terminals = []TeamTerminal{liveTerminal(guideHandle), liveTerminal(builderHandle)}
	res, err = server.EnsureTeam(teamContext(state))
	require.NoError(t, err)
	assert.Equal(t, "not_needed", res.Dialog)

	// 5. Test writer has work and is not running: opens the dialog.
	current = controlplane.TeamAssignments{BuilderHasWork: false, TestWriterHasWork: true}
	res, err = server.EnsureTeam(teamContext(state))
	require.NoError(t, err)
	assert.Equal(t, "opened", res.Dialog)

	host.terminals = []TeamTerminal{liveTerminal(guideHandle), liveTerminal(writerHandle)}
	res, err = server.EnsureTeam(teamContext(state))
	require.NoError(t, err)
	assert.Equal(t, "not_needed", res.Dialog)

	// 6. A failed assignment read records the bounded error on the span without
	// leaking the underlying error text, and counts every listed role.
	readErr = fmt.Errorf("/private/secret/path: read failed")
	beforeSpans := len(ensureSpans())
	res, err = server.EnsureTeam(teamContext(state))
	require.NoError(t, err)
	assert.Equal(t, "opened", res.Dialog, "builder is not running and a failed read counts every role")
	spans = ensureSpans()
	require.Len(t, spans, beforeSpans+1)
	failedSpan := spans[len(spans)-1]
	assert.Equal(t, codes.Error, failedSpan.Status().Code)
	assert.Equal(t, "team assignments could not be read", failedSpan.Status().Description)
	require.NotEmpty(t, failedSpan.Events())
	for _, ev := range failedSpan.Events() {
		for _, attr := range ev.Attributes {
			assert.NotContains(t, attr.Value.AsString(), "/private/secret/path")
		}
	}

	// 7. A fresh project.NewServer bound through controlplane.New has its
	// assignment reader bound synchronously before EnsureTeam runs.
	freshState := testState(t)
	freshDeps := fixtureTeamDependencies(freshState)
	freshServer, err := NewServer(freshState, func() {}, freshDeps)
	require.NoError(t, err)
	cp, err := controlplane.New(controlplane.Options{
		CardsDir: t.TempDir(),
		Team:     &controlplane.TeamBinding{Owner: freshState.Record.Owner, Service: freshServer},
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, cp.Close()) })
	beforeFresh := len(ensureSpans())
	_, err = freshServer.EnsureTeam(teamContext(freshState))
	require.NoError(t, err)
	spans = ensureSpans()
	require.Len(t, spans, beforeFresh+1)
	assert.Equal(t, codes.Unset, spans[len(spans)-1].Status().Code)
}
