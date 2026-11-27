// This file binds herdr-core, Chorus and the installed coding agents at the
// executable's composition boundary.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/hc"
	"github.com/goadesign/highbay/internal/modelsettings"
	"github.com/goadesign/highbay/internal/project"
)

type (
	// teamCommands runs the Chorus command and the coding agents' own
	// option probes. It keeps argv typed, closes stdin, and never returns
	// external output in errors. Every command runs with an explicit
	// environment.
	teamCommands struct {
		// chorus is the Chorus program name, resolved on the server's PATH or ~/.local/bin.
		chorus string
		// run executes one bounded command with env and returns its output.
		run func(ctx context.Context, env []string, program string, args ...string) ([]byte, error)
		// resolveHC optionally locates hc so auto-started Chorus daemons inherit its directory on PATH.
		resolveHC func() (string, error)
		// agentEnv is the environment of the coding agents' own programs:
		// the server's original login environment, unchanged, so each client
		// finds the user's sign-in and model provider settings.
		agentEnv []string
		// chorusEnv is the environment of Chorus commands: the highbay command's without any
		// model provider family (withoutProviderFamilies).
		chorusEnv []string
	}

	// coreTerminals adapts an hc.Client to project.TeamTerminals: the one
	// difference is that Attach returns the stream as the interface the team
	// relays through.
	coreTerminals struct {
		*hc.Client
	}

	// teamCommandExit reports a team program's non-zero exit without its output.
	teamCommandExit struct {
		code int
	}
)

// agentIdentityVariables are the variables herdr-core sets for every terminal
// it runs, describing that terminal. An agent must get the core's values for
// its own terminal, never the server's.
var agentIdentityVariables = []string{"TERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "COLORTERM"}

// effortOption finds the choice group advertised for --effort.
var effortOption = regexp.MustCompile(`(?m)--effort[^\n()]*\((?:choices:\s*)?([a-z0-9_, |\t-]+)\)`)

// Attach opens a viewer stream and returns it as the team's stream interface.
func (c coreTerminals) Attach(ctx context.Context, req hc.AttachRequest) (project.TerminalStream, error) {
	stream, err := c.Client.Attach(ctx, req)
	if err != nil {
		return nil, err
	}
	return stream, nil
}

// Error reports only the exit code.
func (e teamCommandExit) Error() string { return fmt.Sprintf("team command exited %d", e.code) }

// runTeamCommand bounds one external command run with env; stderr is never echoed to the caller.
func runTeamCommand(ctx context.Context, env []string, program string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, program, args...)
	command.Env = env
	raw, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, teamCommandExit{code: exit.ExitCode()}
		}
		return nil, fmt.Errorf("%s command failed", filepath.Base(program))
	}
	return raw, nil
}

// projectTeamDependencies binds the team's external effects to the server's
// original login environment. Nothing runs here: herdr-core is dialed and
// Chorus resolved when the team is first used.
func projectTeamDependencies(ctx context.Context, loginEnv []string) (project.TeamDependencies, error) {
	commands := teamCommands{
		chorus: "chorus", run: runServerTeamCommand, resolveHC: projectHerdrProgram,
		agentEnv: loginEnv, chorusEnv: withoutProviderFamilies(loginEnv),
	}
	return project.TeamDependencies{
		Lifetime:           ctx,
		DialTerminals:      dialTerminals(loginEnv),
		ConnectChorus:      commands.connectChorus,
		DiscoverAgents:     commands.discoverAgents,
		PrepareWorkerTrust: bindPrepareWorkerTrust(loginEnv),
		AgentCommand:       agentCommand,
		AgentEnv:           agentEnvironment(loginEnv),
		Shell:              os.Getenv("SHELL"),
	}, nil
}

// dialTerminals returns the team's herdr-core dialer. Each dial finds the hc
// program and runs "hc connect" with env, which starts the core when none
// runs.
func dialTerminals(env []string) func(context.Context) (project.TeamTerminals, error) {
	return func(ctx context.Context) (project.TeamTerminals, error) {
		program, err := projectHerdrProgram()
		if err != nil {
			return nil, err
		}
		client, err := hc.Dial(ctx, hc.ExecConnector(program, env))
		if err != nil {
			return nil, err
		}
		return coreTerminals{client}, nil
	}
}

