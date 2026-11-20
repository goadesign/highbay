// This file supplies bounded stand-ins for every new startup effect, including tagged tests.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	genproject "github.com/goadesign/surla/gen/project"
	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/project"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureTeamHost keeps runtime helper effects inside each test-owned state root.
type fixtureTeamHost struct{ space project.TeamSpace }

// Spaces returns the fixture listing without reading Herdr.
func (f *fixtureTeamHost) Spaces(context.Context) ([]project.TeamSpace, error) {
	if f.space.ID == "" {
		return nil, nil
	}
	return []project.TeamSpace{f.space}, nil
}

// CreateSpace records the requested local space and returns its terminal mapping.
func (f *fixtureTeamHost) CreateSpace(_ context.Context, name, folder string) (project.TeamSpace, error) {
	f.space = project.TeamSpace{ID: "fixture-space", Name: name, Folder: folder, Tabs: []project.TeamTab{{ID: "fixture-tab", Terminals: []project.TeamPane{{Terminal: "fixture-terminal"}}}}}
	return f.space, nil
}

// Terminals returns only fixture foreground and label observations.
func (f *fixtureTeamHost) Terminals(context.Context, string, string, []string) ([]project.TeamTerminal, error) {
	return nil, nil
}

// Focus records the requested fixture focus without touching a window.
func (f *fixtureTeamHost) Focus(context.Context, project.TeamSpace) error { return nil }

// Overlays returns the fixture dialog names.
func (f *fixtureTeamHost) Overlays(context.Context) ([]string, error) { return nil, nil }

// ReadTerminal refuses every unknown fixture terminal.
func (f *fixtureTeamHost) ReadTerminal(context.Context, string) (project.TeamTerminal, error) {
	return project.TeamTerminal{}, errors.New("fixture has no observed terminal")
}

// LabelGuide leaves the fixture desktop untouched.
func (f *fixtureTeamHost) LabelGuide(context.Context, string, string, string, string, string, string, string) error {
	return nil
}

// OpenCompanion leaves the fixture desktop untouched.
func (f *fixtureTeamHost) OpenCompanion(context.Context) error { return nil }

// LaunchWorkers acknowledges the fixture launch without running an agent.
func (f *fixtureTeamHost) LaunchWorkers(context.Context, string, map[string]*gensurla.RoleAgentChoice) error {
	return nil
}

// ClosePane leaves the fixture desktop untouched.
func (f *fixtureTeamHost) ClosePane(context.Context, string) error { return nil }

// fixtureTeamDependencies binds only the caller's temporary state directory and recorded effects.
func fixtureTeamDependencies(state *project.State) project.TeamDependencies {
	return project.TeamDependencies{Lifetime: context.Background(), Host: &fixtureTeamHost{}, RosterDirectory: filepath.Join(state.Dir, "fixture-rosters"), Shell: "/bin/zsh", Connect: fixtureTeamConnection}
}

// fixtureTeamConnection leaves observations unknown without starting Chorus.
func fixtureTeamConnection(context.Context) (project.TeamConnection, error) {
	return project.TeamConnection{Endpoint: "http://chorus.test"}, nil
}

// bindFixtureTeamEffects explicitly opts a journey into test-owned team effects.
// When SURLA_TEST_PRESENCE_DIR names a folder, the team step reports there, by
// names and booleans only, which provider variables the server's process and
// Herdr's copy of its environment hold (writePresence).
func bindFixtureTeamEffects(runtime *projectRuntime) {
	runtime.teamDependencies = func(ctx context.Context, herdrEnv []string) (project.TeamDependencies, error) {
		if dir := os.Getenv("SURLA_TEST_PRESENCE_DIR"); dir != "" {
			if err := errors.Join(
				writePresence(filepath.Join(dir, "server-process"), os.Environ()),
				writePresence(filepath.Join(dir, "server-herdr"), herdrEnv),
			); err != nil {
				return project.TeamDependencies{}, err
			}
		}
		root, err := runtime.configRoot()
		if err != nil {
			return project.TeamDependencies{}, err
		}
		return project.TeamDependencies{
			Lifetime: ctx, Host: &fixtureTeamHost{}, RosterDirectory: filepath.Join(root, "fixture-rosters"),
			Shell: "/bin/zsh", Connect: fixtureTeamConnection, HerdrAvailable: true,
			DiscoverAgents: func(context.Context) ([]*gensurla.InstalledAgentOption, error) {
				return []*gensurla.InstalledAgentOption{{Client: "codex", Program: "/fixture/codex"}}, nil
			},
		}, nil
	}
}

