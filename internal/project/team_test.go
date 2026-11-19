// This file exercises team ownership through recorded Herdr effects and temporary rosters.
package project

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	genproject "github.com/goadesign/surla/gen/project"
	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/controlplane"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// teamFixture supplies deterministic model pane observations without running Herdr.
type teamFixture struct {
	spaces    []TeamSpace
	terminals []TeamTerminal
	overlays  []string
	calls     []string
	creates   int
	focused   TeamSpace
	choices   []map[string]*gensurla.RoleAgentChoice
	closed    []string
	closeErr  error
}

func (f *teamFixture) Spaces(context.Context) ([]TeamSpace, error) {
	f.calls = append(f.calls, "spaces")
	return f.spaces, nil
}

func (f *teamFixture) CreateSpace(_ context.Context, name, folder string) (TeamSpace, error) {
	f.creates++
	s := TeamSpace{ID: fmt.Sprintf("space-%d", f.creates), Name: name, Folder: folder}
	f.spaces = append(f.spaces, s)
	f.calls = append(f.calls, "create")
	return s, nil
}

func (f *teamFixture) Terminals(_ context.Context, root, space string, handles []string) ([]TeamTerminal, error) {
	f.calls = append(f.calls, "terminals")
	var terminals []TeamTerminal
	for _, terminal := range f.terminals {
		if terminal.Labels["chorus.project"] == root && terminal.Labels["herdr.space"] == space &&
			slices.Contains(handles, terminal.Labels["chorus.handle"]) {
			terminals = append(terminals, terminal)
		}
	}
	return terminals, nil
}

func (f *teamFixture) Focus(_ context.Context, space TeamSpace) error {
	f.focused = space
	f.calls = append(f.calls, "focus")
	return nil
}

func (f *teamFixture) Overlays(context.Context) ([]string, error) {
	f.calls = append(f.calls, "overlays")
	return f.overlays, nil
}

func (f *teamFixture) ReadTerminal(_ context.Context, address string) (TeamTerminal, error) {
	f.calls = append(f.calls, "read:"+address)
	for _, terminal := range f.terminals {
		if terminal.Address == address {
			return terminal, nil
		}
	}
	return TeamTerminal{}, fmt.Errorf("unknown terminal")
}

func (f *teamFixture) LabelGuide(_ context.Context, address, root, swarm, client, handle, space, tab string) error {
	f.calls = append(f.calls, "label")
	for i := range f.terminals {
		if f.terminals[i].Address == address {
			f.terminals[i].Labels = map[string]string{
				"chorus.project": root, "chorus.swarm": swarm, "chorus.agent": client, "chorus.handle": handle,
				"chorus.role": "guide", "chorus.row": "role:" + handle, "herdr.space": space, "herdr.tab": tab,
			}
			return nil
		}
	}
	return fmt.Errorf("unknown terminal")
}

func (f *teamFixture) OpenCompanion(context.Context) error {
	f.calls = append(f.calls, "companion")
	return nil
}

func (f *teamFixture) LaunchWorkers(_ context.Context, _ string, choices map[string]*gensurla.RoleAgentChoice) error {
	f.calls = append(f.calls, "launch")
	f.choices = append(f.choices, choices)
	return nil
}

func (f *teamFixture) ClosePane(_ context.Context, pane string) error {
	f.closed = append(f.closed, pane)
	return f.closeErr
}

// fixtureTeamDependencies keeps tests inside temporary folders and injected effects.
func fixtureTeamDependencies(state *State) TeamDependencies {
	return TeamDependencies{
		Lifetime: context.Background(), Host: &teamFixture{}, HerdrAvailable: true,
		RosterDirectory: filepath.Join(state.Dir, "test-rosters"), Shell: "/bin/zsh",
		Connect: func(context.Context) (TeamConnection, error) {
			return TeamConnection{Endpoint: "http://chorus.test"}, nil
		},
		DiscoverAgents: func(context.Context) ([]*gensurla.InstalledAgentOption, error) {
			return []*gensurla.InstalledAgentOption{
				{Client: "claude", Program: "claude", ModelSupported: true, HeadlessSupported: true},
				{Client: "codex", Program: "codex", ModelSupported: true, HeadlessSupported: true},
			}, nil
		},
	}
}

// teamContext grants management through the same trusted principal boundary used by HTTP.
func teamContext(state *State) context.Context {
	return controlplane.WithPrincipal(context.Background(), controlplane.Principal{ID: state.Record.Owner, CallerID: "management", Kind: controlplane.CallerManagement})
}

// fixtureAssignments selects one stable active build without starting a control plane.
func fixtureAssignments(server *Server, writer bool) {
	server.BindAssignments(func(context.Context) (controlplane.TeamAssignments, error) {
		return controlplane.TeamAssignments{
			BuildIdentity:  &controlplane.BuildIdentity{ChangeID: "change", StartedAt: time.Unix(10, 0)},
			BuilderHasWork: true, TestWriterHasWork: writer,
		}, nil
	})
}

// attachFixtureGuide adopts the user's live pane in place.
func attachFixtureGuide(t *testing.T, server *Server, host *teamFixture) *genproject.AttachGuideResult {
	t.Helper()
	host.spaces = []TeamSpace{{
		ID: "space-guide", Tabs: []TeamTab{{ID: "tab-guide", Terminals: []TeamPane{{Terminal: "t-guide", Pane: "p-guide"}}}},
	}}
	host.terminals = []TeamTerminal{{Address: "t-guide", State: "live", Foreground: &TeamForeground{Argv: []string{"agy"}}}}
	address := "t-guide"
	result, err := server.AttachGuide(teamContext(server.state), &genproject.AttachGuidePayload{HcTerminalID: &address})
	require.NoError(t, err)
	require.True(t, result.AdoptedHerdr)
	return result
}
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

func keys[T any](values map[string]T) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	return result
}

// testAgent records the immutable handle and local credential path.
type testAgent struct {
	ActorID        string
	CredentialFile string
}

func registerTestAgent(server *Server, handle, role, folder string) (*testAgent, error) {
	actual, err := server.registerAgent(handle, agentRecord{Role: role, Folder: folder})
	if err != nil {
		return nil, err
	}
	return &testAgent{ActorID: actual, CredentialFile: server.state.AgentFile(actual)}, nil
}