// agentCommand builds the command line that starts choice's coding agent
// interactively with prompt as its first message. Each client spells its
// model, reasoning effort, unattended-permission and first-prompt switches
// differently; the clients that take the prompt as a plain argument get "--"
// before it, so a prompt can never be read as a switch.
func agentCommand(choice *genhighbay.RoleAgentChoice, prompt string) ([]string, error) {
	argv := []string{choice.Program}
	if choice.Model != nil {
		argv = append(argv, "--model", *choice.Model)
	}
	var unattended string
	promptSwitch := "--"
	switch choice.Client {
	case "claude":
		unattended = "--dangerously-skip-permissions"
		if choice.Thinking != nil {
			argv = append(argv, "--effort", *choice.Thinking)
		}
	case "codex":
		unattended = "--dangerously-bypass-approvals-and-sandbox"
		if choice.Thinking != nil {
			argv = append(argv, "-c", "model_reasoning_effort="+strconv.Quote(*choice.Thinking))
		}
	case "antigravity":
		unattended, promptSwitch = "--dangerously-skip-permissions", "--prompt-interactive"
		if choice.Thinking != nil {
			argv = append(argv, "--effort", *choice.Thinking)
		}
	case "cursor":
		unattended = "--yolo"
	case "gemini":
		unattended, promptSwitch = "--yolo", "--prompt-interactive"
	case "opencode":
		promptSwitch = "--prompt"
	default:
		return nil, fmt.Errorf("unknown coding agent client %q", choice.Client)
	}
	if choice.Yolo {
		if unattended == "" {
			return nil, fmt.Errorf("%s has no unattended permission mode", choice.Client)
		}
		argv = append(argv, unattended)
	}
	return append(argv, promptSwitch, prompt), nil
}

// agentEnvironment turns the server's login environment into the variables
// each agent's terminal starts with. It leaves out the variables that
// describe the server's own terminal: herdr-core supplies those for the
// agent's terminal.
func agentEnvironment(loginEnv []string) map[string]string {
	env := make(map[string]string, len(loginEnv))
	for _, entry := range loginEnv {
		name, value, found := strings.Cut(entry, "=")
		if !found || name == "" || strings.HasPrefix(name, "HC_") || slices.Contains(agentIdentityVariables, name) {
			continue
		}
		env[name] = value
	}
	return env
}

// runServerTeamCommand resolves a required team program at the external operation boundary
// (including ~/.local/bin for standalone companions) and runs it with env.
func runServerTeamCommand(ctx context.Context, env []string, program string, args ...string) ([]byte, error) {
	resolved, err := resolveServerCompanion(program)
	if err != nil {
		return nil, errors.New("required team program is unavailable on the server login PATH")
	}
	return runTeamCommand(ctx, env, resolved, args...)
}

// withoutProviderFamilies returns environ without any model provider family variable. The
// highbay command gives it to the programs that must never receive the shell's provider
// settings: Chorus and the browser opener. herdr-core and the agents it runs keep the
// environment unchanged.
func withoutProviderFamilies(environ []string) []string {
	_, rest := modelsettings.Split(environ)
	return rest
}

// takeProviderFamilies is serve's first step: it returns a copy of the server's original
// environment, for herdr-core and the agents only, and removes every model provider family
// from this process, so no later read or child takes a provider setting from it. Removal
// changes what children inherit, not the server's initial environment block.
func takeProviderFamilies() ([]string, error) {
	original := os.Environ()
	family, _ := modelsettings.Split(original)
	for _, kv := range family {
		name, _, _ := strings.Cut(kv, "=")
		if err := os.Unsetenv(name); err != nil {
			return nil, fmt.Errorf("remove model provider settings from the project server's environment: %w", err)
		}
	}
	return original, nil
}

// codexAgentModels reads visible local model IDs and their advertised efforts.
func codexAgentModels(raw []byte) ([]string, []string) {
	catalog := struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
			Levels     []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}{}
	models, efforts := []string{}, []string{}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return models, efforts
	}
	for _, model := range catalog.Models {
		if model.Visibility != "list" || !agentOptionID(model.Slug) {
			continue
		}
		models = append(models, model.Slug)
		for _, level := range model.Levels {
			if agentOptionID(level.Effort) && !slices.Contains(efforts, level.Effort) {
				efforts = append(efforts, level.Effort)
			}
		}
	}
	return models, efforts
}

// helpContains matches a complete switch or subcommand token.
func helpContains(help, token string) bool {
	for field := range strings.FieldsFuncSeq(help, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-'
	}) {
		if field == token {
			return true
		}
	}
	return false
}

// helpEfforts returns the exact reasoning levels named by the installed CLI.
func helpEfforts(help string) []string {
	match := effortOption.FindStringSubmatch(help)
	if len(match) < 2 {
		return []string{}
	}
	return strings.FieldsFunc(match[1], func(r rune) bool { return r == ',' || r == '|' || unicode.IsSpace(r) })
}

