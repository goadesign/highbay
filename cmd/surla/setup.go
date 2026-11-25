// This file implements "surla setup -global". It registers `surla stdio` as
// the surla MCP server in the global configuration of every coding agent it
// detects and installs the /surla agent skill next to each. When Herdr is
// present it also installs the Surla companion window. "surla up" runs the
// same setup before it starts.
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"

	"github.com/goadesign/surla/skills"
)

type (
	// setupEnvironment holds the machine facts setup reads. runSetup fills it
	// from the running process; tests point it at a temporary home so they
	// never touch the user's own configuration.
	setupEnvironment struct {
		// home is the user's home directory. Every default location is under it.
		home string
		// executable is the absolute path of the surla binary that agents start
		// as `<executable> stdio`.
		executable string
		// getenv reads a variable that moves a client's files, such as
		// CLAUDE_CONFIG_DIR or HERDR_APP_HOME.
		getenv func(string) string
		// onPath reports whether a client's command is installed, so a client
		// that has not created its configuration folder yet is still set up.
		onPath func(string) bool
		// goos selects VS Code's settings folder and the macOS-only Herdr shortcut.
		goos string
	}

	// mcpJSONTarget is one JSON configuration file and the surla entry its
	// client expects.
	mcpJSONTarget struct {
		// path is the configuration file to create or update.
		path string
		// serversKey names the top-level object holding server entries:
		// "mcpServers" for most clients, "servers" for VS Code.
		serversKey string
		// entry is the object written at serversKey["surla"].
		entry map[string]any
	}

	// permissionsJSONTarget is a client settings file whose permissions.allow
	// array pre-authorizes Surla and Chorus MCP tools.
	permissionsJSONTarget struct {
		// path is the settings or CLI configuration file.
		path string
		// rules are the allowlist entries ensured in permissions.allow.
		rules []string
		// createWhenMissing creates path when absent; when false, only an
		// existing non-blank file is updated.
		createWhenMissing bool
	}

	// codexEntryEdit says where mergeCodexConfig writes mcp_servers.surla and
	// optional mcp_servers.chorus defaults in a Codex config.toml document.
	// Zero-length ranges mean the key is absent.
	codexEntryEdit struct {
		// command is the byte range of an existing command string, quotes included.
		command unstable.Range
		// args is the byte range of an existing args array.
		args unstable.Range
		// approvalMode is the byte range of an existing default_tools_approval_mode string.
		approvalMode unstable.Range
		// envVars is the byte range of an existing env_vars array.
		envVars unstable.Range
		// afterHeader is the offset just past the [mcp_servers.surla] header
		// line, or 0 when the document has no such table.
		afterHeader int
		// afterLastSurlaLine is the offset just past the last dotted surla.*
		// key line under [mcp_servers].
		afterLastSurlaLine int
		// chorusAfterHeader is the offset just past a standard
		// [mcp_servers.chorus] header line, or 0 when absent.
		chorusAfterHeader int
		// dotted is true when a dotted key such as surla.command under
		// [mcp_servers] defines part of the entry.
		dotted bool
	}

	// herdrShortcutBinding describes an existing assignment of the companion's
	// key. Command arguments stay private to the keyboard file.
	herdrShortcutBinding struct {
		// section is the keyboard context that already uses the key.
		section string
		// command names the action without its arguments.
		command string
		// opensCompanion is true only for a single keypress that opens this mod.
		opensCompanion bool
	}
)

const (
	// surlaServerName is the MCP server name and skill folder setup writes.
	surlaServerName = "surla"
	// herdrModName is the companion's folder under Herdr's mods.
	herdrModName = "surla"
	// herdrShortcut opens the companion without taking a common terminal key.
	herdrShortcut = "cmd-alt-shift-s"
	// herdrShortcutAction is Herdr's command shorthand that toggles the companion.
	herdrShortcutAction = "mod.open:surla"
	// herdrModCommandPlaceholder is the single declaration in skills/herdr/surla/main.js
	// that setup replaces with the installed surla executable path.
	herdrModCommandPlaceholder = `const SURLA_COMMAND = "surla";`
)

// runSetup implements `surla setup -global` for the CLI. It reads the
// process's home, executable and environment, then runs setupGlobal.
// Without -global it explains the flag instead of writing anything.
func runSetup(args []string, stdout io.Writer) error {
	var global bool
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.BoolVar(&global, "global", false, "Register Surla with every coding agent on this computer and install the Herdr companion")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	if !global {
		return errors.New(`surla setup changes the configuration of every coding agent on this computer; run "surla setup -global" to do that`)
	}
	env, err := processSetupEnvironment()
	if err != nil {
		return err
	}
	return setupGlobal(env, stdout)
}

// setupGlobal registers `<executable> stdio` as the surla MCP server in each
// detected coding agent's global configuration, installs the /surla skill
// for each, and installs the Herdr companion when Herdr's folder exists. It
// writes one line per change to out. Files that already hold the same entry
// or content are left untouched. A configuration file that is not valid, or
// that defines the surla entry in a form setup cannot edit, stops setup with
// an error naming the file; files written before it keep their changes.
func setupGlobal(env setupEnvironment, out io.Writer) error {
	lockDir := configLockDir(env.home, env.getenv, env.goos)
	jsonTargets, tomlTargets, permTargets, skillTargets := setupTargets(env)
	if len(jsonTargets) == 0 && len(tomlTargets) == 0 {
		fmt.Fprintln(out, "No coding agent was found on this computer; install one, then run surla setup -global again.")
	}
	for _, target := range jsonTargets {
		changed, err := mergeMCPJSONConfigWithLock(lockDir, target)
		if err != nil {
			return err
		}
		writeRegistration(out, target.path, changed)
	}
	for _, path := range tomlTargets {
		changed, err := mergeCodexConfigWithLock(lockDir, path, env.executable)
		if err != nil {
			return err
		}
		writeRegistration(out, path, changed)
	}
	for _, target := range permTargets {
		changed, present, err := mergePermissionsAllowJSONWithLock(lockDir, target)
		if err != nil {
			return err
		}
		if present {
			writePermissions(out, target.path, changed)
		}
	}
	for _, path := range skillTargets {
		changed, err := writeIntegrationFile(path, skills.SurlaSkillMD)
		if err != nil {
			return err
		}
		if changed {
			fmt.Fprintf(out, "Installed the /surla skill in %s\n", path)
		}
	}
	modDir, err := setupHerdrMod(env, out)
	if err != nil {
		return fmt.Errorf("install the Surla companion in Herdr: %w", err)
	}
	if modDir != "" {
		fmt.Fprintf(out, "Surla companion is in %s; open it with herdr-app do mod.open --name %s\n", modDir, herdrModName)
	}
	return nil
}

