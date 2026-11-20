// This file binds Herdr and Chorus commands at the executable's composition boundary.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/modelsettings"
	"github.com/goadesign/surla/internal/project"
)

type (
	// teamCommands keeps argv typed, closes stdin, and never returns external output in errors.
	// Every command runs with an explicit environment: run and start receive it from the caller.
	teamCommands struct {
		core, app, chorus string
		run               func(ctx context.Context, env []string, program string, args ...string) ([]byte, error)
		// input passes closed-schema JSON to Chorus's explicit choices stdin.
		input func(context.Context, []string, io.Reader, string, ...string) error
		// herdrEnv is the environment of Herdr's own programs (herdr-app, herdr-core): the
		// surla command's, or the server's original one, unchanged, because a Herdr core
		// these programs start keeps it for every terminal, the agents' included.
		herdrEnv []string
		// chorusEnv is the environment of Chorus commands: the surla command's without any
		// model provider family (withoutProviderFamilies).
		chorusEnv []string
	}
)

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

// projectTeamDependencies binds the server's original login environment.
// The herdr-app home is shared by the roster and optional Surla companion.
func projectTeamDependencies(ctx context.Context, herdrEnv []string) (project.TeamDependencies, error) {
	commands := teamCommands{
		core: "herdr-core", app: "herdr-app", chorus: "chorus", run: runServerTeamCommand,
		input: runTeamInput, herdrEnv: herdrEnv, chorusEnv: withoutProviderFamilies(herdrEnv),
	}
	home := os.Getenv("HERDR_APP_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return project.TeamDependencies{}, err
		}
		home = filepath.Join(userHome, ".config", "herdr-app")
	}
	_, coreErr := exec.LookPath(commands.core)
	_, appErr := exec.LookPath(commands.app)
	available := coreErr == nil && appErr == nil
	connect := commands.connectChorus
	if !available {
		connect = func(context.Context) (project.TeamConnection, error) {
			return project.TeamConnection{}, nil
		}
	}
	companion, err := os.Stat(filepath.Join(home, "mods", "surla", "mod.json"))
	return project.TeamDependencies{
		Lifetime: ctx, Host: commands, Shell: os.Getenv("SHELL"), Connect: connect,
		HerdrAvailable: available, CompanionInstalled: err == nil && companion.Mode().IsRegular(),
		RosterDirectory:    filepath.Join(home, "data", "chorus-launcher", "rosters"),
		DiscoverAgents:     commands.discoverAgents,
		PrepareWorkerTrust: bindPrepareWorkerTrust(herdrEnv),
		WorkerEnv:          herdrEnv,
	}, nil
}

// json decodes command replies and checks JSON-level errors as well as process status.
func (c teamCommands) json(ctx context.Context, program string, result any, args ...string) error {
	raw, err := c.run(ctx, c.herdrEnv, program, args...)
	if err != nil {
		return err
	}
	var envelope *struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return errors.New("Herdr returned invalid JSON")
	}
	if envelope == nil {
		return errors.New("Herdr returned an empty result")
	}
	errorField := envelope.Error
	if len(errorField) > 0 && string(errorField) != "null" && string(errorField) != "false" && string(errorField) != `""` {
		return errors.New("Herdr reported a command error")
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return errors.New("Herdr returned an invalid result")
	}
	return nil
}

// do sends exactly one JSON argument, never shell syntax or a stdin protocol.
func (c teamCommands) do(ctx context.Context, command string, payload any, result any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.json(ctx, c.app, result, "do", command, string(raw))
}

// Spaces reads local spaces and their tab/terminal mapping without changing window state.
func (c teamCommands) Spaces(ctx context.Context) ([]project.TeamSpace, error) {
	var result struct {
		Spaces *[]project.TeamSpace `json:"spaces"`
	}
	if err := c.json(ctx, c.app, &result, "ls"); err != nil {
		return nil, err
	}
	if result.Spaces == nil {
		return nil, errors.New("Herdr omitted its space listing")
	}
	local := make([]project.TeamSpace, 0, len(*result.Spaces))
	for _, space := range *result.Spaces {
		if space.Machine == nil && !strings.ContainsAny(space.ID, "/\\") {
			local = append(local, space)
		}
	}
	return local, nil
}

// CreateSpace uses the space, tab and terminal identifiers returned by space.new.
func (c teamCommands) CreateSpace(ctx context.Context, name, folder string) (project.TeamSpace, error) {
	var result struct {
		Space    string `json:"space"`
		Tab      string `json:"tab"`
		Terminal string `json:"terminal"`
	}
	if err := c.do(ctx, "space.new", struct {
		Name    string `json:"name"`
		Folder  string `json:"folder"`
		Machine string `json:"machine"`
		Focus   bool   `json:"focus"`
	}{Name: name, Folder: folder}, &result); err != nil {
		return project.TeamSpace{}, err
	}
	if result.Space == "" || result.Tab == "" || result.Terminal == "" {
		return project.TeamSpace{}, errors.New("space.new returned incomplete identifiers")
	}
	return project.TeamSpace{ID: result.Space, Name: name, Folder: folder, Tabs: []project.TeamTab{{ID: result.Tab, Terminals: []project.TeamPane{{Terminal: result.Terminal}}}}}, nil
}