// agentOptionID limits catalog entries to literal model or effort IDs.
func agentOptionID(value string) bool {
	return value != "" && value[0] != '-' && len(value) <= 256 && !strings.ContainsFunc(value, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("-._:/[],=+", r)
	})
}

// discoverAgents checks the supported local program names and probes their
// capabilities without starting an agent session. One slow CLI cannot hide
// the other installed choices.
func (c teamCommands) discoverAgents(ctx context.Context) ([]*genhighbay.InstalledAgentOption, error) {
	type installedClient struct {
		key, title, yolo, yoloEffect string
		names                        []string
		helpPrefix                   []string
	}
	clients := []installedClient{
		{key: "codex", title: "Codex", yolo: "--dangerously-bypass-approvals-and-sandbox",
			yoloEffect: "Disables approval prompts and command sandboxing.", names: []string{"codex"}},
		{key: "claude", title: "Claude Code", yolo: "--dangerously-skip-permissions",
			yoloEffect: "Bypasses tool permission checks.", names: []string{"claude"}},
		{key: "antigravity", title: "Antigravity", yolo: "--dangerously-skip-permissions",
			yoloEffect: "Automatically approves tool permission requests.", names: []string{"agy", "antigravity"}},
		{key: "cursor", title: "Cursor", yolo: "--yolo",
			yoloEffect: "Allows commands unless explicitly denied, with the client's sandbox setting.", names: []string{"cursor-agent", "agent"}},
		{key: "gemini", title: "Gemini CLI", yolo: "--yolo",
			yoloEffect: "Automatically approves all tool calls. Gemini CLI controls sandbox behavior.", names: []string{"gemini"}},
		{key: "opencode", title: "OpenCode", names: []string{"opencode"}, helpPrefix: []string{"run"}},
	}
	options := make([]*genhighbay.InstalledAgentOption, len(clients))
	var probes sync.WaitGroup
	for index, client := range clients {
		probes.Go(func() {
			var program string
			for _, name := range client.names {
				path, err := exec.LookPath(name)
				if err == nil && filepath.IsAbs(path) {
					program = path
					break
				}
			}
			if program == "" {
				return
			}
			bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			raw, err := c.run(bounded, c.agentEnv, program, "--help")
			if err != nil {
				return
			}
			help := string(raw)
			if len(client.helpPrefix) > 0 {
				raw, err := c.run(bounded, c.agentEnv, program, append(client.helpPrefix, "--help")...)
				if err == nil {
					help = string(raw)
				}
			}
			option := &genhighbay.InstalledAgentOption{
				Client: client.key, Program: program, Title: client.title,
				ModelSupported: helpContains(help, "--model"),
				YoloSupported:  client.yolo != "" && helpContains(help, client.yolo),
				Models:         []string{}, Efforts: []string{},
			}
			if option.YoloSupported {
				option.YoloEffect = &client.yoloEffect
			}
			c.discoverAgentModels(bounded, option, help)
			options[index] = option
		})
	}
	probes.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return slices.DeleteFunc(options, func(option *genhighbay.InstalledAgentOption) bool { return option == nil }), nil
}

// discoverAgentModels reads only client-owned catalogs and help. Clients that
// accept models without publishing a catalog keep an empty advertised list.
func (c teamCommands) discoverAgentModels(ctx context.Context, option *genhighbay.InstalledAgentOption, help string) {
	if !option.ModelSupported {
		return
	}
	switch option.Client {
	case "claude":
		option.Models = []string{"opus", "sonnet", "haiku"}
		option.Efforts = helpEfforts(help)
	case "antigravity":
		option.Efforts = helpEfforts(help)
		option.Models = c.listedAgentModels(ctx, option.Program, "\t", "models")
	case "cursor":
		option.Models = c.listedAgentModels(ctx, option.Program, " - ", "--list-models")
	case "codex":
		raw, err := c.run(ctx, c.agentEnv, option.Program, "debug", "models")
		if err == nil {
			option.Models, option.Efforts = codexAgentModels(raw)
		}
		if !helpContains(help, "--config") && !helpContains(help, "-c") {
			option.Efforts = []string{}
		}
	}
	option.ThinkingSupported = len(option.Efforts) > 0
}

// listedAgentModels narrows the installed CLI's tabular output to model IDs.
func (c teamCommands) listedAgentModels(ctx context.Context, program, separator string, args ...string) []string {
	raw, err := c.run(ctx, c.agentEnv, program, args...)
	models := []string{}
	if err != nil {
		return models
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		model, title, found := strings.Cut(strings.TrimSpace(line), separator)
		if found && title != "" && agentOptionID(model) && !slices.Contains(models, model) {
			models = append(models, model)
		}
	}
	return models
}