func TestTeamCommandProtocol(t *testing.T) {
	var calls [][]string
	c := teamCommands{core: "herdr-core", app: "herdr-app", chorus: "chorus", run: func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		switch args[0] {
		case "do":
			if args[1] == "space.new" {
				return []byte(`{"space":"s1","tab":"t1","terminal":"term1"}`), nil
			}
			return []byte(`{}`), nil
		case "ls":
			if name == "herdr-app" {
				return []byte(`{"spaces":[{"id":"s1","name":"team","folder":"/project","tabs":[{"id":"t1","terminals":[{"terminal":"term1"}]}]},{"id":"remote/s2"}]}`), nil
			}
			return []byte(`{"terminals":[{"address":"term1"}]}`), nil
		case "get":
			return []byte(`{"address":"term1","state":"live","labels":{"chorus.handle":"guide"},"foreground":{"argv":["codex"]}}`), nil
		default:
			return []byte(`{"overlays":["chorus-launcher"]}`), nil
		}
	}}
	spaces, err := c.Spaces(t.Context())
	require.NoError(t, err)
	require.Len(t, spaces, 1)
	created, err := c.CreateSpace(t.Context(), "project's team", "/project with spaces")
	require.NoError(t, err)
	assert.Equal(t, "s1", created.ID)
	assert.JSONEq(t, `{"name":"project's team","folder":"/project with spaces","machine":"","focus":false}`, calls[1][3])
	require.NoError(t, c.Focus(t.Context(), created))
	require.NoError(t, c.OpenCompanion(t.Context()))
	require.NoError(t, c.LabelGuide(t.Context(), "term1", "/project with spaces", "swarm", "codex", "guide", "s1", "t1"))
	assert.Equal(t, []string{
		"herdr-core", "label", "term1", "herdr.space=s1", "herdr.tab=t1", "chorus.project=/project with spaces",
		"chorus.swarm=swarm", "chorus.agent=codex", "chorus.handle=guide", "chorus.role=guide",
		"chorus.row=role:guide", "chorus.options=",
	}, calls[len(calls)-1])
	require.NoError(t, c.ClosePane(t.Context(), "pane"))
	c.input = func(_ context.Context, _ []string, stdin io.Reader, program string, args ...string) error {
		assert.Equal(t, "chorus", program)
		assert.Equal(t, []string{"team", "launch", "--space", "s1", "--choices", "-"}, args)
		raw, err := io.ReadAll(stdin)
		require.NoError(t, err)
		assert.JSONEq(t, `{"builder":{"client":"codex","program":"codex","model":null,"thinking":null,"yolo":false}}`, string(raw))
		return nil
	}
	require.NoError(t, c.LaunchWorkers(t.Context(), "s1", map[string]*gensurla.RoleAgentChoice{"builder": {Client: "codex", Program: "codex"}}))
	terminals, err := c.Terminals(t.Context(), "/project", "s1", []string{"guide"})
	require.NoError(t, err)
	require.Len(t, terminals, 1)
	overlays, err := c.Overlays(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"chorus-launcher"}, overlays)
	for _, call := range calls {
		if len(call) > 1 && call[1] == "do" {
			require.Len(t, call, 4)
			var payload map[string]json.RawMessage
			assert.NoError(t, json.Unmarshal([]byte(call[3]), &payload))
		}
	}
	for _, reply := range []string{`{"error":"private"}`, `broken`, `{}`, `null`} {
		c.run = func(context.Context, []string, string, ...string) ([]byte, error) { return []byte(reply), nil }
		_, err := c.Spaces(t.Context())
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "private")
	}
	assert.Equal(t, time.Second, projectHTTPClient().Timeout)
	assert.Equal(t, 50*time.Second, teamHTTPClient().Timeout)
}