// processSetupEnvironment reads the facts setup needs from this process. The
// executable must be an installed binary: a `go run` build lives in a
// temporary folder that disappears, and agents would fail to start it later.
func processSetupEnvironment() (setupEnvironment, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return setupEnvironment{}, fmt.Errorf("resolve home directory: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return setupEnvironment{}, fmt.Errorf("resolve the surla executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return setupEnvironment{}, fmt.Errorf("resolve the surla executable: %w", err)
	}
	if temporaryExecutable(executable, os.TempDir(), os.Getenv("GOTMPDIR")) {
		return setupEnvironment{}, fmt.Errorf("surla is running from the temporary file %s; install it (go install github.com/goadesign/surla/cmd/surla@latest) and run that surla setup -global", executable)
	}
	return setupEnvironment{
		home:       home,
		executable: executable,
		getenv:     os.Getenv,
		onPath: func(name string) bool {
			_, err := exec.LookPath(name)
			return err == nil
		},
		goos: runtime.GOOS,
	}, nil
}

// temporaryExecutable reports whether executable lies under one of the
// temporary folders, where `go run` builds. Symlinks are resolved on both
// sides, since macOS reaches /var and /tmp through /private. An empty folder
// name is skipped.
func temporaryExecutable(executable string, tempDirs ...string) bool {
	exe := resolveExistingPrefix(executable)
	for _, dir := range tempDirs {
		if dir == "" {
			continue
		}
		if rel, err := filepath.Rel(resolveExistingPrefix(dir), exe); err == nil && filepath.IsLocal(rel) {
			return true
		}
	}
	return false
}

// resolveExistingPrefix resolves symlinks in the longest existing leading
// part of path and appends the rest unchanged.
func resolveExistingPrefix(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path
	}
	return filepath.Join(resolveExistingPrefix(parent), filepath.Base(path))
}

// setupTargets lists the configuration and skill files of the coding agents
// installed under env.home: Claude Code, OpenAI Codex, Cursor, Antigravity,
// Gemini CLI, Windsurf, GitHub Copilot CLI and VS Code. The universal
// ~/.agents skill folder is always included.
func setupTargets(env setupEnvironment) ([]mcpJSONTarget, []string, []permissionsJSONTarget, []string) {
	var (
		home         = env.home
		jsonTargets  []mcpJSONTarget
		tomlTargets  []string
		permTargets  []permissionsJSONTarget
		skillTargets = []string{skillPath(filepath.Join(home, ".agents"))}
		stdioEntry   = func() map[string]any {
			return map[string]any{"command": env.executable, "args": []any{"stdio"}}
		}
		isDir = func(path string) bool {
			info, err := os.Stat(path)
			return err == nil && info.IsDir()
		}
		exists = func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		}
	)

	// Claude Code keeps user servers in .claude.json, tool permissions in
	// settings.json, and skills in skills/, under CLAUDE_CONFIG_DIR when set.
	claudeDir := filepath.Join(home, ".claude")
	claudeConfig := filepath.Join(home, ".claude.json")
	if dir := env.getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		claudeDir = dir
		claudeConfig = filepath.Join(dir, ".claude.json")
	}
	if isDir(claudeDir) || exists(claudeConfig) || env.onPath("claude") {
		jsonTargets = append(jsonTargets, mcpJSONTarget{path: claudeConfig, serversKey: "mcpServers", entry: stdioEntry()})
		permTargets = append(permTargets, permissionsJSONTarget{
			path:              filepath.Join(claudeDir, "settings.json"),
			rules:             []string{"mcp__surla__*", "mcp__chorus__*"},
			createWhenMissing: true,
		})
		skillTargets = append(skillTargets, skillPath(claudeDir))
	}
	codexDir := cmp.Or(env.getenv("CODEX_HOME"), filepath.Join(home, ".codex"))
	if isDir(codexDir) || env.onPath("codex") {
		tomlTargets = append(tomlTargets, filepath.Join(codexDir, "config.toml"))
		skillTargets = append(skillTargets, skillPath(codexDir))
	}
	for _, dir := range []string{
		filepath.Join(home, ".cursor"),
		filepath.Join(home, ".gemini", "config"),
		filepath.Join(home, ".codeium", "windsurf"),
	} {
		if !isDir(dir) {
			continue
		}
		config := filepath.Join(dir, "mcp.json")
		if filepath.Base(dir) != ".cursor" {
			config = filepath.Join(dir, "mcp_config.json")
		} else {
			permTargets = append(permTargets, permissionsJSONTarget{
				path:              filepath.Join(dir, "cli-config.json"),
				rules:             []string{"Mcp(surla:*)", "Mcp(chorus:*)"},
				createWhenMissing: false,
			})
		}
		jsonTargets = append(jsonTargets, mcpJSONTarget{path: config, serversKey: "mcpServers", entry: stdioEntry()})
		skillTargets = append(skillTargets, skillPath(dir))
	}
	// Gemini CLI resolves under GEMINI_CLI_HOME/.gemini when set, else ~/.gemini.
	// When sharing ~/.gemini with Antigravity's ~/.gemini/config, it counts as
	// installed only through its own settings, skills or command.
	geminiDir := geminiConfigDir(home, env.getenv("GEMINI_CLI_HOME"))
	geminiSettings := filepath.Join(geminiDir, "settings.json")
	if exists(geminiSettings) || isDir(filepath.Join(geminiDir, "skills")) || env.onPath("gemini") {
		entry := stdioEntry()
		entry["trust"] = true
		jsonTargets = append(jsonTargets, mcpJSONTarget{path: geminiSettings, serversKey: "mcpServers", entry: entry})
		skillTargets = append(skillTargets, skillPath(geminiDir))
	}
	copilotDir := cmp.Or(env.getenv("COPILOT_HOME"), filepath.Join(home, ".copilot"))
	if isDir(copilotDir) || env.onPath("copilot") {
		entry := stdioEntry()
		entry["type"] = "local"
		entry["tools"] = []any{"*"}
		jsonTargets = append(jsonTargets, mcpJSONTarget{path: filepath.Join(copilotDir, "mcp-config.json"), serversKey: "mcpServers", entry: entry})
		skillTargets = append(skillTargets, skillPath(copilotDir))
	}
	if dir := vscodeUserDir(home, env.goos); dir != "" && isDir(dir) {
		entry := stdioEntry()
		entry["type"] = "stdio"
		jsonTargets = append(jsonTargets, mcpJSONTarget{path: filepath.Join(dir, "mcp.json"), serversKey: "servers", entry: entry})
	}
	return jsonTargets, tomlTargets, permTargets, skillTargets
}

