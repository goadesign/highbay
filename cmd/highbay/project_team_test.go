// This file supplies bounded stand-ins for every new startup effect, including
// tagged tests, and tests the team's bindings at the executable's boundary.
package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	genchorus "github.com/goadesign/chorus/gen/view"
	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/hc"
	"github.com/goadesign/highbay/internal/project"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	// fixtureTerminals stands in for herdr-core in tests that start a real
	// project server: it keeps the terminals it is asked to run in memory
	// and reports no changes, so no test reaches a real core.
	fixtureTerminals struct {
		// mu guards terminals.
		mu sync.Mutex
		// terminals holds what Run created, by address.
		terminals map[string]hc.Terminal
		// done never closes: the stand-in connection never ends.
		done chan struct{}
	}

	// fixtureChorus stands in for the local Chorus server: an empty swarm
	// that accepts every post.
	fixtureChorus struct{}
)

// fixtureTeamDependencies binds the team to in-memory stand-ins.
func fixtureTeamDependencies(*project.State) project.TeamDependencies {
	return fixtureTeam(context.Background())
}

// fixtureTeam returns team dependencies that reach no program on this
// computer: an in-memory herdr-core and Chorus, one installed coding agent,
// and a command line that only names it.
func fixtureTeam(lifetime context.Context) project.TeamDependencies {
	terminals := &fixtureTerminals{terminals: map[string]hc.Terminal{}, done: make(chan struct{})}
	return project.TeamDependencies{
		Lifetime: lifetime,
		Shell:    "/bin/zsh",
		DialTerminals: func(context.Context) (project.TeamTerminals, error) {
			return terminals, nil
		},
		ConnectChorus: func(context.Context) (project.TeamChorus, error) {
			return fixtureChorus{}, nil
		},
		DiscoverAgents: func(context.Context) ([]*genhighbay.InstalledAgentOption, error) {
			return []*genhighbay.InstalledAgentOption{{Client: "codex", Program: "/fixture/codex"}}, nil
		},
		AgentCommand: func(choice *genhighbay.RoleAgentChoice, prompt string) ([]string, error) {
			return []string{choice.Program, "--", prompt}, nil
		},
	}
}

// bindFixtureTeamEffects explicitly opts a journey into test-owned team effects.
// When HIGHBAY_TEST_PRESENCE_DIR names a folder, the team step reports there, by
// names and booleans only, which provider variables the server's process and
// the agents' copy of its environment hold (writePresence).
func bindFixtureTeamEffects(runtime *projectRuntime) {
	runtime.teamDependencies = func(ctx context.Context, loginEnv []string) (project.TeamDependencies, error) {
		if dir := os.Getenv("HIGHBAY_TEST_PRESENCE_DIR"); dir != "" {
			if err := errors.Join(
				writePresence(filepath.Join(dir, "server-process"), os.Environ()),
				writePresence(filepath.Join(dir, "server-herdr"), loginEnv),
			); err != nil {
				return project.TeamDependencies{}, err
			}
		}
		return fixtureTeam(ctx), nil
	}
}

func (f *fixtureTerminals) Run(_ context.Context, req hc.RunRequest) (hc.Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	terminal := hc.Terminal{
		Address: "t" + string(rune('1'+len(f.terminals))), Labels: req.Labels, Argv: req.Argv, Cwd: req.Cwd,
		State: hc.StateLive, CreatedMS: uint64(time.Now().UnixMilli()),
	}
	f.terminals[terminal.Address] = terminal
	return terminal, nil
}

func (f *fixtureTerminals) List(_ context.Context, req hc.ListRequest) ([]hc.Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var found []hc.Terminal
	for _, terminal := range f.terminals {
		matches := req.All || terminal.State == hc.StateLive
		for key, value := range req.Labels {
			matches = matches && terminal.Labels[key] == value
		}
		if matches {
			found = append(found, terminal)
		}
	}
	return found, nil
}