// TestAttachGuideTeamFailureIsFixed drives the CLI decoder with the team
// failures attach_guide's owner returns, as surla stdio meets them when a guide
// attaches. Each names its repair and the retry is calling the Surla tool
// again, never typing surla. project_state comes from a real project server
// whose registered guide credential is damaged; stopping comes from a stand-in
// reply with the owner's exact body.
func TestAttachGuideTeamFailureIsFixed(t *testing.T) {
	t.Run("project_state", func(t *testing.T) {
		isolateServeWorkspace(t)
		state, err := fixtureProjectState(t.TempDir())
		require.NoError(t, err)
		management := startStdioProjectServer(t, state)
		guide, err := attachFixtureGuide(t.Context(), state, management)
		require.NoError(t, err)
		const garbage = "not-a-credential-value"
		require.NoError(t, os.WriteFile(state.AgentFile(guide.Handle), []byte(garbage), 0o600))

		_, err = projectClient(state).AttachGuide(t.Context(), &genproject.AttachGuidePayload{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "a file in this project's private state "+state.Dir+" could not be used")
		assert.Contains(t, err.Error(), "chmod 600 on that file")
		assert.Contains(t, err.Error(), "then call the Surla tool again")
		assert.NotContains(t, err.Error(), garbage)
		assert.NotContains(t, strings.ToLower(err.Error()), "run surla")
	})
	t.Run("stopping", func(t *testing.T) {
		isolateServeWorkspace(t)
		state, err := fixtureProjectState(t.TempDir())
		require.NoError(t, err)
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("goa-error", "team_unavailable")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, err := io.WriteString(w, `{"name":"team_unavailable","cause":"stopping"}`)
			assert.NoError(t, err)
		}))
		defer api.Close()
		state.Record.Port = api.Listener.Addr().(*net.TCPAddr).Port
		require.NoError(t, state.SaveRunning(project.Running{ManagementCredential: strings.Repeat("a", 64)}))
		_, err = projectClient(state).AttachGuide(t.Context(), &genproject.AttachGuidePayload{})
		require.ErrorContains(t, err, "the project server is stopping; call the Surla tool again to start it (HTTP 503)")
	})
}

// TestInstalledTeamOptions proves argument discovery against local programs
// and their fixture help/catalog output without launching an agent.
func TestInstalledTeamOptions(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	for _, name := range []string{"codex", "claude", "opencode"} {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	}
	commands := teamCommands{run: func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
		switch filepath.Base(name) {
		case "codex":
			if strings.Join(args, " ") == "debug models" {
				return []byte(`{"models":[{"slug":"fixture","visibility":"list","supported_reasoning_levels":[{"effort":"high"}]},{"slug":"hidden","visibility":"hide"}]}`), nil
			}
			return []byte("exec\n-c, --config <KEY=VALUE>\n--model <MODEL>\n--dangerously-bypass-approvals-and-sandbox"), nil
		case "claude":
			return []byte("--print\n--model <NAME>\n--effort <LEVEL> (low, medium, high)"), nil
		case "opencode":
			return []byte("run\n--model NAME"), nil
		default:
			return nil, errors.New("unexpected installed fixture")
		}
	}}
	options, err := commands.discoverAgents(t.Context())
	require.NoError(t, err)
	require.Len(t, options, 3)
	assert.Equal(t, "codex", options[0].Client)
	assert.Equal(t, filepath.Join(bin, "codex"), options[0].Program)
	assert.Equal(t, []string{"fixture"}, options[0].Models)
	assert.Equal(t, []string{"high"}, options[0].Efforts)
	assert.True(t, options[0].HeadlessSupported)
	assert.True(t, options[0].YoloSupported)
	assert.Equal(t, "claude", options[1].Client)
	assert.Equal(t, []string{"low", "medium", "high"}, options[1].Efforts)
	assert.True(t, options[1].HeadlessSupported)
	assert.False(t, options[1].YoloSupported)
	assert.Equal(t, "opencode", options[2].Client)
	assert.True(t, options[2].HeadlessSupported)
	assert.False(t, options[2].ThinkingSupported)
}