// skillPath is where a client whose folder is dir reads the /surla skill.
func skillPath(dir string) string {
	return filepath.Join(dir, "skills", surlaServerName, "SKILL.md")
}

// vscodeUserDir returns VS Code's global User settings folder on macOS and
// Linux, or "" on other systems.
func vscodeUserDir(home, goos string) string {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Code", "User")
	case "linux":
		return filepath.Join(home, ".config", "Code", "User")
	default:
		return ""
	}
}

// writeRegistration reports whether setup changed a client's configuration.
func writeRegistration(out io.Writer, path string, changed bool) {
	if changed {
		fmt.Fprintf(out, "Registered the surla MCP server in %s\n", path)
		return
	}
	fmt.Fprintf(out, "The surla MCP server is already registered in %s\n", path)
}

// writePermissions reports whether setup updated a client's tool permissions.
func writePermissions(out io.Writer, path string, changed bool) {
	if changed {
		fmt.Fprintf(out, "Configured Surla tool permissions in %s\n", path)
		return
	}
	fmt.Fprintf(out, "Surla tool permissions are already configured in %s\n", path)
}

// mergePermissionsAllowJSON ensures target.rules appear in permissions.allow
// inside target.path when not blocked by permissions.deny or permissions.ask,
// preserving every other key, number representation and permission list. When
// target.createWhenMissing is false, a missing or blank file is left untouched,
// while an existing JSON object without permissions or allow receives them.
func mergePermissionsAllowJSON(target permissionsJSONTarget) (changed, present bool, err error) {
	return mergePermissionsAllowJSONWithLock("", target)
}

// mergePermissionsAllowJSONWithLock runs mergePermissionsAllowJSON under lockDir.
func mergePermissionsAllowJSONWithLock(
	lockDir string, target permissionsJSONTarget,
) (changed, present bool, err error) {
	path := target.path
	changed, err = updateLockedConfigFile(context.Background(), lockDir, path, nil, 0o644, func(raw []byte, existed bool) ([]byte, bool, error) {
		if (!existed || len(bytes.TrimSpace(raw)) == 0) && !target.createWhenMissing {
			present = false
			return nil, false, nil
		}
		present = true
		root, err := decodeConfigJSONBytes(path, raw)
		if err != nil {
			return nil, false, err
		}
		perms := make(map[string]any)
		if existing, has := root["permissions"]; has {
			var ok bool
			perms, ok = existing.(map[string]any)
			if !ok || perms == nil {
				return nil, false, fmt.Errorf("%s: permissions must be a JSON object", path)
			}
		}
		var allow []any
		if existing, has := perms["allow"]; has {
			var ok bool
			allow, ok = existing.([]any)
			if !ok || allow == nil {
				return nil, false, fmt.Errorf("%s: permissions.allow must be a JSON array", path)
			}
		}
		existingRules := make([]string, 0, len(allow))
		for _, item := range allow {
			s, ok := item.(string)
			if !ok {
				return nil, false, fmt.Errorf("%s: permissions.allow must contain strings", path)
			}
			existingRules = append(existingRules, s)
		}
		added := false
		for _, rule := range target.rules {
			if slices.Contains(existingRules, rule) || permissionListsBlockCandidateRule(perms, rule) {
				continue
			}
			allow = append(allow, rule)
			existingRules = append(existingRules, rule)
			added = true
		}
		if !added {
			return nil, false, nil
		}
		perms["allow"] = allow
		root["permissions"] = perms
		encoded, err := encodeConfigJSONBytes(path, root)
		if err != nil {
			return nil, false, err
		}
		return encoded, true, nil
	})
	if err != nil {
		return false, false, err
	}
	return changed, present, nil
}

// mergeMCPJSONConfig updates target.serversKey["surla"] to Surla's stdio
// transport in a JSON configuration file while preserving every other key,
// server, number representation, and user-configured surla entry fields such as
// includeTools, excludeTools, env, timeout, and explicit trust.
func mergeMCPJSONConfig(target mcpJSONTarget) (bool, error) {
	return mergeMCPJSONConfigWithLock("", target)
}