// Terminals selects each registered handle with both owner labels before reading facts.
func (c teamCommands) Terminals(ctx context.Context, root, space string, handles []string) ([]project.TeamTerminal, error) {
	result := make([]project.TeamTerminal, 0, len(handles))
	for _, handle := range handles {
		var listing struct {
			Terminals *[]struct {
				Address string `json:"address"`
			} `json:"terminals"`
		}
		if err := c.json(ctx, c.core, &listing, "ls", "--json", "--label", "chorus.project="+root, "--label", "herdr.space="+space, "--label", "chorus.handle="+handle); err != nil {
			return nil, err
		}
		if listing.Terminals == nil {
			return nil, errors.New("Herdr omitted its terminal listing")
		}
		for _, item := range *listing.Terminals {
			if item.Address == "" {
				return nil, errors.New("Herdr returned a missing terminal identifier")
			}
			terminal, err := c.ReadTerminal(ctx, item.Address)
			if err != nil {
				return nil, err
			}
			result = append(result, terminal)
		}
	}
	return result, nil
}

// Focus shows the owned space and focuses the requested terminal.
func (c teamCommands) Focus(ctx context.Context, space project.TeamSpace) error {
	var result map[string]json.RawMessage
	if err := c.do(ctx, "space.select", struct {
		ID string `json:"id"`
	}{space.ID}, &result); err != nil {
		return err
	}
	for _, tab := range space.Tabs {
		for _, pane := range tab.Terminals {
			if pane.Terminal != "" {
				return c.do(ctx, "pane.focus", struct {
					Terminal string `json:"terminal"`
				}{pane.Terminal}, &result)
			}
		}
	}
	return errors.New("Herdr space has no terminal to focus")
}

// Overlays reads the names reported by herdr-app status; opening a mod is a toggle.
func (c teamCommands) Overlays(ctx context.Context) ([]string, error) {
	var result struct {
		Overlays *[]string `json:"overlays"`
	}
	if err := c.json(ctx, c.app, &result, "status"); err != nil {
		return nil, err
	}
	if result.Overlays == nil {
		return nil, errors.New("Herdr status omitted overlay names")
	}
	return *result.Overlays, nil
}

// ReadTerminal loads only narrow labels and foreground facts for the exact address.
func (c teamCommands) ReadTerminal(ctx context.Context, address string) (project.TeamTerminal, error) {
	var result teamTerminalFacts
	err := c.json(ctx, c.core, &result, "get", address, "--json")
	if err == nil && result.Address != address {
		err = errors.New("Herdr returned a different terminal")
	}
	return result.terminal(), err
}

// LabelGuide identifies the existing guide row without sending terminal input.
func (c teamCommands) LabelGuide(ctx context.Context, address, root, swarm, client, handle, space, tab string) error {
	_, err := c.run(ctx, c.herdrEnv, c.core, "label", address,
		"herdr.space="+space, "herdr.tab="+tab,
		"chorus.project="+root, "chorus.swarm="+swarm, "chorus.agent="+client,
		"chorus.handle="+handle, "chorus.role=guide", "chorus.row=role:"+handle, "chorus.options=")
	return err
}

// OpenCompanion toggles only the optional Surla companion, after the owner checks visibility.
func (c teamCommands) OpenCompanion(ctx context.Context) error {
	var result map[string]json.RawMessage
	return c.do(ctx, "mod.open", struct {
		Name string `json:"name"`
	}{"surla"}, &result)
}

// LaunchWorkers lets Chorus place explicitly selected workers using the saved
// roster. The wire object preserves null model and thinking keys required by Chorus.
func (c teamCommands) LaunchWorkers(ctx context.Context, space string, choices map[string]*gensurla.RoleAgentChoice) error {
	type chorusChoice struct {
		Client   string  `json:"client"`
		Program  string  `json:"program"`
		Model    *string `json:"model"`
		Thinking *string `json:"thinking"`
		Yolo     bool    `json:"yolo"`
	}
	wire := make(map[string]chorusChoice, len(choices))
	for role, choice := range choices {
		wire[role] = chorusChoice(*choice)
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return err
	}
	return c.input(ctx, c.chorusEnv, bytes.NewReader(raw), c.chorus,
		"team", "launch", "--space", space, "--choices", "-")
}