func (f *fixtureTerminals) Get(_ context.Context, address string) (hc.Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	terminal, found := f.terminals[address]
	if !found {
		return hc.Terminal{}, &hc.KindError{Kind: "not_found", Message: "no terminal " + address}
	}
	return terminal, nil
}

func (f *fixtureTerminals) Label(ctx context.Context, address string, _ map[string]string) (hc.Terminal, error) {
	return f.Get(ctx, address)
}

func (f *fixtureTerminals) Interrupt(context.Context, string) (hc.InterruptResult, error) {
	return hc.InterruptResult{Outcome: "stopped", Key: "esc"}, nil
}

func (f *fixtureTerminals) Kill(ctx context.Context, address string) (hc.Terminal, error) {
	terminal, err := f.Get(ctx, address)
	if err != nil {
		return hc.Terminal{}, err
	}
	terminal.State = hc.StateFinished
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminals[address] = terminal
	return terminal, nil
}

func (f *fixtureTerminals) Remove(_ context.Context, address string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.terminals, address)
	return nil
}

// SubscribeEvents reports the terminals once and then nothing until ctx ends.
func (f *fixtureTerminals) SubscribeEvents(ctx context.Context, labels map[string]string, emit func(hc.StreamUpdate)) error {
	terminals, err := f.List(ctx, hc.ListRequest{All: true, Labels: labels})
	if err != nil {
		return err
	}
	emit(hc.Snapshot{Terminals: terminals})
	<-ctx.Done()
	return ctx.Err()
}

func (f *fixtureTerminals) Attach(context.Context, hc.AttachRequest) (project.TerminalStream, error) {
	return nil, hc.ErrUnavailable
}

func (f *fixtureTerminals) Done() <-chan struct{} { return f.done }

func (f *fixtureTerminals) Close() error { return nil }

// View waits out the caller's context and then says nothing changed.
func (fixtureChorus) View(ctx context.Context, p *genchorus.ViewPayload) (*genchorus.SwarmView, error) {
	if p.IfNoneMatch == nil {
		return &genchorus.SwarmView{Swarm: p.Swarm, Etag: "fixture"}, nil
	}
	<-ctx.Done()
	return nil, genchorus.MakeNotModified(ctx.Err())
}

func (fixtureChorus) PostMessage(_ context.Context, p *genchorus.PostMessagePayload) (*genchorus.PostMessageResult, error) {
	return &genchorus.PostMessageResult{Target: p.To, Message: &genchorus.Message{Seq: 1, Sender: p.Sender, Text: p.Text, ReplyTo: p.ReplyTo}}, nil
}