// mergeMCPJSONConfigWithLock runs mergeMCPJSONConfig under lockDir.
func mergeMCPJSONConfigWithLock(lockDir string, target mcpJSONTarget) (bool, error) {
	path := target.path
	return updateLockedConfigFile(context.Background(), lockDir, path, nil, 0o644, func(raw []byte, _ bool) ([]byte, bool, error) {
		root, err := decodeConfigJSONBytes(path, raw)
		if err != nil {
			return nil, false, err
		}
		servers := make(map[string]any)
		if existing, present := root[target.serversKey]; present {
			var ok bool
			if servers, ok = existing.(map[string]any); !ok || servers == nil {
				return nil, false, fmt.Errorf("%s: %s must be a JSON object", path, target.serversKey)
			}
		}
		var desired map[string]any
		if existingRaw, hasExisting := servers[surlaServerName]; hasExisting {
			existingServer, ok := existingRaw.(map[string]any)
			if !ok || existingServer == nil {
				return nil, false, fmt.Errorf("%s: %s.%s must be a JSON object", path, target.serversKey, surlaServerName)
			}
			desired = maps.Clone(existingServer)
			delete(desired, "url")
			delete(desired, "httpUrl")
			delete(desired, "headers")
			if transportType, hasType := target.entry["type"]; hasType {
				desired["type"] = transportType
			} else {
				delete(desired, "type")
			}
			desired["command"] = target.entry["command"]
			desired["args"] = target.entry["args"]
			for key, val := range target.entry {
				if key == "command" || key == "args" || key == "type" {
					continue
				}
				if _, alreadySet := desired[key]; !alreadySet {
					desired[key] = val
				}
			}
		} else {
			desired = maps.Clone(target.entry)
		}
		if sameJSON(servers[surlaServerName], desired) {
			return nil, false, nil
		}
		servers[surlaServerName] = desired
		root[target.serversKey] = servers
		encoded, err := encodeConfigJSONBytes(path, root)
		if err != nil {
			return nil, false, err
		}
		return encoded, true, nil
	})
}

// sameJSON reports whether existing, decoded from a file, already equals
// want once both are in JSON's decoded form.
func sameJSON(existing any, want map[string]any) bool {
	if existing == nil {
		return false
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return false
	}
	return reflect.DeepEqual(existing, normalized)
}

// mergeCodexConfig sets mcp_servers.surla to command = executable,
// args = ["stdio"], env_vars containing "HC_TERMINAL_ID" (preserving both plain
// strings and structured { name = ..., source = ... } items), and adds
// default_tools_approval_mode = "approve" to [mcp_servers.surla] and an
// existing standard [mcp_servers.chorus] table only when no approval mode is
// set yet. Every other byte stays unchanged so comments, formatting, explicit
// approval policies, other servers and other surla keys such as env survive.
func mergeCodexConfig(path, executable string) (bool, error) {
	return mergeCodexConfigWithLock("", path, executable)
}

// mergeCodexConfigWithLock runs mergeCodexConfig under lockDir.
func mergeCodexConfigWithLock(lockDir, path, executable string) (bool, error) {
	commandValue := strconv.Quote(executable)
	const (
		argsValue       = `["stdio"]`
		approvalValue   = `"approve"`
		defaultEnvVars  = `["HC_TERMINAL_ID"]`
		terminalEnvName = "HC_TERMINAL_ID"
	)
	byHand := fmt.Sprintf("set [mcp_servers.surla] command = %s and args = %s by hand", commandValue, argsValue)

	return updateLockedConfigFile(context.Background(), lockDir, path, nil, 0o600, func(content []byte, _ bool) ([]byte, bool, error) {
		// Decoding checks what the position-aware parser does not: duplicate keys,
		// redefined tables and type conflicts, which Codex itself refuses.
		var decoded map[string]any
		if err := toml.Unmarshal(content, &decoded); err != nil {
			return nil, false, fmt.Errorf("%s is not valid TOML: %w; %s", path, err, byHand)
		}
		servers := map[string]any{}
		if existing, present := decoded["mcp_servers"]; present {
			var ok bool
			if servers, ok = existing.(map[string]any); !ok {
				return nil, false, fmt.Errorf("%s: mcp_servers is not a table; %s", path, byHand)
			}
		}
		var (
			surlaEntry      map[string]any
			surlaHasApprove bool
			envVarsMatched  bool
		)
		if entry, ok := servers[surlaServerName].(map[string]any); ok {
			surlaEntry = entry
			if _, ok := entry["url"]; ok {
				return nil, false, fmt.Errorf("%s: mcp_servers.surla has a url; remove it and %s", path, byHand)
			}
			_, surlaHasApprove = entry["default_tools_approval_mode"]
			if rawVars, present := entry["env_vars"]; present {
				matched, err := codexEnvVarsHasName(rawVars, terminalEnvName)
				if err != nil {
					return nil, false, fmt.Errorf("%s: %w; %s", path, err, byHand)
				}
				envVarsMatched = matched
			}
		}
		surlaMatched := surlaEntry != nil &&
			surlaEntry["command"] == executable &&
			reflect.DeepEqual(surlaEntry["args"], []any{"stdio"}) &&
			surlaHasApprove &&
			envVarsMatched

		chorusEntry, hasChorus := servers["chorus"].(map[string]any)
		_, chorusHasApproval := chorusEntry["default_tools_approval_mode"]
		if surlaMatched && (!hasChorus || chorusHasApproval) {
			return nil, false, nil
		}

		edit, err := locateCodexSurlaEntry(content)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w; %s", path, err, byHand)
		}
		chorusNeedsApproval := hasChorus && !chorusHasApproval && edit.chorusAfterHeader > 0
		if surlaMatched && !chorusNeedsApproval {
			return nil, false, nil
		}

		type replacement struct {
			at, length int
			text       string
		}
		applyReplacements := func(base []byte, edits []replacement) []byte {
			if len(edits) == 0 {
				return slices.Clone(base)
			}
			slices.SortFunc(edits, func(a, b replacement) int { return b.at - a.at })
			result := slices.Clone(base)
			for _, r := range edits {
				text := r.text
				if r.length == 0 && r.at > 0 && result[r.at-1] != '\n' {
					text = "\n" + text
				}
				result = slices.Concat(result[:r.at], []byte(text), result[r.at+r.length:])
			}
			return result
		}

		var edits []replacement
		if chorusNeedsApproval {
			edits = append(edits, replacement{edit.chorusAfterHeader, 0, "default_tools_approval_mode = " + approvalValue + "\n"})
		}

		var out []byte
		switch {
		case surlaMatched:
			out = applyReplacements(content, edits)
		case edit.afterHeader == 0 && !edit.dotted:
			base := applyReplacements(content, edits)
			out = append(out, bytes.TrimRight(base, " \t\r\n")...)
			if len(out) > 0 {
				out = append(out, "\n\n"...)
			}
			out = append(out, "[mcp_servers.surla]\n"+
				"command = "+commandValue+"\n"+
				"args = "+argsValue+"\n"+
				"default_tools_approval_mode = "+approvalValue+"\n"+
				"env_vars = "+defaultEnvVars+"\n"...)
		case edit.afterHeader == 0 && (edit.command.Length == 0 || edit.args.Length == 0):
			return nil, false, fmt.Errorf("%s: mcp_servers.surla is defined with dotted keys missing command or args; %s", path, byHand)
		default:
			keyPrefix := ""
			insertAt := edit.afterHeader
			if edit.dotted {
				keyPrefix = surlaServerName + "."
				insertAt = edit.afterLastSurlaLine
			}
			var missing string
			if edit.command.Length > 0 {
				if surlaEntry["command"] != executable {
					edits = append(edits, replacement{int(edit.command.Offset), int(edit.command.Length), commandValue})
				}
			} else {
				missing += keyPrefix + "command = " + commandValue + "\n"
			}
			if edit.args.Length > 0 {
				if !reflect.DeepEqual(surlaEntry["args"], []any{"stdio"}) {
					edits = append(edits, replacement{int(edit.args.Offset), int(edit.args.Length), argsValue})
				}
			} else {
				missing += keyPrefix + "args = " + argsValue + "\n"
			}
			if edit.approvalMode.Length == 0 && !surlaHasApprove {
				missing += keyPrefix + "default_tools_approval_mode = " + approvalValue + "\n"
			}
			if edit.envVars.Length > 0 {
				if !envVarsMatched {
					rawArray := content[edit.envVars.Offset : edit.envVars.Offset+edit.envVars.Length]
					spliced, spliceErr := appendTOMLArrayString(rawArray, terminalEnvName)
					if spliceErr != nil {
						return nil, false, fmt.Errorf("%s: %w; %s", path, spliceErr, byHand)
					}
					edits = append(edits, replacement{int(edit.envVars.Offset), int(edit.envVars.Length), string(spliced)})
				}
			} else {
				missing += keyPrefix + "env_vars = " + defaultEnvVars + "\n"
			}
			if missing != "" {
				edits = append(edits, replacement{insertAt, 0, missing})
			}
			out = applyReplacements(content, edits)
		}
		var verified map[string]any
		if err := toml.Unmarshal(out, &verified); err != nil {
			return nil, false, fmt.Errorf("%s: validate updated TOML: %w; %s", path, err, byHand)
		}
		return out, true, nil
	})
}