// ClosePane ends only the explicitly verified model pane.
func (c teamCommands) ClosePane(ctx context.Context, pane string) error {
	var result map[string]json.RawMessage
	return c.do(ctx, "pane.close", struct {
		Pane string `json:"pane"`
	}{pane}, &result)
}

// runTeamInput writes small typed JSON without shell interpretation or retaining output.
func runTeamInput(ctx context.Context, env []string, input io.Reader, program string, args ...string) error {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = env
	cmd.Stdin = input
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return errors.New("Chorus could not launch the requested team")
	}
	return nil
}

// runServerTeamCommand resolves a required team program at the external operation boundary
// and runs it with env.
func runServerTeamCommand(ctx context.Context, env []string, program string, args ...string) ([]byte, error) {
	resolved, err := exec.LookPath(program)
	if err != nil {
		return nil, errors.New("required team program is unavailable on the server login PATH")
	}
	return runTeamCommand(ctx, env, resolved, args...)
}

// withoutProviderFamilies returns environ without any model provider family variable. The
// surla command gives it to the programs that must never receive the shell's provider
// settings: Chorus and the browser opener. Herdr's programs keep the environment unchanged.
func withoutProviderFamilies(environ []string) []string {
	_, rest := modelsettings.Split(environ)
	return rest
}

// takeProviderFamilies is serve's first step: it returns a copy of the server's original
// environment, for Herdr's own programs only, and removes every model provider family from
// this process, so no later read or child takes a provider setting from Herdr's environment.
// Removal changes what children inherit, not the server's initial environment block.
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

// discoverAgents checks the supported local program names and probes their
// capabilities without starting an agent session. One slow CLI cannot hide
// the other installed choices.
func (c teamCommands) discoverAgents(ctx context.Context) ([]*gensurla.InstalledAgentOption, error) {
	type installedClient struct {
		key, title, headless, yolo, yoloEffect string
		names                                  []string
		helpPrefix                             []string
	}
	clients := []installedClient{
		{key: "codex", title: "Codex", headless: "exec", yolo: "--dangerously-bypass-approvals-and-sandbox",
			yoloEffect: "Disables approval prompts and command sandboxing.", names: []string{"codex"}},
		{key: "claude", title: "Claude Code", headless: "--print", yolo: "--dangerously-skip-permissions",
			yoloEffect: "Bypasses tool permission checks.", names: []string{"claude"}},
		{key: "antigravity", title: "Antigravity", headless: "--print", yolo: "--dangerously-skip-permissions",
			yoloEffect: "Automatically approves tool permission requests.", names: []string{"agy", "antigravity"}},
		{key: "cursor", title: "Cursor", headless: "--print", yolo: "--yolo",
			yoloEffect: "Allows commands unless explicitly denied, with the client's sandbox setting.", names: []string{"cursor-agent", "agent"}},
		{key: "gemini", title: "Gemini CLI", headless: "--prompt", yolo: "--yolo",
			yoloEffect: "Automatically approves all tool calls. Gemini CLI controls sandbox behavior.", names: []string{"gemini"}},
		{key: "opencode", title: "OpenCode", headless: "run",
			names: []string{"opencode"}, helpPrefix: []string{"run"}},
	}
	options := make([]*gensurla.InstalledAgentOption, len(clients))
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
			raw, err := c.run(bounded, c.herdrEnv, program, "--help")
			if err != nil {
				return
			}
			rootHelp := string(raw)
			help := rootHelp
			if len(client.helpPrefix) > 0 {
				raw, err := c.run(bounded, c.herdrEnv, program, append(client.helpPrefix, "--help")...)
				if err == nil {
					help = string(raw)
				}
			}
			option := &gensurla.InstalledAgentOption{
				Client: client.key, Program: program, Title: client.title,
				ModelSupported:    helpContains(help, "--model"),
				YoloSupported:     client.yolo != "" && helpContains(help, client.yolo),
				HeadlessSupported: helpContains(rootHelp, client.headless),
				Models:            []string{}, Efforts: []string{},
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
	return slices.DeleteFunc(options, func(option *gensurla.InstalledAgentOption) bool { return option == nil }), nil
}

// discoverAgentModels reads only client-owned catalogs and help. Clients that
// accept models without publishing a catalog keep an empty advertised list.
func (c teamCommands) discoverAgentModels(ctx context.Context, option *gensurla.InstalledAgentOption, help string) {
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
		raw, err := c.run(ctx, c.herdrEnv, option.Program, "debug", "models")
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
	raw, err := c.run(ctx, c.herdrEnv, program, args...)
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

// effortOption finds the choice group advertised for --effort.
var effortOption = regexp.MustCompile(`(?m)--effort[^\n()]*\((?:choices:\s*)?([a-z0-9_, |\t-]+)\)`)

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