// TestAgentCommand checks the command line built for each supported coding
// agent: where its model, reasoning effort and unattended-permission switches
// go, and how its first message is passed.
func TestAgentCommand(t *testing.T) {
	model, effort := "big-model", "high"
	for _, test := range []struct {
		name   string
		choice genhighbay.RoleAgentChoice
		want   []string
	}{
		{name: "claude, plain", choice: genhighbay.RoleAgentChoice{Client: "claude", Program: "/bin/claude"}, want: []string{"/bin/claude", "--", "first message"}},
		{
			name:   "claude, every option",
			choice: genhighbay.RoleAgentChoice{Client: "claude", Program: "/bin/claude", Model: &model, Thinking: &effort, Yolo: true},
			want:   []string{"/bin/claude", "--model", "big-model", "--effort", "high", "--dangerously-skip-permissions", "--", "first message"},
		},
		{
			name:   "codex, every option",
			choice: genhighbay.RoleAgentChoice{Client: "codex", Program: "/bin/codex", Model: &model, Thinking: &effort, Yolo: true},
			want: []string{
				"/bin/codex", "--model", "big-model", "-c", `model_reasoning_effort="high"`,
				"--dangerously-bypass-approvals-and-sandbox", "--", "first message",
			},
		},
		{
			name:   "antigravity, every option",
			choice: genhighbay.RoleAgentChoice{Client: "antigravity", Program: "/bin/agy", Model: &model, Thinking: &effort, Yolo: true},
			want:   []string{"/bin/agy", "--model", "big-model", "--effort", "high", "--dangerously-skip-permissions", "--prompt-interactive", "first message"},
		},
		{
			name:   "cursor has no effort switch",
			choice: genhighbay.RoleAgentChoice{Client: "cursor", Program: "/bin/cursor-agent", Model: &model, Thinking: &effort, Yolo: true},
			want:   []string{"/bin/cursor-agent", "--model", "big-model", "--yolo", "--", "first message"},
		},
		{
			name:   "gemini",
			choice: genhighbay.RoleAgentChoice{Client: "gemini", Program: "/bin/gemini", Yolo: true},
			want:   []string{"/bin/gemini", "--yolo", "--prompt-interactive", "first message"},
		},
		{
			name:   "opencode",
			choice: genhighbay.RoleAgentChoice{Client: "opencode", Program: "/bin/opencode", Model: &model},
			want:   []string{"/bin/opencode", "--model", "big-model", "--prompt", "first message"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := agentCommand(&test.choice, "first message")
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}

	t.Run("a first message that looks like a switch stays a message", func(t *testing.T) {
		got, err := agentCommand(&genhighbay.RoleAgentChoice{Client: "claude", Program: "/bin/claude"}, "--dangerously-skip-permissions")
		require.NoError(t, err)
		assert.Equal(t, []string{"/bin/claude", "--", "--dangerously-skip-permissions"}, got)
	})

	for _, test := range []struct {
		name   string
		choice genhighbay.RoleAgentChoice
	}{
		{name: "a client this version does not know", choice: genhighbay.RoleAgentChoice{Client: "other", Program: "/bin/other"}},
		{name: "unattended mode for a client without one", choice: genhighbay.RoleAgentChoice{Client: "opencode", Program: "/bin/opencode", Yolo: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := agentCommand(&test.choice, "first message")
			assert.Error(t, err)
		})
	}
}

// TestAgentEnvironment checks which of the server's login variables an agent
// starts with: all but those that describe the server's own terminal.
func TestAgentEnvironment(t *testing.T) {
	got := agentEnvironment([]string{
		"PATH=/usr/bin", "ANTHROPIC_API_KEY=secret", "EMPTY=", "WITH=equals=inside",
		"TERM=xterm", "TERM_PROGRAM=ghostty", "TERM_PROGRAM_VERSION=1", "COLORTERM=truecolor",
		"HC_TERMINAL_ID=t3", "HC_SESSION=dev", "=odd", "malformed",
	})
	assert.Equal(t, map[string]string{
		"PATH": "/usr/bin", "ANTHROPIC_API_KEY": "secret", "EMPTY": "", "WITH": "equals=inside",
	}, got)
}

// TestTeamHTTPClients pins the two client timeouts the CLI relies on.
func TestTeamHTTPClients(t *testing.T) {
	assert.Equal(t, time.Second, projectHTTPClient().Timeout)
	assert.Equal(t, 50*time.Second, teamHTTPClient().Timeout)
}

// TestAttachGuideTeamFailureIsFixed drives the CLI decoder with the team
// failures attach_guide's owner returns, as highbay stdio meets them when a guide
// attaches. Each names its repair and the retry is calling the Highbay tool
// again, never typing highbay. project_state comes from a real project server
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
		assert.Contains(t, err.Error(), "then call the Highbay tool again")
		assert.NotContains(t, err.Error(), garbage)
		assert.NotContains(t, strings.ToLower(err.Error()), "run highbay")
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
		require.ErrorContains(t, err, "the project server is stopping; call the Highbay tool again to start it (HTTP 503)")
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
	assert.True(t, options[0].YoloSupported)
	assert.Equal(t, "claude", options[1].Client)
	assert.Equal(t, []string{"low", "medium", "high"}, options[1].Efforts)
	assert.False(t, options[1].YoloSupported)
	assert.Equal(t, "opencode", options[2].Client)
	assert.False(t, options[2].ThinkingSupported)
}