// codexEnvVarsHasName validates a Codex env_vars array (accepting both string
// entries and inline tables with a non-empty string "name") and reports whether
// targetName is already listed.
func codexEnvVarsHasName(rawVars any, targetName string) (bool, error) {
	items, ok := rawVars.([]any)
	if !ok {
		return false, errors.New("mcp_servers.surla.env_vars is not an array")
	}
	found := false
	for _, item := range items {
		switch entry := item.(type) {
		case string:
			if entry == targetName {
				found = true
			}
		case map[string]any:
			name, ok := entry["name"].(string)
			if !ok || name == "" {
				return false, errors.New(`mcp_servers.surla.env_vars must contain strings or { name = "..." } tables`)
			}
			if name == targetName {
				found = true
			}
		default:
			return false, errors.New(`mcp_servers.surla.env_vars must contain strings or { name = "..." } tables`)
		}
	}
	return found, nil
}

// appendTOMLArrayString appends value as a quoted string to a validated TOML
// array literal `[ ... ]`, preserving existing items (including inline tables),
// quotes, comments and multiline indentation.
func appendTOMLArrayString(rawArray []byte, value string) ([]byte, error) {
	if len(rawArray) < 2 || rawArray[0] != '[' || rawArray[len(rawArray)-1] != ']' {
		return nil, errors.New("mcp_servers.surla.env_vars cannot be edited in place")
	}
	quoted := strconv.Quote(value)
	lastIdx := lastTOMLArrayContentIndex(rawArray)
	multiline := bytes.ContainsRune(rawArray, '\n')

	if lastIdx == 0 {
		if !multiline {
			return []byte("[" + quoted + "]"), nil
		}
		closeLineStart := bytes.LastIndexByte(rawArray[:len(rawArray)-1], '\n') + 1
		closeIndent := leadingIndent(rawArray[closeLineStart : len(rawArray)-1])
		insertion := closeIndent + "  " + quoted + "\n"
		return slices.Concat(rawArray[:closeLineStart], []byte(insertion), rawArray[closeLineStart:]), nil
	}

	if !multiline {
		sep := ", "
		if rawArray[lastIdx] == ',' {
			sep = " "
		}
		return slices.Concat(rawArray[:lastIdx+1], []byte(sep+quoted), rawArray[lastIdx+1:]), nil
	}

	closeLineStart := bytes.LastIndexByte(rawArray[:len(rawArray)-1], '\n') + 1
	if closeLineStart <= lastIdx {
		sep := ", "
		if rawArray[lastIdx] == ',' {
			sep = " "
		}
		return slices.Concat(rawArray[:lastIdx+1], []byte(sep+quoted), rawArray[lastIdx+1:]), nil
	}

	elemLineStart := bytes.LastIndexByte(rawArray[:lastIdx], '\n') + 1
	elemIndent := leadingIndent(rawArray[elemLineStart:lastIdx])
	if rawArray[lastIdx] == ',' {
		insertion := elemIndent + quoted + ",\n"
		return slices.Concat(rawArray[:closeLineStart], []byte(insertion), rawArray[closeLineStart:]), nil
	}
	withComma := slices.Concat(rawArray[:lastIdx+1], []byte(","), rawArray[lastIdx+1:])
	shiftedCloseLine := closeLineStart + 1
	insertion := elemIndent + quoted + "\n"
	return slices.Concat(withComma[:shiftedCloseLine], []byte(insertion), withComma[shiftedCloseLine:]), nil
}

// lastTOMLArrayContentIndex returns the byte index inside rawArray (`[...]`) of
// the last non-whitespace, non-comment token character, or 0 when empty.
func lastTOMLArrayContentIndex(rawArray []byte) int {
	lastIdx := 0
	end := len(rawArray) - 1
	for i := 1; i < end; i++ {
		switch {
		case rawArray[i] == '#':
			for i < end && rawArray[i] != '\n' {
				i++
			}
		case bytes.HasPrefix(rawArray[i:], []byte(`"""`)):
			i += 3
			for i < end && !bytes.HasPrefix(rawArray[i:], []byte(`"""`)) {
				if rawArray[i] == '\\' && i+1 < end {
					i++
				}
				i++
			}
			if i+2 < end {
				i += 2
				lastIdx = i
			}
		case bytes.HasPrefix(rawArray[i:], []byte(`'''`)):
			i += 3
			for i < end && !bytes.HasPrefix(rawArray[i:], []byte(`'''`)) {
				i++
			}
			if i+2 < end {
				i += 2
				lastIdx = i
			}
		case rawArray[i] == '"':
			i++
			for i < end && rawArray[i] != '"' {
				if rawArray[i] == '\\' && i+1 < end {
					i++
				}
				i++
			}
			if i < end {
				lastIdx = i
			}
		case rawArray[i] == '\'':
			i++
			for i < end && rawArray[i] != '\'' {
				i++
			}
			if i < end {
				lastIdx = i
			}
		case rawArray[i] == ' ' || rawArray[i] == '\t' || rawArray[i] == '\r' || rawArray[i] == '\n':
			continue
		default:
			lastIdx = i
		}
	}
	return lastIdx
}

// leadingIndent returns the leading spaces and tabs of line.
func leadingIndent(line []byte) string {
	end := 0
	for end < len(line) && (line[end] == ' ' || line[end] == '\t') {
		end++
	}
	return string(line[:end])
}

// locateCodexSurlaEntry parses a Codex config.toml and reports where
// mergeCodexConfig writes mcp_servers.surla and any standard
// [mcp_servers.chorus] header. It returns an error for invalid TOML and for
// entries it cannot edit in place. Tables nested under the entry, such as
// [mcp_servers.surla.env], are left as they are.
func locateCodexSurlaEntry(doc []byte) (codexEntryEdit, error) {
	var (
		edit         codexEntryEdit
		table        []string
		parser       unstable.Parser
		entry        = []string{"mcp_servers", surlaServerName}
		chorusEntry  = []string{"mcp_servers", "chorus"}
		command      = []string{"mcp_servers", surlaServerName, "command"}
		args         = []string{"mcp_servers", surlaServerName, "args"}
		approvalMode = []string{"mcp_servers", surlaServerName, "default_tools_approval_mode"}
		envVars      = []string{"mcp_servers", surlaServerName, "env_vars"}
	)
	afterLine := func(offset int) int {
		if nl := bytes.IndexByte(doc[offset:], '\n'); nl >= 0 {
			return offset + nl + 1
		}
		return len(doc)
	}
	parser.Reset(doc)
	for parser.NextExpression() {
		expr := parser.Expression()
		switch expr.Kind {
		case unstable.Table, unstable.ArrayTable:
			var last *unstable.Node
			table, last = tomlKeyPath(nil, expr.Key())
			if slices.Equal(table, chorusEntry) && expr.Kind == unstable.Table {
				edit.chorusAfterHeader = afterLine(int(last.Raw.Offset + last.Raw.Length))
				continue
			}
			if !slices.Equal(table, entry) {
				continue
			}
			if expr.Kind == unstable.ArrayTable {
				return edit, errors.New("mcp_servers.surla is defined as an array of tables")
			}
			edit.afterHeader = afterLine(int(last.Raw.Offset + last.Raw.Length))
		case unstable.KeyValue:
			key, last := tomlKeyPath(slices.Clone(table), expr.Key())
			value := expr.Value()
			switch {
			case slices.Equal(key, command):
				if value.Kind != unstable.String {
					return edit, errors.New("mcp_servers.surla.command is not a string")
				}
				edit.command = tomlValueRange(doc, expr, last)
			case slices.Equal(key, args):
				if value.Kind != unstable.Array {
					return edit, errors.New("mcp_servers.surla.args is not an array")
				}
				edit.args = tomlValueRange(doc, expr, last)
			case slices.Equal(key, approvalMode):
				if value.Kind != unstable.String {
					return edit, errors.New("mcp_servers.surla.default_tools_approval_mode is not a string")
				}
				edit.approvalMode = tomlValueRange(doc, expr, last)
			case slices.Equal(key, envVars):
				if value.Kind != unstable.Array {
					return edit, errors.New("mcp_servers.surla.env_vars is not an array")
				}
				edit.envVars = tomlValueRange(doc, expr, last)
			case slices.Equal(key, entry) || slices.Equal(key, entry[:1]):
				return edit, errors.New("mcp_servers or mcp_servers.surla is an inline table")
			}
			if len(key) > 2 && slices.Equal(key[:2], entry) && len(table) < 2 {
				edit.dotted = true
				edit.afterLastSurlaLine = afterLine(int(expr.Raw.Offset + expr.Raw.Length))
			}
		}
	}
	if err := parser.Error(); err != nil {
		return edit, fmt.Errorf("not valid TOML: %w", err)
	}
	return edit, nil
}

// tomlValueRange returns the bytes of a key/value expression's value: from
// the first non-blank byte after the '=' that follows the key's last segment
// to the end of the expression. The parser records no range for arrays, and
// the expression's own range ends where its value ends, before any comment.
func tomlValueRange(doc []byte, expr, lastKey *unstable.Node) unstable.Range {
	start := int(lastKey.Raw.Offset + lastKey.Raw.Length)
	start += bytes.IndexByte(doc[start:], '=') + 1
	for start < len(doc) && (doc[start] == ' ' || doc[start] == '\t') {
		start++
	}
	end := int(expr.Raw.Offset + expr.Raw.Length)
	return unstable.Range{Offset: uint32(start), Length: uint32(end - start)}
}

// tomlKeyPath appends the decoded segments of a TOML key (bare, quoted or
// dotted) to prefix and returns the path with the last segment's node, whose
// Raw range ends the key in the input.
func tomlKeyPath(prefix []string, it unstable.Iterator) ([]string, *unstable.Node) {
	var last *unstable.Node
	for it.Next() {
		last = it.Node()
		prefix = append(prefix, string(last.Data))
	}
	return prefix, last
}

// setupHerdrMod installs the companion's runtime files into Herdr's existing
// home folder (HERDR_APP_HOME, else ~/.config/herdr-app), which Herdr creates
// on its first start. Only mod.json and main.js are written, manifest first;
// identical files are left alone because Herdr reloads a mod whenever its
// files are written. On macOS the shortcut is added when its key is free. It
// returns the mod folder, or "" when Herdr has no home folder.
func setupHerdrMod(env setupEnvironment, out io.Writer) (string, error) {
	herdrHome := cmp.Or(env.getenv("HERDR_APP_HOME"), filepath.Join(env.home, ".config", "herdr-app"))
	info, err := os.Stat(herdrHome)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect Herdr configuration %s: %w", herdrHome, err)
	}
	if !info.IsDir() {
		return "", nil
	}
	modDir := filepath.Join(herdrHome, "mods", herdrModName)
	for _, name := range []string{"mod.json", "main.js"} {
		content, err := skills.HerdrMod.ReadFile("herdr/surla/" + name)
		if err != nil {
			return "", fmt.Errorf("read bundled %s: %w", name, err)
		}
		content, err = renderHerdrModFile(name, content, env.executable)
		if err != nil {
			return "", err
		}
		if _, err := writeIntegrationFile(filepath.Join(modDir, name), content); err != nil {
			return "", err
		}
	}
	if env.goos == "darwin" {
		lockDir := configLockDir(env.home, env.getenv, env.goos)
		if err := setupHerdrShortcutWithLock(lockDir, herdrHome, out); err != nil {
			return "", err
		}
	}
	return modDir, nil
}

// renderHerdrModFile binds the installed surla executable path into main.js so
// GUI-launched Herdr can run surla even when ~/go/bin is outside login PATH.
func renderHerdrModFile(name string, content []byte, executable string) ([]byte, error) {
	if name != "main.js" || executable == "" {
		return content, nil
	}
	placeholder := []byte(herdrModCommandPlaceholder)
	if count := bytes.Count(content, placeholder); count != 1 {
		return nil, fmt.Errorf("bundled Herdr companion main.js expected 1 %q declaration, found %d", herdrModCommandPlaceholder, count)
	}
	configured := []byte("const SURLA_COMMAND = " + strconv.Quote(executable) + ";")
	return bytes.Replace(content, placeholder, configured, 1), nil
}

// setupHerdrShortcut adds cmd-alt-shift-s to Herdr's global keyboard section
// when no section binds that key yet. A missing or read-only keyboard file is
// reported and left alone; a malformed one is an error and is not written.
// Editing the file makes Herdr treat it as the user's own on later upgrades.
func setupHerdrShortcut(herdrHome string, out io.Writer) error {
	return setupHerdrShortcutWithLock("", herdrHome, out)
}

// setupHerdrShortcutWithLock runs setupHerdrShortcut under lockDir.
func setupHerdrShortcutWithLock(lockDir, herdrHome string, out io.Writer) error {
	path := filepath.Join(herdrHome, "keys.json")
	var message string
	wrote, err := updateLockedConfigFile(context.Background(), lockDir, path, nil, 0o644, func(raw []byte, existed bool) ([]byte, bool, error) {
		if !existed {
			message = "Herdr's keyboard file is missing; open Herdr once and run surla setup -global again to add the shortcut."
			return nil, false, nil
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, false, fmt.Errorf("inspect Herdr keyboard settings %s: %w", path, statErr)
		}
		if info.Mode().Perm()&0o222 == 0 {
			message = fmt.Sprintf("Herdr keyboard settings %s are read-only; kept them unchanged.", path)
			return nil, false, nil
		}
		updated, existing, parseErr := addHerdrShortcut(raw)
		if parseErr != nil {
			return nil, false, fmt.Errorf("parse Herdr keyboard settings %s: %w", path, parseErr)
		}
		if existing != nil {
			if existing.opensCompanion {
				message = fmt.Sprintf("Herdr shortcut %s already opens the Surla companion.", herdrShortcut)
			} else {
				message = fmt.Sprintf("Herdr shortcut %s is taken by %q in %q; kept that binding.", herdrShortcut, existing.command, existing.section)
			}
			return nil, false, nil
		}
		return updated, true, nil
	})
	if err != nil {
		return err
	}
	if wrote {
		fmt.Fprintf(out, "Added Herdr shortcut %s to open the Surla companion. Herdr now treats keys.json as your own.\n", herdrShortcut)
	} else if message != "" {
		fmt.Fprintln(out, message)
	}
	return nil
}

// addHerdrShortcut validates a whole keyboard document and adds the companion
// binding to its global object, creating that object when absent. Any binding
// of the same first keypress in any section prevents the insertion and is
// returned instead. Bytes outside the insertion stay unchanged.
func addHerdrShortcut(raw []byte) ([]byte, *herdrShortcutBinding, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil, errors.New("keyboard settings file is empty")
		}
		return nil, nil, err
	}
	if token != json.Delim('{') {
		return nil, nil, errors.New("keyboard settings must contain a JSON object")
	}
	var existing *herdrShortcutBinding
	var rootHasMembers, globalHasMembers bool
	globalClose := -1
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, nil, err
		}
		section := token.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, nil, err
		}
		rootHasMembers = true
		if section != "global" && section != "terminal" && !strings.HasPrefix(section, "layout:") && !strings.HasPrefix(section, "mod:") {
			continue
		}
		var bindings map[string]json.RawMessage
		if err := json.Unmarshal(value, &bindings); err != nil || bindings == nil {
			return nil, nil, fmt.Errorf("%s must contain a JSON object", section)
		}
		if section == "global" {
			if globalClose >= 0 {
				return nil, nil, errors.New("keyboard settings contain more than one global section")
			}
			globalClose = int(decoder.InputOffset()) - 1
			globalHasMembers = len(bindings) > 0
		}
		for chord, action := range bindings {
			if !herdrShortcutMatches(chord) {
				continue
			}
			binding := herdrBindingFor(section, chord, action)
			// A conflicting binding wins over one that opens the companion,
			// because it can intercept the key in its context.
			if existing == nil || !binding.opensCompanion {
				existing = binding
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, nil, err
	}
	rootClose := int(decoder.InputOffset()) - 1
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("keyboard settings must contain one JSON object")
	}
	if existing != nil {
		return raw, existing, nil
	}
	member := fmt.Sprintf("%q: %q", herdrShortcut, herdrShortcutAction)
	if globalClose >= 0 {
		return insertJSONMember(raw, globalClose, globalHasMembers, member), nil, nil
	}
	member = fmt.Sprintf("%q: {%s}", "global", member)
	return insertJSONMember(raw, rootClose, rootHasMembers, member), nil, nil
}

// herdrBindingFor describes the action bound to chord in section, which is
// either the "command:arg" shorthand or a {command, args} object.
func herdrBindingFor(section, chord string, action json.RawMessage) *herdrShortcutBinding {
	var name string
	var object struct {
		// Command is the app action selected by this binding.
		Command string `json:"command"`
		// Args holds the action's named arguments without re-encoding them.
		Args map[string]json.RawMessage `json:"args"`
	}
	opens := false
	if json.Unmarshal(action, &name) == nil {
		opens = name == herdrShortcutAction
		name, _, _ = strings.Cut(name, ":")
	} else if json.Unmarshal(action, &object) == nil {
		name = object.Command
		var mod string
		opens = object.Command == "mod.open" && len(object.Args) == 1 && json.Unmarshal(object.Args["name"], &mod) == nil && mod == herdrModName
	}
	return &herdrShortcutBinding{
		section:        section,
		command:        name,
		opensCompanion: opens && len(strings.Fields(chord)) == 1,
	}
}

// herdrShortcutMatches reports whether chord starts with cmd-alt-shift-s,
// accepting Herdr's modifier aliases and the implicit shift of an uppercase
// letter. A sequence that starts with this keypress also occupies it.
func herdrShortcutMatches(chord string) bool {
	strokes := strings.Fields(chord)
	if len(strokes) == 0 {
		return false
	}
	parts := strings.Split(strokes[0], "-")
	key := parts[len(parts)-1]
	if key != "s" && key != "S" {
		return false
	}
	held := make(map[string]bool)
	for _, modifier := range parts[:len(parts)-1] {
		modifier = strings.ToLower(modifier)
		switch modifier {
		case "secondary", "super", "win":
			modifier = "cmd"
		case "option":
			modifier = "alt"
		}
		held[modifier] = true
	}
	if key == "S" {
		held["shift"] = true
	}
	return len(held) == 3 && held["cmd"] && held["alt"] && held["shift"]
}

// insertJSONMember adds member before a validated object's closing brace at
// closeOffset, matching the object's existing indentation and line endings
// and leaving every other byte unchanged.
func insertJSONMember(raw []byte, closeOffset int, hasMembers bool, member string) []byte {
	at := closeOffset
	for at > 0 && strings.ContainsRune(" \t\r\n", rune(raw[at-1])) {
		at--
	}
	separator := ""
	if hasMembers {
		separator = ","
	}
	var insertion string
	if bytes.ContainsRune(raw[at:closeOffset], '\n') {
		line := bytes.LastIndexByte(raw[:closeOffset], '\n') + 1
		indent := string(raw[line:closeOffset])
		memberIndent := indent + "  "
		if hasMembers {
			previous := bytes.LastIndexByte(raw[:at], '\n') + 1
			end := previous
			for end < at && (raw[end] == ' ' || raw[end] == '\t') {
				end++
			}
			memberIndent = string(raw[previous:end])
		}
		newline := "\n"
		if bytes.Contains(raw[at:closeOffset], []byte("\r\n")) {
			newline = "\r\n"
		}
		insertion = separator + newline + memberIndent + member + newline + indent
	} else {
		insertion = separator + member + string(raw[at:closeOffset])
	}
	return slices.Concat(raw[:at], []byte(insertion), raw[closeOffset:])
}

// replaceConfigFile atomically replaces the file at path with content,
// following a symlink to its target so dotfile managers keep their link. An
// existing file keeps its permissions; a new one gets perm and any missing
// parent folders.
func replaceConfigFile(path string, content []byte, perm os.FileMode) (err error) {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("resolve %s: %w", path, err)
	}
	if info, err := os.Stat(target); err == nil {
		perm = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect %s: %w", target, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create folder for %s: %w", target, err)
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".surla-setup-*")
	if err != nil {
		return fmt.Errorf("create update for %s: %w", target, err)
	}
	defer func() {
		if cleanupErr := os.Remove(file.Name()); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove temporary file for %s: %w", target, cleanupErr))
		}
	}()
	writeErr := file.Chmod(perm)
	if writeErr == nil {
		_, writeErr = file.Write(content)
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	if err := os.Rename(file.Name(), target); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}

// writeIntegrationFile installs bundled skill or mod content at path,
// creating folders as needed, and reports whether it wrote. The file is
// replaced atomically so Herdr, which reloads a mod whenever its files are
// written, never reads a partly written one; identical files are left alone.
func writeIntegrationFile(path string, content []byte) (bool, error) {
	installed, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if err == nil && bytes.Equal(installed, content) {
		return false, nil
	}
	if err := replaceConfigFile(path, content, 0o644); err != nil {
		return false, err
	}
	return true, nil
}
